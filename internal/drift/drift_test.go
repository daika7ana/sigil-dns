package drift

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// TestExpectedNamespaces pins the bypass-domain to NRPT-namespace mapping used
// by the drift comparison.
func TestExpectedNamespaces(t *testing.T) {
	got := expectedNamespaces([]string{"*.Corp.Example", "", "corp.example", "local.example."})
	want := []string{"", "corp.example", "local.example"}
	if !slices.Equal(got, want) {
		t.Fatalf("expectedNamespaces = %q, want %q", got, want)
	}

	if got := namespaceKey("  ExAmple.ORG. "); got != "example.org" {
		t.Fatalf("namespaceKey = %q, want %q", got, "example.org")
	}
}

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
		{
			name:     "catch-all missing while an exemption survives",
			expected: State{NRPTOurs: true, Namespaces: []string{"", "corp.example"}},
			actual:   State{NRPTOurs: true, Namespaces: []string{"corp.example"}},
			want:     DecisionApply,
		},
		{
			name:     "bypass exemption missing",
			expected: State{NRPTOurs: true, Namespaces: []string{"", "corp.example"}},
			actual:   State{NRPTOurs: true, Namespaces: []string{""}},
			want:     DecisionApply,
		},
		{
			name:     "stale own rule is drift",
			expected: State{NRPTOurs: true, Namespaces: []string{""}},
			actual:   State{NRPTOurs: true, Namespaces: []string{"", "old.example"}},
			want:     DecisionApply,
		},
		{
			name: "catch-all name servers differ",
			expected: State{
				NRPTOurs:   true,
				Namespaces: []string{""},
				Servers:    map[string][]string{"": {"127.0.0.1"}},
			},
			actual: State{
				NRPTOurs:   true,
				Namespaces: []string{""},
				Servers:    map[string][]string{"": {"127.0.0.1", "::1"}},
			},
			want: DecisionApply,
		},
		{
			name: "catch-all name servers match",
			expected: State{
				NRPTOurs:   true,
				Namespaces: []string{""},
				Servers:    map[string][]string{"": {"::1", "127.0.0.1"}},
			},
			actual: State{
				NRPTOurs:   true,
				Namespaces: []string{""},
				Servers:    map[string][]string{"": {"0:0:0:0:0:0:0:1", "127.0.0.1"}},
			},
			want: DecisionOK,
		},
		{
			name:     "foreign conflict with missing ours",
			expected: State{NRPTOurs: true, Namespaces: []string{""}},
			actual:   State{NRPTConflicts: []string{"vpn|."}},
			want:     DecisionApply,
		},
		{
			name:     "conflict only when ours is present",
			expected: State{NRPTOurs: true, Namespaces: []string{""}},
			actual:   State{NRPTOurs: true, Namespaces: []string{""}, NRPTConflicts: []string{"vpn|."}},
			want:     DecisionConflict,
		},
		{
			name:     "namespaces compare dot-normalized",
			expected: State{NRPTOurs: true, Namespaces: []string{" .Corp.Example. "}},
			actual:   State{NRPTOurs: true, Namespaces: []string{"corp.example"}},
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

	// Re-applies 1 and 2 fail their re-probe; the third failed re-apply
	// auto-disables.
	if got := ap.applies.Load(); got != 3 {
		t.Errorf("apply calls = %d, want 3", got)
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

func TestReconcilerConflictDoesNotResetStrikes(t *testing.T) {
	conflict := Observation{
		Expected: State{NRPTOurs: true, Namespaces: []string{""}},
		Actual:   State{NRPTOurs: true, Namespaces: []string{""}, NRPTConflicts: []string{"vpn|."}},
	}
	// Two failed re-applies, one conflict tick, then a third failed re-apply.
	p := &fakeProber{fn: func(call int) (Observation, error) {
		if call == 5 {
			return conflict, nil
		}

		return driftObs(), nil
	}}
	ap := &fakeApplier{}
	dis := &fakeDisabler{}

	r := NewReconciler(Options{Prober: p, Applier: ap, Disabler: dis, Interval: time.Millisecond, MaxAttempts: 3})

	done, cancel := runAsync(r)
	defer cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a conflict tick reset the strike count")
	}

	if got := dis.disables.Load(); got != 1 {
		t.Errorf("disable calls = %d, want 1", got)
	}
}

func TestReconcileBypassOnlyDriftNeverDisables(t *testing.T) {
	obs := Observation{
		Expected: State{NRPTOurs: true, Namespaces: []string{"", "corp.example"}},
		Actual:   State{NRPTOurs: true, Namespaces: []string{""}},
	}
	p := &fakeProber{fn: func(int) (Observation, error) { return obs, nil }}
	ap := &fakeApplier{}
	dis := &fakeDisabler{}

	// MaxAttempts 1 would disable on the first strike if best-effort bypass
	// drift were ever counted.
	r := NewReconciler(Options{Prober: p, Applier: ap, Disabler: dis, MaxAttempts: 1})

	attempts := 0
	for range 5 {
		if stop := r.reconcile(context.Background(), &attempts); stop {
			t.Fatal("reconcile stopped for a bypass-only drift")
		}
	}

	if got := ap.applies.Load(); got != 1 {
		t.Errorf("apply calls = %d, want 1 (retry once, then stop)", got)
	}
	if got := dis.disables.Load(); got != 0 {
		t.Errorf("disable calls = %d, want 0", got)
	}
	if attempts != 0 {
		t.Errorf("attempts = %d, want 0", attempts)
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

func healthyObs() (obs Observation) {
	return Observation{
		Expected: State{NRPTOurs: true, Namespaces: []string{""}},
		Actual:   State{NRPTOurs: true, Namespaces: []string{""}},
	}
}

// TestReconcileHealthFailuresAutoDisable: a bound but wedged resolver is a
// silent outage, so N consecutive health failures take the same fail-closed
// path as persistent drift.
func TestReconcileHealthFailuresAutoDisable(t *testing.T) {
	p := &fakeProber{fn: func(int) (Observation, error) { return healthyObs(), nil }}
	dis := &fakeDisabler{}
	stopped := false

	r := NewReconciler(Options{
		Prober: p, Disabler: dis, Interval: time.Millisecond,
		Health:    func(context.Context) error { return errors.New("upstream timeout") },
		OnDisable: func() { stopped = true },
	})

	attempts := 0
	for i := 0; i < r.maxHealthFails-1; i++ {
		if stop := r.reconcile(context.Background(), &attempts); stop {
			t.Fatalf("reconcile %d stopped before the health threshold", i+1)
		}
	}
	if got := dis.disables.Load(); got != 0 {
		t.Fatalf("disable calls = %d, want 0 before the threshold", got)
	}

	if stop := r.reconcile(context.Background(), &attempts); !stop {
		t.Fatal("reconcile did not stop after repeated health failures")
	}
	if got := dis.disables.Load(); got != 1 {
		t.Errorf("disable calls = %d, want 1", got)
	}
	if !stopped {
		t.Error("OnDisable not called after a health-triggered disable")
	}
}

// TestReconcileHealthSuccessResetsFailures: one healthy probe between failures
// clears the streak, so a flapping upstream never disables protection.
func TestReconcileHealthSuccessResetsFailures(t *testing.T) {
	p := &fakeProber{fn: func(int) (Observation, error) { return healthyObs(), nil }}
	dis := &fakeDisabler{}

	healthCalls := 0
	r := NewReconciler(Options{
		Prober: p, Disabler: dis, Interval: time.Millisecond,
		Health: func(context.Context) error {
			healthCalls++
			if healthCalls == 3 {
				return nil
			}

			return errors.New("upstream timeout")
		},
	})

	attempts := 0
	for range 4 {
		if stop := r.reconcile(context.Background(), &attempts); stop {
			t.Fatal("reconcile stopped although a success reset the streak")
		}
	}
	if got := dis.disables.Load(); got != 0 {
		t.Fatalf("disable calls = %d, want 0", got)
	}
}

// TestReconcileBypassRetryAfterBackoff: a transient bypass-only failure is
// retried after the backoff instead of staying frozen until restart; it still
// never auto-disables catch-all protection.
func TestReconcileBypassRetryAfterBackoff(t *testing.T) {
	obs := Observation{
		Expected: State{NRPTOurs: true, Namespaces: []string{"", "corp.example"}},
		Actual:   State{NRPTOurs: true, Namespaces: []string{""}},
	}
	p := &fakeProber{fn: func(int) (Observation, error) { return obs, nil }}
	ap := &fakeApplier{}
	dis := &fakeDisabler{}

	r := NewReconciler(Options{Prober: p, Applier: ap, Disabler: dis, MaxAttempts: 1})
	r.bypassRetryDelay = 10 * time.Millisecond

	attempts := 0
	if stop := r.reconcile(context.Background(), &attempts); stop {
		t.Fatal("reconcile stopped for a bypass-only drift")
	}
	if got := ap.applies.Load(); got != 1 {
		t.Fatalf("apply calls = %d, want 1", got)
	}

	// Inside the backoff window: no retry.
	if stop := r.reconcile(context.Background(), &attempts); stop {
		t.Fatal("reconcile stopped for a bypass-only drift")
	}
	if got := ap.applies.Load(); got != 1 {
		t.Fatalf("apply calls = %d, want 1 inside the backoff", got)
	}

	time.Sleep(3 * r.bypassRetryDelay)
	if stop := r.reconcile(context.Background(), &attempts); stop {
		t.Fatal("reconcile stopped for a bypass-only drift")
	}
	if got := ap.applies.Load(); got != 2 {
		t.Fatalf("apply calls = %d, want 2 after the backoff", got)
	}
	if got := dis.disables.Load(); got != 0 {
		t.Fatalf("disable calls = %d, want 0", got)
	}
}

// TestReconcilerProbeFailuresStopSupervision: supervision cannot see anything
// while probing fails, so N consecutive errors stop the loop via the service
// stop path (OnDisable), which restores interception.
func TestReconcilerProbeFailuresStopSupervision(t *testing.T) {
	probeErr := errors.New("state file missing")
	p := &fakeProber{fn: func(int) (Observation, error) { return Observation{}, probeErr }}
	stopped := false

	r := NewReconciler(Options{
		Prober: p, Interval: time.Millisecond,
		OnDisable: func() { stopped = true },
	})

	attempts := 0
	for i := range maxProbeFails - 1 {
		if stop := r.reconcile(context.Background(), &attempts); stop {
			t.Fatalf("reconcile stopped after %d probe failures", i+1)
		}
	}
	if stopped {
		t.Fatal("OnDisable ran before the failure threshold")
	}

	if stop := r.reconcile(context.Background(), &attempts); !stop {
		t.Fatal("reconcile did not stop after repeated probe failures")
	}
	if !stopped {
		t.Error("OnDisable not called after persistent probe failures")
	}
}

// TestReconcilerProbeSuccessResetsFailures: one successful probe clears the
// streak, so a flapping probe never stops a healthy service.
func TestReconcilerProbeSuccessResetsFailures(t *testing.T) {
	probeErr := errors.New("state file missing")
	p := &fakeProber{fn: func(call int) (Observation, error) {
		if call == 3 {
			return okObs(), nil
		}

		return Observation{}, probeErr
	}}
	stopped := false

	r := NewReconciler(Options{
		Prober: p, Interval: time.Millisecond,
		OnDisable: func() { stopped = true },
	})

	attempts := 0
	for i := range 5 {
		if stop := r.reconcile(context.Background(), &attempts); stop {
			t.Fatalf("reconcile stopped at call %d although a success reset the streak", i+1)
		}
	}
	if stopped {
		t.Fatal("OnDisable ran although the streak was reset")
	}

	if stop := r.reconcile(context.Background(), &attempts); !stop {
		t.Fatal("reconcile did not stop after three consecutive failures")
	}
	if !stopped {
		t.Error("OnDisable not called after the reset streak reached the threshold")
	}
}

// TestReconcilerConflictStillChecksHealth: a VPN catch-all suppresses drift
// auto-apply, but it must not suppress resolver-health detection.
func TestReconcilerConflictStillChecksHealth(t *testing.T) {
	conflict := Observation{
		Expected: State{NRPTOurs: true, Namespaces: []string{""}},
		Actual:   State{NRPTOurs: true, Namespaces: []string{""}, NRPTConflicts: []string{"vpn|."}},
	}
	p := &fakeProber{fn: func(int) (Observation, error) { return conflict, nil }}
	dis := &fakeDisabler{}
	stopped := false

	r := NewReconciler(Options{
		Prober: p, Disabler: dis, Interval: time.Millisecond,
		Health:    func(context.Context) error { return errors.New("upstream timeout") },
		OnDisable: func() { stopped = true },
	})

	attempts := 0
	for i := 0; i < r.maxHealthFails-1; i++ {
		if stop := r.reconcile(context.Background(), &attempts); stop {
			t.Fatalf("reconcile stopped on conflict before the health threshold (%d)", i+1)
		}
	}
	if got := dis.disables.Load(); got != 0 {
		t.Fatalf("disable calls = %d, want 0 before the health threshold", got)
	}

	if stop := r.reconcile(context.Background(), &attempts); !stop {
		t.Fatal("reconcile did not stop on conflict after repeated health failures")
	}
	if !stopped {
		t.Error("OnDisable not called after a conflict-path health disable")
	}
}
