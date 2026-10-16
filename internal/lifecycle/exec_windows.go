//go:build windows

package lifecycle

import (
	"os/exec"
	"syscall"
)

// hideWindow keeps a console-less service/GUI process from flashing a console
// window for a spawned child.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
}
