//go:build gui && !windows

package gui

// The tray host is Windows-only.  The off-Windows GUI build is the headless
// smoke test, which never starts one.

// RunTrayHost is not available off Windows.
func RunTrayHost() error { return ErrNotBuilt }

func startTrayHost() error { return ErrNotBuilt }

func stopTrayHost() {}
