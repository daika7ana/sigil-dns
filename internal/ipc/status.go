package ipc

import "time"

// HealthSummary is the sanitized health view exposed on the status pipe.
type HealthSummary struct {
	Detail string `json:"detail,omitempty"`
	Ours   bool   `json:"ours"`
	IPv4   bool   `json:"ipv4"`
	IPv6   bool   `json:"ipv6"`
}

// RuntimeStats is the sanitized live-run view: counters since the current
// resolver started (a config reload builds a new one) plus the filtering pause
// expiry.  It carries no query names or upstream data.
type RuntimeStats struct {
	// PausedUntil is the pause expiry; absent when filtering is not paused.
	PausedUntil *time.Time `json:"paused_until,omitempty"`

	Queries uint64 `json:"queries"`
	Blocked uint64 `json:"blocked"`
}

// LeakSummary is the last plain-DNS leak probe.  Servers are the adapters'
// configured DNS addresses that answered a direct query: they are ordinary
// interface configuration (already visible to any local user via ipconfig), not
// resolver ids, so naming them here breaks no sanitization rule.
type LeakSummary struct {
	// CheckedAt is when the probe last ran; absent when it never has.
	CheckedAt *time.Time `json:"checked_at,omitempty"`

	// Servers are the configured, non-loopback servers that answered directly.
	Servers []string `json:"servers,omitempty"`

	// Checked is false when the probe could not run (e.g. enumeration failed).
	Checked bool `json:"checked"`

	// Open is true when at least one configured server answered a direct
	// query, so an app that ignores the system resolver can resolve outside
	// Sigil DNS.
	Open bool `json:"open"`
}

// StatusDTO is the sanitized status returned on the read-only status pipe.  It
// deliberately omits active_upstream (which may contain a NextDNS/ControlD
// resolver id) and per-interface GUIDs, so an unelevated reader learns nothing
// sensitive.
type StatusDTO struct {
	Health        *HealthSummary `json:"last_health,omitempty"`
	Stats         *RuntimeStats  `json:"stats,omitempty"`
	Leak          *LeakSummary   `json:"leak,omitempty"`
	Mode          string         `json:"mode"`
	Mechanism     string         `json:"mechanism"`
	SchemaVersion int            `json:"schema_version"`
	Enabled       bool           `json:"enabled"`
	Dirty         bool           `json:"dirty"`
}
