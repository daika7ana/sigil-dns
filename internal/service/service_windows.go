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

// Registration (equivalent to the CLI's `dns-client service install`):
//
//	sc create dns-client binPath= "\"C:\path\to\dns-client.exe\" --service" ^
//	    start= auto obj= "LocalSystem"
//	sc description dns-client "System-wide encrypted DNS client (loopback resolver + NRPT interception)."
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
	const accepts = svc.AcceptStop | svc.AcceptShutdown

	changes <- svc.Status{State: svc.StartPending}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Report Running before crash recovery + re-enable (which can invoke a slow
	// PowerShell/health check) so the SCM ~30 s start budget (error 1053) is not
	// tripped.  A degraded start keeps the service up so an operator can
	// status/disable.
	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	errCh := make(chan error, 1)
	go func() { errCh <- h.agent.Start(ctx) }()

	for {
		select {
		case err := <-errCh:
			errCh = nil

			if err != nil {
				h.agent.logger.Error("service start failed", "err", err)
				changes <- svc.Status{State: svc.StopPending}

				return false, 1
			}
		case c, ok := <-r:
			if !ok {
				return false, 0
			}

			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				cancel()

				if err := h.agent.Stop(context.Background()); err != nil {
					h.agent.logger.Error("service stop failed", "err", err)

					return false, 1
				}

				return false, 0
			}
		}
	}
}

// Run runs the service host under the SCM.
func Run(agent *Agent) (err error) {
	return svc.Run(Name, &handler{agent: agent})
}

// Executable returns the canonical service binary for this process image: the
// non-GUI sibling dns-client.exe when running as the GUI exe and that sibling
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

// Uninstall stops the service (best-effort) and then removes it, so a dangling
// NRPT rule can never point at a service that no longer exists.
func Uninstall() (err error) {
	_ = Stop()

	return Delete()
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
