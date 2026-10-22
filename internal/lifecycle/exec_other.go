//go:build !windows

package lifecycle

import (
	"errors"
	"os/exec"
)

// hideWindow is a no-op off Windows.
func hideWindow(*exec.Cmd) {}

// ipconfigPath has no meaning off Windows: FlushDNS returns before using it.
func ipconfigPath() (string, error) {
	return "", errors.New("the DNS cache flush is Windows-only")
}
