package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/daika7ana/sigil-dns/internal/config"
	"github.com/daika7ana/sigil-dns/internal/ipc"
	"github.com/daika7ana/sigil-dns/internal/lifecycle"
	"github.com/daika7ana/sigil-dns/internal/logging"
	"github.com/daika7ana/sigil-dns/internal/state"
)

// TestServiceLifecycleEnableWithoutConfig verifies crash recovery runs from
// state alone, while Enable reports a clear error until the config loads (M4).
func TestServiceLifecycleEnableWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	inner := lifecycle.New(lifecycle.Config{StatePath: filepath.Join(dir, "state.json")})

	want := errors.New("configuration unavailable")
	lc := &serviceLifecycle{svcDaemon: inner, enableErr: want}

	// Recovery works without config: no state means nothing to restore.
	if err := lc.Startup(context.Background()); err != nil {
		t.Fatalf("Startup: %v", err)
	}

	if _, err := lc.Status(); !errors.Is(err, state.ErrNoState) {
		t.Fatalf("Status err = %v, want ErrNoState", err)
	}

	if err := lc.Enable(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Enable err = %v, want %v", err, want)
	}

	// A failed enable must release the lock Startup retained, or the SCM's
	// restart attempt collides with the dying process.
	lock, err := state.AcquireLock(filepath.Join(dir, "state.json") + ".lock")
	if err != nil {
		t.Fatalf("lock still held after a config-error enable: %v", err)
	}
	_ = lock.Release()
}

// fakeDaemon is an in-memory svcDaemon for reload tests.
type fakeDaemon struct {
	st           *state.State
	enableErr    error
	disableErr   error
	enableCalls  int
	disableCalls int
	disableHook  func()
}

func (f *fakeDaemon) Status() (st *state.State, err error) { return f.st, nil }

func (f *fakeDaemon) Startup(context.Context) (err error) { return nil }

func (f *fakeDaemon) Enable(context.Context) (err error) {
	f.enableCalls++

	return f.enableErr
}

func (f *fakeDaemon) Disable(context.Context) (err error) {
	f.disableCalls++
	if f.disableHook != nil {
		f.disableHook()
	}

	return f.disableErr
}

func (f *fakeDaemon) Restore(context.Context) (err error) { return nil }

func (f *fakeDaemon) RefreshLists(context.Context) (err error) { return nil }
func (f *fakeDaemon) ClearCache(context.Context) (err error)   { return nil }

func TestReloadSwapWhenIdle(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOff}}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOff}}
	want := ipc.ConfigSummary{Profile: "default", Provider: "Quad9", ConfigValid: true}

	lc := &serviceLifecycle{
		svcDaemon: old,
		build:     func() (svcDaemon, ipc.ConfigSummary, error) { return next, want, nil },
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
	if lc.svcDaemon != svcDaemon(next) {
		t.Fatal("inner was not swapped")
	}
}

func TestReloadRestoresAndReenables(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}

	lc := &serviceLifecycle{
		svcDaemon: old,
		build:     func() (svcDaemon, ipc.ConfigSummary, error) { return next, ipc.ConfigSummary{ConfigValid: true}, nil },
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
	if lc.svcDaemon != svcDaemon(next) {
		t.Fatal("inner was not swapped after reload")
	}
}

func TestReloadRollsBackOnEnableFailure(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}, enableErr: errors.New("new config bad")}

	lc := &serviceLifecycle{
		svcDaemon: old,
		build:     func() (svcDaemon, ipc.ConfigSummary, error) { return next, ipc.ConfigSummary{}, nil },
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
	if lc.svcDaemon != svcDaemon(old) {
		t.Fatal("inner should remain the last-known-good lifecycle")
	}
}

// TestReloadRollbackReseeds verifies N2: when the new config fails to enable,
// the old profile's redaction seeds are restored, not left on the new profile.
func TestReloadRollbackReseeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	l, closer, err := logging.Open(logging.Options{Level: "error", Path: path, Secrets: []string{"OLDID12"}})
	if err != nil {
		t.Fatalf("logging.Open: %v", err)
	}
	defer func() { _ = closer.Close() }()

	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}, enableErr: errors.New("new config bad")}

	lc := &serviceLifecycle{
		svcDaemon: old,
		logger:    l,
		build: func() (svcDaemon, ipc.ConfigSummary, error) {
			logging.UpdateSecrets(l, []string{"NEWID99"})
			return next, ipc.ConfigSummary{}, nil
		},
	}

	if _, err = lc.Reload(context.Background()); err == nil {
		t.Fatal("Reload should fail when the new config cannot be enabled")
	}

	seeds := strings.Join(logging.Secrets(l), ",")
	if !strings.Contains(seeds, "OLDID12") {
		t.Fatalf("old seeds not restored after rollback: %v", seeds)
	}
	if strings.Contains(seeds, "NEWID99") {
		t.Fatalf("new seeds left after rollback: %v", seeds)
	}
}

// TestReloadUnionSeedsDuringSwap verifies both the old and the new profile's
// ids are seeded while the old lifecycle is still serving, and only the new one
// remains after a successful swap.
func TestReloadUnionSeedsDuringSwap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	l, closer, err := logging.Open(logging.Options{Level: "error", Path: path, Secrets: []string{"OLDID12"}})
	if err != nil {
		t.Fatalf("logging.Open: %v", err)
	}
	defer func() { _ = closer.Close() }()

	var during []string

	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}, disableHook: func() {
		during = logging.Secrets(l)
	}}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}

	lc := &serviceLifecycle{
		svcDaemon: old,
		logger:    l,
		build: func() (svcDaemon, ipc.ConfigSummary, error) {
			logging.UpdateSecrets(l, []string{"NEWID99"})
			return next, ipc.ConfigSummary{}, nil
		},
	}

	if _, err = lc.Reload(context.Background()); err != nil {
		t.Fatalf("Reload: %v", err)
	}

	seedDuring := strings.Join(during, ",")
	if !strings.Contains(seedDuring, "OLDID12") || !strings.Contains(seedDuring, "NEWID99") {
		t.Fatalf("swap-window seeds = %v, want both OLDID12 and NEWID99", during)
	}

	seeds := strings.Join(logging.Secrets(l), ",")
	if strings.Contains(seeds, "OLDID12") {
		t.Fatalf("old seed left after a successful swap: %v", seeds)
	}
	if !strings.Contains(seeds, "NEWID99") {
		t.Fatalf("new seed missing after a successful swap: %v", seeds)
	}
}

func TestReloadRollbackFailure(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}, enableErr: errors.New("rollback bad")}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}, enableErr: errors.New("new bad")}
	stopped := false

	lc := &serviceLifecycle{
		svcDaemon: old,
		stop:      func() { stopped = true },
		build:     func() (svcDaemon, ipc.ConfigSummary, error) { return next, ipc.ConfigSummary{}, nil },
	}

	if _, err := lc.Reload(context.Background()); err == nil {
		t.Fatal("Reload should fail")
	}
	if lc.svcDaemon != svcDaemon(old) {
		t.Fatal("inner should remain the old lifecycle")
	}
	if !stopped {
		t.Fatal("a failed rollback must stop the service")
	}
}

func TestReloadDisableFailureStops(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}, disableErr: errors.New("restore failed")}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}
	stopped := false

	lc := &serviceLifecycle{
		svcDaemon: old,
		stop:      func() { stopped = true },
		build:     func() (svcDaemon, ipc.ConfigSummary, error) { return next, ipc.ConfigSummary{}, nil },
	}

	if _, err := lc.Reload(context.Background()); err == nil {
		t.Fatal("Reload should fail when the old lifecycle cannot disable")
	}
	if !stopped {
		t.Fatal("a failed disable must stop the service")
	}
}

func TestReloadRestoreFailureKeepsRunning(t *testing.T) {
	old := &fakeDaemon{
		st:         &state.State{Mode: state.ModeOn},
		disableErr: fmt.Errorf("%w: nrpt failed", lifecycle.ErrRestore),
	}
	next := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}
	stopped := false

	lc := &serviceLifecycle{
		svcDaemon: old,
		stop:      func() { stopped = true },
		build:     func() (svcDaemon, ipc.ConfigSummary, error) { return next, ipc.ConfigSummary{}, nil },
	}

	if _, err := lc.Reload(context.Background()); err == nil {
		t.Fatal("Reload should fail")
	}
	if stopped {
		t.Fatal("a failed restore must not stop the still-running resolver")
	}
}

func TestReloadBuildErrorKeepsCurrent(t *testing.T) {
	old := &fakeDaemon{st: &state.State{Mode: state.ModeOn}}
	buildErr := errors.New("config corrupt")

	lc := &serviceLifecycle{
		svcDaemon: old,
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
	if lc.svcDaemon != svcDaemon(old) {
		t.Fatal("inner should be unchanged")
	}
}

// TestResolverSecrets covers H2/M2: seeds survive absent metadata, mixed or
// ambiguous upstreams, and per-domain-only ids.
func TestResolverSecrets(t *testing.T) {
	tests := []struct {
		name string
		in   config.Resolved
		want []string
	}{
		{
			name: "metadata absent DoH path id",
			in: config.Resolved{
				Upstreams: []string{"https://dns.nextdns.io/abc123"},
			},
			want: []string{"abc123"},
		},
		{
			name: "metadata absent DoT host label",
			in: config.Resolved{
				Upstreams: []string{"tls://abc123.dns.nextdns.io:853"},
			},
			want: []string{"abc123"},
		},
		{
			name: "ambiguous upstreams both seeded",
			in: config.Resolved{
				Provider:  "nextdns",
				Protocol:  "doh",
				Upstreams: []string{"https://dns.nextdns.io/idA123", "https://dns.nextdns.io/idB456"},
			},
			want: []string{"idA123", "idB456"},
		},
		{
			name: "short id not seeded",
			in: config.Resolved{
				Provider:  "nextdns",
				Protocol:  "doh",
				Upstreams: []string{"https://dns.nextdns.io/a"},
			},
			want: nil,
		},
		{
			name: "mixed extractable and miss",
			in: config.Resolved{
				Provider:  "controld",
				Protocol:  "doh",
				Upstreams: []string{"https://dns.controld.com/xyz789", "https://freedns.controld.com/p0"},
			},
			want: []string{"xyz789"},
		},
		{
			name: "per-domain-only id",
			in: config.Resolved{
				Provider:  "nextdns",
				Protocol:  "doh",
				Upstreams: []string{"https://dns.quad9.net/dns-query"},
				PerDomain: []config.PerDomain{{Domains: []string{"corp.example"}, Upstream: "https://dns.nextdns.io/corp99"}},
			},
			want: []string{"corp99"},
		},
		{
			name: "no id yields no seeds",
			in: config.Resolved{
				Upstreams: []string{"https://dns.quad9.net/dns-query"},
			},
			want: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolverSecrets(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("resolverSecrets = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("resolverSecrets = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestPlanPerDomain splits bypass rules from routing rules and drops disabled
// rules.
func TestPlanPerDomain(t *testing.T) {
	off := false

	pd := []config.PerDomain{
		{Domains: []string{"a.example"}, Upstream: "tls://a:853"},
		{Domains: []string{"local.example"}, Bypass: true},
		{Domains: []string{"skip.example"}, Upstream: "tls://s:853", Enabled: &off},
		{Domains: []string{"*.corp.example"}, Bypass: true},
	}

	rules, bypass := planPerDomain(pd)

	if len(rules) != 1 || rules[0].Upstream != "tls://a:853" || rules[0].Domains[0] != "a.example" {
		t.Fatalf("rules = %+v", rules)
	}
	if len(bypass) != 2 || bypass[0] != "local.example" || bypass[1] != "*.corp.example" {
		t.Fatalf("bypass = %v", bypass)
	}
}

// TestServiceConfigSanitizesErrors asserts a raw YAML/validation error that
// quotes a config value (possibly an id) never escapes serviceConfig.
func TestServiceConfigSanitizesErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// A validation error quotes the offending value verbatim.
	bad := "schema_version: 1\nprofile: SECRETMODE\nprofiles:\n  default:\n    upstreams: [https://x]\n    cache: {enabled: true, size_bytes: 1024}\n"
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
	good := "schema_version: 1\nprofile: default\nprofiles:\n  default:\n    upstreams: [https://x]\n    cache: {enabled: true, size_bytes: 1024}\n"
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
