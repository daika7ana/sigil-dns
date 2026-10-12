//go:build gui && windows

package gui

import "golang.org/x/sys/windows"

// kernel32.dll's FreeConsole.  x/sys/windows does not surface FreeConsole, so
// the proc is resolved directly.
var (
	kernel32        = windows.NewLazySystemDLL("kernel32.dll")
	procFreeConsole = kernel32.NewProc("FreeConsole")
)

// detachConsole detaches the process from any inherited console, so launching
// the GUI by double-click closes the blank console window immediately instead
// of leaving a cmd window open for the whole session.  When the process was
// started from an existing terminal, that console belongs to the shell, so
// detaching here does not close the user's terminal.  A process with no console
// gets ERROR_INVALID_HANDLE, which is ignored.
//
// Called only from the GUI path (Run); the CLI commands never detach.
func detachConsole() {
	_, _, _ = procFreeConsole.Call()
}
