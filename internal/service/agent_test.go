package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/state"
)

// fakeAgentLC is an in-memory service.Lifecycle.
type fakeAgentLC struct {
	st           *state.State
	startupErr   error
	enableErr    error
	stErr        error
	reenable     bool
	enableCalls  int
	disableCalls int
}

func (f *fakeAgentLC) Status() (st *state.State, err error) { return f.st, f.stErr }

func (f *fakeAgentLC) Startup(context.Context) (reenable bool, err error) {
	return f.reenable, f.startupErr
}

func (f *fakeAgentLC) Enable(context.Context) (err error) {
	f.enableCalls++

	return f.enableErr
}

func (f *fakeAgentLC) Disable(context.Context) (err error) {
	f.disableCalls++

	return nil
}

func (f *fakeAgentLC) Restore(context.Context) (err error) { return nil }

// testEndpoint returns a platform-appropriate, unique IPC endpoint: a named
// pipe on Windows (a filesystem path is not a valid pipe name), else a temp
// Unix socket.
func testEndpoint(t *testing.T, kind string) (endpoint string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		var b [8]byte
		_, _ = rand.Read(b[:])

		return `\\.\pipe\dns-client-test-` + kind + "-" + hex.EncodeToString(b[:])
	}

	return filepath.Join(t.TempDir(), kind+".sock")
}

// newTestAgent returns an Agent with isolated IPC endpoints.
func newTestAgent(t *testing.T, lc Lifecycle) (a *Agent) {
	t.Helper()

	a = NewAgent(lc, nil, testEndpoint(t, "control"))
	a.SetStatusEndpoint(testEndpoint(t, "status"))

	return a
}

func TestAgentRecoveryReenables(t *testing.T) {
	lc := &fakeAgentLC{reenable: true, st: &state.State{Mode: state.ModeOn}}
	a := newTestAgent(t, lc)

	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = a.Stop(context.Background()) }()

	if lc.enableCalls != 1 {
		t.Fatalf("enableCalls = %d, want 1", lc.enableCalls)
	}

	if err := a.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if lc.disableCalls != 1 {
		t.Fatalf("disableCalls = %d, want 1", lc.disableCalls)
	}
}

func TestAgentNoReenable(t *testing.T) {
	lc := &fakeAgentLC{reenable: false}
	a := newTestAgent(t, lc)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = a.Stop(context.Background()) }()

	if lc.enableCalls != 0 {
		t.Fatalf("enableCalls = %d, want 0", lc.enableCalls)
	}
}

// TestAgentStartupErrorDegraded: a recovery failure must not take the service
// down; IPC stays available.
func TestAgentStartupErrorDegraded(t *testing.T) {
	want := errors.New("boom")
	lc := &fakeAgentLC{startupErr: want, st: &state.State{Mode: state.ModeOn}}
	a := newTestAgent(t, lc)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start should degrade, got err = %v", err)
	}
	defer func() { _ = a.Stop(context.Background()) }()

	c := ipc.NewClient(a.endpoint)
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatalf("IPC not available after degraded start: %v", err)
	}
}

// TestAgentEnableFailureDegraded: an enable failure keeps the service up.
func TestAgentEnableFailureDegraded(t *testing.T) {
	lc := &fakeAgentLC{reenable: true, enableErr: errors.New("nrpt failed"), st: &state.State{Mode: state.ModeOn}}
	a := newTestAgent(t, lc)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start should degrade, got err = %v", err)
	}
	defer func() { _ = a.Stop(context.Background()) }()

	if lc.enableCalls != 1 {
		t.Fatalf("enableCalls = %d, want 1", lc.enableCalls)
	}
}

func TestAgentServesIPC(t *testing.T) {
	lc := &fakeAgentLC{st: &state.State{Mode: state.ModeOn}}
	a := newTestAgent(t, lc)

	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = a.Stop(context.Background()) }()

	c := ipc.NewClient(a.endpoint)

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st == nil || st.Mode != state.ModeOn {
		t.Fatalf("Status = %+v", st)
	}

	if err = c.Enable(ctx); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if lc.enableCalls != 1 {
		t.Fatalf("enableCalls = %d, want 1", lc.enableCalls)
	}

	if err = a.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if lc.disableCalls != 1 {
		t.Fatalf("disableCalls = %d, want 1", lc.disableCalls)
	}
}

// TestAgentStatusPipeReadOnly checks the read-only status pipe returns the
// sanitized DTO and rejects state-changing methods.
func TestAgentStatusPipeReadOnly(t *testing.T) {
	lc := &fakeAgentLC{st: &state.State{
		Mode:      state.ModeOn,
		Intercept: state.MechanismNRPT,
		Upstream:  "https://dns.nextdns.io/SECRETID",
		Dirty:     true,
	}}
	a := newTestAgent(t, lc)

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = a.Stop(context.Background()) }()

	c := ipc.NewClient(a.statusEndpoint)
	dto, err := c.StatusSummary(context.Background())
	if err != nil {
		t.Fatalf("StatusSummary: %v", err)
	}
	if dto == nil || !dto.Enabled || dto.Mode != string(state.ModeOn) || dto.Mechanism != string(state.MechanismNRPT) {
		t.Fatalf("DTO = %+v", dto)
	}

	if err = c.Enable(context.Background()); err == nil {
		t.Fatal("Enable on status pipe should be forbidden")
	}
	if lc.enableCalls != 0 {
		t.Fatal("status pipe reached Enable")
	}
}

// TestAgentStopGated checks Stop does not error or call Disable when nothing was
// enabled.
func TestAgentStopGated(t *testing.T) {
	t.Run("no state", func(t *testing.T) {
		lc := &fakeAgentLC{stErr: state.ErrNoState}
		a := newTestAgent(t, lc)

		if err := a.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := a.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if lc.disableCalls != 0 {
			t.Fatalf("disableCalls = %d, want 0", lc.disableCalls)
		}
	})

	t.Run("mode off clean", func(t *testing.T) {
		lc := &fakeAgentLC{st: &state.State{Mode: state.ModeOff}}
		a := newTestAgent(t, lc)

		if err := a.Start(context.Background()); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := a.Stop(context.Background()); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if lc.disableCalls != 0 {
			t.Fatalf("disableCalls = %d, want 0", lc.disableCalls)
		}
	})
}
