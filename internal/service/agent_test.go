package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/daika7ana/sigil-dns/internal/ipc"
	"github.com/daika7ana/sigil-dns/internal/state"
)

// fakeAgentLC is an in-memory service.Lifecycle.
type fakeAgentLC struct {
	st           *state.State
	startupErr   error
	enableErr    error
	stErr        error
	enableCalls  int
	disableCalls int
}

func (f *fakeAgentLC) Status() (st *state.State, err error) { return f.st, f.stErr }

func (f *fakeAgentLC) Startup(context.Context) (err error) {
	return f.startupErr
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

func (f *fakeAgentLC) RefreshLists(context.Context) (err error) { return nil }

// testEndpoint returns a platform-appropriate, unique IPC endpoint: a named
// pipe on Windows (a filesystem path is not a valid pipe name), else a temp
// Unix socket.
func testEndpoint(t *testing.T, kind string) (endpoint string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		var b [8]byte
		_, _ = rand.Read(b[:])

		return `\\.\pipe\sigildns-test-` + kind + "-" + hex.EncodeToString(b[:])
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

func TestAgentStartEnables(t *testing.T) {
	lc := &fakeAgentLC{st: &state.State{Mode: state.ModeOn}}
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

// TestAgentStartupErrorStops: a recovery failure stops the service instead of
// leaving it running with interception off.
func TestAgentStartupErrorStops(t *testing.T) {
	want := errors.New("boom")
	lc := &fakeAgentLC{startupErr: want, st: &state.State{Mode: state.ModeOn}}
	a := newTestAgent(t, lc)

	defer func() { _ = a.Stop(context.Background()) }()

	if err := a.Start(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Start err = %v, want %v", err, want)
	}
	if lc.disableCalls != 1 {
		t.Fatalf("disableCalls = %d, want 1 cleanup attempt", lc.disableCalls)
	}
}

// TestAgentEnableFailureStops: an enable failure stops the service and attempts
// to restore whatever interception was partially applied.
func TestAgentEnableFailureStops(t *testing.T) {
	want := errors.New("nrpt failed")
	lc := &fakeAgentLC{enableErr: want, st: &state.State{Mode: state.ModeOn}}
	a := newTestAgent(t, lc)

	defer func() { _ = a.Stop(context.Background()) }()

	if err := a.Start(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Start err = %v, want %v", err, want)
	}

	if lc.enableCalls != 1 {
		t.Fatalf("enableCalls = %d, want 1", lc.enableCalls)
	}
	if lc.disableCalls != 1 {
		t.Fatalf("disableCalls = %d, want 1 cleanup attempt", lc.disableCalls)
	}
}

// blockingAgentLC blocks Startup until the test releases it.
type blockingAgentLC struct {
	fakeAgentLC
	started chan struct{}
	release chan struct{}
}

func (b *blockingAgentLC) Startup(context.Context) (err error) {
	close(b.started)
	<-b.release

	return nil
}

// TestAgentStopDuringStartupDoesNotEnable: a stop that arrives while Start is
// still recovering must wait for Start and must not let a late enable run.
func TestAgentStopDuringStartupDoesNotEnable(t *testing.T) {
	lc := &blockingAgentLC{started: make(chan struct{}), release: make(chan struct{})}
	a := newTestAgent(t, lc)

	startErr := make(chan error, 1)
	go func() { startErr <- a.Start(context.Background()) }()
	<-lc.started

	// Signal the stop before Stop itself is scheduled, so this test is about
	// the no-late-enable guarantee, not goroutine timing.
	a.RequestStop()

	stopErr := make(chan error, 1)
	go func() { stopErr <- a.Stop(context.Background()) }()

	select {
	case err := <-stopErr:
		t.Fatalf("Stop returned before Start finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(lc.release)

	select {
	case err := <-startErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return")
	}

	select {
	case err := <-stopErr:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return")
	}

	if lc.enableCalls != 0 {
		t.Fatalf("enableCalls = %d, want 0 after a stop during startup", lc.enableCalls)
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

	conn, err := ipc.Dial(ctx, a.endpoint)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var st *state.State
	if err = ipc.Exchange(conn, ipc.MethodStatus, &st); err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st == nil || st.Mode != state.ModeOn {
		t.Fatalf("Status = %+v", st)
	}

	// Start always enables, so the control-pipe Enable is the second call.
	if lc.enableCalls != 1 {
		t.Fatalf("enableCalls after Start = %d, want 1", lc.enableCalls)
	}

	if err = c.Enable(ctx); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if lc.enableCalls != 2 {
		t.Fatalf("enableCalls after Enable = %d, want 2", lc.enableCalls)
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

	ctx := context.Background()
	conn, err := ipc.Dial(ctx, a.statusEndpoint)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var dto ipc.StatusDTO
	if err = ipc.Exchange(conn, ipc.MethodStatus, &dto); err != nil {
		t.Fatalf("StatusSummary: %v", err)
	}
	if !dto.Enabled || dto.Mode != string(state.ModeOn) || dto.Mechanism != string(state.MechanismNRPT) {
		t.Fatalf("DTO = %+v", dto)
	}

	before := lc.enableCalls

	if err = c.Enable(ctx); err == nil {
		t.Fatal("Enable on status pipe should be forbidden")
	}
	if lc.enableCalls != before {
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
