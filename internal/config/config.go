// Package config implements the Phase-2 YAML configuration (SPEC §8): active
// profile, upstream/bootstrap lists, cache settings, and the
// phase-3 fields (per_domain, filtering) which parse and validate but are not
// used yet.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/daika7ana/sigil-dns/internal/providers"
	"github.com/daika7ana/sigil-dns/internal/resolver"
	"github.com/daika7ana/sigil-dns/internal/winacl"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the current config schema version.
const SchemaVersion = 1

// DefaultCacheSizeBytes is 64 MiB.
const DefaultCacheSizeBytes = resolver.DefaultCacheSizeBytes

// DefaultProfileName is the name of the default profile.
const DefaultProfileName = "default"

// Logging levels.
const (
	LogLevelOff   = "off"
	LogLevelError = "error"
	LogLevelInfo  = "info"
	LogLevelDebug = "debug"
)

// Default log rotation sizes, in MiB.
const (
	DefaultLogMaxSizeMB = 10
	DefaultLogKeepMB    = 3

	// MaxLogSizeMB bounds rotation sizes so converting MiB to bytes cannot
	// overflow an int64 left shift.
	MaxLogSizeMB = 1 << 20 // 1 TiB
)

// LogConfig is the optional file-logging block.  Only the service writes the
// log; the level/path take effect at the next service start.
type LogConfig struct {
	Level     string `yaml:"level"`
	Path      string `yaml:"path"`
	MaxSizeMB int    `yaml:"max_size_mb"`
	KeepMB    int    `yaml:"keep_mb"`
}

// normalize fills the log-block defaults and clamps the keep window.
func (l *LogConfig) normalize() {
	l.Level = strings.TrimSpace(l.Level)
	if l.Level == "" {
		l.Level = LogLevelOff
	}
	if l.MaxSizeMB <= 0 {
		l.MaxSizeMB = DefaultLogMaxSizeMB
	}
	if l.MaxSizeMB > MaxLogSizeMB {
		l.MaxSizeMB = MaxLogSizeMB
	}
	if l.KeepMB <= 0 {
		l.KeepMB = DefaultLogKeepMB
	}
	if l.KeepMB > l.MaxSizeMB {
		l.KeepMB = l.MaxSizeMB
	}
}

// validate checks the log block.  The keep/max relation is clamped by normalize,
// not rejected here.
func (l LogConfig) validate() (err error) {
	if err = ValidateLogLevel(l.Level); err != nil {
		return err
	}
	if l.MaxSizeMB <= 0 {
		return errors.New("log.max_size_mb must be positive")
	}
	if l.KeepMB <= 0 {
		return errors.New("log.keep_mb must be positive")
	}
	if l.Path == "" {
		return nil
	}

	return ValidateLogPath(l.Path)
}

// ValidateLogLevel reports whether level is one of off/error/info/debug.
func ValidateLogLevel(level string) (err error) {
	switch level {
	case LogLevelOff, LogLevelError, LogLevelInfo, LogLevelDebug:
		return nil
	default:
		return fmt.Errorf("invalid log level %q (want off, error, info, or debug)", level)
	}
}

// ValidateLogPath rejects a non-empty log path that is not a clean absolute
// file path.  It never echoes the path in its error, which can reach an
// unelevated caller through Result.Err.
func ValidateLogPath(path string) (err error) {
	if strings.ContainsRune(path, 0) {
		return errors.New("log path must not contain NUL")
	}
	if strings.HasPrefix(path, `\\.\`) || strings.HasPrefix(path, `\\?\`) {
		return errors.New("log path must not name a device")
	}
	if !filepath.IsAbs(path) {
		return errors.New("log path must be absolute")
	}
	if filepath.Clean(path) != path {
		return errors.New("log path must be clean")
	}
	if filepath.Dir(path) == path {
		return errors.New("log path must name a file")
	}

	return nil
}

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
// by internal/resolver (see rulesToLines + proxy.ParseUpstreamsConfig).  Domains
// accept the dnsmasq conventions the resolver documents: "*.sub.domain" matches
// the subdomains of sub.domain (not the base), while "sub.domain" matches the
// base and all subdomains.  A rule is one of two kinds: a forward rule routes
// Domains to Upstream, while a bypass rule (Bypass) resolves them with the
// system's normal DNS and carries no Upstream.
type PerDomain struct {
	Domains  []string `yaml:"domains"`
	Upstream string   `yaml:"upstream,omitempty"`

	// Bypass sends the domains to the system resolver instead of Upstream.
	Bypass bool `yaml:"bypass,omitempty"`

	// Enabled toggles the rule without deleting it.  A nil (absent) field means
	// enabled, so existing rules keep matching across an upgrade.
	Enabled *bool `yaml:"enabled,omitempty"`
}

// IsEnabled reports whether the rule is active.  A nil Enabled (an absent
// field, or a legacy config) is enabled.
func (pd PerDomain) IsEnabled() (on bool) { return pd.Enabled == nil || *pd.Enabled }

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

	// FallbackProfile names another profile whose upstreams answer queries
	// when this profile's upstreams time out or fail (see docs/CONFIG.md).
	// Only that profile's upstreams and bootstrap are used; its per-domain
	// rules, filtering and cache are not.  Fallback chains are not followed.
	FallbackProfile string `yaml:"fallback_profile,omitempty"`
}

// Config is the top-level configuration.
type Config struct {
	Profiles      map[string]Profile `yaml:"profiles"`
	Profile       string             `yaml:"profile"`
	SchemaVersion int                `yaml:"schema_version"`
	Log           LogConfig          `yaml:"log"`
}

// Default returns the default configuration: one "default" profile, cache
// enabled at 64 MiB, ControlD free p0.
func Default() (c *Config) {
	return &Config{
		SchemaVersion: SchemaVersion,
		Profile:       DefaultProfileName,
		Log:           LogConfig{Level: LogLevelOff, MaxSizeMB: DefaultLogMaxSizeMB, KeepMB: DefaultLogKeepMB},
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
		return filepath.Join(dir, "SigilDNS", "config.yaml")
	}

	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "sigildns", "config.yaml")
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "sigildns", "config.yaml")
	}

	return filepath.Join(os.TempDir(), "sigildns", "config.yaml")
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
	c.normalize()
	if err = c.Validate(); err != nil {
		return err
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}

	// winacl.WriteFileAtomic handles the temp+rename and the SYSTEM/Admins ACL.
	if err = winacl.WriteFileAtomic(path, data); err != nil {
		return fmt.Errorf("writing config: %w", err)
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
	if len(c.Profiles) == 0 {
		c.Profiles = Default().Profiles
	}
	if c.Profile == "" {
		c.Profile = DefaultProfileName
	}

	c.Log.normalize()

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
		p.FallbackProfile = strings.TrimSpace(p.FallbackProfile)

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

	if _, ok := c.Profiles[c.Profile]; !ok {
		return fmt.Errorf("active profile %q not found in profiles", c.Profile)
	}

	for name, p := range c.Profiles {
		if err = p.validate(name); err != nil {
			return err
		}
		if fb := p.FallbackProfile; fb != "" {
			if fb == name {
				return fmt.Errorf("profile %q: fallback_profile must not reference itself", name)
			}
			if _, ok := c.Profiles[fb]; !ok {
				return fmt.Errorf("profile %q: fallback profile %q not found", name, fb)
			}
		}
	}

	return c.Log.validate()
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
		if len(pd.Domains) == 0 {
			return fmt.Errorf("profile %q: per_domain %d needs domains", name, i)
		}
		if pd.Bypass {
			if strings.TrimSpace(pd.Upstream) != "" {
				return fmt.Errorf("profile %q: per_domain %d is a bypass rule with an upstream", name, i)
			}

			continue
		}
		if strings.TrimSpace(pd.Upstream) == "" {
			return fmt.Errorf("profile %q: per_domain %d needs an upstream", name, i)
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

// maxProfileName is the maximum profile name length in characters.
const maxProfileName = 64

// normalizeProfileName trims and validates a profile name.
func normalizeProfileName(name string) (n string, err error) {
	n = strings.TrimSpace(name)
	if n == "" {
		return "", errors.New("profile name must not be empty")
	}
	if utf8.RuneCountInString(n) > maxProfileName {
		return "", fmt.Errorf("profile name must be at most %d characters", maxProfileName)
	}
	if strings.ContainsFunc(n, unicode.IsControl) {
		return "", errors.New("profile name must not contain control characters")
	}

	return n, nil
}

// SetActiveProfile makes name the active profile.
func (c *Config) SetActiveProfile(name string) (err error) {
	n, err := normalizeProfileName(name)
	if err != nil {
		return err
	}
	if _, ok := c.Profiles[n]; !ok {
		return fmt.Errorf("profile %q not found", n)
	}

	c.Profile = n

	return nil
}

// AddProfile adds a fresh default profile under name.  The new profile is
// deep-copied so it never aliases the default's slices or cache pointer.
func (c *Config) AddProfile(name string) (err error) {
	n, err := normalizeProfileName(name)
	if err != nil {
		return err
	}
	if _, ok := c.Profiles[n]; ok {
		return fmt.Errorf("profile %q already exists", n)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}

	c.Profiles[n] = cloneProfile(Default().Profiles[DefaultProfileName])

	return nil
}

// RenameProfile renames from to to.  If from is the active profile the active
// selection follows the rename.
func (c *Config) RenameProfile(from, to string) (err error) {
	f, err := normalizeProfileName(from)
	if err != nil {
		return err
	}
	t, err := normalizeProfileName(to)
	if err != nil {
		return err
	}
	if f == t {
		return errors.New("profile name is unchanged")
	}

	p, ok := c.Profiles[f]
	if !ok {
		return fmt.Errorf("profile %q not found", f)
	}
	if _, ok := c.Profiles[t]; ok {
		return fmt.Errorf("profile %q already exists", t)
	}

	delete(c.Profiles, f)
	c.Profiles[t] = p

	if c.Profile == f {
		c.Profile = t
	}

	// Keep fallback references pointing at the renamed profile.
	for name, q := range c.Profiles {
		if q.FallbackProfile == f {
			q.FallbackProfile = t
			c.Profiles[name] = q
		}
	}

	return nil
}

// DeleteProfile removes name.  The active profile must be switched first, and
// the last remaining profile can never be deleted.
func (c *Config) DeleteProfile(name string) (err error) {
	n, err := normalizeProfileName(name)
	if err != nil {
		return err
	}
	if _, ok := c.Profiles[n]; !ok {
		return fmt.Errorf("profile %q not found", n)
	}
	if c.Profile == n {
		return errors.New("cannot delete the active profile; switch profiles first")
	}
	if len(c.Profiles) <= 1 {
		return errors.New("cannot delete the only profile")
	}
	for other, p := range c.Profiles {
		if p.FallbackProfile == n {
			return fmt.Errorf("profile %q is the fallback for %q; clear that reference first", n, other)
		}
	}

	delete(c.Profiles, n)

	return nil
}

// ApplySelection expands sel and stores the resulting upstreams, bootstrap, and
// display-only metadata on the profile.  Expand validates the selection; the
// metadata uses the same defaults Expand applies (empty variant -> provider
// default, empty protocol -> DoH).
func (p *Profile) ApplySelection(sel providers.Selection) (err error) {
	ep, err := providers.Expand(sel)
	if err != nil {
		return err
	}

	p.Upstreams = ep.Upstreams
	p.Bootstrap = ep.Bootstrap

	provider := strings.TrimSpace(sel.Provider)
	variant := strings.TrimSpace(sel.Variant)
	if variant == "" {
		if pr, ok := providers.Lookup(provider); ok {
			variant = pr.DefaultVariant
		}
	}

	proto := sel.Protocol
	if proto == "" {
		proto = providers.DoH
	}

	p.Provider = provider
	p.Variant = variant
	p.Protocol = string(proto)

	return nil
}

// ValidBootstrap reports whether s is a bare IP literal or an IP:port literal.
func ValidBootstrap(s string) (ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return true
	}
	if _, err := netip.ParseAddrPort(s); err == nil {
		return true
	}

	return false
}

// cloneProfile deep-copies p so mutations never alias the original's slices or
// cache pointer.
func cloneProfile(p Profile) (q Profile) {
	q = p
	if p.Cache != nil {
		cache := *p.Cache
		q.Cache = &cache
	}
	q.Upstreams = slices.Clone(p.Upstreams)
	q.Bootstrap = slices.Clone(p.Bootstrap)
	q.Filtering.Lists = slices.Clone(p.Filtering.Lists)

	if p.PerDomain != nil {
		q.PerDomain = make([]PerDomain, len(p.PerDomain))
		for i, pd := range p.PerDomain {
			q.PerDomain[i] = pd
			q.PerDomain[i].Domains = slices.Clone(pd.Domains)
			if pd.Enabled != nil {
				enabled := *pd.Enabled
				q.PerDomain[i].Enabled = &enabled
			}
		}
	}

	return q
}

// Clone returns a deep copy of c so a caller can snapshot it before mutating.
func (c *Config) Clone() (out *Config) {
	if c == nil {
		return nil
	}

	out = new(Config)
	*out = *c
	out.Profiles = make(map[string]Profile, len(c.Profiles))
	for name, p := range c.Profiles {
		out.Profiles[name] = cloneProfile(p)
	}

	return out
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
	Log         LogConfig

	// FallbackProfile is the configured fallback profile name, if any, with
	// its effective upstreams and bootstrap.
	FallbackProfile   string
	FallbackUpstreams []string
	FallbackBootstrap []string
	FallbackProvider  string
	FallbackProtocol  string
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

	if fb := strings.TrimSpace(p.FallbackProfile); fb != "" {
		fp, ok := cfg.Profiles[fb]
		if !ok {
			return Resolved{}, fmt.Errorf("fallback profile %q not found in config", fb)
		}
		r.FallbackProfile = fb
		r.FallbackUpstreams = fp.Upstreams
		r.FallbackBootstrap = fp.Bootstrap
		r.FallbackProvider = strings.TrimSpace(fp.Provider)
		r.FallbackProtocol = strings.TrimSpace(fp.Protocol)
	}

	logCfg := cfg.Log
	logCfg.normalize()
	r.Log = logCfg

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
