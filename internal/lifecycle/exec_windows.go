//go:build windows

package lifecycle

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

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

// FlushDNS clears the Windows resolver cache.  ipconfig.exe is resolved
// through the system API, never %SystemRoot% or PATH, so an inherited
// environment cannot steer the elevated helper to another executable.
func FlushDNS(ctx context.Context) (err error) {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	ipconfig, pathErr := ipconfigPath()
	if pathErr != nil {
		return pathErr
	}

	cmd := exec.CommandContext(c, ipconfig, "/flushdns")
	hideWindow(cmd)

	if err = cmd.Run(); err != nil {
		return fmt.Errorf("ipconfig /flushdns: %w", err)
	}

	return nil
}
