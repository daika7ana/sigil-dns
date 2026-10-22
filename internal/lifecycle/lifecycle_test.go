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

	"github.com/daika7ana/sigil-dns/internal/health"
	"github.com/daika7ana/sigil-dns/internal/resolver"
	"github.com/daika7ana/sigil-dns/internal/state"
)

// testResolver is a resolverHandle that records Start/Shutdown into a shared
// event log instead of binding sockets.
type testResolver struct {
	events      *[]string
	cacheClears int
}

func (r *testResolver) Start(context.Context) error {
	*r.events = append(*r.events, "bind")

	return nil
}

func (r *testResolver) Shutdown(context.Context) error {
	*r.events = append(*r.events, "shutdown")

	return nil
}

func (r *testResolver) SyntheticName() string              { return "probe.sigildns-test.invalid." }
func (r *testResolver) UDPAddrs() []string                 { return []string{"127.0.0.1:53"} }
func (r *testResolver) RefreshLists(context.Context) error { return nil }
func (r *testResolver) ClearCache()                        { r.cacheClears++ }

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

// dirtyState is a persisted state left behind by a crash: mode on, dirty set.
func dirtyState() *state.State {
	return &state.State{
		SchemaVersion: state.SchemaVersion,
		Mode:          state.ModeOn,
		Intercept:     state.MechanismNRPT,
		Dirty:         true,
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

// TestStartupAlwaysEnables: a running service always intercepts, so Startup
// readies an enable regardless of the persisted mode (off, missing, or a dirty
// crash leftover).
func TestStartupAlwaysEnables(t *testing.T) {
	tests := []struct {
		name string
		seed *state.State
	}{
		{"no state", nil},
		{"mode off", &state.State{SchemaVersion: state.SchemaVersion, Mode: state.ModeOff}},
		{"dirty leftover", dirtyState()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			if tt.seed != nil {
				e.seed(t, tt.seed)
			}

			ctx := context.Background()
			if err := e.lc.Startup(ctx); err != nil {
				t.Fatalf("Startup: %v", err)
			}
			if err := e.lc.Enable(ctx); err != nil {
				t.Fatalf("Enable after Startup: %v", err)
			}

			st, err := e.store.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if st.Mode != state.ModeOn || !st.Dirty {
				t.Fatalf("state = %+v, want mode=on dirty=true", st)
			}
		})
	}
}

// TestRestoreKeepsLiveSessionLock: a control-pipe Restore runs while the
// service already holds the lock; it must not release it under the running
// session.
func TestRestoreKeepsLiveSessionLock(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()

	if err := e.lc.Enable(ctx); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if e.lc.lock == nil {
		t.Fatal("Enable did not retain the lock")
	}

	if err := e.lc.Restore(ctx); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if e.lc.lock == nil {
		t.Fatal("Restore released a lock it did not acquire")
	}

	if err := e.lc.Disable(ctx); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if e.lc.lock != nil {
		t.Fatal("Disable did not release the lock")
	}
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
			name: "second enable is a no-op while enabled",
			run: func(ctx context.Context, e *testEnv) error {
				if err := e.lc.Enable(ctx); err != nil {
					return err
				}

				return e.lc.Enable(ctx)
			},
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("Enable twice: %v", err)
				}
				want := []string{"bind", "health", "intercept-enable", "flush"}
				if !reflect.DeepEqual(*e.events, want) {
					t.Fatalf("events = %v, want %v", *e.events, want)
				}
				if e.ic.enabled != 1 {
					t.Fatalf("intercept enable = %d, want 1", e.ic.enabled)
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
			name: "enable failure restores and stops the resolver",
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
					t.Fatal("resolver was not stopped after a successful restore")
				}
				st, lErr := e.store.Load()
				if lErr != nil {
					t.Fatalf("Load: %v", lErr)
				}
				if st.Dirty || st.Mode != state.ModeOff {
					t.Fatalf("state = %+v, want dirty=false mode=off", st)
				}
				if e.ic.disabled != 1 {
					t.Fatalf("intercept disable = %d, want 1", e.ic.disabled)
				}
			},
		},
		{
			name: "enable failure keeps dirty and resolver when restore fails",
			setup: func(t *testing.T, e *testEnv) {
				t.Helper()
				e.ic.enableErr = errors.New("boom")
				e.ic.disableErr = errors.New("restore failed")
			},
			run: func(ctx context.Context, e *testEnv) error { return e.lc.Enable(ctx) },
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err == nil || !strings.Contains(err.Error(), "enabling interception") {
					t.Fatalf("Enable err = %v, want enabling-interception error", err)
				}
				if indexOf(*e.events, "shutdown") >= 0 {
					t.Fatal("resolver was stopped although interception could not be restored")
				}
				st, lErr := e.store.Load()
				if lErr != nil {
					t.Fatalf("Load: %v", lErr)
				}
				if !st.Dirty || st.Mode != state.ModeOn {
					t.Fatalf("state = %+v, want dirty=true preserved", st)
				}
				if e.lc.res == nil {
					t.Fatal("resolver should stay up so DNS is not pointed at a dead port")
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
			name: "disable restores before stopping the resolver",
			run: func(ctx context.Context, e *testEnv) error {
				if err := e.lc.Enable(ctx); err != nil {
					return err
				}

				return e.lc.Disable(ctx)
			},
			check: func(t *testing.T, e *testEnv, err error) {
				t.Helper()
				if err != nil {
					t.Fatalf("Enable+Disable: %v", err)
				}
				disable := indexOf(*e.events, "intercept-disable")
				shutdown := indexOf(*e.events, "shutdown")
				if disable < 0 || shutdown < 0 || disable > shutdown {
					t.Fatalf("events = %v, want intercept-disable before shutdown", *e.events)
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

// TestClearCacheClearsResolver proves the cache action reaches the running
// resolver, not only the OS cache.
func TestClearCacheClearsResolver(t *testing.T) {
	env := newTestEnv(t)
	res := &testResolver{events: env.events}
	env.lc.res = res

	if err := env.lc.ClearCache(context.Background()); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}
	if res.cacheClears != 1 {
		t.Fatalf("resolver cache clears = %d, want 1", res.cacheClears)
	}
}
