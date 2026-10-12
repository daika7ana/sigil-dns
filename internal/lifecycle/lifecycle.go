// Package lifecycle orchestrates the enable/disable/restore sequence.
//
// Enable follows the strict order from the spec: bind listeners, run the
// two-stage health check, persist the snapshot and atomic dirty marker, apply
// interception, then flush DNS.  Nothing touches interception until the
// resolver is proven listening and forwarding.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/daika7ana/dns-client/internal/health"
	"github.com/daika7ana/dns-client/internal/intercept"
	"github.com/daika7ana/dns-client/internal/resolver"
	"github.com/daika7ana/dns-client/internal/state"
)

// ErrNotRunning is returned when a resolver-scoped operation is requested while
// no resolver is running.
var ErrNotRunning = errors.New("resolver is not running")

// Config configures a Lifecycle.
type Config struct {
	Logger         *slog.Logger
	StatePath      string
	ProfileName    string
	Upstreams      []string
	Bootstrap      []string
	Rules          []resolver.Rule
	Filter         resolver.FilterConfig
	Listen         []string
	CacheSizeBytes int
	CacheDisabled  bool
}

// Lifecycle owns the resolver, state store, and interception controller.
type Lifecycle struct {
	lock   *state.Lock
	res    *resolver.Resolver
	store  *state.Store
	ic     intercept.Controller
	logger *slog.Logger
	cfg    Config
}

// New returns a Lifecycle for cfg.
func New(cfg Config) (l *Lifecycle) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "lifecycle")
	}

	return &Lifecycle{
		cfg:    cfg,
		store:  state.NewStore(cfg.StatePath),
		ic:     intercept.New(),
		logger: logger,
	}
}

// Enable binds the resolver, runs the health check, snapshots state, and
// applies interception.  On success the resolver is left running and the
// single-instance lock is held until Disable.  The caller blocks on its signal
// context and then calls Disable.
func (l *Lifecycle) Enable(ctx context.Context) (err error) {
	if err = l.acquire(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			l.release()
		}
	}()

	// Never re-snapshot a dirty system; restore from the persisted snapshot
	// first.
	if err = l.restore(ctx, true); err != nil {
		return err
	}

	// 1. Bind listeners.
	res, err := l.newResolver()
	if err != nil {
		return err
	}
	if err = res.Start(ctx); err != nil {
		_ = res.Shutdown(ctx)

		return err
	}
	l.res = res

	// 2. Two-stage health check.
	hr := l.healthCheck(ctx, res)
	if !hr.OK {
		_ = l.stopResolver(ctx)

		return fmt.Errorf("health check failed: %s", hr.Detail)
	}

	// 3. Persist snapshot + atomic dirty marker.
	st := &state.State{
		SchemaVersion: state.SchemaVersion,
		Mode:          state.ModeOn,
		Intercept:     state.MechanismNRPT,
		Snapshot:      &state.Snapshot{},
		Dirty:         true,
		Upstream:      strings.Join(l.upstreamsOrDefault(), ","),
		Health:        toStateHealth(hr),
	}
	if err = l.store.Save(st); err != nil {
		_ = l.stopResolver(ctx)

		return err
	}

	// 4. Apply interception.
	if err = l.ic.Enable(ctx); err != nil {
		// Keep the dirty marker so a later restore undoes any partial change.
		_ = l.stopResolver(ctx)

		return fmt.Errorf("enabling interception: %w", err)
	}

	// 5. Flush DNS.
	l.flush(ctx)
	// Never log upstreams: a DoT/DoQ URL carries the resolver id as a hostname
	// label, so even redacted scheme+host leaks it.  Log the profile name and
	// the upstream count instead.
	l.logger.InfoContext(ctx, "enabled", "profile", l.cfg.ProfileName, "upstreams", len(l.upstreamsOrDefault()), "health", hr.Detail)

	return nil
}

// Disable stops the resolver, restores interception, flushes, and clears the
// dirty marker.  It is idempotent.
func (l *Lifecycle) Disable(ctx context.Context) (err error) {
	if err = l.acquire(); err != nil {
		return err
	}
	defer l.release()

	return l.disable(ctx)
}

// disable implements Disable without taking the lock.
func (l *Lifecycle) disable(ctx context.Context) (err error) {
	// 1. Stop resolver.
	errs := []error{l.stopResolver(ctx)}

	// 2. Restore interception.
	if iErr := l.ic.Disable(ctx); iErr != nil {
		// Keep dirty on partial failure.
		errs = append(errs, fmt.Errorf("restoring interception: %w", iErr))

		return errors.Join(errs...)
	}

	// 3. Flush.
	l.flush(ctx)

	// 4. Clear dirty marker.
	errs = append(errs, l.markClean(ctx))

	return errors.Join(errs...)
}

// Restore undoes interception from the persisted state.  It is idempotent and
// keeps the dirty marker set on partial failure.
func (l *Lifecycle) Restore(ctx context.Context) (err error) {
	if err = l.acquire(); err != nil {
		return err
	}
	defer l.release()

	return l.restore(ctx, false)
}

// restore removes interception and clears the dirty marker.  If onlyIfDirty is
// true it is a no-op when the state is clean.
func (l *Lifecycle) restore(ctx context.Context, onlyIfDirty bool) (err error) {
	st, lErr := l.store.Load()
	dirty := lErr == nil && st.Dirty
	if lErr != nil && !errors.Is(lErr, state.ErrNoState) {
		return lErr
	}
	if onlyIfDirty && !dirty {
		return nil
	}

	if err = l.ic.Disable(ctx); err != nil {
		return fmt.Errorf("restoring interception (state kept dirty): %w", err)
	}

	l.flush(ctx)

	if dirty {
		st.Mode = state.ModeOff
		st.Dirty = false

		return l.store.Save(st)
	}

	return nil
}

// Startup performs crash recovery: if the dirty marker is set it restores
// first, then reports whether mode=on so the caller can re-enable.  On a
// re-enable the lock is retained for the caller; otherwise it is released.
func (l *Lifecycle) Startup(ctx context.Context) (reenable bool, err error) {
	if err = l.acquire(); err != nil {
		return false, err
	}

	if err = l.restore(ctx, true); err != nil {
		l.release()

		return false, err
	}

	st, sErr := l.store.Load()
	if sErr != nil && !errors.Is(sErr, state.ErrNoState) {
		l.release()

		return false, sErr
	}

	if sErr == nil && st.Mode == state.ModeOn {
		return true, nil
	}

	l.release()

	return false, nil
}

// Run starts the resolver in the foreground without touching interception.
// It blocks until ctx is canceled.  Intended for dev and CI.
func (l *Lifecycle) Run(ctx context.Context) (err error) {
	if err = l.acquire(); err != nil {
		return err
	}
	defer l.release()

	res, err := l.newResolver()
	if err != nil {
		return err
	}
	if err = res.Start(ctx); err != nil {
		_ = res.Shutdown(ctx)

		return err
	}
	l.res = res

	l.logger.InfoContext(ctx, "resolver running", "listen", res.UDPAddrs())

	<-ctx.Done()

	return l.stopResolver(context.Background())
}

// Status returns the persisted state, or state.ErrNoState.
func (l *Lifecycle) Status() (st *state.State, err error) { return l.store.Load() }

// RefreshLists re-downloads the running resolver's filter lists and rebuilds
// its filter engine (SPEC §15.1).  It returns ErrNotRunning when no resolver is
// active.
func (l *Lifecycle) RefreshLists(ctx context.Context) (err error) {
	if l.res == nil {
		return ErrNotRunning
	}

	return l.res.RefreshLists(ctx)
}

// newResolver builds a resolver from the config.
func (l *Lifecycle) newResolver() (res *resolver.Resolver, err error) {
	return resolver.New(resolver.Config{
		Logger:         l.logger,
		Upstreams:      l.upstreamsOrDefault(),
		Bootstrap:      l.bootstrapOrDefault(),
		Rules:          l.cfg.Rules,
		Filter:         l.cfg.Filter,
		Listen:         l.cfg.Listen,
		CacheSizeBytes: l.cfg.CacheSizeBytes,
		CacheDisabled:  l.cfg.CacheDisabled,
	})
}

// healthCheck runs the two-stage health check against res.
func (l *Lifecycle) healthCheck(ctx context.Context, res *resolver.Resolver) (r health.Result) {
	timeout := 10 * time.Second
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return health.Check(c, health.Target{
		Synthetic: res.SyntheticName(),
		UDPAddrs:  res.UDPAddrs(),
		Timeout:   5 * time.Second,
	})
}

// stopResolver shuts the resolver down if running.
func (l *Lifecycle) stopResolver(ctx context.Context) (err error) {
	if l.res == nil {
		return nil
	}

	err = l.res.Shutdown(ctx)
	l.res = nil

	return err
}

// markClean clears the dirty marker.
func (l *Lifecycle) markClean(ctx context.Context) (err error) {
	st, err := l.store.Load()
	if err != nil {
		if errors.Is(err, state.ErrNoState) {
			return nil
		}

		return err
	}
	st.Mode = state.ModeOff
	st.Dirty = false

	return l.store.Save(st)
}

// acquire takes the single-instance lock, or is a no-op if already held.
func (l *Lifecycle) acquire() (err error) {
	if l.lock != nil {
		return nil
	}

	l.lock, err = state.AcquireLock(l.cfg.StatePath + ".lock")

	return err
}

// release drops the single-instance lock.
func (l *Lifecycle) release() {
	if l.lock != nil {
		_ = l.lock.Release()
		l.lock = nil
	}
}

// flush flushes the OS DNS cache.  It is a no-op off Windows.
func (l *Lifecycle) flush(ctx context.Context) {
	if runtime.GOOS != "windows" {
		return
	}

	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := exec.CommandContext(c, "ipconfig", "/flushdns").Run(); err != nil {
		l.logger.WarnContext(ctx, "ipconfig /flushdns failed", "err", err)
	}
}

// upstreamsOrDefault returns the configured upstreams or the resolver default.
func (l *Lifecycle) upstreamsOrDefault() (ups []string) {
	if len(l.cfg.Upstreams) > 0 {
		return l.cfg.Upstreams
	}

	return []string{resolver.DefaultUpstream}
}

// bootstrapOrDefault returns the configured bootstrap or the default.
func (l *Lifecycle) bootstrapOrDefault() (b []string) {
	if len(l.cfg.Bootstrap) > 0 {
		return l.cfg.Bootstrap
	}

	return []string{resolver.DefaultBootstrap}
}

// toStateHealth converts a health result for persistence.
func toStateHealth(r health.Result) (h *state.Health) {
	return &state.Health{
		Ours:   r.Ours,
		IPv4:   r.IPv4,
		IPv6:   r.IPv6,
		Detail: r.Detail,
	}
}
