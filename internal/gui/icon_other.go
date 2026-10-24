//go:build gui && !windows

package gui

// trayIconPixels is unused outside Windows; the tests that build the tray
// config still get a valid icon.
func trayIconPixels() int { return 32 }

// installWindowIcons is a no-op where go-gui owns the window icon.
func installWindowIcons() {}
