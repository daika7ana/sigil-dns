//go:build gui && !windows

package gui

// detachConsole is a no-op off Windows: there is no console window to detach.
// The GUI path still calls it so Run is platform-agnostic.
func detachConsole() {}
