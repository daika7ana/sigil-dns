// Command dns-client is the CLI for the dns-client loopback DNS resolver.
//
// Commands:
//
//	enable   bind, health-check, snapshot, intercept, then run until Ctrl-C
//	disable  stop resolver, restore interception, clear the dirty marker
//	restore  restore interception from persisted state (crash recovery)
//	status   print the persisted state
//	run      foreground resolver only, for dev and CI
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

	"github.com/daika7ana/dns-client/internal/lifecycle"
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
	upstream  string
	bootstrap string
	listen    string
	statePath string
}

// parseFlags parses rest for cmd and returns the options.
func parseFlags(cmd string, rest []string) (o options, err error) {
	fs := flag.NewFlagSet("dns-client "+cmd, flag.ContinueOnError)
	fs.StringVar(&o.upstream, "upstream", "", "upstream DNS URL (default "+defaultUpstream+")")
	fs.StringVar(&o.bootstrap, "bootstrap", "", "bootstrap resolver address (default "+defaultBootstrap+")")
	fs.StringVar(&o.listen, "listen", "", "comma-separated loopback listen addresses")
	fs.StringVar(&o.statePath, "state", "", "state file path")

	if err = fs.Parse(rest); err != nil {
		return o, err
	}

	if o.statePath == "" {
		o.statePath = defaultStatePath()
	}

	return o, nil
}

// lifecycleFor builds a Lifecycle from the parsed options.
func (o options) lifecycleFor() (lc *lifecycle.Lifecycle) {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	var listen []string
	if o.listen != "" {
		listen = strings.Split(o.listen, ",")
	}

	return lifecycle.New(lifecycle.Config{
		Logger:    logger,
		StatePath: o.statePath,
		Upstream:  o.upstream,
		Bootstrap: o.bootstrap,
		Listen:    listen,
	})
}

func cmdEnable(args []string) (err error) {
	o, err := parseFlags("enable", args)
	if err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()

	lc := o.lifecycleFor()
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

	return o.lifecycleFor().Disable(ctx)
}

func cmdRestore(args []string) (err error) {
	o, err := parseFlags("restore", args)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return o.lifecycleFor().Restore(ctx)
}

func cmdStatus(args []string) (err error) {
	o, err := parseFlags("status", args)
	if err != nil {
		return err
	}

	lc := o.lifecycleFor()
	st, err := lc.Status()
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

	ctx, stop := signalContext()
	defer stop()

	return o.lifecycleFor().Run(ctx)
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
  enable    bind, health-check, snapshot, apply interception, run until Ctrl-C
  disable   stop resolver, restore interception, clear dirty marker
  restore   restore interception from persisted state
  status    print persisted state
  run       foreground resolver only (dev/CI)

Flags:
  --upstream <url>      upstream DNS URL
  --bootstrap <addr>    bootstrap resolver address
  --listen <addr,...>   loopback listen addresses
  --state <path>        state file path
`)
}
