// Command sigildns is the CLI for the sigildns loopback DNS resolver.
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
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/daika7ana/sigil-dns/internal/config"
	"github.com/daika7ana/sigil-dns/internal/drift"
	"github.com/daika7ana/sigil-dns/internal/elevate"
	"github.com/daika7ana/sigil-dns/internal/filter"
	"github.com/daika7ana/sigil-dns/internal/gui"
	"github.com/daika7ana/sigil-dns/internal/ipc"
	"github.com/daika7ana/sigil-dns/internal/lifecycle"
	"github.com/daika7ana/sigil-dns/internal/logging"
	"github.com/daika7ana/sigil-dns/internal/providers"
	"github.com/daika7ana/sigil-dns/internal/redact"
	"github.com/daika7ana/sigil-dns/internal/resolver"
	"github.com/daika7ana/sigil-dns/internal/service"
	"github.com/daika7ana/sigil-dns/internal/state"
	"github.com/daika7ana/sigil-dns/internal/tray"
)

// Default upstream settings shown in help text.
const (
	defaultUpstream  = resolver.DefaultUpstream
	defaultBootstrap = resolver.DefaultBootstrap
)

// The Windows binary embeds an application manifest requesting
// requireAdministrator, so every launch elevates (UAC). The generated .syso is
// committed; regenerate from the repo root with:
//
//	./dev/go generate ./cmd/sigildns
//
//go:generate go run github.com/akavel/rsrc@v0.10.2 -manifest sigildns.manifest -ico ../../assets/sigildns.ico -arch amd64 -o rsrc_windows_amd64.syso
func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "sigildns:", err)
		os.Exit(1)
	}
}

func run(args []string) (err error) {
	if len(args) == 0 {
		// A -tags gui build makes the status window the default UI for a
		// no-argument launch; the console build's gui.Run is a stub, so this
		// falls back to CLI usage.
		if guiErr := gui.Run(); !errors.Is(guiErr, gui.ErrNotBuilt) {
			return guiErr
		}

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
	case "reload":
		return cmdReload(rest)
	case "refresh":
		return cmdRefresh(rest)
	case "run":
		return cmdRun(rest)
	case "providers":
		return cmdProviders(rest)
	case "config":
		return cmdConfig(rest)
	case "service":
		return cmdService(rest)
	case "--service":
		return cmdServiceHost(rest)
	case "--apply":
		return cmdApply(rest)
	case "--gui":
		return cmdGUI(rest)
	case "--tray":
		// A gui build runs the native tray host (no window, no GL); other
		// builds keep the retired tray stub, which reports ErrNotBuilt.
		if guiErr := gui.RunTrayHost(); !errors.Is(guiErr, gui.ErrNotBuilt) {
			return guiErr
		}

		return cmdTray(rest)
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
	summary    bool
}

// parseFlags parses rest for cmd, registering only the flags cmd supports.
func parseFlags(cmd string, rest []string) (o options, err error) {
	fs := flag.NewFlagSet("sigildns "+cmd, flag.ContinueOnError)

	switch cmd {
	case "enable", "run", "service":
		fs.StringVar(&o.upstream, "upstream", "", "upstream DNS URL (default "+defaultUpstream+")")
		fs.StringVar(&o.bootstrap, "bootstrap", "", "bootstrap resolver address (default "+defaultBootstrap+")")
		fs.StringVar(&o.profile, "profile", "", "config profile name (default the config's active profile)")
		fs.StringVar(&o.listen, "listen", "", "comma-separated loopback listen addresses")
		fs.StringVar(&o.statePath, "state", "", "state file path")
		fs.StringVar(&o.configPath, "config", "", "config file path (default "+defaultConfigPath()+")")
	case "disable", "restore":
		fs.StringVar(&o.statePath, "state", "", "state file path")
	case "status":
		fs.StringVar(&o.statePath, "state", "", "state file path")
		fs.StringVar(&o.configPath, "config", "", "config file path (default "+defaultConfigPath()+")")
		fs.BoolVar(&o.summary, "summary", false, "status: print the sanitized config summary")
	case "config":
		fs.StringVar(&o.configPath, "config", "", "config file path (default "+defaultConfigPath()+")")
	}

	if err = fs.Parse(rest); err != nil {
		return o, err
	}

	if o.statePath == "" {
		o.statePath = defaultStatePath()
	}
	if o.configPath == "" {
		o.configPath = defaultConfigPath()
	}

	return o, nil
}

// logger returns the CLI logger.
func logger() (l *slog.Logger) {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// fallbackLogger is the fail-open logger used when the config cannot be
// resolved.  It still scrubs URLs/stamps from the stderr stream.
func fallbackLogger() *slog.Logger {
	return slog.New(redact.Handler(logger().Handler(), nil))
}

// loggerFor builds the service's durable, redacting file logger from an
// already-resolved config.  It is fail-open: an unusable file yields a stderr
// logger (LOGGING_PLAN §1.5/§4.5).  The closer is nil when no file was opened.
func loggerFor(resolved config.Resolved) (l *slog.Logger, closer io.Closer) {
	l, closer, err := logging.Open(logging.Options{
		Level:     resolved.Log.Level,
		Path:      resolved.Log.Path,
		MaxSizeMB: resolved.Log.MaxSizeMB,
		KeepMB:    resolved.Log.KeepMB,
		Secrets:   resolverSecrets(resolved),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "sigildns: file logging unavailable, using stderr:", err)
	}

	return l, closer
}

// resolverSecrets collects every resolver id recoverable from the active
// profile and its per-domain rules, to seed the redacting handler.  A miss on
// one upstream never drops the others, and absent provider metadata falls back
// to URL path/host-label extraction.  The ids are scrub seeds only; they are
// never logged themselves.
func resolverSecrets(r config.Resolved) (secrets []string) {
	ups := make([]string, 0, len(r.Upstreams)+len(r.PerDomain))
	ups = append(ups, r.Upstreams...)
	for _, pd := range r.PerDomain {
		if pd.Upstream != "" {
			ups = append(ups, pd.Upstream)
		}
	}

	proto := providers.Protocol(strings.TrimSpace(r.Protocol))

	secrets = providers.ExtractIDs(strings.TrimSpace(r.Provider), proto, ups)

	// The fallback profile's upstreams carry their own resolver id, parsed
	// with that profile's metadata, so it must be scrubbed too.
	if len(r.FallbackUpstreams) > 0 {
		fbProto := providers.Protocol(strings.TrimSpace(r.FallbackProtocol))
		secrets = append(secrets, providers.ExtractIDs(
			strings.TrimSpace(r.FallbackProvider), fbProto, r.FallbackUpstreams,
		)...)
	}

	return secrets
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

// planPerDomain splits the enabled per-domain rules into resolver routing rules
// and the bypass domains that must be resolved by the system's own DNS.
func planPerDomain(pd []config.PerDomain) (rules []resolver.Rule, bypass []string) {
	for _, p := range pd {
		if !p.IsEnabled() {
			continue
		}
		if p.Bypass {
			bypass = append(bypass, p.Domains...)

			continue
		}
		rules = append(rules, resolver.Rule{Domains: p.Domains, Upstream: p.Upstream})
	}

	return rules, bypass
}

// lifecycleFor builds a Lifecycle using resolved settings and the given logger.
// The service passes its durable file logger; CLI commands pass the stderr
// logger (only the service writes the log, LOGGING_PLAN §1.1).
func (o options) lifecycleFor(r config.Resolved, l *slog.Logger) (lc *lifecycle.Lifecycle) {
	rules, bypass := planPerDomain(r.PerDomain)

	lists := make([]filter.List, 0, len(r.Filtering.Lists))
	for _, l := range r.Filtering.Lists {
		if !l.Enabled {
			continue
		}
		lists = append(lists, filter.List{Source: l.URL})
	}

	return lifecycle.New(lifecycle.Config{
		Logger:      l,
		StatePath:   o.statePath,
		ProfileName: r.ProfileName,
		Upstreams:   r.Upstreams,
		Bootstrap:   r.Bootstrap,
		Rules:       rules,
		Bypass:      bypass,
		Filter: resolver.FilterConfig{
			Enabled:       r.Filtering.Enabled,
			Lists:         lists,
			BlockResponse: r.Filtering.BlockResponse,
			TTL:           r.Filtering.BlockTTL,
		},
		Listen:         o.listenAddrs(),
		CacheSizeBytes: r.Cache.SizeBytes,
		CacheDisabled:  !r.Cache.Enabled,

		FallbackUpstreams: r.FallbackUpstreams,
		FallbackBootstrap: r.FallbackBootstrap,
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

	lc := o.lifecycleFor(resolved, logger())
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

	if o.summary {
		return printConfigSummary(o.configPath)
	}

	st, err := state.NewStore(o.statePath).Load()
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

// printConfigSummary prints the sanitized config summary, preferring the running
// service's status pipe and falling back to a local, read-only summary.
func printConfigSummary(configPath string) (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if sum, cErr := ipc.NewClient(ipc.StatusEndpoint()).ConfigSummary(ctx); cErr == nil {
		return printSummary(sum)
	}

	sum := ipc.LoadSummary(configPath)

	return printSummary(&sum)
}

// printSummary JSON-encodes a config summary to stdout.
func printSummary(sum *ipc.ConfigSummary) (err error) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	return enc.Encode(sum)
}

// cmdReload asks the running service (control pipe) to reload its config.
func cmdReload(args []string) (err error) {
	if _, err = parseFlags("reload", args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	sum, err := ipc.NewClient(ipc.DefaultEndpoint()).Reload(ctx)
	if err != nil {
		return fmt.Errorf("reload failed (is the Sigil DNS service running?): %w", err)
	}

	return printSummary(sum)
}

// cmdRefresh asks the running service (control pipe) to re-download its filter
// lists and rebuild the filter engine.
func cmdRefresh(args []string) (err error) {
	if _, err = parseFlags("refresh", args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err = ipc.NewClient(ipc.DefaultEndpoint()).RefreshLists(ctx); err != nil {
		return fmt.Errorf("refresh failed (is the Sigil DNS service running?): %w", err)
	}

	fmt.Fprintln(os.Stderr, "filter lists refreshed")

	return nil
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

	return o.lifecycleFor(resolved, logger()).Run(ctx)
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

// cmdService manages the Windows service via the SCM.
func cmdService(args []string) (err error) {
	if len(args) == 0 {
		return errors.New("usage: service <install|uninstall|start|stop>")
	}

	switch sub := args[0]; sub {
	case "install":
		return service.Install()
	case "uninstall":
		// Fail-closed: elevate.ServiceUninstall restores interception, stops
		// and confirms the service, proves interception is down, then deletes.
		return elevate.ServiceUninstall()
	case "start":
		return service.Start()
	case "stop":
		// Verify interception is actually down, not just that the SCM reports
		// Stopped (the handler's shutdown restore is best-effort).
		return elevate.ServiceStop()
	default:
		return fmt.Errorf("unknown service subcommand %q", sub)
	}
}

// svcDaemon is the lifecycle surface the service wrapper drives.
// *lifecycle.Lifecycle satisfies it; tests inject fakes.
type svcDaemon interface {
	Status() (st *state.State, err error)
	Startup(ctx context.Context) (err error)
	Enable(ctx context.Context) (err error)
	Disable(ctx context.Context) (err error)
	Restore(ctx context.Context) (err error)
	RefreshLists(ctx context.Context) (err error)
	ClearCache(ctx context.Context) (err error)
}

// serviceLifecycle lets crash recovery run even when the config is corrupt:
// Startup/Status/Disable work from persisted state alone, while Enable reports
// a clear error until the config loads (and the service then stops).  It also
// implements reload (SPEC §15.1): swap when idle, or restore + re-enable with
// rollback when active; if the rollback also fails the service stops.
type serviceLifecycle struct {
	svcDaemon
	build     func() (lc svcDaemon, sum ipc.ConfigSummary, err error)
	enableErr error

	// stop asks the service host to stop when a reload cannot restore
	// protection (both the new enable and the rollback failed).
	stop func()

	// logger is the service's redacting logger; Reload re-seeds it on rollback
	// so the restored profile's id stays scrubbed.
	logger *slog.Logger
}

// Enable implements service.Lifecycle.
func (s *serviceLifecycle) Enable(ctx context.Context) (err error) {
	if s.enableErr != nil {
		// Startup retained the single-instance lock; release it so a bad config
		// cannot collide with the SCM's restart attempt.
		_ = s.svcDaemon.Disable(ctx)

		return s.enableErr
	}

	return s.svcDaemon.Enable(ctx)
}

// Reload re-reads the config and swaps in a freshly built lifecycle (SPEC §15.1).
func (s *serviceLifecycle) Reload(ctx context.Context) (sum ipc.ConfigSummary, err error) {
	if s.build == nil {
		return ipc.ConfigSummary{}, errors.New("reload is not configured")
	}

	// Capture the active profile's seeds before build swaps to the new profile.
	oldSecrets := logging.Secrets(s.logger)

	newInner, sum, err := s.build()
	if err != nil {
		// Keep the current lifecycle; the summary carries the secret-free error.
		return sum, err
	}

	// build replaced the seeds with the new profile's.  Install the union while
	// the old lifecycle still serves, so no line logged in the swap window
	// (e.g. a DoT/DoQ upstream timeout) carries an unscrubbed old id.
	newSecrets := logging.Secrets(s.logger)
	logging.UpdateSecrets(s.logger, append(append([]string{}, oldSecrets...), newSecrets...))

	st, sErr := s.svcDaemon.Status()
	enabled := sErr == nil && st != nil && st.Mode == state.ModeOn

	if !enabled {
		// Idle: just replace the in-memory config; only the new profile is live.
		logging.UpdateSecrets(s.logger, newSecrets)

		s.svcDaemon = newInner
		s.enableErr = nil

		return sum, nil
	}

	// Active: restore, then enable with the new config; roll back to the
	// last-known-good config on failure.
	old := s.svcDaemon
	if err = old.Disable(ctx); err != nil {
		// The swap never happened, so the old profile is still live.
		logging.UpdateSecrets(s.logger, oldSecrets)

		// A failed restore leaves the old resolver running (and probably still
		// protected); stopping now would point DNS at a dead port.  Only stop
		// when the failure came after interception was removed.
		if !errors.Is(err, lifecycle.ErrRestore) && s.stop != nil {
			s.stop()
		}

		return sum, fmt.Errorf("reload: disable: %w", err)
	}
	if err = newInner.Enable(ctx); err != nil {
		// The old profile is active again after rollback, so restore its seeds
		// (dropping the new profile's) before re-enabling.
		logging.UpdateSecrets(s.logger, oldSecrets)

		if rErr := old.Enable(ctx); rErr != nil {
			// Leave the dirty marker set for crash recovery.  Stop the service:
			// it must never keep running with interception off.
			if s.stop != nil {
				s.stop()
			}

			return sum, fmt.Errorf("reload: enable failed (%v); rollback also failed: %w", err, rErr)
		}

		return sum, fmt.Errorf("reload: enable failed, rolled back: %w", err)
	}

	// Swap is final: keep only the new profile's seeds.
	logging.UpdateSecrets(s.logger, newSecrets)

	s.svcDaemon = newInner
	s.enableErr = nil

	return sum, nil
}

// serviceConfig loads and resolves the service's config, returning a sanitized
// summary and a secret-free error.  A raw YAML error can quote a config value
// (possibly a resolver id) and this error can reach the unelevated helper's
// Result.Err, so it must never be returned verbatim.
func serviceConfig(configPath, profile, upstream, bootstrap string) (sum ipc.ConfigSummary, resolved config.Resolved, err error) {
	cfg, lErr := config.Load(configPath)
	if lErr != nil && !errors.Is(lErr, config.ErrNoConfig) {
		return ipc.ConfigSummary{
			ConfigValid: false,
			ConfigError: ipc.SanitizedConfigError(lErr),
			Revision:    ipc.Revision(configPath),
		}, config.Resolved{}, errors.New(ipc.SanitizedConfigError(lErr))
	}

	resolved, rErr := config.Resolve(cfg, config.Overrides{
		Profile:   profile,
		Upstream:  upstream,
		Bootstrap: bootstrap,
	})
	if rErr != nil {
		return ipc.ConfigSummary{
			ConfigValid: false,
			ConfigError: "config could not be resolved",
			Revision:    ipc.Revision(configPath),
		}, config.Resolved{}, errors.New("config could not be resolved")
	}

	effCfg := cfg
	if effCfg == nil {
		effCfg = config.Default()
	}
	sum = ipc.Summarize(effCfg)
	sum.ConfigValid = true
	sum.Revision = ipc.Revision(configPath)

	return sum, resolved, nil
}

// cmdServiceHost runs the service host.  It is invoked by the SCM (via the
// registered "--service" argument), not directly.
func cmdServiceHost(args []string) (err error) {
	o, err := parseFlags("service", args)
	if err != nil {
		return err
	}

	// Pre-flight single-instance guard: take and immediately release the state
	// lock before opening the log file, so a second --service instance exits
	// before it can overlap the first instance's file logger.  The lifecycle
	// re-acquires the lock lazily on its first operation.
	lock, lockErr := state.AcquireLock(state.LockPath(o.statePath))
	if lockErr != nil {
		return lockErr
	}
	if err = lock.Release(); err != nil {
		return err
	}

	// Load and resolve the config once at startup; the same result seeds the log
	// file and the initial lifecycle.  A bad config still starts the host for
	// crash recovery, using the fail-open stderr logger.
	_, resolved, cfgErr := serviceConfig(o.configPath, o.profile, o.upstream, o.bootstrap)

	var l *slog.Logger
	var closer io.Closer
	if cfgErr != nil {
		l = fallbackLogger()
	} else {
		l, closer = loggerFor(resolved)
		if closer != nil {
			defer func() { _ = closer.Close() }()
		}
	}

	// build reloads the config and constructs a lifecycle + sanitized summary.
	build := func() (lc svcDaemon, sum ipc.ConfigSummary, err error) {
		sum, resolved, cfgErr := serviceConfig(o.configPath, o.profile, o.upstream, o.bootstrap)
		if cfgErr != nil {
			return nil, sum, cfgErr
		}

		// Track the active profile on every (re)build so redaction seeds never
		// go stale after a reload.
		logging.UpdateSecrets(l, resolverSecrets(resolved))

		return o.lifecycleFor(resolved, l), sum, nil
	}

	// Never refuse to run crash recovery on a bad config: start the host so
	// restore still runs, then let the failed enable stop the service (a
	// running service must never have interception off).
	slc := &serviceLifecycle{build: build, logger: l}

	if cfgErr != nil {
		l.Error("config unavailable; service will stop after recovery", "err", ipc.SanitizedConfigError(cfgErr))

		slc.svcDaemon = o.lifecycleForState()
		slc.enableErr = fmt.Errorf("%w: %v", service.ErrConfigUnavailable, cfgErr)
	} else {
		slc.svcDaemon = o.lifecycleFor(resolved, l)
	}

	agent := service.NewAgent(slc, l, "")
	slc.stop = agent.RequestStop
	agent.SetConfigPath(o.configPath)
	agent.SetReloader(slc.Reload)
	agent.SetWatcher(drift.NewSystemReconciler(o.statePath, l, agent.RequestStop, agent.Interlock()))

	return service.Run(agent)
}

// cmdApply is the elevated one-shot helper entry point (SPEC §15.2).  It is
// invoked with the staging request path by the unelevated elevate.Apply call.
// It runs headless: it never initialises a UI, and the request (which may carry
// settings) never appears on the command line.
func cmdApply(args []string) (err error) {
	if len(args) != 1 {
		return errors.New("usage: sigildns --apply <staging-request-path>")
	}

	return elevate.HelperMain(args[0])
}

// cmdGUI starts the read-only go-gui status window (SPEC §15).  It is inert
// unless the binary was built with -tags gui (gui.Run returns ErrNotBuilt).
func cmdGUI(args []string) (err error) {
	if _, err = parseFlags("gui", args); err != nil {
		return err
	}

	return gui.Run()
}

// cmdTray starts the tray UI, which talks to the service over IPC.
func cmdTray(args []string) (err error) {
	if _, err = parseFlags("tray", args); err != nil {
		return err
	}

	ctx, stop := signalContext()
	defer stop()

	return tray.Run(ctx, ipc.NewClient(""))
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
	fs := flag.NewFlagSet("sigildns providers show", flag.ContinueOnError)
	id := fs.String("id", "", "resolver id")
	upstream := fs.String("upstream", "", "custom upstream URL")
	bootstrap := fs.String("bootstrap", "", "comma-separated bootstrap addresses")

	// A FlagSet stops at the first positional, so parse in a loop to keep
	// allowing flags on either side of the selection.
	var spec string
	for {
		if err = fs.Parse(args); err != nil {
			return err
		}

		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		if spec != "" {
			return fmt.Errorf("unexpected argument %q", rest[0])
		}

		spec = rest[0]
		args = rest[1:]
	}

	if spec == "" {
		return errors.New("usage: providers show <provider>[/<variant>[/<protocol>]] [--id x]")
	}

	parts := strings.Split(spec, "/")
	if len(parts) > 3 {
		return fmt.Errorf("invalid selection %q", spec)
	}

	sel := providers.Selection{Provider: parts[0], ID: *id, URL: *upstream}
	if len(parts) > 1 {
		sel.Variant = parts[1]
	}
	if len(parts) > 2 {
		sel.Protocol = providers.Protocol(parts[2])
	}
	for b := range strings.SplitSeq(*bootstrap, ",") {
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

// defaultConfigPath returns the hardened, machine-wide config path when it can
// be resolved, else the platform default.  On Windows this is the same
// ProgramData-KnownFolder path the elevated helper uses, so the config the
// service reads and the helper writes never diverge.
func defaultConfigPath() (p string) {
	if p = config.SystemPath(); p != "" {
		return p
	}

	return config.DefaultPath()
}

// defaultStatePath returns the platform-appropriate state file path.  On
// Windows config.SystemStatePath is the ProgramData-KnownFolder path matching
// the elevated helper's, preserving the state-lock exclusion.
func defaultStatePath() (p string) { return config.SystemStatePath() }

// usage prints CLI help.
func usage() {
	fmt.Fprint(os.Stderr, `sigildns - system-wide encrypted DNS on loopback

Usage:
  sigildns <command> [flags]

Commands:
  enable             bind, health-check, snapshot, apply interception, run until Ctrl-C
  disable            stop resolver, restore interception, clear dirty marker
  restore            restore interception from persisted state
  status             print persisted state (--summary: sanitized config summary)
  reload             reload config.yaml via the service (admin)
  refresh            re-download filter lists via the service (admin)
  run                foreground resolver only (dev/CI)
  providers list     list provider presets
  providers show     show generated endpoints for provider[/variant[/protocol]]
  config path        print the resolved config path
  config init        write a default config (never clobbers)
  service install    register the Windows service (runs as LocalSystem)
  service uninstall  remove the Windows service
  service start      start the Windows service
  service stop       stop the Windows service
  --service          run the service host (invoked by the SCM)
  --gui              run the status window (requires a -tags gui build)
  --tray             start hidden to the system tray (gui build; tray setting on)

Flags:
  --upstream <url>      upstream DNS URL (highest precedence)
  --bootstrap <addr,...> bootstrap resolver address(es)
  --config <path>       config file path
  --profile <name>      config profile to use
  --listen <addr,...>   loopback listen addresses
  --state <path>        state file path
`)
}
