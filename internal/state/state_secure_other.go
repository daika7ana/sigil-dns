//go:build !windows

package state

// secure is a no-op off Windows.
func secure(path string) (err error) { return nil }
