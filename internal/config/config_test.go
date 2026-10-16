package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/daika7ana/dns-client/internal/providers"
)

func TestDefault(t *testing.T) {
	c := Default()

	if c.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", c.SchemaVersion, SchemaVersion)
	}
	if c.Mode != ModeOff {
		t.Errorf("Mode = %q, want %q", c.Mode, ModeOff)
	}
	if c.Profile != DefaultProfileName {
		t.Errorf("Profile = %q, want %q", c.Profile, DefaultProfileName)
	}

	p, ok := c.Profiles[DefaultProfileName]
	if !ok {
		t.Fatalf("default profile %q missing", DefaultProfileName)
	}
	if len(p.Upstreams) == 0 {
		t.Error("default profile has no upstreams")
	}
	if p.Cache == nil || !p.Cache.Enabled || p.Cache.SizeBytes != DefaultCacheSizeBytes {
		t.Errorf("default cache = %+v, want enabled %d", p.Cache, DefaultCacheSizeBytes)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	c := Default()
	c.Mode = ModeOn
	c.Profiles[DefaultProfileName] = Profile{
		Upstreams: []string{"https://dns.quad9.net/dns-query", "tls://dns.quad9.net:853"},
		Bootstrap: []string{"9.9.9.9", "2620:fe::fe"},
		Cache:     &Cache{Enabled: true, SizeBytes: 1 << 20},
		PerDomain: []PerDomain{{Domains: []string{"corp.example"}, Upstream: "tls://10.0.0.1"}},
		Filtering: Filtering{Enabled: true, BlockResponse: "0.0.0.0"},
	}
	if err := c.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Mode != ModeOn {
		t.Errorf("Mode = %q, want %q", got.Mode, ModeOn)
	}
	p := got.Profiles[DefaultProfileName]
	if !slices.Equal(p.Upstreams, c.Profiles[DefaultProfileName].Upstreams) {
		t.Errorf("upstreams = %v", p.Upstreams)
	}
	if !slices.Equal(p.Bootstrap, c.Profiles[DefaultProfileName].Bootstrap) {
		t.Errorf("bootstrap = %v", p.Bootstrap)
	}
	if p.Cache == nil || p.Cache.SizeBytes != 1<<20 {
		t.Errorf("cache = %+v", p.Cache)
	}
	if len(p.PerDomain) != 1 || p.PerDomain[0].Upstream != "tls://10.0.0.1" {
		t.Errorf("per_domain = %+v", p.PerDomain)
	}
	if !p.Filtering.Enabled || p.Filtering.BlockResponse != "0.0.0.0" {
		t.Errorf("filtering = %+v", p.Filtering)
	}
}

func TestValidationErrors(t *testing.T) {
	valid := func() *Config {
		return &Config{
			SchemaVersion: SchemaVersion,
			Mode:          ModeOff,
			Profile:       "default",
			Log:           LogConfig{Level: LogLevelOff, MaxSizeMB: DefaultLogMaxSizeMB, KeepMB: DefaultLogKeepMB},
			Profiles: map[string]Profile{
				"default": {
					Upstreams: []string{"https://dns.quad9.net/dns-query"},
					Cache:     &Cache{Enabled: true, SizeBytes: DefaultCacheSizeBytes},
				},
			},
		}
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"bad mode", func(c *Config) { c.Mode = "sometimes" }},
		{"missing active profile", func(c *Config) { c.Profile = "ghost" }},
		{"empty upstreams", func(c *Config) {
			c.Profiles["default"] = Profile{Cache: &Cache{Enabled: true, SizeBytes: 1024}}
		}},
		{"blank upstream", func(c *Config) {
			c.Profiles["default"] = Profile{Upstreams: []string{"  "}, Cache: &Cache{Enabled: true, SizeBytes: 1024}}
		}},
		{"negative cache", func(c *Config) {
			c.Profiles["default"] = Profile{Upstreams: []string{"x"}, Cache: &Cache{Enabled: false, SizeBytes: -1}}
		}},
		{"missing cache block", func(c *Config) { c.Profiles["default"] = Profile{Upstreams: []string{"x"}} }},
		{"bad per_domain", func(c *Config) {
			c.Profiles["default"] = Profile{
				Upstreams: []string{"x"},
				Cache:     &Cache{Enabled: true, SizeBytes: 1024},
				PerDomain: []PerDomain{{Domains: nil, Upstream: ""}},
			}
		}},
		{"bad block_response", func(c *Config) {
			c.Profiles["default"] = Profile{
				Upstreams: []string{"x"},
				Cache:     &Cache{Enabled: true, SizeBytes: 1024},
				Filtering: Filtering{BlockResponse: "1.2.3.4"},
			}
		}},
		{"bad filtering list", func(c *Config) {
			c.Profiles["default"] = Profile{
				Upstreams: []string{"x"},
				Cache:     &Cache{Enabled: true, SizeBytes: 1024},
				Filtering: Filtering{Lists: []FilterList{{URL: ""}}},
			}
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(c)
			if err := c.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error for %s", tc.name)
			}
		})
	}

	if err := valid().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestLoadMissingAndBadYAML(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.yaml")); !errors.Is(err, ErrNoConfig) {
		t.Fatalf("Load missing = %v, want ErrNoConfig", err)
	}

	bad := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(bad, []byte("mode: [not,a,scalar]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Fatal("Load bad YAML = nil, want error")
	}
}

func TestResolvePrecedence(t *testing.T) {
	cfg := &Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeOn,
		Profile:       "default",
		Profiles: map[string]Profile{
			"default": {
				Upstreams: []string{"https://cfg-default"},
				Bootstrap: []string{"1.1.1.1"},
				Cache:     &Cache{Enabled: true, SizeBytes: 1 << 20},
			},
			"other": {
				Upstreams: []string{"https://cfg-other"},
				Bootstrap: []string{"2.2.2.2"},
				Cache:     &Cache{Enabled: false, SizeBytes: 2 << 20},
				PerDomain: []PerDomain{{Domains: []string{"corp.example"}, Upstream: "tls://10.0.0.1"}},
			},
		},
	}

	t.Run("config only", func(t *testing.T) {
		r, err := Resolve(cfg, Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.Upstreams, []string{"https://cfg-default"}) || !slices.Equal(r.Bootstrap, []string{"1.1.1.1"}) {
			t.Errorf("resolved = %+v", r)
		}
		if r.Cache.SizeBytes != 1<<20 {
			t.Errorf("cache = %+v", r.Cache)
		}
	})

	t.Run("flag overrides upstream only", func(t *testing.T) {
		r, err := Resolve(cfg, Overrides{Upstream: "https://flag"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.Upstreams, []string{"https://flag"}) {
			t.Errorf("upstreams = %v", r.Upstreams)
		}
		if !slices.Equal(r.Bootstrap, []string{"1.1.1.1"}) {
			t.Errorf("bootstrap should stay from config, got %v", r.Bootstrap)
		}
	})

	t.Run("flag overrides both, comma split", func(t *testing.T) {
		r, err := Resolve(cfg, Overrides{Upstream: "https://a,https://b", Bootstrap: "9.9.9.9, 2620:fe::fe"})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(r.Upstreams, []string{"https://a", "https://b"}) {
			t.Errorf("upstreams = %v", r.Upstreams)
		}
		if !slices.Equal(r.Bootstrap, []string{"9.9.9.9", "2620:fe::fe"}) {
			t.Errorf("bootstrap = %v", r.Bootstrap)
		}
	})

	t.Run("profile override", func(t *testing.T) {
		r, err := Resolve(cfg, Overrides{Profile: "other"})
		if err != nil {
			t.Fatal(err)
		}
		if r.ProfileName != "other" || !slices.Equal(r.Upstreams, []string{"https://cfg-other"}) {
			t.Errorf("resolved = %+v", r)
		}
		if r.Cache.Enabled {
			t.Errorf("cache should follow the selected profile: %+v", r.Cache)
		}
		if len(r.PerDomain) != 1 || r.PerDomain[0].Upstream != "tls://10.0.0.1" {
			t.Errorf("per_domain should follow the selected profile: %+v", r.PerDomain)
		}
	})

	t.Run("nil config uses defaults", func(t *testing.T) {
		r, err := Resolve(nil, Overrides{})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Upstreams) == 0 || len(r.Bootstrap) == 0 {
			t.Errorf("defaults not applied: %+v", r)
		}
		if r.Filtering.BlockResponse != "0.0.0.0" || r.Filtering.BlockTTL == 0 {
			t.Errorf("filtering defaults not applied: %+v", r.Filtering)
		}
		if r.Filtering.Enabled {
			t.Errorf("filtering should default off: %+v", r.Filtering)
		}
	})

	t.Run("unknown profile errors", func(t *testing.T) {
		if _, err := Resolve(cfg, Overrides{Profile: "ghost"}); err == nil {
			t.Fatal("want error for unknown profile")
		}
	})
}

func TestInitIdempotence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if err := Init(path); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config not written: %v", err)
	}

	if err := Init(path); !errors.Is(err, ErrExists) {
		t.Fatalf("second Init = %v, want ErrExists", err)
	}

	// The existing file must be left untouched.
	if _, err := Load(path); err != nil {
		t.Fatalf("Load after Init: %v", err)
	}
}

// TestProfileOps covers the structural profile operations and every reject
// case.
func TestProfileOps(t *testing.T) {
	t.Run("add trims and stores", func(t *testing.T) {
		c := Default()
		if err := c.AddProfile("  work  "); err != nil {
			t.Fatalf("AddProfile: %v", err)
		}
		if _, ok := c.Profiles["work"]; !ok {
			t.Fatalf("profile not added: %v", c.Profiles)
		}
	})

	t.Run("add rejects existing/empty/long/control", func(t *testing.T) {
		c := Default()
		if err := c.AddProfile("default"); err == nil {
			t.Error("duplicate name accepted")
		}
		if err := c.AddProfile("   "); err == nil {
			t.Error("empty name accepted")
		}
		if err := c.AddProfile(strings.Repeat("a", 65)); err == nil {
			t.Error("over-long name accepted")
		}
		if err := c.AddProfile("bad\x00name"); err == nil {
			t.Error("control character accepted")
		}
	})

	t.Run("set active unknown rejected", func(t *testing.T) {
		c := Default()
		if err := c.SetActiveProfile("ghost"); err == nil {
			t.Error("unknown active profile accepted")
		}
	})

	t.Run("rename", func(t *testing.T) {
		c := Default()
		if err := c.AddProfile("work"); err != nil {
			t.Fatal(err)
		}
		if err := c.RenameProfile("ghost", "x"); err == nil {
			t.Error("unknown from accepted")
		}
		if err := c.RenameProfile("work", "default"); err == nil {
			t.Error("collision accepted")
		}
		if err := c.RenameProfile("work", "work"); err == nil {
			t.Error("from==to accepted")
		}

		// Renaming the active profile moves the active selection.
		if err := c.RenameProfile("default", "main"); err != nil {
			t.Fatalf("rename active: %v", err)
		}
		if c.Profile != "main" {
			t.Fatalf("active profile = %q, want main", c.Profile)
		}
		if _, ok := c.Profiles["main"]; !ok {
			t.Fatal("renamed profile missing")
		}
		if _, ok := c.Profiles["default"]; ok {
			t.Fatal("old profile name still present")
		}
	})

	t.Run("delete", func(t *testing.T) {
		c := Default()
		if err := c.DeleteProfile("ghost"); err == nil {
			t.Error("unknown delete accepted")
		}
		if err := c.DeleteProfile("default"); err == nil {
			t.Error("active delete accepted")
		}
		if err := c.AddProfile("work"); err != nil {
			t.Fatal(err)
		}
		if err := c.DeleteProfile("work"); err != nil {
			t.Fatalf("delete inactive: %v", err)
		}

		// Deleting the last profile is refused.
		last := Default()
		if err := last.DeleteProfile(DefaultProfileName); err == nil {
			t.Error("deleting the only profile accepted")
		}
	})
}

// TestCloneProfileIsDeep guards the aliasing hazard a value copy would have.
func TestCloneProfileIsDeep(t *testing.T) {
	original := Profile{
		Upstreams: []string{"https://a", "https://b"},
		Bootstrap: []string{"1.1.1.1"},
		Cache:     &Cache{Enabled: true, SizeBytes: 1024},
		PerDomain: []PerDomain{{Domains: []string{"corp.example"}, Upstream: "tls://10.0.0.1", Enabled: new(false)}},
		Filtering: Filtering{Lists: []FilterList{{URL: "https://list.example/x", Enabled: true}}},
	}

	clone := cloneProfile(original)

	clone.Upstreams[0] = "https://changed"
	clone.Bootstrap[0] = "2.2.2.2"
	clone.Cache.SizeBytes = 999
	clone.PerDomain[0].Domains[0] = "changed.example"
	*clone.PerDomain[0].Enabled = true
	clone.Filtering.Lists[0].URL = "https://changed"

	if original.Upstreams[0] != "https://a" {
		t.Error("Upstreams aliased")
	}
	if original.Bootstrap[0] != "1.1.1.1" {
		t.Error("Bootstrap aliased")
	}
	if original.Cache.SizeBytes != 1024 {
		t.Error("Cache pointer aliased")
	}
	if original.PerDomain[0].Domains[0] != "corp.example" {
		t.Error("PerDomain inner slice aliased")
	}
	if original.PerDomain[0].Enabled == nil || *original.PerDomain[0].Enabled {
		t.Error("PerDomain Enabled pointer aliased")
	}
	if original.Filtering.Lists[0].URL != "https://list.example/x" {
		t.Error("Filtering lists aliased")
	}
}

// TestPerDomainIsEnabled pins the nil-means-enabled default: an absent field
// (legacy config or a new rule) is enabled, and only an explicit false disables.
func TestPerDomainIsEnabled(t *testing.T) {
	if !(PerDomain{}).IsEnabled() {
		t.Error("an absent Enabled should be enabled")
	}

	off, on := false, true
	if (PerDomain{Enabled: &off}).IsEnabled() {
		t.Error("Enabled=false should be disabled")
	}
	if !(PerDomain{Enabled: &on}).IsEnabled() {
		t.Error("Enabled=true should be enabled")
	}
}

// TestPerDomainBypass pins the two rule kinds: a bypass rule needs no upstream,
// serializes without an upstream key and round-trips, while a forward rule still
// requires one and a bypass rule must not carry one.
func TestPerDomainBypass(t *testing.T) {
	t.Run("bypass rule is valid and round-trips", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		c := Default()
		c.Profiles[DefaultProfileName] = Profile{
			Upstreams: []string{"https://dns.quad9.net/dns-query"},
			Cache:     &Cache{Enabled: true, SizeBytes: DefaultCacheSizeBytes},
			PerDomain: []PerDomain{{Domains: []string{"local.example"}, Bypass: true}},
		}
		if err := c.Save(path); err != nil {
			t.Fatalf("Save: %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if strings.Contains(string(data), "upstream:") {
			t.Errorf("bypass rule serialized an upstream key:\n%s", data)
		}

		got, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		pd := got.Profiles[DefaultProfileName].PerDomain
		if len(pd) != 1 || !pd[0].Bypass || pd[0].Upstream != "" {
			t.Errorf("per_domain = %+v, want one bypass rule with no upstream", pd)
		}
	})

	t.Run("bypass rule with an upstream is rejected", func(t *testing.T) {
		c := Default()
		c.Profiles[DefaultProfileName] = Profile{
			Upstreams: []string{"https://dns.quad9.net/dns-query"},
			Cache:     &Cache{Enabled: true, SizeBytes: DefaultCacheSizeBytes},
			PerDomain: []PerDomain{{Domains: []string{"local.example"}, Bypass: true, Upstream: "tls://10.0.0.1"}},
		}
		if err := c.Validate(); err == nil {
			t.Error("a bypass rule with an upstream should be rejected")
		}
	})

	t.Run("non-bypass rule with a blank upstream is rejected", func(t *testing.T) {
		c := Default()
		c.Profiles[DefaultProfileName] = Profile{
			Upstreams: []string{"https://dns.quad9.net/dns-query"},
			Cache:     &Cache{Enabled: true, SizeBytes: DefaultCacheSizeBytes},
			PerDomain: []PerDomain{{Domains: []string{"corp.example"}}},
		}
		if err := c.Validate(); err == nil {
			t.Error("a non-bypass rule with a blank upstream should be rejected")
		}
	})
}

// TestApplySelection checks Expand results plus the display-only metadata.
func TestApplySelection(t *testing.T) {
	t.Run("nextdns", func(t *testing.T) {
		p := Profile{}
		sel := providers.Selection{Provider: providers.NextDNS, ID: "abc", Protocol: providers.DoT}
		if err := p.ApplySelection(sel); err != nil {
			t.Fatalf("ApplySelection: %v", err)
		}
		if !slices.Equal(p.Upstreams, []string{"tls://abc.dns.nextdns.io:853"}) {
			t.Errorf("upstreams = %v", p.Upstreams)
		}
		if p.Provider != "nextdns" || p.Variant != "ultralow" || p.Protocol != "dot" {
			t.Errorf("metadata = %q/%q/%q", p.Provider, p.Variant, p.Protocol)
		}
	})

	t.Run("controld defaults protocol", func(t *testing.T) {
		p := Profile{}
		if err := p.ApplySelection(providers.Selection{Provider: providers.ControlD, ID: "xyz"}); err != nil {
			t.Fatalf("ApplySelection: %v", err)
		}
		if !slices.Equal(p.Upstreams, []string{"https://dns.controld.com/xyz"}) {
			t.Errorf("upstreams = %v", p.Upstreams)
		}
		if p.Protocol != "doh" || p.Variant != "custom" {
			t.Errorf("metadata = %q/%q/%q", p.Provider, p.Variant, p.Protocol)
		}
	})

	t.Run("invalid selection leaves profile unchanged", func(t *testing.T) {
		p := Profile{Upstreams: []string{"https://keep"}}
		err := p.ApplySelection(providers.Selection{Provider: providers.Quad9, Variant: "bogus"})
		if err == nil {
			t.Fatal("bogus variant accepted")
		}
		if !slices.Equal(p.Upstreams, []string{"https://keep"}) {
			t.Errorf("profile mutated on error: %v", p.Upstreams)
		}
	})
}

func TestValidBootstrap(t *testing.T) {
	valid := []string{"9.9.9.9", "1.1.1.1:53", "2620:fe::fe", "[2620:fe::fe]:53", " 9.9.9.9 "}
	for _, s := range valid {
		if !ValidBootstrap(s) {
			t.Errorf("ValidBootstrap(%q) = false, want true", s)
		}
	}

	invalid := []string{"", "   ", "example.com", "9.9.9.999", "1.1.1.1:abc", "1.1.1.1:99999"}
	for _, s := range invalid {
		if ValidBootstrap(s) {
			t.Errorf("ValidBootstrap(%q) = true, want false", s)
		}
	}
}

// TestExampleConfigLoads guards the shipped config.example.yaml.
func TestExampleConfigLoads(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip("config.example.yaml not present")
	}

	if _, err := Load(path); err != nil {
		t.Fatalf("config.example.yaml does not load: %v", err)
	}
}

// TestProfileMetadataRoundTrip checks the display-only provider/variant/protocol
// fields survive Save/Load/Resolve, are trimmed, and default to empty.
func TestProfileMetadataRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	c := Default()
	p := c.Profiles[DefaultProfileName]
	p.Provider = "  NextDNS  "
	p.Variant = "ultralow"
	p.Protocol = "doh"
	c.Profiles[DefaultProfileName] = p

	if err := c.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gp := got.Profiles[DefaultProfileName]
	if gp.Provider != "NextDNS" || gp.Variant != "ultralow" || gp.Protocol != "doh" {
		t.Fatalf("metadata not preserved: %+v", gp)
	}

	r, err := Resolve(got, Overrides{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if r.Provider != "NextDNS" || r.Variant != "ultralow" || r.Protocol != "doh" {
		t.Fatalf("metadata not resolved: provider=%q variant=%q protocol=%q", r.Provider, r.Variant, r.Protocol)
	}

	// A config without metadata still loads (older files).
	bare := Default()
	if bare.Profiles[DefaultProfileName].Provider != "" {
		t.Fatal("default profile should have empty metadata")
	}
}

// TestLogConfigDefaultsAndParse covers the log block (LOGGING_PLAN §2): absent
// block defaults to off, a block parses, and KeepMB is clamped.
func TestLogConfigDefaultsAndParse(t *testing.T) {
	if d := Default(); d.Log.Level != LogLevelOff || d.Log.MaxSizeMB != DefaultLogMaxSizeMB ||
		d.Log.KeepMB != DefaultLogKeepMB {
		t.Fatalf("default log = %+v", d.Log)
	}

	// A block parses.
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "schema_version: 1\nmode: off\nprofile: default\n" +
		"log:\n  level: debug\n  max_size_mb: 5\n  keep_mb: 2\n" +
		"profiles:\n  default:\n    upstreams: [\"https://dns.quad9.net/dns-query\"]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Log.Level != LogLevelDebug || c.Log.MaxSizeMB != 5 || c.Log.KeepMB != 2 {
		t.Fatalf("parsed log = %+v", c.Log)
	}

	// An absent block defaults to off.
	bare := filepath.Join(t.TempDir(), "config.yaml")
	body = "schema_version: 1\nmode: off\nprofile: default\n" +
		"profiles:\n  default:\n    upstreams: [\"https://dns.quad9.net/dns-query\"]\n"
	if err = os.WriteFile(bare, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	bc, err := Load(bare)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if bc.Log.Level != LogLevelOff || bc.Log.MaxSizeMB != DefaultLogMaxSizeMB {
		t.Fatalf("absent log block = %+v", bc.Log)
	}

	// KeepMB > MaxSizeMB is clamped by normalize.
	clamp := LogConfig{Level: LogLevelInfo, MaxSizeMB: 4, KeepMB: 9}
	clamp.normalize()
	if clamp.KeepMB != 4 {
		t.Fatalf("clamped KeepMB = %d, want 4", clamp.KeepMB)
	}

	// An overflowing MaxSizeMB is clamped so the MiB->bytes shift cannot wrap.
	overflow := LogConfig{Level: LogLevelInfo, MaxSizeMB: 1 << 62, KeepMB: 9}
	overflow.normalize()
	if overflow.MaxSizeMB != MaxLogSizeMB || overflow.KeepMB != 9 {
		t.Fatalf("overflow clamp = %+v", overflow)
	}
}

// TestLogConfigValidate covers the invalid cases (LOGGING_PLAN §2).
func TestLogConfigValidate(t *testing.T) {
	badLevel := Default()
	badLevel.Log.Level = "verbose"
	if err := badLevel.Validate(); err == nil {
		t.Fatal("unknown log level accepted")
	}

	badPath := Default()
	badPath.Log.Path = "relative.log"
	if err := badPath.Validate(); err == nil {
		t.Fatal("relative log path accepted")
	}

	dirOnly := Default()
	dirOnly.Log.Path = string(filepath.Separator)
	if err := dirOnly.Validate(); err == nil {
		t.Fatal("directory log path accepted")
	}

	for _, dev := range []string{`\\.\PhysicalDrive0`, `\\?\C:\dns-client.log`} {
		device := Default()
		device.Log.Path = dev
		if err := device.Validate(); err == nil {
			t.Fatalf("device log path %q accepted", dev)
		}
	}

	good := Default()
	good.Log = LogConfig{Level: LogLevelInfo, Path: filepath.Join(t.TempDir(), "dns-client.log"), MaxSizeMB: 5, KeepMB: 2}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid log config rejected: %v", err)
	}
}
