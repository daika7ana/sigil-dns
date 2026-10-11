package ipc

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/daika7ana/dns-client/internal/config"
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
}

// Summarize builds a sanitized summary from a valid config.  It is shared by the
// service (over IPC) and the CLI (locally).  A nil config yields an empty
// summary.
func Summarize(cfg *config.Config) (sum ConfigSummary) {
	if cfg == nil {
		return sum
	}

	sum.Profile = cfg.Profile

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
