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
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/daika7ana/sigil-dns/internal/health"
	"github.com/daika7ana/sigil-dns/internal/intercept"
	"github.com/daika7ana/sigil-dns/internal/resolver"
	"github.com/daika7ana/sigil-dns/internal/state"
)

// ErrNotRunning is returned when a resolver-scoped operation is requested while
// no resolver is running.
var ErrNotRunning = errors.New("resolver is not running")

// FilteringPauseDuration is the fixed pause interval offered in the GUI.  A
// fixed interval keeps the control-pipe method parameterless.
const FilteringPauseDuration = 15 * time.Minute

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
	PauseFiltering(d time.Duration)
	ResumeFiltering()
	Stats() resolver.Stats
}

// Lifecycle owns the resolver, state store, and interception controller.
type Lifecycle struct {
	lock   *state.Lock
	res    resolverHandle
	store  *state.Store
	ic     intercept.Controller
	logger *slog.Logger
	cfg    Config

	// resMu guards res: a status read may call RuntimeStats while Enable or
	// Disable swaps the resolver.
	resMu sync.RWMutex

	// pauseDuration is the filtering pause length.  It is a field so tests can
	// shorten it; production keeps FilteringPauseDuration.
	pauseDuration time.Duration

	// pauseMu guards pauseTimer and pauseSeq.  pauseSeq invalidates a timer
	// callback that had already started when a pause was replaced or canceled.
	pauseMu    sync.Mutex
	pauseTimer *time.Timer
	pauseSeq   uint64

	// newResolver, checkHealth and flushDNS are the injectable dependencies;
	// New points them at production and tests replace them to stay hermetic.
	newResolver func(ctx context.Context, cfg resolver.Config) (resolverHandle, error)
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
		cfg:           cfg,
		store:         state.NewStore(cfg.StatePath),
		ic:            intercept.New(logger),
		logger:        logger,
		pauseDuration: FilteringPauseDuration,
		newResolver:   func(ctx context.Context, c resolver.Config) (resolverHandle, error) { return resolver.New(ctx, c) },
		checkHealth:   health.Check,
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
	// A lock adopted for a reload must survive a failed enable: the reload
	// hands it back to the old session for rollback.
	adopted := l.lock != nil
	if err = l.acquire(); err != nil {
		return err
	}
	defer func() {
		if err != nil && !adopted {
			l.release()
		}
	}()

	// Enable is idempotent: a client Enable can race service startup, and a
	// running enabled session must not be torn down and re-enabled.
	if st, sErr := l.store.Load(); sErr == nil && st.Mode == state.ModeOn && l.resolver() != nil {
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

	// 2. Refuse a listener that interception cannot reach.  The NRPT rule
	// advertises a bare loopback host (always port 53), so a custom port would
	// pass the health check and then point system DNS at an unanswered port.
	servers, err := interceptServers(res)
	if err != nil {
		_ = l.stopResolver(ctx)

		return err
	}

	// 3. Two-stage health check.
	hr := l.healthCheck(ctx, res)
	if !hr.OK {
		_ = l.stopResolver(ctx)

		return fmt.Errorf("health check failed: %s", hr.Detail)
	}

	// 4. Persist the atomic dirty marker.
	st := &state.State{
		SchemaVersion: state.SchemaVersion,
		Mode:          state.ModeOn,
		Intercept:     state.MechanismNRPT,
		Dirty:         true,
		Upstream:      strings.Join(l.upstreamsOrDefault(), ","),
		Health:        toStateHealth(hr),
		Bypass:        l.cfg.Bypass,
		NameServers:   servers,
	}
	if err = l.store.Save(st); err != nil {
		_ = l.stopResolver(ctx)

		return err
	}

	// 5. Apply interception to the listeners that actually bound.
	if err = l.ic.Enable(ctx, l.cfg.Bypass, servers); err != nil {
		enableErr := fmt.Errorf("enabling interception: %w", err)

		// A partial apply must not be left pointing at a resolver we are about
		// to stop.  Restore first; if that fails, leave the resolver running so
		// DNS still works and the caller can retry.
		if rErr := l.restore(ctx, false); rErr != nil {
			return errors.Join(enableErr, rErr)
		}
		if sErr := l.stopResolver(ctx); sErr != nil {
			return errors.Join(enableErr, sErr)
		}

		return enableErr
	}

	// 6. Flush DNS.
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

	if err = l.disable(ctx); err != nil {
		// Keep the lock: a failed restore leaves interception (and possibly the
		// resolver) alive, so another process must not take over.
		return err
	}
	l.release()

	return nil
}

// DisableKeepLock is Disable without releasing the session lock.  Reload uses
// it so the lock is held across the disable/enable swap and its rollback.
func (l *Lifecycle) DisableKeepLock(ctx context.Context) (err error) {
	if err = l.acquire(); err != nil {
		return err
	}

	return l.disable(ctx)
}

// TakeLock transfers the held session lock to the caller and forgets it.
func (l *Lifecycle) TakeLock() (lk *state.Lock) {
	lk = l.lock
	l.lock = nil

	return lk
}

// AdoptLock makes l hold lk, the other half of the reload hand-off.
func (l *Lifecycle) AdoptLock(lk *state.Lock) { l.lock = lk }

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
// keeps the dirty marker and the session lock set on partial failure.
func (l *Lifecycle) Restore(ctx context.Context) (err error) {
	// A running service already holds the lock; only drop it when this call
	// acquired it, or a concurrent client could race the live session.
	acquired := l.lock == nil
	if err = l.acquire(); err != nil {
		return err
	}
	if acquired {
		defer func() {
			if err == nil {
				l.release()
			}
		}()
	}

	return l.restore(ctx, false)
}

// restore removes interception and clears the dirty marker.  If onlyIfDirty is
// true it is a no-op when the state is clean.
func (l *Lifecycle) restore(ctx context.Context, onlyIfDirty bool) (err error) {
	st, lErr := l.store.Load()
	dirty := lErr == nil && st.Dirty
	if lErr != nil && !errors.Is(lErr, state.ErrNoState) {
		// A corrupt state file must not block cleanup (M2): undo any partial
		// interception, then quarantine the bad file and record a clean off
		// state so the next start can proceed.  If interception cannot be
		// proven down, stay fail-closed and surface the errors.
		if dErr := l.ic.Disable(ctx); dErr != nil {
			return errors.Join(lErr, dErr)
		}
		if errors.Is(lErr, state.ErrCorrupt) {
			if rErr := l.store.MarkClean(); rErr != nil {
				return errors.Join(lErr, rErr)
			}
			l.logger.WarnContext(ctx, "quarantined a corrupt state file", "err", lErr)

			return nil
		}

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
// its filter engine (SPEC §15.1).  It then flushes the OS DNS cache so a
// previously blocked answer does not outlive the refreshed lists.  It returns
// ErrNotRunning when no resolver is active.
func (l *Lifecycle) RefreshLists(ctx context.Context) (err error) {
	res := l.resolver()
	if res == nil {
		return ErrNotRunning
	}

	if err = res.RefreshLists(ctx); err != nil {
		return err
	}

	// The lists are already live; a failed ipconfig must not report the
	// refresh as failed (Enable treats the same failure as a warning).
	l.flush(ctx)

	return nil
}

// ClearCache clears the running resolver's cache and the OS DNS cache.  A
// stopped resolver still gets the OS flush: there is no in-process cache to
// clear, and the Windows cache may hold an answer that predates a rule change.
func (l *Lifecycle) ClearCache(ctx context.Context) (err error) {
	if res := l.resolver(); res != nil {
		res.ClearCache()
	}

	return FlushDNS(ctx)
}

// PauseFiltering suspends the running resolver's filter stage for
// FilteringPauseDuration and flushes the OS cache, so an answer blocked just
// before the pause does not outlive it.  Interception stays on and queries keep
// resolving; only filtering is skipped.
func (l *Lifecycle) PauseFiltering(ctx context.Context) (err error) {
	res := l.resolver()
	if res == nil {
		return ErrNotRunning
	}

	// Invalidate any previous pause before starting a new one, so an old
	// expiry callback cannot resume this pause.
	l.stopPauseTimer()

	res.PauseFiltering(l.pauseDuration)
	l.schedulePauseExpiry(res)
	l.flush(ctx)

	return nil
}

// ResumeFiltering turns the running resolver's filtering back on before the
// pause expires and flushes the OS cache, so an answer allowed during the
// pause does not outlive it.  Interception is untouched.
func (l *Lifecycle) ResumeFiltering(ctx context.Context) (err error) {
	res := l.resolver()
	if res == nil {
		return ErrNotRunning
	}

	l.stopPauseTimer()
	res.ResumeFiltering()
	l.flush(ctx)

	return nil
}

// schedulePauseExpiry arranges the cache flush that automatic pause expiry
// needs.  The resolver stops filtering on its own by deadline; only the caches
// (the resolver's and the OS DNS cache) would otherwise keep an answer obtained
// during the pause.
func (l *Lifecycle) schedulePauseExpiry(res resolverHandle) {
	l.pauseMu.Lock()
	defer l.pauseMu.Unlock()

	l.pauseSeq++
	seq := l.pauseSeq
	l.pauseTimer = time.AfterFunc(l.pauseDuration, func() { l.expirePause(seq, res) })
}

// expirePause runs when a pause deadline passes.
func (l *Lifecycle) expirePause(seq uint64, res resolverHandle) {
	l.pauseMu.Lock()
	defer l.pauseMu.Unlock()

	if seq != l.pauseSeq {
		return
	}
	l.pauseTimer = nil
	res.ResumeFiltering()
	l.flushDNS(context.Background())
}

// stopPauseTimer cancels the pending expiry callback and invalidates one that
// has already started.  It waits for a running callback to finish, so the
// caller can safely shut the resolver down afterwards.
func (l *Lifecycle) stopPauseTimer() {
	l.pauseMu.Lock()
	defer l.pauseMu.Unlock()

	if l.pauseTimer != nil {
		l.pauseTimer.Stop()
		l.pauseTimer = nil
	}
	l.pauseSeq++
}

// RuntimeStats returns the live resolver counters for the sanitized status
// DTO.  It never takes the lifecycle lock, so a status read cannot block behind
// a slow reload, and it is safe while Enable/Disable swap the resolver.
func (l *Lifecycle) RuntimeStats() (queries, blocked uint64, pausedUntil time.Time) {
	res := l.resolver()
	if res == nil {
		return 0, 0, time.Time{}
	}

	s := res.Stats()

	return s.Queries, s.Blocked, s.PausedUntil
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
	res := l.resolver()
	if res == nil {
		return nil
	}

	// A pause expiry callback must finish before the resolver is torn down.
	l.stopPauseTimer()

	err = res.Shutdown(ctx)
	l.setResolver(nil)

	return err
}

// markClean clears the dirty marker.  A missing state file is not an error, and
// MarkClean replaces a corrupt one with a clean off state.
func (l *Lifecycle) markClean() (err error) {
	err = l.store.MarkClean()
	if errors.Is(err, state.ErrNoState) {
		return nil
	}

	return err
}

// startResolver binds and starts the resolver, shutting it down if the bind
// succeeded but Start failed.  It leaves l.res set on success.
func (l *Lifecycle) startResolver(ctx context.Context) (res resolverHandle, err error) {
	res, err = l.newResolver(ctx, l.resolverConfig())
	if err != nil {
		return nil, err
	}
	if err = res.Start(ctx); err != nil {
		_ = res.Shutdown(ctx)

		return nil, err
	}
	l.setResolver(res)

	return res, nil
}

// resolver returns the current resolver, or nil when none is running.
func (l *Lifecycle) resolver() (res resolverHandle) {
	l.resMu.RLock()
	defer l.resMu.RUnlock()

	return l.res
}

// setResolver swaps the resolver pointer.
func (l *Lifecycle) setResolver(res resolverHandle) {
	l.resMu.Lock()
	l.res = res
	l.resMu.Unlock()
}

// acquire takes the single-instance lock, or is a no-op if already held.
func (l *Lifecycle) acquire() (err error) {
	if l.lock != nil {
		return nil
	}

	l.lock, err = state.AcquireLockSecure(state.LockPath(l.cfg.StatePath))

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

// bootstrapOrDefault returns the configured bootstrap or the default.  An
// explicit empty list is kept: it means no bootstrap (NextDNS Ultralow).
func (l *Lifecycle) bootstrapOrDefault() (b []string) {
	if l.cfg.Bootstrap != nil {
		return l.cfg.Bootstrap
	}

	return []string{resolver.DefaultBootstrap}
}

// interceptServers returns the resolver's bound loopback UDP hosts for the NRPT
// rule.  The rule has no port field (it always targets 53), so a listener on
// any other port must be refused instead of silently advertised as :53.
func interceptServers(res resolverHandle) (servers []string, err error) {
	for _, addr := range res.UDPAddrs() {
		host, port, sErr := net.SplitHostPort(addr)
		if sErr != nil {
			continue
		}
		ip, pErr := netip.ParseAddr(host)
		if pErr != nil || !ip.IsLoopback() {
			continue
		}
		if port != "53" {
			return nil, fmt.Errorf("listener %s is not on port 53; interception only supports port 53 (use the run command for a resolver-only custom port)", addr)
		}
		servers = append(servers, host)
	}
	if len(servers) == 0 {
		return nil, errors.New("no loopback listener on port 53 to intercept")
	}

	return servers, nil
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
