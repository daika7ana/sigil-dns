//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Registration (equivalent to the CLI's `sigildns service install`):
//
//	sc create sigildns binPath= "\"C:\path\to\sigildns.exe\" --service" ^
//	    start= auto obj= "LocalSystem"
//	sc description sigildns "System-wide encrypted DNS client (loopback resolver + NRPT interception)."
//
// v1 runs as LocalSystem (see SPEC §2): the least-privilege virtual account
// cannot own the pipe instances, is locked out of its own state file, and
// lacks the admin rights NRPT/adapter writes require.  Install/Uninstall use the
// SCM API.

// handler adapts Agent to the SCM's svc.Handler.
type handler struct {
	agent *Agent
}

// Execute implements svc.Handler.  The return values are (svcSpecificEC,
// exitCode).
func (h *handler) Execute(
	_ []string,
	r <-chan svc.ChangeRequest,
	changes chan<- svc.Status,
) (svcSpecificEC bool, exitCode uint32) {
	const (
		accepts   = svc.AcceptStop | svc.AcceptShutdown | svc.AcceptPreShutdown
		waitHint  = 2 * 60 * 1000 // ms; Start checks in every 10 s
		checkStep = 10 * time.Second
	)

	// Stay in StartPending until Agent.Start has actually enabled interception.
	// Reporting Running first would expose a Running-but-unprotected service
	// (the exact state this service must never have).  Checkpoint every 10 s so
	// the SCM does not trip its start budget while PowerShell/NRPT is slow.
	changes <- svc.Status{State: svc.StartPending, Accepts: accepts, CheckPoint: 1, WaitHint: waitHint}

	// A start can race an in-progress shutdown; enabling interception would
	// leave a rule pointing at a resolver that is about to disappear.
	if shutdownProbe() {
		h.agent.logger.Info("system is shutting down; not starting")
		changes <- svc.Status{State: svc.StopPending}

		return false, 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- h.agent.Start(ctx) }()

	ticker := time.NewTicker(checkStep)
	defer ticker.Stop()

	checkpoint := uint32(1)

	for {
		select {
		case err := <-errCh:
			errCh = nil

			if err != nil {
				// A stop that is already waiting means this start was aborted,
				// not broken — usually a machine shutdown.  Finish it as a
				// stop: a non-zero exit would queue the SCM's recovery actions
				// against a machine that is going down.  Agent.Start restored
				// on its own failure path already.  Interrogates are answered
				// and skipped: only a real stop request aborts the start.
				if c, ok := drainPending(r, changes); ok {
					changes <- svc.Status{State: svc.StopPending}
					h.stop(c.Cmd)

					return false, 0
				}

				h.agent.logger.Error("service start failed", "err", err)
				changes <- svc.Status{State: svc.StopPending}

				if errors.Is(err, ErrConfigUnavailable) {
					// A bad config will not fix itself; retrying just churns.
					return false, 0
				}

				return false, 1
			}

			changes <- svc.Status{State: svc.Running, Accepts: accepts}
		case <-ticker.C:
			if errCh != nil {
				checkpoint++
				changes <- svc.Status{State: svc.StartPending, Accepts: accepts, CheckPoint: checkpoint, WaitHint: waitHint}
			}
		case <-h.agent.Done():
			// Persistent drift could not be reconciled; stop so the service is
			// never left running with interception off.  A control request
			// already queued selects the restore budget: do not rob a queued
			// pre-shutdown of its 8 s window.
			changes <- svc.Status{State: svc.StopPending}
			cancel()

			stopCmd := svc.Stop
			if c, ok := drainPending(r, changes); ok {
				stopCmd = c.Cmd
			}

			h.stop(stopCmd)

			return false, 0
		case c, ok := <-r:
			if !ok {
				cancel()
				h.stop(svc.Stop)
				return false, 0
			}

			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown, svc.PreShutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				h.stop(c.Cmd)

				return false, 0
			}
		}
	}
}

// preshutdownRestoreBudget caps the restore attempt on the preshutdown path.
// Preshutdown is the primary path: it arrives while DnsClient/CIM are still
// alive, so the restore normally succeeds.  The budget must stay under the
// SCM's preshutdown time-out, which defaults to 10 s on every build since
// Windows 10 1703 (we never raise it) — past it the SCM resumes shutting down
// and can kill the process mid-restore.
const preshutdownRestoreBudget = 8 * time.Second

// shutdownRestoreBudget caps the restore attempt on the legacy SHUTDOWN path.
// The SCM terminates a service that overruns WaitToKillServiceTimeout (5 s by
// default), so a blocked PowerShell must not hold the shutdown screen, and the
// warning line, until then.  Giving up early loses nothing: the state stays
// dirty and the next start retries.  It caps the restore itself, not the whole
// stop: the wait for a service that is still starting, and the wait for a
// lifecycle call already in flight (a list download, say), are not
// context-aware and can still outlast it.
const shutdownRestoreBudget = 3 * time.Second

// userStopRetryBudget bounds the single retry after a failed user-stop
// restore.
const userStopRetryBudget = 10 * time.Second

// restoreBudget is how long a stop of the given kind may spend restoring
// interception.  Preshutdown arrives while DnsClient/CIM are alive; the legacy
// shutdown notification arrives later and must fit inside the SCM's
// WaitToKillServiceTimeout.  A user stop is not bounded: nothing is waiting for
// it to finish.
func restoreBudget(c svc.Cmd) time.Duration {
	switch c {
	case svc.PreShutdown:
		return preshutdownRestoreBudget
	case svc.Shutdown:
		return shutdownRestoreBudget
	}

	return 0
}

// pendingRequest reports a control request that is already waiting, which the
// SCM can only do by blocking on an unbuffered send.
func pendingRequest(r <-chan svc.ChangeRequest) (c svc.ChangeRequest, ok bool) {
	select {
	case c, ok = <-r:
	default:
	}

	return c, ok
}

// drainPending answers queued interrogates and returns the first real control
// request, if any.
func drainPending(r <-chan svc.ChangeRequest, changes chan<- svc.Status) (c svc.ChangeRequest, ok bool) {
	for {
		c, ok = pendingRequest(r)
		if !ok {
			return svc.ChangeRequest{}, false
		}
		if c.Cmd != svc.Interrogate {
			return c, true
		}

		changes <- c.CurrentStatus
	}
}

// stop restores interception and logs a cleanup failure.  A stop the user (or
// drift) asked for must exit 0 even if cleanup failed: returning a failure
// would trigger the SCM recovery actions and restart the service.
//
// During a system shutdown the restore can fail: PowerShell exits 1 with no
// message while Windows stops services (the DnsClient cmdlets go through a
// provider that may already be gone).  The state stays dirty, so the next start
// retries the restore, and the machine is going down anyway — a warning, not an
// error.
//
// c is the control request that caused the stop; it selects the restore budget
// and whether a failure is a shutdown deferral rather than an error.
func (h *handler) stop(c svc.Cmd) {
	ctx := context.Background()
	shutdown := c != svc.Stop
	if budget := restoreBudget(c); shutdown && budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
	}

	err := h.agent.Stop(ctx)
	if err != nil && !shutdown {
		// A user stop is not bounded, but a transient CIM/PowerShell hiccup
		// must not leave the machine loopbacked to a dead resolver: retry once
		// under a bound.  This keeps exit-0 semantics (see above).
		retryCtx, retryCancel := context.WithTimeout(context.Background(), userStopRetryBudget)
		defer retryCancel()

		err = h.agent.Stop(retryCtx)
	}

	if err != nil {
		if shutdown {
			h.agent.logger.Warn("interception restore deferred to next service start", "err", err)

			return
		}
		h.agent.logger.Error("service stop failed; run 'sigildns restore' to undo interception", "err", err)
	}
}

// Run runs the service host under the SCM.
func Run(agent *Agent) (err error) {
	return svc.Run(Name, &handler{agent: agent})
}

// Executable returns the canonical service binary for this process image: the
// non-GUI sibling sigildns.exe when running as the GUI exe and that sibling
// exists, else the current executable.
func Executable() (exe string, err error) {
	cur, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolving executable: %w", err)
	}

	return canonicalServiceExe(cur, func(p string) bool {
		_, statErr := os.Stat(p)

		return statErr == nil
	}), nil
}

// Install registers the service with the SCM against the canonical service
// binary for this process image.
func Install() (err error) {
	exe, err := Executable()
	if err != nil {
		return err
	}

	return InstallExe(exe)
}

// InstallExe registers the service with the SCM against exe.
func InstallExe(exe string) (err error) {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.CreateService(Name, exe, mgr.Config{
		DisplayName:      DisplayName,
		Description:      Description,
		StartType:        mgr.StartAutomatic,
		ServiceStartName: Account,
	}, "--service")
	if err != nil {
		return fmt.Errorf("creating service: %w", err)
	}
	defer func() { _ = s.Close() }()

	// Restart on failure (SPEC §9: keep DNS working; never leave a loopbacked
	// system with a dead service).
	err = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 86400)
	if err != nil {
		return fmt.Errorf("setting recovery actions: %w", err)
	}

	// Also run the recovery actions for non-crash failures.
	_ = s.SetRecoveryActionsOnNonCrashFailures(true)

	return nil
}

// Delete removes the service registration without stopping it.  Callers that
// need interception cleared first must run the ordered uninstall themselves.
func Delete() (err error) {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(Name)
	if err != nil {
		return fmt.Errorf("opening service: %w", err)
	}
	defer func() { _ = s.Close() }()

	return s.Delete()
}

// Start starts the service.
func Start() (err error) {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(Name)
	if err != nil {
		return fmt.Errorf("opening service: %w", err)
	}
	defer func() { _ = s.Close() }()

	// Refuse to start a registration that points at some other binary (the
	// anti-hijack check).  Errors are static and path-free.
	exe, err := Executable()
	if err != nil {
		return errors.New("could not resolve the service executable")
	}

	cfg, err := s.Config()
	if err != nil {
		return errors.New("could not read the service registration")
	}
	if !binaryMatches(cfg.BinaryPathName, exe) {
		return errors.New("the registered service does not point at this binary")
	}

	return s.Start("--service")
}

// Stop stops the service and waits for it to reach the stopped state.
func Stop() (err error) {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connecting to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(Name)
	if err != nil {
		return fmt.Errorf("opening service: %w", err)
	}
	defer func() { _ = s.Close() }()

	st, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("sending stop: %w", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	for st.State != svc.Stopped {
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for service to stop")
		}

		time.Sleep(300 * time.Millisecond)

		st, err = s.Query()
		if err != nil {
			return fmt.Errorf("querying service: %w", err)
		}
	}

	return nil
}
