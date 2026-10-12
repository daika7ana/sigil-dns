//go:build gui && windows

package gui

import (
	"context"
	"net"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/service"
)

// Read verifies the status pipe server is the dns-client service before
// trusting any read (SPEC §15.1/§15.3.12).  The check is
// GetNamedPipeServerProcessId(conn) == the SCM-reported ProcessId, performed on
// the same connection the reads use.  A service that is not installed or not
// running yields a stopped snapshot; a PID mismatch yields unverified.
func (s *pipeSource) Read(ctx context.Context) (snapshot, error) {
	pid, running, installed, state, err := serviceProcess()
	if err != nil {
		return snapshot{trust: trustVerified, updated: time.Now(),
			err: "could not query service state"}, nil
	}
	if !installed {
		return snapshot{trust: trustVerified, updated: time.Now(),
			scmState: "not installed"}, nil
	}
	if !running {
		return snapshot{trust: trustVerified, updated: time.Now(),
			installed: true, scmState: state}, nil
	}

	conn, err := ipc.Dial(ctx, s.endpoint)
	if err != nil {
		return snapshot{trust: trustVerified, updated: time.Now(),
			installed: true, scmState: state, err: "status pipe not reachable"}, nil
	}
	defer func() { _ = conn.Close() }()

	fd, ok := connFD(conn)
	if !ok {
		return snapshot{trust: trustUnverified, running: true, installed: true,
			scmState: state, updated: time.Now()}, nil
	}

	var serverPID uint32
	if err = windows.GetNamedPipeServerProcessId(windows.Handle(fd), &serverPID); err != nil {
		return snapshot{trust: trustUnverified, running: true, installed: true,
			scmState: state, updated: time.Now()}, nil
	}
	if serverPID == 0 || serverPID != pid {
		// Pipe squatted, or the server is not the service: refuse the data.
		return snapshot{trust: trustUnverified, running: true, installed: true,
			scmState: state, updated: time.Now()}, nil
	}

	snap := snapshot{trust: trustVerified, running: true, installed: true,
		scmState: state, updated: time.Now()}
	snap.status, snap.summary, snap.info = readPayload(conn)

	return snap, nil
}

// connFD returns the OS handle behind a go-winio pipe connection.
func connFD(c net.Conn) (fd uintptr, ok bool) {
	f, ok := c.(interface{ Fd() uintptr })
	if !ok {
		return 0, false
	}

	return f.Fd(), true
}

// serviceProcess reports the dns-client service's PID, whether it is running,
// whether it is installed, and its state name from the SCM.  Querying status
// needs no elevation (SC_MANAGER_CONNECT + SERVICE_QUERY_STATUS are granted to
// authenticated users).
func serviceProcess() (pid uint32, running, installed bool, state string, err error) {
	m, err := mgr.Connect()
	if err != nil {
		return 0, false, false, "", err
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(service.Name)
	if err != nil {
		return 0, false, false, "", nil // not installed
	}
	defer func() { _ = s.Close() }()

	st, err := s.Query()
	if err != nil {
		return 0, false, true, "", err
	}

	return st.ProcessId, st.State == svc.Running, true, serviceStateName(st.State), nil
}

// serviceStateName renders an SCM state as a short, plain word for the UI.
func serviceStateName(st svc.State) (name string) {
	switch st {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "resuming"
	case svc.PausePending:
		return "pausing"
	case svc.Paused:
		return "paused"
	default:
		return "unknown"
	}
}
