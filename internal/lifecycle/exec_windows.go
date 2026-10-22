//go:build windows

package lifecycle

import (
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindow keeps a console-less service/GUI process from flashing a console
// window for a spawned child.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}

// ipconfigPath is the system ipconfig.exe.  It comes from GetSystemDirectory,
// not %SystemRoot% or PATH: an inherited environment must not be able to
// steer an elevated helper to a different executable.
func ipconfigPath() (string, error) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "ipconfig.exe"), nil
}
