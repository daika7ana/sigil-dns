package ipc

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/providers"
	"github.com/daika7ana/dns-client/internal/redact"
)

// Info is the version/capability payload returned by the info method.
type Info struct {
	// AppVersion is the application version (see internal/version).
	AppVersion string `json:"app_version"`

	// ProtocolVersion is the IPC protocol version.
	ProtocolVersion int `json:"protocol_version"`

	// Capabilities is the bit set of supported methods.
	Capabilities uint64 `json:"capabilities"`
}

// ListSummary is one filter list, reduced to its host.
type ListSummary struct {
	// Host is the list URL's scheme+host (never the full path).
	Host string `json:"host"`

	// Enabled reports whether the list is enabled.
	Enabled bool `json:"enabled"`
}

// ProfileSummary is the sanitized per-profile view.  It never contains resolver
// ids, upstream URLs, or full list URLs; IDSet is a boolean only.
type ProfileSummary struct {
	// Name is the profile name and Active marks the selected profile.
	Name   string `json:"name"`
	Active bool   `json:"active"`

	// Provider, Variant, Protocol are the profile's display-only labels.
	Provider string `json:"provider,omitempty"`
	Variant  string `json:"variant,omitempty"`
	Protocol string `json:"protocol,omitempty"`

	// IDSet reports whether a resolver id appears to be configured.  The id
	// itself is never exposed.
	IDSet bool `json:"id_set"`

	// FilteringEnabled reports whether filtering is enabled.
	FilteringEnabled bool `json:"filtering_enabled"`

	// CacheEnabled reports whether the cache is enabled.
	CacheEnabled bool `json:"cache_enabled"`

	// CacheSizeBytes is the configured cache size.
	CacheSizeBytes int `json:"cache_size_bytes"`

	// BlockResponse is the configured block response (e.g. "0.0.0.0").
	BlockResponse string `json:"block_response,omitempty"`

	// ListCount is the number of filter lists.
	ListCount int `json:"list_count"`

	// PerDomainCount is the number of per-domain routing rules.
	PerDomainCount int `json:"per_domain_count"`

	// Bootstrap is the list of bootstrap IP literals.
	Bootstrap []string `json:"bootstrap,omitempty"`
}

// ConfigSummary is the sanitized configuration view (SPEC §15.1).  It never
// contains resolver ids, upstream URLs, credentials, or full list URLs.
type ConfigSummary struct {
	// Provider, Variant, Protocol are the profile's display-only labels.
	Provider string `json:"provider,omitempty"`
	Variant  string `json:"variant,omitempty"`
	Protocol string `json:"protocol,omitempty"`

	// Profile is the active profile name.
	Profile string `json:"profile"`

	// ConfigError is a secret-free error string when the YAML is unusable.
	ConfigError string `json:"config_error,omitempty"`

	// Revision is an opaque token (config file mtime + size) for conflict
	// detection.  It is not a content hash: ids are low-entropy.
	Revision string `json:"revision,omitempty"`

	// BlockResponse is the configured block response (e.g. "0.0.0.0").
	BlockResponse string `json:"block_response,omitempty"`

	// Bootstrap is the list of bootstrap IP literals.
	Bootstrap []string `json:"bootstrap,omitempty"`

	// Lists is the per-list (host only) summary.
	Lists []ListSummary `json:"lists,omitempty"`

	// PerDomainCount is the number of per-domain routing rules.
	PerDomainCount int `json:"per_domain_count"`

	// ListCount is the number of filter lists.
	ListCount int `json:"list_count"`

	// CacheSizeBytes is the configured cache size.
	CacheSizeBytes int `json:"cache_size_bytes"`

	// BlockTTL is the blocked-response TTL.
	BlockTTL uint32 `json:"block_ttl,omitempty"`

	// IDSet reports whether a resolver id appears to be configured.  It is a
	// boolean only; the id itself is never exposed.
	IDSet bool `json:"id_set"`

	// ConfigValid reports whether the config loaded and validated.
	ConfigValid bool `json:"config_valid"`

	// CacheEnabled reports whether the cache is enabled.
	CacheEnabled bool `json:"cache_enabled"`

	// FilteringEnabled reports whether filtering is enabled.
	FilteringEnabled bool `json:"filtering_enabled"`

	// Profiles is the sanitized per-profile list, sorted by name.
	Profiles []ProfileSummary `json:"profiles,omitempty"`
}

// Summarize builds a sanitized summary from a valid config.  It is shared by the
// service (over IPC) and the CLI (locally).  A nil config yields an empty
// summary.
func Summarize(cfg *config.Config) (sum ConfigSummary) {
	if cfg == nil {
		return sum
	}

	sum.Profile = cfg.Profile
	sum.Profiles = summarizeProfiles(cfg)

	p, ok := cfg.Profiles[cfg.Profile]
	if !ok {
		return sum
	}

	sum.Provider = p.Provider
	sum.Variant = p.Variant
	sum.Protocol = p.Protocol

	for _, b := range p.Bootstrap {
		if b = strings.TrimSpace(b); b != "" {
			sum.Bootstrap = append(sum.Bootstrap, redact.Upstream(b))
		}
	}

	sum.PerDomainCount = len(p.PerDomain)
	sum.ListCount = len(p.Filtering.Lists)
	sum.FilteringEnabled = p.Filtering.Enabled
	sum.BlockResponse = p.Filtering.BlockResponse
	sum.BlockTTL = p.Filtering.BlockTTL

	if p.Cache != nil {
		sum.CacheEnabled = p.Cache.Enabled
		sum.CacheSizeBytes = p.Cache.SizeBytes
	}

	for _, l := range p.Filtering.Lists {
		sum.Lists = append(sum.Lists, ListSummary{
			Host:    redact.Upstream(l.URL),
			Enabled: l.Enabled,
		})
	}

	sum.IDSet = anyIDSet(p.Upstreams)

	return sum
}

// summarizeProfiles builds the sanitized per-profile list, sorted by name so
// the output is deterministic.
func summarizeProfiles(cfg *config.Config) (out []ProfileSummary) {
	out = make([]ProfileSummary, 0, len(cfg.Profiles))
	for name, p := range cfg.Profiles {
		out = append(out, summarizeProfile(name, name == cfg.Profile, p))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// summarizeProfile reduces one profile to its non-secret fields.
func summarizeProfile(name string, active bool, p config.Profile) (s ProfileSummary) {
	s = ProfileSummary{
		Name:             name,
		Active:           active,
		Provider:         p.Provider,
		Variant:          p.Variant,
		Protocol:         p.Protocol,
		FilteringEnabled: p.Filtering.Enabled,
		BlockResponse:    p.Filtering.BlockResponse,
		ListCount:        len(p.Filtering.Lists),
		PerDomainCount:   len(p.PerDomain),
	}

	if p.Cache != nil {
		s.CacheEnabled = p.Cache.Enabled
		s.CacheSizeBytes = p.Cache.SizeBytes
	}

	for _, b := range p.Bootstrap {
		if b = strings.TrimSpace(b); b != "" {
			s.Bootstrap = append(s.Bootstrap, redact.Upstream(b))
		}
	}

	s.IDSet = profileIDSet(p)

	return s
}

// profileIDSet is the provider-aware id detector: when provider metadata is
// present it trusts providers.ExtractID (so a ControlD free preset, whose path
// is a shared name, is not mistaken for a private id); otherwise it falls back
// to the path heuristic.  It never returns or logs the id.
func profileIDSet(p config.Profile) (ok bool) {
	if strings.TrimSpace(p.Provider) == "" {
		return anyIDSet(p.Upstreams)
	}

	proto := providers.DoH
	if pr := strings.TrimSpace(p.Protocol); pr != "" {
		proto = providers.Protocol(pr)
	}

	_, err := providers.ExtractID(p.Provider, proto, p.Upstreams)

	return err == nil
}

// anyIDSet reports whether any upstream looks like it carries a resolver id (a
// non-standard DoH path).  It never returns the id.
func anyIDSet(upstreams []string) (ok bool) {
	for _, u := range upstreams {
		if upstreamHasPathID(u) {
			return true
		}
	}

	return false
}

// upstreamHasPathID reports whether u has a DoH path beyond the well-known
// /dns-query (NextDNS/ControlD put the id there).
func upstreamHasPathID(u string) (ok bool) {
	if !strings.Contains(u, "://") {
		return false
	}

	parsed, err := url.Parse(u)
	if err != nil {
		return false
	}

	path := strings.Trim(parsed.Path, "/")
	if path == "" {
		return false
	}

	return !strings.EqualFold(path, "dns-query")
}

// Revision returns an opaque revision token for path: config file mtime + size.
// It returns "" when the file is absent.  Deliberately not a content hash.
func Revision(path string) (rev string) {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}

	return fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size())
}

// SanitizedConfigError converts a config load/validate error into a secret-free
// string.  Raw yaml errors can quote file contents, so they are never returned.
func SanitizedConfigError(err error) (msg string) {
	if errors.Is(err, config.ErrNoConfig) {
		return "config file not found"
	}

	return "config could not be loaded or validated"
}
