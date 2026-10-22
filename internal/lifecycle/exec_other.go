//go:build !windows

package lifecycle

import "context"

// FlushDNS is a no-op off Windows.
func FlushDNS(context.Context) error { return nil }
