//go:build gui && !windows

package gui

// Window-position persistence is a Windows-only best-effort: the GUI toolkit
// exposes no position API, so the off-Windows build (used by the headless smoke
// test) simply has nothing to move or read.

func restoreWindowPosition(string, int, int) {}

func windowPosition(string) (x, y int, ok bool) { return 0, 0, false }

func focusWindow(string) {}
