package lifecycle

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/daika7ana/dns-client/internal/health"
	"github.com/daika7ana/dns-client/internal/resolver"
	"github.com/daika7ana/dns-client/internal/state"
)

// testResolver is a resolverHandle that records Start/Shutdown into a shared
// event log instead of binding sockets.
type testResolver struct {
	events *[]string
}

func (r *testResolver) Start(context.Context) error {
	*r.events = append(*r.events, "bind")

	return nil
}

func (r *testResolver) Shutdown(context.Context) error {
	*r.events = append(*r.events, "shutdown")

	return nil
}

func (r *testResolver) SyntheticName() string              { return "probe.dns-client-test.invalid." }
func (r *testResolver) UDPAddrs() []string                 { return []string{"127.0.0.1:53"} }
func (r *testResolver) RefreshLists(context.Context) error { return nil }

// testIntercept is an intercept.Controller that records calls, captures the
// dirty flag it observes when Enable runs (proving the snapshot was persisted
// first), and can fail on demand.
type testIntercept struct {
	events        *[]string
	store         *state.Store
	enableErr     error
	disableErr    error
	enabled       int
	disabled      int
	dirtyAtEnable *bool
}

func (i *testIntercept) Enable(_ context.Context, _ []string) error {
	*i.events = append(*i.events, "intercept-enable")
	i.enabled++
	if st, err := i.store.Load(); err == nil {
		dirty := st.Dirty
		i.dirtyAtEnable = &dirty
	}

	return i.enableErr
}

func (i *testIntercept) Disable(context.Context) error {
	*i.events = append(*i.events, "intercept-disable")
	i.disabled++

	return i.disableErr
}

// testEnv wires a Lifecycle to hermetic fakes and a temp state path.
type testEnv struct {
	lc     *Lifecycle
	store  *state.Store
	ic     *testIntercept
	events *[]string
	path   string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	path := filepath.Join(t.TempDir(), "state.json")
	events := &[]string{}

	lc := New(Config{
		StatePath: path,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	store := state.NewStore(path)
	ic := &testIntercept{events: events, store: store}

	lc.ic = ic
	lc.newResolver = func(resolver.Config) (resolverHandle, error) {
		return &testResolver{events: events}, nil
	}
	lc.checkHealth = func(context.Context, health.Target) health.Result {
		*events = append(*events, "health")

		return health.Result{OK: true, Ours: true, IPv4: true, Detail: "ok"}
	}
	lc.flushDNS = func(context.Context) { *events = append(*events, "flush") }

	return &testEnv{lc: lc, store: store, ic: ic, events: events, path: path}
}

// seed writes st to the state file.
func (e *testEnv) seed(t *testing.T, st *state.State) {
	t.Helper()

	if err := e.store.Save(st); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

// dirtyState is a persisted state left behind by a crash: mode on, dirty set,
// with the real pre-interception snapshot captured.
func dirtyState() *state.State {
	return &state.State{
		SchemaVersion: state.SchemaVersion,
		Mode:          state.ModeOn,
		Intercept:     state.MechanismNRPT,
		Dirty:         true,
		Snapshot: &state.Snapshot{
			Interfaces: []state.InterfaceDNS{{GUID: "{real}", IPv4: []string{"1.1.1.1"}}},
		},
	}
}

func indexOf(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}

	return -1
}

// TestLifecycleInvariants pins the enable/disable/restore safety invariants with
// hermetic fakes; the default New wiring is unchanged (the production defaults
// are the real resolver, health.Check and the OS flush).
func TestLifecycleInvariants(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *testEnv)
		run   func(context.Context, *testEnv) error
		check func(*testing.T, *testEnv, error)
	}{
		{
			name: "enable order: bind, health, snapshot+dirty, intercept, flush",
			run:  func(ctx context.Context, e *testEnv) error { return e.lc.Enable(ctx) },
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("Enable: %v", err)
				}
				want := []string{"bind", "health", "intercept-enable", "flush"}
				if !reflect.DeepEqual(*e.events, want) {
					t.Fatalf("events = %v, want %v", *e.events, want)
				}
				// The snapshot+dirty marker must be persisted before interception
				// is applied.
				if e.ic.dirtyAtEnable == nil || !*e.ic.dirtyAtEnable {
					t.Fatal("intercept Enable ran before the dirty marker was persisted")
				}
				st, err := e.store.Load()
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				if st.Mode != state.ModeOn || !st.Dirty {
					t.Fatalf("state = %+v, want mode=on dirty=true", st)
				}
			},
		},
		{
			name: "enable restores the dirty snapshot before binding",
			setup: func(t *testing.T, e *testEnv) {
				t.Helper()
				e.seed(t, dirtyState())
			},
			run: func(ctx context.Context, e *testEnv) error { return e.lc.Enable(ctx) },
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("Enable: %v", err)
				}
				want := []string{"intercept-disable", "flush", "bind", "health", "intercept-enable", "flush"}
				if !reflect.DeepEqual(*e.events, want) {
					t.Fatalf("events = %v, want %v", *e.events, want)
				}
				if e.ic.disabled != 1 || e.ic.enabled != 1 {
					t.Fatalf("intercept disable/enable = %d/%d, want 1/1", e.ic.disabled, e.ic.enabled)
				}
			},
		},
		{
			name: "enable keeps dirty when interception apply fails",
			setup: func(t *testing.T, e *testEnv) {
				t.Helper()
				e.ic.enableErr = errors.New("boom")
			},
			run: func(ctx context.Context, e *testEnv) error { return e.lc.Enable(ctx) },
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "enabling interception") {
					t.Fatalf("Enable err = %v, want enabling-interception error", err)
				}
				if indexOf(*e.events, "shutdown") < 0 {
					t.Fatal("resolver was not stopped after interception failed")
				}
				st, lErr := e.store.Load()
				if lErr != nil {
					t.Fatalf("Load: %v", lErr)
				}
				if !st.Dirty || st.Mode != state.ModeOn {
					t.Fatalf("state = %+v, want dirty=true preserved", st)
				}
			},
		},
		{
			name: "enable twice restores before re-enabling (never re-snapshots dirty)",
			run: func(ctx context.Context, e *testEnv) error {
				if err := e.lc.Enable(ctx); err != nil {
					return err
				}

				return e.lc.Enable(ctx)
			},
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("second Enable: %v", err)
				}
				// The second enable must undo the first before re-applying, rather
				// than snapshotting over the loopbacked system.
				if e.ic.disabled != 1 {
					t.Fatalf("intercept disable = %d, want 1 (restore first)", e.ic.disabled)
				}
				events := *e.events
				secondDisable := indexOf(events[indexOf(events, "intercept-enable")+1:], "intercept-disable")
				if secondDisable < 0 {
					t.Fatalf("events = %v, want a restore before the second apply", events)
				}
			},
		},
		{
			name: "disable when off is a no-op returning nil",
			run:  func(ctx context.Context, e *testEnv) error { return e.lc.Disable(ctx) },
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("Disable: %v", err)
				}
			},
		},
		{
			name: "status without state returns ErrNoState",
			run: func(context.Context, *testEnv) error {
				return nil
			},
			check: func(t *testing.T, e *testEnv, _ error) {
				t.Helper()
				if _, err := e.lc.Status(); !errors.Is(err, state.ErrNoState) {
					t.Fatalf("Status err = %v, want ErrNoState", err)
				}
			},
		},
		{
			name: "restore clears dirty only after success",
			setup: func(t *testing.T, e *testEnv) {
				t.Helper()
				e.seed(t, dirtyState())
			},
			run: func(ctx context.Context, e *testEnv) error { return e.lc.Restore(ctx) },
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("Restore: %v", err)
				}
				st, lErr := e.store.Load()
				if lErr != nil {
					t.Fatalf("Load: %v", lErr)
				}
				if st.Dirty || st.Mode != state.ModeOff {
					t.Fatalf("state = %+v, want dirty=false mode=off", st)
				}
				// The original snapshot must survive the restore untouched.
				got := st.Snapshot.Interfaces[0].IPv4[0]
				if got != "1.1.1.1" {
					t.Fatalf("snapshot IPv4 = %q, want the original 1.1.1.1", got)
				}
			},
		},
		{
			name: "restore keeps dirty on failure",
			setup: func(t *testing.T, e *testEnv) {
				t.Helper()
				e.seed(t, dirtyState())
				e.ic.disableErr = errors.New("restore failed")
			},
			run: func(ctx context.Context, e *testEnv) error { return e.lc.Restore(ctx) },
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err == nil {
					t.Fatal("Restore should fail")
				}
				st, lErr := e.store.Load()
				if lErr != nil {
					t.Fatalf("Load: %v", lErr)
				}
				if !st.Dirty {
					t.Fatalf("state = %+v, want dirty=true kept on failure", st)
				}
			},
		},
		{
			name: "restore with corrupt state still disables interception",
			setup: func(t *testing.T, e *testEnv) {
				t.Helper()
				if err := os.WriteFile(e.path, []byte("{not valid json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			run: func(ctx context.Context, e *testEnv) error { return e.lc.Restore(ctx) },
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err == nil {
					t.Fatal("Restore should surface the corrupt-state error")
				}
				if errors.Is(err, state.ErrNoState) {
					t.Fatalf("err = %v, corrupt is not missing", err)
				}
				if !strings.Contains(err.Error(), "parsing state") {
					t.Fatalf("err = %v, want the original parse error", err)
				}
				if e.ic.disabled != 1 {
					t.Fatalf("intercept disable = %d, want 1 (best-effort cleanup)", e.ic.disabled)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			if tc.setup != nil {
				tc.setup(t, env)
			}

			err := tc.run(context.Background(), env)
			tc.check(t, env, err)
		})
	}
}
