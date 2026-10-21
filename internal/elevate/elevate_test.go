package elevate

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/providers"
	"github.com/daika7ana/dns-client/internal/state"

	"gopkg.in/yaml.v3"
)

func TestValidate(t *testing.T) {
	on := config.ModeOn
	bad := "bogus"

	cases := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{"enable", Request{Action: ActionEnable, UUID: "u"}, false},
		{"restore", Request{Action: ActionRestore, UUID: "u"}, false},
		{"apply mode", Request{Action: ActionApply, UUID: "u", Patch: Patch{Mode: &on}}, false},
		{"unknown action", Request{Action: "frobnicate", UUID: "u"}, true},
		{"missing uuid", Request{Action: ActionEnable}, true},
		{"bad mode", Request{Action: ActionApply, UUID: "u", Patch: Patch{Mode: &bad}}, true},
	}

	for _, tc := range cases {
		if err := tc.req.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("%s: Validate() err = %v, wantErr = %v", tc.name, err, tc.wantErr)
		}
	}
}

func TestCheckRevision(t *testing.T) {
	if err := checkRevision("", "123-4"); err != nil {
		t.Fatalf("empty expected should not fail: %v", err)
	}
	if err := checkRevision("123-4", "123-4"); err != nil {
		t.Fatalf("matching revision: %v", err)
	}
	if err := checkRevision("123-4", "999-9"); !errors.Is(err, ErrRevision) {
		t.Fatalf("mismatched revision = %v, want ErrRevision", err)
	}
}

// ptr is a tiny helper for building allowlisted pointer patches.
func ptr[T any](v T) *T { return &v }

// TestValidatePatchShape covers the field-level constraints on Request.Validate.
func TestValidatePatchShape(t *testing.T) {
	ttlZero := uint32(0)
	empty := ""
	sizeZero := 0
	sizeOK := 4096

	cases := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{
			name:    "service action takes no patch",
			req:     Request{Action: ActionServiceStart, UUID: "u", Patch: Patch{Mode: ptr("on")}},
			wantErr: true,
		},
		{name: "service action empty patch", req: Request{Action: ActionServiceStop, UUID: "u"}, wantErr: false},
		{
			name: "block ttl zero rejected",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {Filtering: &FilteringPatch{BlockTTL: &ttlZero}},
			}}},
			wantErr: true,
		},
		{
			name: "block response empty rejected",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {Filtering: &FilteringPatch{BlockResponse: &empty}},
			}}},
			wantErr: true,
		},
		{
			name: "cache size zero while enabled rejected",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {Cache: &CachePatch{SizeBytes: &sizeZero}},
			}}},
			wantErr: true,
		},
		{
			name: "cache size zero while explicitly disabled ok",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {Cache: &CachePatch{Enabled: ptr(false), SizeBytes: &sizeZero}},
			}}},
			wantErr: false,
		},
		{
			name: "non-ip bootstrap rejected",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {Bootstrap: &[]string{"example.com"}},
			}}},
			wantErr: true,
		},
		{
			name: "ip bootstrap ok",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {Bootstrap: &[]string{"9.9.9.9", "1.1.1.1:53"}},
			}}},
			wantErr: false,
		},
		{
			name: "more than one structural op rejected",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{
				AddProfile:    ptr("work"),
				DeleteProfile: ptr("other"),
			}},
			wantErr: true,
		},
		{
			name: "invalid id charset rejected",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {ID: ptr("bad/id")},
			}}},
			wantErr: true,
		},
		{
			name: "over-long id rejected",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {ID: ptr(strings.Repeat("a", 65))},
			}}},
			wantErr: true,
		},
		{
			name: "url without scheme rejected",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {URL: ptr("dns.example")},
			}}},
			wantErr: true,
		},
		{
			name: "good url ok",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {URL: ptr("tls://1.1.1.1:853"), Bootstrap: &[]string{"1.1.1.1"}},
			}}},
			wantErr: false,
		},
		{
			name: "valid cache size ok",
			req: Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				"default": {Cache: &CachePatch{SizeBytes: &sizeOK}},
			}}},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.req.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestApplyRequestGuards covers the pure apply path: unknown profiles, ignored
// unknown fields, revision aborts, no-op detection, and secret containment.
func TestApplyRequestGuards(t *testing.T) {
	t.Run("unknown profiles key rejected", func(t *testing.T) {
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			"ghost": {Cache: &CachePatch{Enabled: ptr(false)}},
		}}}
		if err := applyRequest(config.Default(), req, "rev-1"); err == nil {
			t.Fatal("unknown profile patch accepted")
		}
	})

	t.Run("unknown request fields ignored", func(t *testing.T) {
		var req Request
		raw := `{"action":"apply","uuid":"u","profile":"evil","patch":{"mode":"on","upstream":"https://evil.example/SECRET"}}`
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		cfg := config.Default()
		if err := applyRequest(cfg, req, "rev-1"); err != nil {
			t.Fatalf("applyRequest: %v", err)
		}
		if cfg.Mode != config.ModeOn {
			t.Fatalf("mode = %q, want on", cfg.Mode)
		}
		if cfg.Profile != config.DefaultProfileName {
			t.Fatal("unknown field changed the active profile")
		}
	})

	t.Run("revision mismatch aborts before mutation", func(t *testing.T) {
		cfg := config.Default()
		req := Request{
			Action:           ActionApply,
			UUID:             "u",
			ExpectedRevision: "rev-1",
			Patch:            Patch{Mode: ptr(config.ModeOn)},
		}
		if err := applyRequest(cfg, req, "rev-2"); !errors.Is(err, ErrRevision) {
			t.Fatalf("applyRequest = %v, want ErrRevision", err)
		}
		if cfg.Mode != config.ModeOff {
			t.Fatal("config mutated despite a revision mismatch")
		}
	})

	t.Run("per-field no-op yields unchanged config", func(t *testing.T) {
		cfg := config.Default()
		before, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}

		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			config.DefaultProfileName: {Cache: &CachePatch{Enabled: ptr(true)}},
		}}}
		if err = applyRequest(cfg, req, "rev"); err != nil {
			t.Fatalf("applyRequest: %v", err)
		}

		after, err := yaml.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("a no-op patch changed the serialized config")
		}
	})

	t.Run("planted id never appears in the result", func(t *testing.T) {
		const planted = "PLANTEDSECRET123"

		// A planted id with an invalid character trips the static charset error.
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			config.DefaultProfileName: {Provider: ptr(providers.NextDNS), ID: ptr(planted + "!")},
		}}}
		err := applyRequest(config.Default(), req, "rev")
		if err == nil {
			t.Fatal("invalid id accepted")
		}

		// A provider switch that cannot preserve the existing id also fails
		// statically, never echoing the upstream.
		cfg := config.Default()
		p := cfg.Profiles[config.DefaultProfileName]
		p.Upstreams = []string{"https://dns.quad9.net/dns-query"}
		p.Provider = providers.NextDNS
		cfg.Profiles[config.DefaultProfileName] = p

		req2 := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			config.DefaultProfileName: {Provider: ptr(providers.NextDNS)},
		}}}
		err2 := applyRequest(cfg, req2, "rev")
		if err2 == nil {
			t.Fatal("id preservation should have failed")
		}

		for _, res := range []Result{{Err: err.Error()}, {Err: err2.Error()}} {
			body, _ := json.Marshal(res)
			for _, secret := range []string{planted, "dns.quad9.net", "dns.nextdns.io"} {
				if strings.Contains(string(body), secret) {
					t.Fatalf("Result leaked %q: %s", secret, body)
				}
			}
		}
	})
}

// TestApplyRequestCustomReplaceOnly covers the replace-only custom-provider
// path: a blank URL keeps the stored (secret) upstream, a missing upstream is a
// static error, and a typed URL still replaces it.
func TestApplyRequestCustomReplaceOnly(t *testing.T) {
	const secret = "https://secret.example/dns-query/PLANTEDID"
	def := config.DefaultProfileName

	// assertNoSecret fails if err somehow echoed the URL or its id.
	assertNoSecret := func(t *testing.T, err error, res Result) {
		t.Helper()

		body, _ := json.Marshal(res)
		for _, s := range []string{secret, "PLANTEDID", "secret.example"} {
			if err != nil && strings.Contains(err.Error(), s) {
				t.Fatalf("error leaked %q: %v", s, err)
			}
			if strings.Contains(string(body), s) {
				t.Fatalf("Result leaked %q: %s", s, body)
			}
		}
	}

	t.Run("blank url keeps upstreams", func(t *testing.T) {
		cfg := config.Default()
		p := cfg.Profiles[def]
		p.Provider = providers.Custom
		p.Variant = providers.Custom
		p.Protocol = string(providers.DoT)
		p.Upstreams = []string{secret}
		p.Bootstrap = []string{"1.1.1.1"}
		cfg.Profiles[def] = p

		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom)},
		}}}

		if err := applyRequest(cfg, req, "rev"); err != nil {
			assertNoSecret(t, err, Result{Err: err.Error()})
			t.Fatalf("applyRequest: %v", err)
		}

		got := cfg.Profiles[def]
		if !slices.Equal(got.Upstreams, []string{secret}) {
			t.Fatalf("upstreams not preserved: %v", got.Upstreams)
		}
		if got.Provider != providers.Custom || got.Variant != providers.Custom {
			t.Fatalf("metadata = %q/%q, want custom/custom", got.Provider, got.Variant)
		}
		if got.Protocol != string(providers.DoT) {
			t.Fatalf("protocol = %q, want the kept upstream's dot", got.Protocol)
		}
		if !slices.Equal(got.Bootstrap, []string{"1.1.1.1"}) {
			t.Fatalf("bootstrap not preserved: %v", got.Bootstrap)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("resulting config invalid: %v", err)
		}
	})

	t.Run("blank url with explicit bootstrap overrides", func(t *testing.T) {
		cfg := config.Default()
		p := cfg.Profiles[def]
		p.Upstreams = []string{secret}
		p.Bootstrap = []string{"1.1.1.1"}
		cfg.Profiles[def] = p

		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom), Bootstrap: &[]string{"9.9.9.9"}},
		}}}

		if err := applyRequest(cfg, req, "rev"); err != nil {
			t.Fatalf("applyRequest: %v", err)
		}
		if got := cfg.Profiles[def]; !slices.Equal(got.Bootstrap, []string{"9.9.9.9"}) {
			t.Fatalf("bootstrap = %v, want explicit override", got.Bootstrap)
		}
	})

	t.Run("blank url with no upstreams is a static error", func(t *testing.T) {
		cfg := config.Default()
		p := cfg.Profiles[def]
		p.Provider = providers.Custom
		p.Upstreams = nil
		cfg.Profiles[def] = p

		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom)},
		}}}

		err := applyRequest(cfg, req, "rev")
		if err == nil {
			t.Fatal("blank url with no upstreams accepted")
		}
		assertNoSecret(t, err, Result{Err: err.Error()})
	})

	t.Run("typed url replaces upstreams", func(t *testing.T) {
		cfg := config.Default()
		p := cfg.Profiles[def]
		p.Provider = providers.Custom
		p.Upstreams = []string{secret}
		cfg.Profiles[def] = p

		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom), URL: ptr("tls://new.example:853"), Bootstrap: &[]string{"1.1.1.1"}},
		}}}

		if err := applyRequest(cfg, req, "rev"); err != nil {
			t.Fatalf("applyRequest: %v", err)
		}

		got := cfg.Profiles[def]
		if !slices.Equal(got.Upstreams, []string{"tls://new.example:853"}) {
			t.Fatalf("upstreams = %v, want the typed url", got.Upstreams)
		}
		for _, u := range got.Upstreams {
			if strings.Contains(u, "SECRET") || strings.Contains(u, "PLANTEDID") {
				t.Fatalf("old secret survived replace: %v", got.Upstreams)
			}
		}
	})
}

// TestApplyRequestProtocolChangePreservesID guards Defect 1: the id must be
// recovered from the existing upstreams using the existing protocol, then
// re-expanded with the requested protocol.
func TestApplyRequestProtocolChangePreservesID(t *testing.T) {
	def := config.DefaultProfileName

	cases := []struct {
		name     string
		provider string
		variant  string
		oldProto providers.Protocol
		newProto providers.Protocol
		oldURL   string
		id       string
		wantURL  string
	}{
		{
			"nextdns_doh_to_dot", providers.NextDNS, "ultralow", providers.DoH, providers.DoT,
			"https://dns.nextdns.io/abcd1234", "abcd1234", "tls://abcd1234.dns.nextdns.io:853",
		},
		{
			"nextdns_doh_to_doq", providers.NextDNS, "ultralow", providers.DoH, providers.DoQ,
			"https://dns.nextdns.io/abcd1234", "abcd1234", "quic://abcd1234.dns.nextdns.io:853",
		},
		{
			"controld_doh_to_dot", providers.ControlD, "custom", providers.DoH, providers.DoT,
			"https://dns.controld.com/xyz789", "xyz789", "tls://xyz789.dns.controld.com:853",
		},
		{
			"controld_doh_to_doq", providers.ControlD, "custom", providers.DoH, providers.DoQ,
			"https://dns.controld.com/xyz789", "xyz789", "quic://xyz789.dns.controld.com:853",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Default()
			p := cfg.Profiles[def]
			p.Provider = tc.provider
			p.Variant = tc.variant
			p.Protocol = string(tc.oldProto)
			p.Upstreams = []string{tc.oldURL}
			cfg.Profiles[def] = p

			req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
				def: {Provider: ptr(tc.provider), Protocol: ptr(string(tc.newProto))},
			}}}

			if err := applyRequest(cfg, req, "rev"); err != nil {
				t.Fatalf("protocol change failed: %v", err)
			}

			got := cfg.Profiles[def]
			if !slices.Equal(got.Upstreams, []string{tc.wantURL}) {
				t.Fatalf("upstreams = %v, want %v", got.Upstreams, tc.wantURL)
			}
			if !strings.Contains(strings.Join(got.Upstreams, " "), tc.id) {
				t.Fatalf("id lost: upstreams = %v", got.Upstreams)
			}
			if got.Protocol != string(tc.newProto) {
				t.Fatalf("protocol = %q, want %q", got.Protocol, tc.newProto)
			}
			if got.Provider != tc.provider || got.Variant != tc.variant {
				t.Fatalf("metadata = %q/%q, want %q/%q", got.Provider, got.Variant, tc.provider, tc.variant)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("resulting config invalid: %v", err)
			}
		})
	}
}

// TestApplyRequestCustomProtocol guards Defect 2: an explicit protocol for the
// custom provider must agree with the URL scheme, never silently drop.
func TestApplyRequestCustomProtocol(t *testing.T) {
	const secret = "tls://secret.example:853"
	def := config.DefaultProfileName

	newCustomCfg := func(upstream, protocol string) *config.Config {
		cfg := config.Default()
		p := cfg.Profiles[def]
		p.Provider = providers.Custom
		p.Variant = providers.Custom
		p.Protocol = protocol
		p.Upstreams = []string{upstream}
		cfg.Profiles[def] = p

		return cfg
	}

	t.Run("blank url matching protocol ok", func(t *testing.T) {
		cfg := newCustomCfg(secret, "dot")
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom), Protocol: ptr("dot")},
		}}}

		if err := applyRequest(cfg, req, "rev"); err != nil {
			t.Fatalf("applyRequest: %v", err)
		}
		got := cfg.Profiles[def]
		if !slices.Equal(got.Upstreams, []string{secret}) || got.Protocol != "dot" {
			t.Fatalf("upstreams/protocol = %v/%q, want preserved/dot", got.Upstreams, got.Protocol)
		}
	})

	t.Run("blank url mismatching protocol rejected without write", func(t *testing.T) {
		cfg := newCustomCfg(secret, "dot")
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom), Protocol: ptr("doh")},
		}}}

		err := applyRequest(cfg, req, "rev")
		if !errors.Is(err, errCustomProtocolFixed) {
			t.Fatalf("err = %v, want the static protocol-fixed error", err)
		}
		got := cfg.Profiles[def]
		if !slices.Equal(got.Upstreams, []string{secret}) || got.Protocol != "dot" {
			t.Fatalf("profile mutated on error: upstreams=%v protocol=%q", got.Upstreams, got.Protocol)
		}
		for _, s := range []string{secret, "secret.example"} {
			if strings.Contains(err.Error(), s) {
				t.Fatalf("error leaked %q: %v", s, err)
			}
		}
	})

	t.Run("typed url matching protocol ok", func(t *testing.T) {
		cfg := newCustomCfg("https://old.example/dns-query", "doh")
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom), URL: ptr("tls://new.example:853"), Protocol: ptr("dot")},
		}}}

		if err := applyRequest(cfg, req, "rev"); err != nil {
			t.Fatalf("applyRequest: %v", err)
		}
		got := cfg.Profiles[def]
		if !slices.Equal(got.Upstreams, []string{"tls://new.example:853"}) || got.Protocol != "dot" {
			t.Fatalf("upstreams/protocol = %v/%q", got.Upstreams, got.Protocol)
		}
	})

	t.Run("typed url mismatching protocol rejected", func(t *testing.T) {
		cfg := newCustomCfg("https://old.example/dns-query", "doh")
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom), URL: ptr("tls://new.example:853"), Protocol: ptr("doh")},
		}}}

		if err := applyRequest(cfg, req, "rev"); !errors.Is(err, errCustomProtocolFixed) {
			t.Fatalf("err = %v, want the static protocol-fixed error", err)
		}
	})

	t.Run("unrecognized scheme rejected", func(t *testing.T) {
		cfg := newCustomCfg("https://old.example/dns-query", "doh")
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom), URL: ptr("sdns://AQcAAAAA"), Protocol: ptr("doh")},
		}}}

		if err := applyRequest(cfg, req, "rev"); !errors.Is(err, errCustomProtocolFixed) {
			t.Fatalf("err = %v, want the static protocol-fixed error", err)
		}
	})
}

// TestUninstallPlan pins the uninstall policy table.
func TestUninstallPlan(t *testing.T) {
	cases := []struct {
		name                            string
		running, enabled, dirty, active bool
		wantRestore                     bool
	}{
		{"running", true, false, false, false, true},
		{"running_ignores_active", true, false, false, true, true},
		{"active_probe", false, false, false, true, true},
		{"stopped_dirty", false, false, true, false, true},
		{"stopped_enabled", false, true, false, false, true},
		{"stopped_clean", false, false, false, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore, reason := uninstallPlan(tc.running, tc.enabled, tc.dirty, tc.active)
			if restore != tc.wantRestore {
				t.Fatalf("plan restore = %v (%s), want %v", restore, reason, tc.wantRestore)
			}
			if reason == "" {
				t.Fatal("empty reason")
			}
		})
	}
}

// TestUninstallStateFrom covers the fail-open default: a missing/corrupt state
// file must not be treated as "clean", and a live probe must force a restore.
func TestUninstallStateFrom(t *testing.T) {
	load := func(enabled, dirty, ok bool) func() (bool, bool, bool) {
		return func() (bool, bool, bool) { return enabled, dirty, ok }
	}

	t.Run("missing state plus active probe forces restore", func(t *testing.T) {
		enabled, dirty, active := uninstallStateFrom(false, load(false, false, false), func() bool { return true })
		if enabled || dirty || !active {
			t.Fatalf("state = %v/%v/%v, want false/false/true", enabled, dirty, active)
		}
		if restore, _ := uninstallPlan(false, enabled, dirty, active); !restore {
			t.Fatal("missing state + active interception must still restore")
		}
	})

	t.Run("missing state and inactive probe is clean", func(t *testing.T) {
		enabled, dirty, active := uninstallStateFrom(false, load(false, false, false), func() bool { return false })
		if restore, reason := uninstallPlan(false, enabled, dirty, active); restore || reason == "" {
			t.Fatalf("restore=%v reason=%q, want false/non-empty", restore, reason)
		}
	})

	t.Run("running service skips the probe", func(t *testing.T) {
		probed := false
		uninstallStateFrom(true, load(false, false, false), func() bool { probed = true; return true })
		if probed {
			t.Fatal("probe ran for a running service")
		}
	})

	t.Run("recorded enabled restores even when probe is quiet", func(t *testing.T) {
		enabled, dirty, active := uninstallStateFrom(false, load(true, false, true), func() bool { return false })
		if restore, _ := uninstallPlan(false, enabled, dirty, active); !restore {
			t.Fatal("enabled state must restore")
		}
	})
}

// TestUninstallSequenceAbortsBeforeDelete proves delete is reached only after
// restore, stop, and the post-stop verification all succeeded.
func TestUninstallSequenceAbortsBeforeDelete(t *testing.T) {
	noop := func() error { return nil }
	fail := func() error { return errors.New("step failed") }

	t.Run("failed restore does not delete", func(t *testing.T) {
		deleted := false
		err := uninstallSequence(true, fail, noop, noop, func() error { deleted = true; return nil })
		if err == nil || deleted {
			t.Fatalf("err=%v deleted=%v, want error and no delete", err, deleted)
		}
	})

	t.Run("failed stop does not delete", func(t *testing.T) {
		deleted := false
		err := uninstallSequence(true, noop, fail, noop, func() error { deleted = true; return nil })
		if err == nil || deleted {
			t.Fatalf("err=%v deleted=%v, want error and no delete", err, deleted)
		}
	})

	t.Run("failed verify does not delete", func(t *testing.T) {
		deleted := false
		err := uninstallSequence(false, noop, noop, fail, func() error { deleted = true; return nil })
		if err == nil || deleted {
			t.Fatalf("err=%v deleted=%v, want error and no delete", err, deleted)
		}
	})

	t.Run("clean path deletes", func(t *testing.T) {
		deleted := false
		err := uninstallSequence(false, noop, noop, noop, func() error { deleted = true; return nil })
		if err != nil || !deleted {
			t.Fatalf("err=%v deleted=%v, want no error and delete", err, deleted)
		}
	})
}

// TestUninstallSequenceOrdering proves the ordered
// restore -> stop -> verify -> delete flow; the clean case skips restore but
// still verifies (exercises the probe) before delete.
func TestUninstallSequenceOrdering(t *testing.T) {
	rec := func(order *[]string, name string) func() error {
		return func() error { *order = append(*order, name); return nil }
	}

	cases := []struct {
		name    string
		restore bool
		want    []string
	}{
		{"restore_stop_verify_delete", true, []string{"restore", "stop", "verify", "delete"}},
		{"clean_stop_verify_delete", false, []string{"stop", "verify", "delete"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			err := uninstallSequence(tc.restore,
				rec(&order, "restore"), rec(&order, "stop"), rec(&order, "verify"), rec(&order, "delete"))
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if !slices.Equal(order, tc.want) {
				t.Fatalf("order = %v, want %v", order, tc.want)
			}
		})
	}
}

// TestRestoreInterceptionWith proves the control-pipe restore falls back to the
// local restore, and that a lock-held (ErrLocked) local failure aborts.
func TestRestoreInterceptionWith(t *testing.T) {
	ok := func() error { return nil }
	pipeFail := func() error { return errors.New("pipe failed") }

	t.Run("no pid goes straight to local", func(t *testing.T) {
		pipeCalled, localCalled := false, false
		err := restoreInterceptionWith(0,
			func() error { pipeCalled = true; return nil },
			func() error { localCalled = true; return nil })
		if err != nil || pipeCalled || !localCalled {
			t.Fatalf("err=%v pipe=%v local=%v, want nil/false/true", err, pipeCalled, localCalled)
		}
	})

	t.Run("pipe success skips local", func(t *testing.T) {
		localCalled := false
		err := restoreInterceptionWith(42, ok, func() error { localCalled = true; return nil })
		if err != nil || localCalled {
			t.Fatalf("err=%v local=%v, want nil/false", err, localCalled)
		}
	})

	t.Run("pipe failure falls back to local", func(t *testing.T) {
		localCalled := false
		err := restoreInterceptionWith(42, pipeFail, func() error { localCalled = true; return nil })
		if err != nil || !localCalled {
			t.Fatalf("err=%v local=%v, want nil/true", err, localCalled)
		}
	})

	t.Run("locked local restore aborts", func(t *testing.T) {
		err := restoreInterceptionWith(42, pipeFail, func() error { return state.ErrLocked })
		if !errors.Is(err, state.ErrLocked) {
			t.Fatalf("err = %v, want ErrLocked", err)
		}
	})
}

// TestApplyRequestIDSafety covers the id/provider/metadata hardening.
func TestApplyRequestIDSafety(t *testing.T) {
	const secretURL = "https://dns.nextdns.io/ab%2Fcd"
	def := config.DefaultProfileName

	t.Run("id without provider rejected", func(t *testing.T) {
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {ID: ptr("abc")},
		}}}
		if err := applyRequest(config.Default(), req, "rev"); err == nil {
			t.Fatal("id-only patch accepted")
		}
	})

	t.Run("id with provider accepted", func(t *testing.T) {
		cfg := config.Default()
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.NextDNS), ID: ptr("abc123")},
		}}}
		if err := applyRequest(cfg, req, "rev"); err != nil {
			t.Fatalf("applyRequest: %v", err)
		}
		if !slices.Equal(cfg.Profiles[def].Upstreams, []string{"https://dns.nextdns.io/abc123"}) {
			t.Fatalf("upstreams = %v", cfg.Profiles[def].Upstreams)
		}
	})

	t.Run("custom with id rejected", func(t *testing.T) {
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom), ID: ptr("abc"), URL: ptr("https://x.example/dns-query")},
		}}}
		if err := applyRequest(config.Default(), req, "rev"); err == nil {
			t.Fatal("custom+id accepted")
		}
	})

	t.Run("partial variant without provider rejected", func(t *testing.T) {
		cfg := config.Default()
		p := cfg.Profiles[def]
		p.Provider = providers.NextDNS
		cfg.Profiles[def] = p

		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Variant: ptr("anycast")},
		}}}
		if err := applyRequest(cfg, req, "rev"); err == nil {
			t.Fatal("partial variant patch accepted")
		}
	})

	t.Run("partial protocol without provider rejected", func(t *testing.T) {
		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Protocol: ptr("dot")},
		}}}
		if err := applyRequest(config.Default(), req, "rev"); err == nil {
			t.Fatal("partial protocol patch accepted")
		}
	})

	t.Run("custom unknown variant rejected", func(t *testing.T) {
		cfg := config.Default()
		p := cfg.Profiles[def]
		p.Upstreams = []string{"https://keep.example"}
		cfg.Profiles[def] = p

		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.Custom), Variant: ptr("bogus")},
		}}}
		if err := applyRequest(cfg, req, "rev"); err == nil {
			t.Fatal("bogus custom variant accepted")
		}
	})

	t.Run("recovered id with encoded slash rejected statically", func(t *testing.T) {
		cfg := config.Default()
		p := cfg.Profiles[def]
		p.Provider = providers.NextDNS
		p.Protocol = "doh"
		p.Upstreams = []string{secretURL}
		cfg.Profiles[def] = p

		req := Request{Action: ActionApply, UUID: "u", Patch: Patch{Profiles: map[string]ProfilePatch{
			def: {Provider: ptr(providers.NextDNS)},
		}}}
		err := applyRequest(cfg, req, "rev")
		if err == nil {
			t.Fatal("recovered id with an encoded slash accepted")
		}
		for _, s := range []string{secretURL, "ab/cd", "ab%2Fcd", "dns.nextdns.io"} {
			if strings.Contains(err.Error(), s) {
				t.Fatalf("error leaked %q: %v", s, err)
			}
		}
	})
}

// TestApplyShouldReload pins the apply policy: reload only when the service is
// already running AND the config bytes changed.
func TestApplyShouldReload(t *testing.T) {
	cases := []struct {
		name             string
		running, changed bool
		want             bool
	}{
		{"running_changed_reloads", true, true, true},
		{"running_unchanged_skips", true, false, false},
		{"stopped_changed_skips", false, true, false},
		{"stopped_unchanged_skips", false, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := applyShouldReload(tc.running, tc.changed); got != tc.want {
				t.Fatalf("applyShouldReload(%v, %v) = %v, want %v", tc.running, tc.changed, got, tc.want)
			}
		})
	}
}

// TestServiceBinaryMatches pins the anti-hijack registration check.
func TestServiceBinaryMatches(t *testing.T) {
	exe := `C:\Program Files\dns-client\dns-client.exe`
	plain := `C:\dns-client\dns-client.exe`

	cases := []struct {
		name    string
		binPath string
		exe     string
		want    bool
	}{
		{"quoted with arg", `"` + exe + `" --service`, exe, true},
		{"unquoted with arg", plain + ` --service`, plain, true},
		{"wrong binary", `"C:\evil\evil.exe" --service`, exe, false},
		{"missing arg", `"` + exe + `"`, exe, false},
		{"extra arg", `"` + exe + `" --service --evil`, exe, false},
		{"empty", ``, exe, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serviceBinaryMatches(tc.binPath, tc.exe); got != tc.want {
				t.Fatalf("serviceBinaryMatches(%q) = %v, want %v", tc.binPath, got, tc.want)
			}
		})
	}
}

// TestApplyLogPatch covers the logging patch surface (LOGGING_PLAN §4.4).
func TestApplyLogPatch(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "dns-client.log")

	cfg := config.Default()
	req := Request{Action: ActionApply, UUID: "u", Patch: Patch{
		LogLevel:     ptr("debug"),
		LogPath:      ptr(logPath),
		LogMaxSizeMB: ptr(20),
		LogKeepMB:    ptr(5),
	}}
	if err := applyRequest(cfg, req, "rev"); err != nil {
		t.Fatalf("applyRequest(log): %v", err)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Path != logPath ||
		cfg.Log.MaxSizeMB != 20 || cfg.Log.KeepMB != 5 {
		t.Fatalf("log patch not applied: %+v", cfg.Log)
	}

	// Invalid level rejected.
	bad := Request{Action: ActionApply, UUID: "u", Patch: Patch{LogLevel: ptr("verbose")}}
	if err := applyRequest(config.Default(), bad, "rev"); err == nil {
		t.Fatal("invalid log level accepted")
	}

	// Relative path rejected.
	rel := Request{Action: ActionApply, UUID: "u", Patch: Patch{LogPath: ptr("relative.log")}}
	if err := applyRequest(config.Default(), rel, "rev"); err == nil {
		t.Fatal("relative log path accepted")
	}

	// Missing parent directory rejected: otherwise the service silently fails
	// open to stderr and the user gets no file.
	missing := filepath.Join(t.TempDir(), "absent", "dns-client.log")
	missReq := Request{Action: ActionApply, UUID: "u", Patch: Patch{LogPath: ptr(missing)}}
	if err := applyRequest(config.Default(), missReq, "rev"); err == nil {
		t.Fatal("log path with a missing parent directory accepted")
	}

	// Explicit zero size rejected.
	zero := Request{Action: ActionApply, UUID: "u", Patch: Patch{LogMaxSizeMB: ptr(0)}}
	if err := applyRequest(config.Default(), zero, "rev"); err == nil {
		t.Fatal("explicit zero log size accepted")
	}
}

// TestReloadRelevant pins reload suppression: a log-only edit must not reload,
// every other field still does (LOGGING_PLAN §1.6).
func TestReloadRelevant(t *testing.T) {
	base := config.Default()

	logOnly := base.Clone()
	logOnly.Log.Level = config.LogLevelDebug
	if reloadRelevant(base, logOnly) {
		t.Fatal("log-only change must not reload")
	}

	modeChange := base.Clone()
	modeChange.Mode = config.ModeOn
	if !reloadRelevant(base, modeChange) {
		t.Fatal("mode change must reload")
	}

	logAndMode := logOnly.Clone()
	logAndMode.Mode = config.ModeOn
	if !reloadRelevant(base, logAndMode) {
		t.Fatal("mixed log+non-log change must reload")
	}
}

// TestApplyProfileFallbackPatch covers the allowlisted fallback_profile field:
// it trims on set, clears on an explicit empty value, and a nil patch leaves it
// untouched.
func TestApplyProfileFallbackPatch(t *testing.T) {
	p := config.Profile{Upstreams: []string{"https://x"}, Cache: &config.Cache{Enabled: true, SizeBytes: 1024}}

	if err := applyProfilePatch(&p, ProfilePatch{}); err != nil {
		t.Fatalf("nil patch: %v", err)
	}
	if p.FallbackProfile != "" {
		t.Fatalf("nil patch set fallback = %q", p.FallbackProfile)
	}

	fb := "  backup  "
	if err := applyProfilePatch(&p, ProfilePatch{FallbackProfile: &fb}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if p.FallbackProfile != "backup" {
		t.Fatalf("fallback = %q, want backup", p.FallbackProfile)
	}

	clear := ""
	if err := applyProfilePatch(&p, ProfilePatch{FallbackProfile: &clear}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if p.FallbackProfile != "" {
		t.Fatalf("fallback not cleared: %q", p.FallbackProfile)
	}
}
