//go:build !windows

package lifecycle

import "os/exec"

// hideWindow is a no-op off Windows.
func hideWindow(*exec.Cmd) {}
