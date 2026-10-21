//go:build gui && windows

package gui

import (
	"context"
	"net"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/daika7ana/sigil-dns/internal/ipc"
	"github.com/daika7ana/sigil-dns/internal/service"
)

// liveBase performs the SCM query and, when the service is running, the dial and
// the pipe-identity check shared by Read and ReadLive.  The check is
// GetNamedPipeServerProcessId(conn) == the SCM-reported ProcessId, performed on
// the same connection the reads use (SPEC §15.1/§15.3.12).  A verified result
// returns the open conn for the caller to read and close.
func (s *pipeSource) liveBase(ctx context.Context) (snapshot, net.Conn, liveOutcome) {
	pid, running, installed, state, err := serviceProcess()
	if err != nil {
		return snapshot{trust: trustVerified, updated: time.Now()}, nil, liveUnavailable
	}
	if !installed {
		return snapshot{trust: trustVerified, updated: time.Now(),
			scmState: "not installed"}, nil, liveStopped
	}
	if !running {
		return snapshot{trust: trustVerified, updated: time.Now(),
			installed: true, scmState: state}, nil, liveStopped
	}

	conn, err := ipc.Dial(ctx, s.endpoint)
	if err != nil {
		return snapshot{trust: trustVerified, updated: time.Now(),
			installed: true, scmState: state}, nil, liveUnavailable
	}
	unverified := func() (snapshot, net.Conn, liveOutcome) {
		_ = conn.Close()

		return snapshot{trust: trustUnverified, running: true, installed: true,
			scmState: state, updated: time.Now()}, nil, liveUnavailable
	}

	fd, ok := connFD(conn)
	if !ok {
		return unverified()
	}

	var serverPID uint32
	if err = windows.GetNamedPipeServerProcessId(windows.Handle(fd), &serverPID); err != nil {
		return unverified()
	}
	if serverPID == 0 || serverPID != pid {
		// Pipe squatted, or the server is not the service: refuse the data.
		return unverified()
	}

	return snapshot{trust: trustVerified, running: true, installed: true,
		scmState: state, updated: time.Now()}, conn, liveRunning
}

// Read is the full read: the SCM state plus the status DTO, the config summary,
// and the service info.  While the service is not running it falls back to the
// on-disk config (offlineSummary).
func (s *pipeSource) Read(ctx context.Context) (snapshot, error) {
	snap, conn, outcome := s.liveBase(ctx)
	switch outcome {
	case liveStopped:
		snap.summary = offlineSummary()
	case liveRunning:
		defer func() { _ = conn.Close() }()
		snap.status, snap.summary, snap.info = readPayload(conn)
	}

	return snap, nil
}

// ReadLive is Read without the heavy config data: just the SCM state and, when
// the service is running, the single status DTO.  It never reads config.yaml —
// the poller calls it on the hot path.
func (s *pipeSource) ReadLive(ctx context.Context) (snapshot, error) {
	snap, conn, outcome := s.liveBase(ctx)
	if outcome != liveRunning {
		return snap, nil
	}
	defer func() { _ = conn.Close() }()

	var st ipc.StatusDTO
	if exchange(conn, ipc.MethodStatus, &st) == nil {
		snap.status = &st
	}

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

// serviceProcess reports the sigildns service's PID, whether it is running,
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
