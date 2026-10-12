package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/lifecycle"
	"github.com/daika7ana/dns-client/internal/state"
)

// TestServiceLifecycleDegradedEnable verifies the service can boot and run
// crash recovery from state alone, while Enable reports a clear error until the
// config loads (M4).
func TestServiceLifecycleDegradedEnable(t *testing.T) {
	dir := t.TempDir()
	inner := lifecycle.New(lifecycle.Config{StatePath: filepath.Join(dir, "state.json")})

	want := errors.New("configuration unavailable")
	lc := &serviceLifecycle{inner: inner, enableErr: want}

	if err := lc.Enable(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Enable err = %v, want %v", err, want)
	}

	// Recovery works without config: no state means nothing to restore.
	reenable, err := lc.Startup(context.Background())
	if err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if reenable {
		t.Fatal("Startup should not request re-enable with no state")
	}

	if _, err = lc.Status(); !errors.Is(err, state.ErrNoState) {
		t.Fatalf("Status err = %v, want ErrNoState", err)
	}
}

// fakeDaemon is an in-memory svcDaemon for reload tests.
type fakeDaemon struct {
	st           *state.State
	enableErr    error
	disableErr   error
	enableCalls  int
	disableCalls int
}

func (f *fakeDaemon) Status() (st *state.State, err error) { return f.st, nil }

func (f *fakeDaemon) Startup(context.Context) (reenable bool, err error) { return false, nil }

func (f *fakeDaemon) Enable(context.Context) (err error) {
	f.enableCalls++

	return f.enableErr
}

func (f *fakeDaemon) Disable(context.Context) (err error) {
	f.disableCalls++

	return f.disableErr
}

func (f *fakeDaemon) Restore(context.Context) (err error) { return nil }

func (f *fakeDaemon) RefreshLists(context.Context) (err error) { return nil }

func TestReloadSwapWhenIdle(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOff}}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOff}}
	want := ipc.ConfigSummary{Profile: "default", Provider: "Quad9", ConfigValid: true}

	lc := &serviceLifecycle{
		inner: old,
		build: func() (svcDaemon, ipc.ConfigSummary, error) { return next, want, nil },
	}

	sum, err := lc.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if sum.Provider != "Quad9" {
		t.Fatalf("summary = %+v", sum)
	}
	if old.disableCalls != 0 || next.enableCalls != 0 {
		t.Fatalf("idle reload should not toggle: disable=%d enable=%d", old.disableCalls, next.enableCalls)
	}
	if lc.inner != svcDaemon(next) {
		t.Fatal("inner was not swapped")
	}
}

func TestReloadRestoresAndReenables(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}

	lc := &serviceLifecycle{
		inner: old,
		build: func() (svcDaemon, ipc.ConfigSummary, error) { return next, ipc.ConfigSummary{ConfigValid: true}, nil },
	}

	if _, err := lc.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if old.disableCalls != 1 {
		t.Fatalf("old.Disable calls = %d, want 1", old.disableCalls)
	}
	if next.enableCalls != 1 || old.enableCalls != 0 {
		t.Fatalf("enable calls: next=%d old=%d, want 1/0", next.enableCalls, old.enableCalls)
	}
	if lc.inner != svcDaemon(next) {
		t.Fatal("inner was not swapped after reload")
	}
}

func TestReloadRollsBackOnEnableFailure(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}, enableErr: errors.New("new config bad")}

	lc := &serviceLifecycle{
		inner: old,
		build: func() (svcDaemon, ipc.ConfigSummary, error) { return next, ipc.ConfigSummary{}, nil },
	}

	_, err := lc.Reload(context.Background())
	if err == nil {
		t.Fatal("Reload should fail when the new config cannot be enabled")
	}
	if old.disableCalls != 1 || next.enableCalls != 1 {
		t.Fatalf("calls: disable=%d nextEnable=%d", old.disableCalls, next.enableCalls)
	}
	if old.enableCalls != 1 {
		t.Fatalf("rollback old.Enable calls = %d, want 1", old.enableCalls)
	}
	if lc.inner != svcDaemon(old) {
		t.Fatal("inner should remain the last-known-good lifecycle")
	}
}

func TestReloadRollbackFailure(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}, enableErr: errors.New("rollback bad")}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}, enableErr: errors.New("new bad")}

	lc := &serviceLifecycle{
		inner: old,
		build: func() (svcDaemon, ipc.ConfigSummary, error) { return next, ipc.ConfigSummary{}, nil },
	}

	if _, err := lc.Reload(context.Background()); err == nil {
		t.Fatal("Reload should fail")
	}
	if lc.inner != svcDaemon(old) {
		t.Fatal("inner should remain the old lifecycle")
	}
}

func TestReloadBuildErrorKeepsCurrent(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}
	buildErr := errors.New("config corrupt")

	lc := &serviceLifecycle{
		inner: old,
		build: func() (svcDaemon, ipc.ConfigSummary, error) {
			return nil, ipc.ConfigSummary{ConfigValid: false, ConfigError: "config could not be loaded or validated"}, buildErr
		},
	}

	sum, err := lc.Reload(context.Background())
	if !errors.Is(err, buildErr) {
		t.Fatalf("err = %v, want build error", err)
	}
	if sum.ConfigValid || sum.ConfigError == "" {
		t.Fatalf("summary = %+v", sum)
	}
	if old.disableCalls != 0 {
		t.Fatal("a build failure must not touch the running lifecycle")
	}
	if lc.inner != svcDaemon(old) {
		t.Fatal("inner should be unchanged")
	}
}

// TestServiceConfigSanitizesErrors asserts a raw YAML/validation error that
// quotes a config value (possibly an id) never escapes serviceConfig.
func TestServiceConfigSanitizesErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// A validation error quotes the offending value verbatim.
	bad := "schema_version: 1\nmode: SECRETMODE\nprofile: default\nprofiles:\n  default:\n    upstreams: [https://x]\n    cache: {enabled: true, size_bytes: 1024}\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatal(err)
	}

	sum, _, err := serviceConfig(path, "", "", "")
	if err == nil {
		t.Fatal("expected a config error")
	}
	if strings.Contains(err.Error(), "SECRETMODE") {
		t.Fatalf("error leaked a config value: %v", err)
	}
	if sum.ConfigValid || sum.ConfigError == "" {
		t.Fatalf("summary = %+v", sum)
	}

	// A resolve error is replaced by a static string too.
	good := "schema_version: 1\nmode: off\nprofile: default\nprofiles:\n  default:\n    upstreams: [https://x]\n    cache: {enabled: true, size_bytes: 1024}\n"
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}

	sum, _, err = serviceConfig(path, "ghost", "", "")
	if err == nil || strings.Contains(err.Error(), "ghost") {
		t.Fatalf("resolve error = %v, want a static error", err)
	}
	if sum.ConfigValid || sum.ConfigError != "config could not be resolved" {
		t.Fatalf("summary = %+v", sum)
	}
}
