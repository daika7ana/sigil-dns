package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/state"
)

// fakeAgentLC is an in-memory service.Lifecycle.
type fakeAgentLC struct {
	st           *state.State
	startupErr   error
	enableErr    error
	reenable     bool
	enableCalls  int
	disableCalls int
}

func (f *fakeAgentLC) Status() (st *state.State, err error) { return f.st, nil }

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

func TestAgentRecoveryReenables(t *testing.T) {
	lc := &fakeAgentLC{reenable: true}
	a := NewAgent(lc, nil, filepath.Join(t.TempDir(), "no.sock"))

	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
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
	a := NewAgent(lc, nil, filepath.Join(t.TempDir(), "no.sock"))

	if err := a.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if lc.enableCalls != 0 {
		t.Fatalf("enableCalls = %d, want 0", lc.enableCalls)
	}
}

func TestAgentStartupError(t *testing.T) {
	want := errors.New("boom")
	a := NewAgent(&fakeAgentLC{startupErr: want}, nil, filepath.Join(t.TempDir(), "no.sock"))

	if err := a.Start(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Start err = %v, want %v", err, want)
	}
}

func TestAgentServesIPC(t *testing.T) {
	lc := &fakeAgentLC{st: &state.State{Mode: state.ModeOn}}
	endpoint := filepath.Join(t.TempDir(), "ipc.sock")
	a := NewAgent(lc, nil, endpoint)

	ctx := context.Background()
	if err := a.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = a.Stop(context.Background()) }()

	c := ipc.NewClient(endpoint)

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
