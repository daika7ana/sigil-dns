// Package config implements the Phase-2 YAML configuration (SPEC §8): active
// mode and profile, upstream/bootstrap lists, cache settings, and the
// phase-3 fields (per_domain, filtering) which parse and validate but are not
// used yet.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/daika7ana/dns-client/internal/resolver"
	"github.com/daika7ana/dns-client/internal/winacl"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the current config schema version.
const SchemaVersion = 1

// Modes.
const (
	ModeOn  = "on"
	ModeOff = "off"
)

// DefaultCacheSizeBytes is 64 MiB.
const DefaultCacheSizeBytes = 64 << 20

// DefaultProfileName is the name of the default profile.
const DefaultProfileName = "default"

// ErrNoConfig is returned by Load when the config file does not exist.
var ErrNoConfig = errors.New("no config file")

// ErrExists is returned by Init when the config file already exists.
var ErrExists = errors.New("config file already exists")

// Cache is the resolver cache configuration.
type Cache struct {
	Enabled   bool `yaml:"enabled"`
	SizeBytes int  `yaml:"size_bytes"`
}

// PerDomain is a per-domain routing rule, wired into dnsproxy's UpstreamConfig
// by internal/resolver (see rulesToLines + proxy.ParseUpstreamsConfig).
type PerDomain struct {
	Domains  []string `yaml:"domains"`
	Upstream string   `yaml:"upstream"`
}

// FilterList is a blocklist entry, loaded into urlfilter by internal/filter.
type FilterList struct {
	URL     string `yaml:"url"`
	Enabled bool   `yaml:"enabled"`
}

// Filtering is the phase-3 filtering configuration.
type Filtering struct {
	Lists         []FilterList `yaml:"lists"`
	BlockResponse string       `yaml:"block_response"`
	BlockTTL      uint32       `yaml:"block_ttl"`
	Enabled       bool         `yaml:"enabled"`
}

// Profile is one named upstream configuration.
//
// Provider, Variant, and Protocol are optional, display-only metadata (for
// example "NextDNS", "ultralow", "doh").  They are never used for resolution and
// are never derived from the upstream URL; they exist so a GUI can label a
// profile without parsing secrets out of it.  Older configs simply omit them.
type Profile struct {
	Cache     *Cache      `yaml:"cache"`
	Upstreams []string    `yaml:"upstreams"`
	Bootstrap []string    `yaml:"bootstrap"`
	PerDomain []PerDomain `yaml:"per_domain"`
	Filtering Filtering   `yaml:"filtering"`
	Provider  string      `yaml:"provider,omitempty"`
	Variant   string      `yaml:"variant,omitempty"`
	Protocol  string      `yaml:"protocol,omitempty"`
}

// Config is the top-level configuration.
type Config struct {
	Profiles      map[string]Profile `yaml:"profiles"`
	Mode          string             `yaml:"mode"`
	Profile       string             `yaml:"profile"`
	SchemaVersion int                `yaml:"schema_version"`
}

// Default returns the default configuration: mode off, one "default" profile,
// cache enabled at 64 MiB, ControlD free p0.
func Default() (c *Config) {
	return &Config{
		SchemaVersion: SchemaVersion,
		Mode:          ModeOff,
		Profile:       DefaultProfileName,
		Profiles: map[string]Profile{
			DefaultProfileName: {
				Upstreams: []string{resolver.DefaultUpstream},
				Bootstrap: []string{resolver.DefaultBootstrap},
				Cache:     &Cache{Enabled: true, SizeBytes: DefaultCacheSizeBytes},
			},
		},
	}
}

// DefaultPath returns the platform-appropriate config file path.
func DefaultPath() (p string) {
	if dir := os.Getenv("ProgramData"); dir != "" {
		return filepath.Join(dir, "dns-client", "config.yaml")
	}

	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "dns-client", "config.yaml")
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "dns-client", "config.yaml")
	}

	return filepath.Join(os.TempDir(), "dns-client", "config.yaml")
}

// Load reads and normalizes the config at path.  It returns ErrNoConfig if the
// file does not exist.
func Load(path string) (c *Config, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoConfig
		}

		return nil, fmt.Errorf("reading config: %w", err)
	}

	c = &Config{}
	if err = yaml.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	c.normalize()
	if err = c.Validate(); err != nil {
		return nil, err
	}

	return c, nil
}

// Save writes the config at path atomically, then ACLs the file and its
// directory (SYSTEM + Administrators on Windows).  An ACL failure is a hard
// error: the config may contain resolver ids.
func (c *Config) Save(path string) (err error) {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	c.normalize()
	if err = c.Validate(); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	// Restrict the directory before the temp file is created in it, so the temp
	// does not inherit a permissive ACL.
	if err = winacl.SecureDir(dir); err != nil {
		return fmt.Errorf("securing config dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing config: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return fmt.Errorf("closing config: %w", err)
	}
	if err = os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming config: %w", err)
	}

	// Protect the final path.
	if err = winacl.SecureFile(path); err != nil {
		return fmt.Errorf("securing config file: %w", err)
	}

	return nil
}

// Init writes a default config to path if none exists.  It never clobbers an
// existing file and returns ErrExists in that case.
func Init(path string) (err error) {
	if _, err = os.Stat(path); err == nil {
		return ErrExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("checking config: %w", err)
	}

	return Default().Save(path)
}

// normalize fills in defaults for missing values.
func (c *Config) normalize() {
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	if c.Mode == "" {
		c.Mode = ModeOff
	}
	if len(c.Profiles) == 0 {
		c.Profiles = Default().Profiles
	}
	if c.Profile == "" {
		c.Profile = DefaultProfileName
	}

	for name, p := range c.Profiles {
		if p.Cache == nil {
			p.Cache = &Cache{Enabled: true, SizeBytes: DefaultCacheSizeBytes}
		} else if p.Cache.SizeBytes == 0 {
			p.Cache.SizeBytes = DefaultCacheSizeBytes
		}
		p.filteringDefaults()

		// Display-only metadata: trim only, never validate.
		p.Provider = strings.TrimSpace(p.Provider)
		p.Variant = strings.TrimSpace(p.Variant)
		p.Protocol = strings.TrimSpace(p.Protocol)

		c.Profiles[name] = p
	}
}

// filteringDefaults fills in the filtering defaults.
func (p *Profile) filteringDefaults() {
	if p.Filtering.BlockResponse == "" {
		p.Filtering.BlockResponse = resolver.BlockResponseZero
	}
	if p.Filtering.BlockTTL == 0 {
		p.Filtering.BlockTTL = resolver.DefaultBlockTTL
	}
}

// Validate returns a clear error for an invalid configuration.
func (c *Config) Validate() (err error) {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported schema_version %d (want %d)", c.SchemaVersion, SchemaVersion)
	}

	switch c.Mode {
	case ModeOn, ModeOff:
	default:
		return fmt.Errorf("invalid mode %q (want %q or %q)", c.Mode, ModeOn, ModeOff)
	}

	if _, ok := c.Profiles[c.Profile]; !ok {
		return fmt.Errorf("active profile %q not found in profiles", c.Profile)
	}

	for name, p := range c.Profiles {
		if err = p.validate(name); err != nil {
			return err
		}
	}

	return nil
}

// validate checks a single profile.
func (p Profile) validate(name string) (err error) {
	if len(p.Upstreams) == 0 {
		return fmt.Errorf("profile %q has no upstreams", name)
	}
	for i, u := range p.Upstreams {
		if strings.TrimSpace(u) == "" {
			return fmt.Errorf("profile %q: upstream %d is empty", name, i)
		}
	}
	for i, b := range p.Bootstrap {
		if strings.TrimSpace(b) == "" {
			return fmt.Errorf("profile %q: bootstrap %d is empty", name, i)
		}
	}

	if p.Cache == nil {
		return fmt.Errorf("profile %q: missing cache block", name)
	}
	if p.Cache.SizeBytes < 0 {
		return fmt.Errorf("profile %q: negative cache size_bytes %d", name, p.Cache.SizeBytes)
	}
	if p.Cache.Enabled && p.Cache.SizeBytes <= 0 {
		return fmt.Errorf("profile %q: cache enabled with non-positive size_bytes", name)
	}

	// Per-domain and filtering fields: validated here, applied by the resolver.
	for i, pd := range p.PerDomain {
		if len(pd.Domains) == 0 || strings.TrimSpace(pd.Upstream) == "" {
			return fmt.Errorf("profile %q: per_domain %d needs domains and an upstream", name, i)
		}
	}
	for i, l := range p.Filtering.Lists {
		if strings.TrimSpace(l.URL) == "" {
			return fmt.Errorf("profile %q: filtering list %d has no url", name, i)
		}
	}
	switch p.Filtering.BlockResponse {
	case "", resolver.BlockResponseZero, resolver.BlockResponseNXDOMAIN:
	default:
		return fmt.Errorf(
			"profile %q: invalid block_response %q (want %q or %q)",
			name, p.Filtering.BlockResponse, resolver.BlockResponseZero, resolver.BlockResponseNXDOMAIN,
		)
	}

	return nil
}

// Overrides are explicit CLI values that take precedence over the config file.
type Overrides struct {
	Profile   string
	Upstream  string
	Bootstrap string
}

// Resolved is the effective settings after applying precedence.
type Resolved struct {
	ProfileName string
	Upstreams   []string
	Bootstrap   []string
	PerDomain   []PerDomain
	Cache       Cache
	Filtering   Filtering
	Provider    string
	Variant     string
	Protocol    string
}

// Resolve applies the precedence flags > config > defaults.  A nil cfg means no
// config file was found, so built-in defaults are used.
func Resolve(cfg *Config, ov Overrides) (r Resolved, err error) {
	if cfg == nil {
		cfg = Default()
	}

	name := ov.Profile
	if name == "" {
		name = cfg.Profile
	}
	if name == "" {
		name = DefaultProfileName
	}

	p, ok := cfg.Profiles[name]
	if !ok {
		return Resolved{}, fmt.Errorf("profile %q not found in config", name)
	}

	r.ProfileName = name
	r.Upstreams = p.Upstreams
	r.Bootstrap = p.Bootstrap
	r.PerDomain = p.PerDomain
	p.filteringDefaults()
	r.Filtering = p.Filtering
	r.Provider = strings.TrimSpace(p.Provider)
	r.Variant = strings.TrimSpace(p.Variant)
	r.Protocol = strings.TrimSpace(p.Protocol)

	if ov.Upstream != "" {
		r.Upstreams = splitList(ov.Upstream)
	}
	if ov.Bootstrap != "" {
		r.Bootstrap = splitList(ov.Bootstrap)
	}

	r.Cache = Cache{Enabled: true, SizeBytes: DefaultCacheSizeBytes}
	if p.Cache != nil {
		r.Cache = *p.Cache
	}
	if len(r.Upstreams) == 0 {
		return Resolved{}, fmt.Errorf("profile %q resolves to no upstreams", name)
	}

	return r, nil
}

// splitList splits a comma-separated flag value, trimming and dropping empties.
func splitList(s string) (out []string) {
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}

	return out
}
