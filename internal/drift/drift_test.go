package drift

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDecide(t *testing.T) {
	cases := []struct {
		name     string
		expected State
		actual   State
		want     Decision
	}{
		{
			name:     "nrpt ok",
			expected: State{NRPTOurs: true},
			actual:   State{NRPTOurs: true},
			want:     DecisionOK,
		},
		{
			name:     "nrpt rule missing",
			expected: State{NRPTOurs: true},
			actual:   State{NRPTOurs: false},
			want:     DecisionApply,
		},
		{
			name:     "conflicting rule",
			expected: State{NRPTOurs: true},
			actual:   State{NRPTOurs: true, NRPTConflicts: []string{"vpn|."}},
			want:     DecisionConflict,
		},
		{
			name:     "conflict ignored when not using nrpt",
			expected: State{NRPTOurs: false},
			actual:   State{NRPTConflicts: []string{"vpn|."}},
			want:     DecisionOK,
		},
		{
			name:     "nothing expected",
			expected: State{},
			actual:   State{NRPTOurs: false},
			want:     DecisionOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Decide(tc.expected, tc.actual); got != tc.want {
				t.Fatalf("Decide = %v, want %v", got, tc.want)
			}
		})
	}
}

// fakes.

type fakeProber struct {
	fn    func(call int) (Observation, error)
	calls atomic.Int32
}

func (f *fakeProber) Probe(context.Context) (Observation, error) {
	return f.fn(int(f.calls.Add(1)))
}

type fakeApplier struct {
	err     error
	applies atomic.Int32
}

func (f *fakeApplier) Apply(context.Context) error {
	f.applies.Add(1)

	return f.err
}

type fakeDisabler struct {
	disables atomic.Int32
	err      error
}

func (f *fakeDisabler) Disable(context.Context) error {
	f.disables.Add(1)

	return f.err
}

func driftObs() (obs Observation) {
	return Observation{Expected: State{NRPTOurs: true}, Actual: State{NRPTOurs: false}}
}

func okObs() (obs Observation) {
	return Observation{Expected: State{NRPTOurs: true}, Actual: State{NRPTOurs: true}}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(time.Millisecond)
	}

	t.Fatal("condition not met in time")
}

func runAsync(r *Reconciler) (done chan error, cancel context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan error, 1)
	go func() { done <- r.Run(ctx, nil) }()

	return done, cancel
}

func TestReconcilerGivesUpOnPersistentDrift(t *testing.T) {
	p := &fakeProber{fn: func(int) (Observation, error) { return driftObs(), nil }}
	ap := &fakeApplier{}
	dis := &fakeDisabler{}
	stopped := false

	r := NewReconciler(Options{
		Prober: p, Applier: ap, Disabler: dis, Interval: time.Millisecond, MaxAttempts: 3,
		OnDisable: func() { stopped = true },
	})

	done, cancel := runAsync(r)
	defer cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not give up")
	}

	// Attempts 1 and 2 re-apply; attempt 3 gives up and auto-disables.
	if got := ap.applies.Load(); got != 2 {
		t.Errorf("apply calls = %d, want 2", got)
	}
	if got := dis.disables.Load(); got != 1 {
		t.Errorf("disable calls = %d, want 1", got)
	}
	if !stopped {
		t.Error("OnDisable not called after auto-disable")
	}
}

func TestReconcilerKeepsRunningWhenAutoDisableFails(t *testing.T) {
	p := &fakeProber{fn: func(int) (Observation, error) { return driftObs(), nil }}
	dis := &fakeDisabler{err: errors.New("nrpt failed")}
	stopped := false

	r := NewReconciler(Options{
		Prober: p, Disabler: dis, Interval: time.Millisecond, MaxAttempts: 3,
		OnDisable: func() { stopped = true },
	})

	done, cancel := runAsync(r)
	defer cancel()

	waitFor(t, func() bool { return dis.disables.Load() >= 1 })
	if stopped {
		t.Fatal("OnDisable must not run when the auto-disable failed")
	}

	select {
	case <-done:
		t.Fatal("Run stopped after a failed auto-disable")
	default:
	}
}

func TestReconcilerReappliesThenResolves(t *testing.T) {
	p := &fakeProber{fn: func(call int) (Observation, error) {
		if call == 1 {
			return driftObs(), nil
		}

		return okObs(), nil
	}}
	ap := &fakeApplier{}

	r := NewReconciler(Options{Prober: p, Applier: ap, Interval: time.Millisecond, MaxAttempts: 5})

	done, cancel := runAsync(r)
	defer cancel()

	waitFor(t, func() bool { return ap.applies.Load() >= 1 })
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}

	if got := ap.applies.Load(); got != 1 {
		t.Errorf("apply calls = %d, want 1", got)
	}
}

func TestReconcilerConflictDoesNotFight(t *testing.T) {
	conflict := Observation{
		Expected: State{NRPTOurs: true},
		Actual:   State{NRPTOurs: true, NRPTConflicts: []string{"vpn|."}},
	}
	p := &fakeProber{fn: func(int) (Observation, error) { return conflict, nil }}
	ap := &fakeApplier{}
	dis := &fakeDisabler{}

	r := NewReconciler(Options{Prober: p, Applier: ap, Disabler: dis, Interval: time.Millisecond, MaxAttempts: 3})

	done, cancel := runAsync(r)

	waitFor(t, func() bool { return p.calls.Load() >= 5 })
	cancel()
	<-done

	if got := ap.applies.Load(); got != 0 {
		t.Errorf("apply calls = %d, want 0 (must not fight a conflict)", got)
	}
	if got := dis.disables.Load(); got != 0 {
		t.Errorf("disable calls = %d, want 0", got)
	}
}

func TestReconcilerStopsOnCancel(t *testing.T) {
	p := &fakeProber{fn: func(int) (Observation, error) { return okObs(), nil }}
	r := NewReconciler(Options{Prober: p, Interval: time.Millisecond})

	done, cancel := runAsync(r)
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop promptly on cancel")
	}
}

func TestReconcilerNilProber(t *testing.T) {
	r := NewReconciler(Options{Interval: time.Millisecond})

	done, cancel := runAsync(r)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestReconcilerStartStop(t *testing.T) {
	p := &fakeProber{fn: func(int) (Observation, error) { return okObs(), nil }}
	r := NewReconciler(Options{Prober: p, Interval: time.Millisecond})

	r.Start()
	waitFor(t, func() bool { return p.calls.Load() >= 1 })
	r.Stop()

	// Stop is idempotent.
	r.Stop()
}
