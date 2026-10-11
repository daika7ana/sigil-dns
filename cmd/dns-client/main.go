// Command dns-client is the CLI for the dns-client loopback DNS resolver.
//
// Commands:
//
//	enable     bind, health-check, snapshot, intercept, then run until Ctrl-C
//	disable    stop resolver, restore interception, clear the dirty marker
//	restore    restore interception from persisted state (crash recovery)
//	status     print the persisted state
//	run        foreground resolver only, for dev and CI
//	providers  list presets or show generated endpoints
//	config     print the config path or write a default config
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/lifecycle"
	"github.com/daika7ana/dns-client/internal/providers"
	"github.com/daika7ana/dns-client/internal/resolver"
	"github.com/daika7ana/dns-client/internal/state"
)

// Default upstream settings shown in help text.
const (
	defaultUpstream  = resolver.DefaultUpstream
	defaultBootstrap = resolver.DefaultBootstrap
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dns-client:", err)
		os.Exit(1)
	}
}

func run(args []string) (err error) {
	if len(args) == 0 {
		usage()

		return errors.New("missing command")
	}

	switch cmd, rest := args[0], args[1:]; cmd {
	case "enable":
		return cmdEnable(rest)
	case "disable":
		return cmdDisable(rest)
	case "restore":
		return cmdRestore(rest)
	case "status":
		return cmdStatus(rest)
	case "run":
		return cmdRun(rest)
	case "providers":
		return cmdProviders(rest)
	case "config":
		return cmdConfig(rest)
	case "-h", "--help", "help":
		usage()

		return nil
	default:
		usage()

		return fmt.Errorf("unknown command %q", cmd)
	}
}

// options holds the flags shared by all commands.
type options struct {
	upstream   string
	bootstrap  string
	listen     string
	statePath  string
	configPath string
	profile    string
}

// parseFlags parses rest for cmd and returns the options.
func parseFlags(cmd string, rest []string) (o options, err error) {
	fs := flag.NewFlagSet("dns-client "+cmd, flag.ContinueOnError)
	fs.StringVar(&o.upstream, "upstream", "", "upstream DNS URL (default "+defaultUpstream+")")
	fs.StringVar(&o.bootstrap, "bootstrap", "", "bootstrap resolver address (default "+defaultBootstrap+")")
	fs.StringVar(&o.listen, "listen", "", "comma-separated loopback listen addresses")
	fs.StringVar(&o.statePath, "state", "", "state file path")
	fs.StringVar(&o.configPath, "config", "", "config file path (default "+config.DefaultPath()+")")
	fs.StringVar(&o.profile, "profile", "", "config profile name (default the config's active profile)")

	if err = fs.Parse(rest); err != nil {
		return o, err
	}

	if o.statePath == "" {
		o.statePath = defaultStatePath()
	}
	if o.configPath == "" {
		o.configPath = config.DefaultPath()
	}

	return o, nil
}

// logger returns the CLI logger.
func logger() (l *slog.Logger) {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// resolveSettings applies flag > config > default precedence.
func (o options) resolveSettings() (r config.Resolved, err error) {
	cfg, err := config.Load(o.configPath)
	if err != nil && !errors.Is(err, config.ErrNoConfig) {
		return config.Resolved{}, err
	}

	return config.Resolve(cfg, config.Overrides{
		Profile:   o.profile,
		Upstream:  o.upstream,
		Bootstrap: o.bootstrap,
	})
}

// listenAddrs returns the configured listen addresses or nil for the default.
func (o options) listenAddrs() (listen []string) {
	if o.listen != "" {
		return strings.Split(o.listen, ",")
	}

	return nil
}

// lifecycleFor builds a Lifecycle using resolved settings.
func (o options) lifecycleFor(r config.Resolved) (lc *lifecycle.Lifecycle) {
	rules := make([]resolver.Rule, 0, len(r.PerDomain))
	for _, pd := range r.PerDomain {
		rules = append(rules, resolver.Rule{Domains: pd.Domains, Upstream: pd.Upstream})
	}

	lists := make([]resolver.FilterList, 0, len(r.Filtering.Lists))
	for _, l := range r.Filtering.Lists {
		if !l.Enabled {
			continue
		}
		lists = append(lists, resolver.FilterList{ID: l.URL, Source: l.URL})
	}

	return lifecycle.New(lifecycle.Config{
		Logger:    logger(),
		StatePath: o.statePath,
		Upstreams: r.Upstreams,
		Bootstrap: r.Bootstrap,
		Rules:     rules,
		Filter: resolver.FilterConfig{
			Enabled:       r.Filtering.Enabled,
			Lists:         lists,
			BlockResponse: r.Filtering.BlockResponse,
			TTL:           r.Filtering.BlockTTL,
		},
		Listen:         o.listenAddrs(),
		CacheSizeBytes: r.Cache.SizeBytes,
		CacheDisabled:  !r.Cache.Enabled,
	})
}

// lifecycleForState builds a Lifecycle that does not depend on the config file.
// Restore must work even when the config is corrupt (SPEC §4.4).
func (o options) lifecycleForState() (lc *lifecycle.Lifecycle) {
	return lifecycle.New(lifecycle.Config{
		Logger:    logger(),
		StatePath: o.statePath,
		Listen:    o.listenAddrs(),
	})
}

func cmdEnable(args []string) (err error) {
	o, err := parseFlags("enable", args)
	if err != nil {
		return err
	}

	resolved, err := o.resolveSettings()
	if err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()

	lc := o.lifecycleFor(resolved)
	if err = lc.Enable(ctx); err != nil {
		return err
	}

	fmt.Fprintln(os.Stderr, "enabled; resolver running. Press Ctrl-C to disable and restore.")

	<-ctx.Done()
	fmt.Fprintln(os.Stderr, "shutting down and restoring...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	return lc.Disable(shutdownCtx)
}

func cmdDisable(args []string) (err error) {
	o, err := parseFlags("disable", args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return o.lifecycleForState().Disable(ctx)
}

func cmdRestore(args []string) (err error) {
	o, err := parseFlags("restore", args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return o.lifecycleForState().Restore(ctx)
}

func cmdStatus(args []string) (err error) {
	o, err := parseFlags("status", args)
	if err != nil {
		return err
	}

	st, err := o.lifecycleForState().Status()
	if err != nil {
		if errors.Is(err, state.ErrNoState) {
			fmt.Printf("state: none (%s)\n", o.statePath)

			return nil
		}

		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	return enc.Encode(st)
}

func cmdRun(args []string) (err error) {
	o, err := parseFlags("run", args)
	if err != nil {
		return err
	}

	resolved, err := o.resolveSettings()
	if err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()

	return o.lifecycleFor(resolved).Run(ctx)
}

func cmdConfig(args []string) (err error) {
	if len(args) == 0 {
		return errors.New("usage: config <path|init>")
	}

	sub, rest := args[0], args[1:]
	o, err := parseFlags("config", rest)
	if err != nil {
		return err
	}

	switch sub {
	case "path":
		fmt.Println(o.configPath)

		return nil
	case "init":
		if err = config.Init(o.configPath); err != nil {
			if errors.Is(err, config.ErrExists) {
				return fmt.Errorf("config already exists at %s (not overwriting)", o.configPath)
			}

			return err
		}

		fmt.Fprintf(os.Stderr, "wrote default config to %s\n", o.configPath)

		return nil
	default:
		return fmt.Errorf("unknown config subcommand %q", sub)
	}
}

func cmdProviders(args []string) (err error) {
	if len(args) == 0 {
		return errors.New("usage: providers <list|show>")
	}

	switch sub, rest := args[0], args[1:]; sub {
	case "list":
		providersList()

		return nil
	case "show":
		return providersShow(rest)
	default:
		return fmt.Errorf("unknown providers subcommand %q", sub)
	}
}

// providersList prints the preset catalog.
func providersList() {
	for _, p := range providers.Registry() {
		fmt.Printf("%s - %s\n", p.Name, p.Description)
		fmt.Printf("  protocols: %s\n", joinProtos(p.Protocols))
		fmt.Printf("  default variant: %s\n", p.DefaultVariant)
		for _, v := range p.Variants {
			notes := ""
			if v.NeedsID {
				notes += " [needs id]"
			}
			if v.ECSLeak {
				notes += " [leaks network prefix]"
			}
			fmt.Printf("    - %s: %s%s\n", v.Name, v.Description, notes)
		}
		fmt.Println()
	}
}

// providersShow expands a selection and prints the generated profile fragment.
// Flags may appear before or after the positional selection.
func providersShow(args []string) (err error) {
	var (
		id        string
		upstream  string
		bootstrap string
		spec      string
	)

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--id", a == "-id":
			if i+1 >= len(args) {
				return fmt.Errorf("flag %s needs a value", a)
			}
			i++
			id = args[i]
		case strings.HasPrefix(a, "--id="), strings.HasPrefix(a, "-id="):
			id = a[strings.IndexByte(a, '=')+1:]
		case a == "--upstream", a == "-upstream":
			if i+1 >= len(args) {
				return fmt.Errorf("flag %s needs a value", a)
			}
			i++
			upstream = args[i]
		case strings.HasPrefix(a, "--upstream="), strings.HasPrefix(a, "-upstream="):
			upstream = a[strings.IndexByte(a, '=')+1:]
		case a == "--bootstrap", a == "-bootstrap":
			if i+1 >= len(args) {
				return fmt.Errorf("flag %s needs a value", a)
			}
			i++
			bootstrap = args[i]
		case strings.HasPrefix(a, "--bootstrap="), strings.HasPrefix(a, "-bootstrap="):
			bootstrap = a[strings.IndexByte(a, '=')+1:]
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q", a)
		default:
			if spec != "" {
				return fmt.Errorf("unexpected argument %q", a)
			}
			spec = a
		}
	}

	if spec == "" {
		return errors.New("usage: providers show <provider>[/<variant>[/<protocol>]] [--id x]")
	}

	parts := strings.Split(spec, "/")
	if len(parts) > 3 {
		return fmt.Errorf("invalid selection %q", spec)
	}

	sel := providers.Selection{Provider: parts[0], ID: id, URL: upstream}
	if len(parts) > 1 {
		sel.Variant = parts[1]
	}
	if len(parts) > 2 {
		sel.Protocol = providers.Protocol(parts[2])
	}
	for _, b := range strings.Split(bootstrap, ",") {
		if b = strings.TrimSpace(b); b != "" {
			sel.Bootstrap = append(sel.Bootstrap, b)
		}
	}

	if sel.Variant == "" {
		if p, ok := providers.Lookup(sel.Provider); ok {
			sel.Variant = p.DefaultVariant
		}
	}

	ep, err := providers.Expand(sel)
	if err != nil {
		return err
	}

	proto := sel.Protocol
	if proto == "" {
		proto = providers.DoH
	}

	fmt.Printf("provider: %s\n", sel.Provider)
	fmt.Printf("variant: %s\n", sel.Variant)
	fmt.Printf("protocol: %s\n", proto)
	fmt.Println("upstreams:")
	for _, u := range ep.Upstreams {
		fmt.Printf("  - %s\n", u)
	}
	fmt.Println("bootstrap:")
	for _, b := range ep.Bootstrap {
		fmt.Printf("  - %s\n", b)
	}

	return nil
}

// joinProtos renders protocols for display.
func joinProtos(ps []providers.Protocol) (s string) {
	strs := make([]string, len(ps))
	for i, p := range ps {
		strs[i] = string(p)
	}

	return strings.Join(strs, ", ")
}

// signalContext returns a context canceled on interrupt or termination.
func signalContext() (ctx context.Context, stop context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// defaultStatePath returns the platform-appropriate state file path.
func defaultStatePath() (p string) {
	if dir := os.Getenv("ProgramData"); dir != "" {
		return filepath.Join(dir, "dns-client", "state.json")
	}

	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "dns-client", "state.json")
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "dns-client", "state.json")
	}

	return filepath.Join(os.TempDir(), "dns-client", "state.json")
}

// usage prints CLI help.
func usage() {
	fmt.Fprint(os.Stderr, `dns-client - system-wide encrypted DNS on loopback

Usage:
  dns-client <command> [flags]

Commands:
  enable             bind, health-check, snapshot, apply interception, run until Ctrl-C
  disable            stop resolver, restore interception, clear dirty marker
  restore            restore interception from persisted state
  status             print persisted state
  run                foreground resolver only (dev/CI)
  providers list     list provider presets
  providers show     show generated endpoints for provider[/variant[/protocol]]
  config path        print the resolved config path
  config init        write a default config (never clobbers)

Flags:
  --upstream <url>      upstream DNS URL (highest precedence)
  --bootstrap <addr,...> bootstrap resolver address(es)
  --config <path>       config file path
  --profile <name>      config profile to use
  --listen <addr,...>   loopback listen addresses
  --state <path>        state file path
`)
}
