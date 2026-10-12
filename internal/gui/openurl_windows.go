//go:build gui && windows

package gui

import (
	"os/exec"
	"syscall"
)

// openURLPlatform hands an already-validated http/https URL to the Windows
// shell.  rundll32's FileProtocolHandler is the standard shell verb for "open
// this with whatever is registered"; the process is released immediately so a
// slow browser start never blocks the frame thread.  HideWindow keeps a
// console-less GUI process from flashing a console for the helper.
func openURLPlatform(rawURL string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Process.Release()
}
