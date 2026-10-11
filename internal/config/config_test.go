package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
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
