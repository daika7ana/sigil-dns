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
	"strings"
	"time"

	"github.com/daika7ana/sigil-dns/internal/health"
	"github.com/daika7ana/sigil-dns/internal/intercept"
	"github.com/daika7ana/sigil-dns/internal/resolver"
	"github.com/daika7ana/sigil-dns/internal/state"
)

// ErrNotRunning is returned when a resolver-scoped operation is requested while
// no resolver is running.
var ErrNotRunning = errors.New("resolver is not running")

// ErrRestore marks a failure to remove interception.  The resolver is still
// running in that case, so callers must not stop it and point DNS at a dead
// port; the service keeps serving and can retry.
var ErrRestore = errors.New("restoring interception")

// Config configures a Lifecycle.
type Config struct {
	Logger         *slog.Logger
	StatePath      string
	ProfileName    string
	Upstreams      []string
	Bootstrap      []string
	Rules          []resolver.Rule
	Bypass         []string
	Filter         resolver.FilterConfig
	Listen         []string
	CacheSizeBytes int
	CacheDisabled  bool

	// FallbackUpstreams/FallbackBootstrap answer when the upstreams above fail.
	FallbackUpstreams []string
	FallbackBootstrap []string
}

// resolverHandle is the lifecycle's view of the running resolver.  The
// production *resolver.Resolver satisfies it; the interface exists so tests can
// inject a stub without binding sockets.
type resolverHandle interface {
	Start(ctx context.Context) error
	Shutdown(ctx context.Context) error
	SyntheticName() string
	UDPAddrs() []string
	RefreshLists(ctx context.Context) error
	ClearCache()
}

// Lifecycle owns the resolver, state store, and interception controller.
type Lifecycle struct {
	lock   *state.Lock
	res    resolverHandle
	store  *state.Store
	ic     intercept.Controller
	logger *slog.Logger
	cfg    Config

	// newResolver, checkHealth and flushDNS are the injectable dependencies;
	// New points them at production and tests replace them to stay hermetic.
	newResolver func(cfg resolver.Config) (resolverHandle, error)
	checkHealth func(ctx context.Context, t health.Target) health.Result
	flushDNS    func(ctx context.Context)
}

// New returns a Lifecycle for cfg.
func New(cfg Config) (l *Lifecycle) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default().With("component", "lifecycle")
	}

	l = &Lifecycle{
		cfg:         cfg,
		store:       state.NewStore(cfg.StatePath),
		ic:          intercept.New(logger),
		logger:      logger,
		newResolver: func(c resolver.Config) (resolverHandle, error) { return resolver.New(c) },
		checkHealth: health.Check,
	}
	l.flushDNS = l.flush

	return l
}

// Enable binds the resolver, runs the health check, snapshots state, and
// applies interception.  On success the resolver is left running and the
// single-instance lock is held until Disable.  An already-enabled session is
// left untouched.  The caller blocks on its signal context and then calls
// Disable.
func (l *Lifecycle) Enable(ctx context.Context) (err error) {
	if err = l.acquire(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			l.release()
		}
	}()

	// Enable is idempotent: a client Enable can race service startup, and a
	// running enabled session must not be torn down and re-enabled.
	if st, sErr := l.store.Load(); sErr == nil && st.Mode == state.ModeOn && l.res != nil {
		return nil
	}

	// Never re-snapshot a dirty system; restore from the persisted snapshot
	// first, while any resolver left by an earlier session is still running.
	if err = l.restore(ctx, true); err != nil {
		return err
	}

	// A resolver left by a now-disabled session is stopped before rebinding.
	if err = l.stopResolver(ctx); err != nil {
		return err
	}

	// 1. Bind listeners.
	res, err := l.startResolver(ctx)
	if err != nil {
		return err
	}

	// 2. Two-stage health check.
	hr := l.healthCheck(ctx, res)
	if !hr.OK {
		_ = l.stopResolver(ctx)

		return fmt.Errorf("health check failed: %s", hr.Detail)
	}

	// 3. Persist the atomic dirty marker.
	st := &state.State{
		SchemaVersion: state.SchemaVersion,
		Mode:          state.ModeOn,
		Intercept:     state.MechanismNRPT,
		Dirty:         true,
		Upstream:      strings.Join(l.upstreamsOrDefault(), ","),
		Health:        toStateHealth(hr),
		Bypass:        l.cfg.Bypass,
	}
	if err = l.store.Save(st); err != nil {
		_ = l.stopResolver(ctx)

		return err
	}

	// 4. Apply interception.
	if err = l.ic.Enable(ctx, l.cfg.Bypass); err != nil {
		enableErr := fmt.Errorf("enabling interception: %w", err)

		// A partial apply must not be left pointing at a resolver we are about
		// to stop.  Restore first; if that fails, leave the resolver running so
		// DNS still works and the caller can retry.
		if rErr := l.restore(ctx, true); rErr != nil {
			return errors.Join(enableErr, rErr)
		}
		if sErr := l.stopResolver(ctx); sErr != nil {
			return errors.Join(enableErr, sErr)
		}

		return enableErr
	}

	// 5. Flush DNS.
	l.flushDNS(ctx)
	// Never log upstreams: a DoT/DoQ URL carries the resolver id as a hostname
	// label, so even redacted scheme+host leaks it.  Log the profile name and
	// the upstream count instead.
	l.logger.InfoContext(ctx, "enabled", "profile", l.cfg.ProfileName, "upstreams", len(l.upstreamsOrDefault()), "health", hr.Detail)

	return nil
}

// Disable restores interception, flushes, stops the resolver, and clears the
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
	// 1. Restore interception before stopping the resolver.  If the restore
	// fails, the resolver is still up, so DNS does not point at a dead port;
	// the caller can retry.
	if iErr := l.ic.Disable(ctx); iErr != nil {
		return fmt.Errorf("%w: %v", ErrRestore, iErr)
	}

	// 2. Flush.
	l.flushDNS(ctx)

	// 3. Stop resolver.
	if sErr := l.stopResolver(ctx); sErr != nil {
		return sErr
	}

	// 4. Clear dirty marker.
	return l.markClean()
}

// Restore undoes interception from the persisted state.  It is idempotent and
// keeps the dirty marker set on partial failure.
func (l *Lifecycle) Restore(ctx context.Context) (err error) {
	// A running service already holds the lock; only drop it when this call
	// acquired it, or a concurrent client could race the live session.
	acquired := l.lock == nil
	if err = l.acquire(); err != nil {
		return err
	}
	if acquired {
		defer l.release()
	}

	return l.restore(ctx, false)
}

// restore removes interception and clears the dirty marker.  If onlyIfDirty is
// true it is a no-op when the state is clean.
func (l *Lifecycle) restore(ctx context.Context, onlyIfDirty bool) (err error) {
	st, lErr := l.store.Load()
	dirty := lErr == nil && st.Dirty
	if lErr != nil && !errors.Is(lErr, state.ErrNoState) {
		// A corrupt state file must not block cleanup (M2): make a best-effort
		// attempt to undo any partial interception, then surface the original
		// load error.  Missing state keeps its existing behaviour below.
		_ = l.ic.Disable(ctx)

		return lErr
	}
	if onlyIfDirty && !dirty {
		return nil
	}

	if err = l.ic.Disable(ctx); err != nil {
		return fmt.Errorf("restoring interception (state kept dirty): %w", err)
	}

	l.flushDNS(ctx)

	if dirty {
		st.Mode = state.ModeOff
		st.Dirty = false

		return l.store.Save(st)
	}

	return nil
}

// Startup performs crash recovery: a running service always means protection
// is on, so the caller must Enable next.  If the dirty marker is set it
// restores first.  The lock is retained for the caller's Enable; a failure
// releases it.
func (l *Lifecycle) Startup(ctx context.Context) (err error) {
	if err = l.acquire(); err != nil {
		return err
	}

	if err = l.restore(ctx, true); err != nil {
		l.release()

		return err
	}

	return nil
}

// Run starts the resolver in the foreground without touching interception.
// It blocks until ctx is canceled.  Intended for dev and CI.
func (l *Lifecycle) Run(ctx context.Context) (err error) {
	if err = l.acquire(); err != nil {
		return err
	}
	defer l.release()

	res, err := l.startResolver(ctx)
	if err != nil {
		return err
	}

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

// ClearCache clears the running resolver's cache and the OS DNS cache.  A
// stopped resolver still gets the OS flush: there is no in-process cache to
// clear, and the Windows cache may hold an answer that predates a rule change.
func (l *Lifecycle) ClearCache(ctx context.Context) (err error) {
	if l.res != nil {
		l.res.ClearCache()
	}

	return FlushDNS(ctx)
}

// resolverConfig builds the resolver configuration from the lifecycle config.
func (l *Lifecycle) resolverConfig() (cfg resolver.Config) {
	return resolver.Config{
		Logger:         l.logger,
		Upstreams:      l.upstreamsOrDefault(),
		Bootstrap:      l.bootstrapOrDefault(),
		Rules:          l.cfg.Rules,
		Filter:         l.cfg.Filter,
		Listen:         l.cfg.Listen,
		CacheSizeBytes: l.cfg.CacheSizeBytes,
		CacheDisabled:  l.cfg.CacheDisabled,

		FallbackUpstreams: l.cfg.FallbackUpstreams,
		FallbackBootstrap: l.cfg.FallbackBootstrap,
	}
}

// healthCheck runs the two-stage health check against res.
func (l *Lifecycle) healthCheck(ctx context.Context, res resolverHandle) (r health.Result) {
	timeout := 10 * time.Second
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return l.checkHealth(c, health.Target{
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

// markClean clears the dirty marker.  A missing state file is not an error.
func (l *Lifecycle) markClean() (err error) {
	if err = l.store.MarkClean(); errors.Is(err, state.ErrNoState) {
		return nil
	}

	return err
}

// startResolver binds and starts the resolver, shutting it down if the bind
// succeeded but Start failed.  It leaves l.res set on success.
func (l *Lifecycle) startResolver(ctx context.Context) (res resolverHandle, err error) {
	res, err = l.newResolver(l.resolverConfig())
	if err != nil {
		return nil, err
	}
	if err = res.Start(ctx); err != nil {
		_ = res.Shutdown(ctx)

		return nil, err
	}
	l.res = res

	return res, nil
}

// acquire takes the single-instance lock, or is a no-op if already held.
func (l *Lifecycle) acquire() (err error) {
	if l.lock != nil {
		return nil
	}

	l.lock, err = state.AcquireLock(state.LockPath(l.cfg.StatePath))

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
	if err := FlushDNS(ctx); err != nil {
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
