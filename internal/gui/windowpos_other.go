//go:build gui && !windows

package gui

import gogui "github.com/go-gui-org/go-gui/gui"

// Window-position persistence is a Windows-only best-effort: the GUI toolkit
// exposes no position API, so the off-Windows build (used by the headless smoke
// test) simply has nothing to move or read.

func restoreWindowPosition(string, int, int) {}

func windowPosition(string) (x, y int, ok bool) { return 0, 0, false }

func windowMinimized(string) bool { return false }

func windowVisible(string) bool { return false }

func windowZoomed(string) bool { return false }

func maximizeWindow(string) {}

func focusWindow(string) {}

func focusExistingWindow() {}

// ensureFocus is a no-op off Windows: nothing to activate or focus.
func ensureFocus(*gogui.Window) {}
