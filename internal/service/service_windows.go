//go:build windows

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Registration (equivalent to the CLI's `dns-client service install`):
//
//	sc create dns-client binPath= "\"C:\path\to\dns-client.exe\" --service" ^
//	    start= auto obj= "NT SERVICE\dns-client" sidtype= unrestricted
//	sc description dns-client "System-wide encrypted DNS client (loopback resolver + NRPT interception)."
//
// The dedicated virtual account `NT SERVICE\dns-client` (not LocalSystem) gets
// a service SID; grant it access to the state and config directories when
// hardening.  Install/Uninstall use the SCM API so the account and SID type are
// set correctly without shelling out.

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

	if err := h.agent.Start(ctx); err != nil {
		h.agent.logger.Error("service start failed", "err", err)

		return false, 1
	}

	changes <- svc.Status{State: svc.Running, Accepts: accepts}

	for c := range r {
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

	return false, 0
}

// Run runs the service host under the SCM.
func Run(agent *Agent) (err error) {
	return svc.Run(Name, &handler{agent: agent})
}

// Install registers the service with the SCM.
func Install() (err error) {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving executable: %w", err)
	}

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
		SidType:          windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}, "--service")
	if err != nil {
		return fmt.Errorf("creating service: %w", err)
	}
	defer func() { _ = s.Close() }()

	return nil
}

// Uninstall removes the service from the SCM.
func Uninstall() (err error) {
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
