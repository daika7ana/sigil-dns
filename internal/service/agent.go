// Package service hosts sigildns as a Windows service and owns the loopback
// resolver, interception, and state (SPEC §2).  The OS-agnostic core (Agent) is
// kept separate from the SCM plumbing so it is testable off Windows.
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/daika7ana/sigil-dns/internal/drift"
	"github.com/daika7ana/sigil-dns/internal/ipc"
	"github.com/daika7ana/sigil-dns/internal/state"
)

// Service identity.  v1 runs as LocalSystem: the least-privilege virtual account
// cannot own the pipe instances or perform NRPT/adapter writes (SPEC §2).
const (
	// Name is the SCM service name.
	Name = "sigildns"

	// DisplayName is the human-readable service name.
	DisplayName = "Sigil DNS"

	// Description is the service description.
	Description = "System-wide encrypted DNS client (loopback resolver + NRPT interception)."

	// Account is the account the service runs as.
	Account = "LocalSystem"
)

// IPC listener retry policy (M6): a transient bind failure must not leave the
// service deaf for its whole lifetime.
const (
	ipcListenAttempts = 5
	ipcListenBackoff  = 200 * time.Millisecond
)

// ErrUnsupported is returned by the non-Windows stubs.
var ErrUnsupported = errors.New("service management is not supported on this platform")

// Lifecycle is the daemon core the service drives.  It satisfies
// ipc.Lifecycle structurally, so the same object backs the IPC server.
type Lifecycle interface {
	// Status returns the persisted state, or state.ErrNoState.
	Status() (st *state.State, err error)

	// Startup runs crash recovery and reports whether mode=on should re-enable.
	Startup(ctx context.Context) (reenable bool, err error)

	// Enable applies interception.
	Enable(ctx context.Context) (err error)

	// Disable removes interception.
	Disable(ctx context.Context) (err error)

	// Restore restores interception from persisted state (crash/dirty
	// recovery), independent of the configuration file.
	Restore(ctx context.Context) (err error)

	// RefreshLists re-downloads the active profile's filter lists and rebuilds
	// the filter engine without tearing down interception.
	RefreshLists(ctx context.Context) (err error)
}

// Agent is the OS-agnostic service core: crash recovery on start, IPC serving,
// and restore on stop.
type Agent struct {
	lc             *lockedLifecycle
	watcher        *drift.Reconciler
	logger         *slog.Logger
	endpoint       string
	statusEndpoint string
	configPath     string
	ln             net.Listener
	sln            net.Listener
	cancel         context.CancelFunc
}

// NewAgent returns an Agent.  An empty endpoint uses the platform default.
// Lifecycle calls are serialized so IPC and the SCM cannot race.
func NewAgent(lc Lifecycle, logger *slog.Logger, endpoint string) (a *Agent) {
	if logger == nil {
		logger = slog.Default().With("component", "service")
	}
	if endpoint == "" {
		endpoint = ipc.DefaultEndpoint()
	}

	return &Agent{
		lc:             &lockedLifecycle{lc: lc},
		logger:         logger,
		endpoint:       endpoint,
		statusEndpoint: ipc.StatusEndpoint(),
	}
}

// SetWatcher sets the optional background reconciler.  It must be called before
// Start.
func (a *Agent) SetWatcher(w *drift.Reconciler) { a.watcher = w }

// SetStatusEndpoint overrides the read-only status endpoint.  Tests use it to
// isolate listeners; production uses the platform default.
func (a *Agent) SetStatusEndpoint(endpoint string) { a.statusEndpoint = endpoint }

// SetConfigPath sets the config file path served by config_summary.
func (a *Agent) SetConfigPath(path string) { a.configPath = path }

// SetReloader sets the handler for the admin reload method.
func (a *Agent) SetReloader(fn func(ctx context.Context) (ipc.ConfigSummary, error)) {
	a.lc.setReload(fn)
}

// lockedLifecycle serializes lifecycle calls from the SCM and IPC clients.
type lockedLifecycle struct {
	reload func(ctx context.Context) (ipc.ConfigSummary, error)
	lc     Lifecycle
	mu     sync.Mutex
}

// setReload installs the reload handler.
func (l *lockedLifecycle) setReload(fn func(ctx context.Context) (ipc.ConfigSummary, error)) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.reload = fn
}

// Reload implements ipc.Reloader, serialized with the other lifecycle calls.
func (l *lockedLifecycle) Reload(ctx context.Context) (sum ipc.ConfigSummary, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.reload == nil {
		return ipc.ConfigSummary{}, ipc.ErrUnsupported
	}

	return l.reload(ctx)
}

// Status implements Lifecycle.
func (l *lockedLifecycle) Status() (st *state.State, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.lc.Status()
}

// Startup implements Lifecycle.
func (l *lockedLifecycle) Startup(ctx context.Context) (reenable bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.lc.Startup(ctx)
}

// Enable implements Lifecycle.
func (l *lockedLifecycle) Enable(ctx context.Context) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.lc.Enable(ctx)
}

// Disable implements Lifecycle.
func (l *lockedLifecycle) Disable(ctx context.Context) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.lc.Disable(ctx)
}

// Restore implements ipc.Restorer, serialized with the other lifecycle calls.
func (l *lockedLifecycle) Restore(ctx context.Context) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.lc.Restore(ctx)
}

// RefreshLists implements ipc.ListRefresher, serialized with the other
// lifecycle calls.
func (l *lockedLifecycle) RefreshLists(ctx context.Context) (err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.lc.RefreshLists(ctx)
}

// Start binds the IPC listeners, then runs crash recovery and (if mode=on)
// re-enable.  It returns an error only when IPC cannot be served at all; a
// recovery or enable failure leaves the service running in a degraded state
// with IPC available so an operator can still status/disable it.
func (a *Agent) Start(ctx context.Context) (err error) {
	if err = a.startIPC(); err != nil {
		return err
	}

	if a.watcher != nil {
		a.watcher.Start()
	}

	reenable, err := a.lc.Startup(ctx)
	if err != nil {
		a.logger.ErrorContext(ctx, "crash recovery failed; running degraded", "err", err)

		return nil
	}

	if !reenable {
		return nil
	}

	if err = a.lc.Enable(ctx); err != nil {
		a.logger.ErrorContext(ctx, "enable failed; running degraded", "err", err)

		return nil
	}

	return nil
}

// Stop shuts the IPC servers down and, only if interception is actually active,
// disables/restores it.
func (a *Agent) Stop(ctx context.Context) (err error) {
	if a.watcher != nil {
		a.watcher.Stop()
	}

	a.stopIPC()

	st, sErr := a.lc.Status()
	if sErr != nil {
		if errors.Is(sErr, state.ErrNoState) {
			// Never enabled: nothing to restore.
			return nil
		}

		return sErr
	}
	if st == nil || (st.Mode != state.ModeOn && !st.Dirty) {
		// Nothing to restore; avoid erroring on the stop path.
		return nil
	}

	return a.lc.Disable(ctx)
}

// startIPC binds the control and status pipes, retrying transient failures with
// backoff.  A control-pipe failure is fatal; a status-pipe failure is logged.
func (a *Agent) startIPC() (err error) {
	ln, err := a.listenWithRetry(a.endpoint)
	if err != nil {
		return err
	}
	a.ln = ln

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	control := ipc.NewServer(a.lc, a.logger).SetConfigPath(a.configPath).SetReloader(a.lc).SetRestorer(a.lc).SetListRefresher(a.lc)
	go func() {
		if sErr := ipc.Serve(ctx, ln, control, ipc.AdminAuthorizer); sErr != nil {
			a.logger.Error("ipc control serve stopped", "err", sErr)
		}
	}()
	a.logger.Info("ipc control listening", "endpoint", a.endpoint)

	a.startStatusPipe(ctx)

	return nil
}

// startStatusPipe starts the read-only status listener, non-fatally.
func (a *Agent) startStatusPipe(ctx context.Context) {
	endpoint := a.statusEndpoint

	sln, err := ipc.ListenStatus(endpoint)
	if err != nil {
		a.logger.Error("ipc status listen failed", "endpoint", endpoint, "err", err)

		return
	}
	a.sln = sln

	status := ipc.NewServer(a.lc, a.logger).SanitizeStatus().SetConfigPath(a.configPath)
	go func() {
		if sErr := ipc.Serve(ctx, sln, status, ipc.ReadOnlyAuthorizer); sErr != nil {
			a.logger.Error("ipc status serve stopped", "err", sErr)
		}
	}()
	a.logger.Info("ipc status listening", "endpoint", endpoint)
}

// listenWithRetry retries Listen with linear backoff.
func (a *Agent) listenWithRetry(endpoint string) (ln net.Listener, err error) {
	for attempt := 1; attempt <= ipcListenAttempts; attempt++ {
		ln, err = ipc.Listen(endpoint)
		if err == nil {
			return ln, nil
		}

		a.logger.Error("ipc control listen failed", "endpoint", endpoint, "attempt", attempt, "err", err)

		if attempt < ipcListenAttempts {
			time.Sleep(ipcListenBackoff * time.Duration(attempt))
		}
	}

	return nil, fmt.Errorf("listening on %s: %w", endpoint, err)
}

// Shutdown bounds.  A go-winio named-pipe listener Close can hang while an
// overlapped ConnectNamedPipe is outstanding with no client connecting
// (upstream microsoft/go-winio#357, golang/go#64482): the close's abort relies
// on CancelIoEx, which is not guaranteed to complete the IO.  wakeListener
// completes the connect so the listener closes cleanly; closeListenerTimeout
// guarantees shutdown cannot wedge even if the wake does not take.
const (
	wakeTimeout          = 250 * time.Millisecond
	closeListenerTimeout = 2 * time.Second
)

// stopIPC stops both IPC listeners.
func (a *Agent) stopIPC() {
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}

	// Wake each blocked Accept so its listener can close without the go-winio
	// CancelIoEx hang, then close behind a bound as a safety net.
	wakeListener(a.endpoint)
	wakeListener(a.statusEndpoint)

	if a.ln != nil {
		ipc.CloseListener(a.ln, closeListenerTimeout)
		a.ln = nil
	}
	if a.sln != nil {
		ipc.CloseListener(a.sln, closeListenerTimeout)
		a.sln = nil
	}
}

// wakeListener makes a throwaway connection to endpoint so a Serve accept loop
// blocked in ConnectNamedPipe returns; seeing the canceled context, Serve drops
// it and exits.  Best-effort: a failure falls back to the bounded close.
func wakeListener(endpoint string) {
	if endpoint == "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout)
	defer cancel()

	if c, err := ipc.Dial(ctx, endpoint); err == nil {
		_ = c.Close()
	}
}
