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
