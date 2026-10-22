//go:build gui && windows

package gui

import (
	"os/exec"
	"syscall"
)

// openShell hands target to the shell's FileProtocolHandler, the standard verb
// for "open this with whatever is registered"; the process is released
// immediately so a slow browser start never blocks the frame thread.  HideWindow
// keeps a console-less GUI process from flashing a console for the helper.
func openShell(target string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Process.Release()
}

// openURLPlatform hands an already-validated http/https URL to the Windows shell.
func openURLPlatform(rawURL string) error {
	return openShell(rawURL)
}

// openPathPlatform opens an already-validated local file with the Windows shell.
// The path is passed as one argv element, never a shell string.
func openPathPlatform(path string) error {
	return openShell(path)
}
