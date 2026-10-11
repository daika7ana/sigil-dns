// Package service hosts dns-client as a Windows service and owns the loopback
// resolver, interception, and state (SPEC §2).  The OS-agnostic core (Agent) is
// kept separate from the SCM plumbing so it is testable off Windows.
package service

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"sync"

	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/state"
)

// Service identity.  The service runs as the virtual account, never
// LocalSystem (SPEC §2).
const (
	// Name is the SCM service name.
	Name = "dns-client"

	// DisplayName is the human-readable service name.
	DisplayName = "dns-client loopback DNS"

	// Description is the service description.
	Description = "System-wide encrypted DNS client (loopback resolver + NRPT interception)."

	// Account is the dedicated virtual account the service runs as.
	Account = `NT SERVICE\dns-client`
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
}

// Watcher is a background reconciler (drift detection) started with the service
// and stopped when the service stops.  It is optional.
type Watcher interface {
	// Start launches the background loop.
	Start()

	// Stop terminates the background loop.
	Stop()
}

// Agent is the OS-agnostic service core: crash recovery on start, IPC serving,
// and restore on stop.
type Agent struct {
	lc       Lifecycle
	watcher  Watcher
	logger   *slog.Logger
	endpoint string
	ln       net.Listener
	cancel   context.CancelFunc
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

	return &Agent{lc: &lockedLifecycle{lc: lc}, logger: logger, endpoint: endpoint}
}

// SetWatcher sets the optional background reconciler.  It must be called before
// Start.
func (a *Agent) SetWatcher(w Watcher) { a.watcher = w }

// lockedLifecycle serializes lifecycle calls from the SCM and IPC clients.
type lockedLifecycle struct {
	lc Lifecycle
	mu sync.Mutex
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

// Start runs crash recovery (restore first, then re-enable if mode=on) and
// begins serving IPC.  Per SPEC §9 the lock is held for the service lifetime
// when a re-enable happens.
func (a *Agent) Start(ctx context.Context) (err error) {
	reenable, err := a.lc.Startup(ctx)
	if err != nil {
		return err
	}

	if reenable {
		if err = a.lc.Enable(ctx); err != nil {
			return err
		}
	}

	a.startIPC()

	// Start drift detection for the service lifetime; it idles when mode=off.
	if a.watcher != nil {
		a.watcher.Start()
	}

	return nil
}

// Stop shuts the IPC server down and disables/restores interception.
func (a *Agent) Stop(ctx context.Context) (err error) {
	if a.watcher != nil {
		a.watcher.Stop()
	}

	a.stopIPC()

	return a.lc.Disable(ctx)
}

// startIPC starts the IPC listener and server, non-fatally.
func (a *Agent) startIPC() {
	ln, err := ipc.Listen(a.endpoint)
	if err != nil {
		a.logger.Error("ipc listen failed", "endpoint", a.endpoint, "err", err)

		return
	}
	a.ln = ln

	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel

	srv := ipc.NewServer(a.lc, a.logger)
	go func() {
		if err := ipc.Serve(ctx, ln, srv, nil); err != nil {
			a.logger.Error("ipc serve stopped", "err", err)
		}
	}()

	a.logger.Info("ipc listening", "endpoint", a.endpoint)
}

// stopIPC stops the IPC listener.
func (a *Agent) stopIPC() {
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
	if a.ln != nil {
		_ = a.ln.Close()
		a.ln = nil
	}
}
