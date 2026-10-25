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

// StatusDTO is the sanitized status returned on the read-only status pipe.  It
// deliberately omits active_upstream (which may contain a NextDNS/ControlD
// resolver id) and per-interface GUIDs, so an unelevated reader learns nothing
// sensitive.
type StatusDTO struct {
	Health        *HealthSummary `json:"last_health,omitempty"`
	Stats         *RuntimeStats  `json:"stats,omitempty"`
	Mode          string         `json:"mode"`
	Mechanism     string         `json:"mechanism"`
	SchemaVersion int            `json:"schema_version"`
	Enabled       bool           `json:"enabled"`
	Dirty         bool           `json:"dirty"`
}
