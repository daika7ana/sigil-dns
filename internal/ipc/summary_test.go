package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/version"
)

// plantedConfig contains a resolver id (as a DoH path), a DoT host-label id, and
// a list URL with a secret path.  None may appear in a summary.
const plantedConfig = `schema_version: 1
mode: on
profile: default
profiles:
  default:
    provider: NextDNS
    variant: ultralow
    protocol: doh
    upstreams:
      - https://dns.nextdns.io/SECRET123
    bootstrap: [9.9.9.9]
    cache: { enabled: true, size_bytes: 67108864 }
    per_domain:
      - domains: ["corp.example"]
        upstream: "tls://SECRETID.dns.controld.com:853"
    filtering:
      enabled: true
      block_response: 0.0.0.0
      block_ttl: 3600
      lists:
        - url: https://lists.example.com/path/to/SECRETLIST.txt
          enabled: true
`

func writeConfigFile(t *testing.T, body string) (path string) {
	t.Helper()

	path = filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestInfo(t *testing.T) {
	srv := NewServer(&fakeLifecycle{}, nil)

	resp := srv.Handle(context.Background(), Caller{Admin: false},
		Request{Version: ProtocolVersion, Method: MethodInfo})
	if !resp.OK {
		t.Fatalf("info: %+v", resp)
	}

	var info Info
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		t.Fatalf("unmarshal info: %v", err)
	}
	if info.ProtocolVersion != ProtocolVersion {
		t.Errorf("ProtocolVersion = %d, want %d", info.ProtocolVersion, ProtocolVersion)
	}
	if info.AppVersion != version.Version {
		t.Errorf("AppVersion = %q, want %q", info.AppVersion, version.Version)
	}
	if info.Capabilities&CapConfigSummary == 0 || info.Capabilities&CapReload == 0 {
		t.Errorf("Capabilities = %b, want info/config_summary/reload", info.Capabilities)
	}
}

// TestConfigSummaryNeverLeaksSecrets is the core sanitization proof.
func TestConfigSummaryNeverLeaksSecrets(t *testing.T) {
	path := writeConfigFile(t, plantedConfig)

	srv := NewServer(&fakeLifecycle{}, nil).SetConfigPath(path)
	resp := srv.Handle(context.Background(), Caller{Admin: false},
		Request{Version: ProtocolVersion, Method: MethodConfigSummary})
	if !resp.OK {
		t.Fatalf("config_summary: %+v", resp)
	}

	// Assert on the raw bytes of both the payload and the whole envelope.
	raw := string(resp.Data)
	envelope, _ := json.Marshal(resp)

	for _, secret := range []string{"SECRET123", "SECRETID", "SECRETLIST"} {
		if strings.Contains(raw, secret) || strings.Contains(string(envelope), secret) {
			t.Fatalf("summary leaked %q: %s", secret, raw)
		}
	}
	for _, forbidden := range []string{
		"dns.nextdns.io/",
		"lists.example.com/path",
		"/path/to/",
		"dns.controld.com",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("summary leaked URL part %q: %s", forbidden, raw)
		}
	}

	var sum ConfigSummary
	if err := json.Unmarshal(resp.Data, &sum); err != nil {
		t.Fatalf("unmarshal summary: %v", err)
	}
	if !sum.ConfigValid {
		t.Fatalf("ConfigValid = false: %+v", sum)
	}
	if sum.Profile != "default" || sum.Provider != "NextDNS" || sum.Variant != "ultralow" || sum.Protocol != "doh" {
		t.Errorf("labels/profile wrong: %+v", sum)
	}
	if !sum.IDSet {
		t.Error("id_set should be true for a DoH path id")
	}
	if sum.PerDomainCount != 1 || sum.ListCount != 1 {
		t.Errorf("counts = %d/%d, want 1/1", sum.PerDomainCount, sum.ListCount)
	}
	if len(sum.Lists) != 1 || sum.Lists[0].Host != "https://lists.example.com" || !sum.Lists[0].Enabled {
		t.Errorf("lists = %+v, want host https://lists.example.com enabled", sum.Lists)
	}
	if len(sum.Bootstrap) != 1 || sum.Bootstrap[0] != "9.9.9.9" {
		t.Errorf("bootstrap = %v", sum.Bootstrap)
	}
	if !sum.FilteringEnabled || sum.BlockResponse != "0.0.0.0" || sum.BlockTTL != 3600 {
		t.Errorf("filtering settings wrong: %+v", sum)
	}
	if !sum.CacheEnabled || sum.CacheSizeBytes != 67108864 {
		t.Errorf("cache settings wrong: %+v", sum)
	}
}

func TestConfigSummaryBrokenConfig(t *testing.T) {
	path := writeConfigFile(t, "mode: [not,a,scalar]\n")

	srv := NewServer(&fakeLifecycle{}, nil).SetConfigPath(path)
	resp := srv.Handle(context.Background(), Caller{Admin: false},
		Request{Version: ProtocolVersion, Method: MethodConfigSummary})
	if !resp.OK {
		t.Fatalf("config_summary on broken config: %+v", resp)
	}

	var sum ConfigSummary
	if err := json.Unmarshal(resp.Data, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.ConfigValid {
		t.Error("ConfigValid should be false for a broken config")
	}
	if sum.ConfigError == "" || sum.Revision == "" {
		t.Fatalf("summary = %+v", sum)
	}
}

// TestPrivilegeTable asserts the classification covers every method and that
// every admin method is rejected for a non-admin caller.
func TestPrivilegeTable(t *testing.T) {
	want := map[Method]Privilege{
		MethodStatus:        ReadOnly,
		MethodInfo:          ReadOnly,
		MethodConfigSummary: ReadOnly,
		MethodEnable:        Admin,
		MethodDisable:       Admin,
		MethodReload:        Admin,
	}
	if len(methodPrivileges) != len(want) {
		t.Fatalf("privilege table has %d entries, want %d", len(methodPrivileges), len(want))
	}
	for m, p := range want {
		got, ok := methodPrivileges[m]
		if !ok {
			t.Fatalf("method %q missing from privilege table", m)
		}
		if got != p {
			t.Fatalf("method %q privilege = %v, want %v", m, got, p)
		}
	}

	reloader := &fakeReloader{sum: ConfigSummary{Profile: "default"}}
	srv := NewServer(&fakeLifecycle{}, nil).SetReloader(reloader)
	ctx := context.Background()

	for m, p := range methodPrivileges {
		resp := srv.Handle(ctx, Caller{Admin: false}, Request{Version: ProtocolVersion, Method: m})
		if p == Admin {
			if resp.OK || resp.Error != ErrForbidden.Error() {
				t.Errorf("method %q non-admin = %+v, want ErrForbidden", m, resp)
			}
		} else if !resp.OK {
			t.Errorf("read-only method %q non-admin = %+v, want ok", m, resp)
		}
	}

	if reloadMethod := Method("bogus"); reloadMethod.valid() {
		t.Fatal("bogus method reported valid")
	}
}

func TestStatusPipeRejectsAdminMethods(t *testing.T) {
	// The status pipe serves with a read-only authorizer; admin methods must be
	// rejected even though the server could handle them.
	srv := NewServer(&fakeLifecycle{}, nil).SetReloader(&fakeReloader{}).SanitizeStatus()
	ctx := context.Background()

	for _, m := range []Method{MethodEnable, MethodDisable, MethodReload} {
		resp := srv.Handle(ctx, ReadOnlyAuthorizer(nil), Request{Version: ProtocolVersion, Method: m})
		if resp.OK || resp.Error != ErrForbidden.Error() {
			t.Errorf("%s on status pipe = %+v, want ErrForbidden", m, resp)
		}
	}
}

type fakeReloader struct {
	sum   ConfigSummary
	err   error
	calls int
}

func (f *fakeReloader) Reload(context.Context) (ConfigSummary, error) {
	f.calls++

	return f.sum, f.err
}

func TestReloadMethod(t *testing.T) {
	reloader := &fakeReloader{sum: ConfigSummary{Profile: "default", Provider: "Quad9"}}
	srv := NewServer(&fakeLifecycle{}, nil).SetReloader(reloader)
	ctx := context.Background()

	// Admin succeeds and returns the summary.
	resp := srv.Handle(ctx, Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodReload})
	if !resp.OK {
		t.Fatalf("reload: %+v", resp)
	}
	var sum ConfigSummary
	if err := json.Unmarshal(resp.Data, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Provider != "Quad9" || reloader.calls != 1 {
		t.Fatalf("reload result = %+v, calls = %d", sum, reloader.calls)
	}

	// Reloader error propagates.
	reloader.err = errors.New("boom")
	resp = srv.Handle(ctx, Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodReload})
	if resp.OK || !strings.Contains(resp.Error, "boom") {
		t.Fatalf("reload error = %+v", resp)
	}

	// No reloader configured -> unsupported.
	bare := NewServer(&fakeLifecycle{}, nil)
	resp = bare.Handle(ctx, Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodReload})
	if resp.OK || resp.Error != ErrUnsupported.Error() {
		t.Fatalf("reload without reloader = %+v, want ErrUnsupported", resp)
	}
}

func TestRevisionChanges(t *testing.T) {
	path := writeConfigFile(t, plantedConfig)
	first := Revision(path)
	if first == "" {
		t.Fatal("Revision returned empty for an existing file")
	}

	// Change the content (and size) and the token must change.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte(plantedConfig+"# trailing comment\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if second := Revision(path); second == first {
		t.Fatalf("Revision did not change after edit: %q", second)
	}

	if got := Revision(filepath.Join(t.TempDir(), "missing.yaml")); got != "" {
		t.Fatalf("Revision(missing) = %q, want empty", got)
	}
}

func TestSanitizedConfigError(t *testing.T) {
	if got := SanitizedConfigError(config.ErrNoConfig); got != "config file not found" {
		t.Errorf("ErrNoConfig = %q", got)
	}
	if got := SanitizedConfigError(errors.New("yaml: line 3: https://dns.nextdns.io/SECRET123")); strings.Contains(got, "SECRET123") {
		t.Errorf("SanitizedConfigError leaked a secret: %q", got)
	}
}
