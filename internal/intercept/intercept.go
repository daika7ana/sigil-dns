// Package intercept applies and removes the system DNS interception.
//
// On Windows the primary mechanism is a single NRPT rule covering all
// namespaces; a per-adapter loopback fallback is also implemented but not wired
// into the default path.  On other platforms a stub returns ErrUnsupported so
// Linux builds and tests compile.
package intercept

import (
	"context"
	"errors"
)

// DisplayName tags our NRPT rule so it can be found and removed exactly.
const DisplayName = "dns-client"

// ErrUnsupported is returned by the non-Windows stub.
var ErrUnsupported = errors.New("dns interception is not supported on this platform")

// Status describes the current interception state.
type Status struct {
	Mechanism string
	Detail    string
	Enabled   bool
}

// Controller applies, removes, and reports the DNS interception.
type Controller interface {
	// Enable turns interception on.  It must be idempotent.
	Enable(ctx context.Context) error

	// Disable removes interception.  It must be idempotent.
	Disable(ctx context.Context) error

	// Status reports whether interception is currently active.
	Status(ctx context.Context) (Status, error)
}

// AdapterEntry is a per-interface DNS configuration.  It is exported so drift
// detection can reuse the snapshot/apply/reset helpers.
type AdapterEntry struct {
	// IPv4 and IPv6 are the configured DNS servers per family.
	IPv4 []string
	IPv6 []string

	// Index is the interface index.
	Index int
}

// NRPTState is the observed NRPT state: whether our rule is present and the
// foreign rules that may conflict.
type NRPTState struct {
	// Conflicts are foreign equal-or-more-specific rules (DisplayName|Namespace).
	Conflicts []string

	// Ours is true when our own rule is present.
	Ours bool
}
