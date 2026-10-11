package ipc

// HealthSummary is the sanitized health view exposed on the status pipe.
type HealthSummary struct {
	Detail string `json:"detail,omitempty"`
	Ours   bool   `json:"ours"`
	IPv4   bool   `json:"ipv4"`
	IPv6   bool   `json:"ipv6"`
}

// StatusDTO is the sanitized status returned on the read-only status pipe.  It
// deliberately omits active_upstream (which may contain a NextDNS/ControlD
// resolver id) and per-interface GUIDs, so an unelevated reader learns nothing
// sensitive.
type StatusDTO struct {
	Health        *HealthSummary `json:"last_health,omitempty"`
	Mode          string         `json:"mode"`
	Mechanism     string         `json:"mechanism"`
	SchemaVersion int            `json:"schema_version"`
	Enabled       bool           `json:"enabled"`
	Dirty         bool           `json:"dirty"`
}
