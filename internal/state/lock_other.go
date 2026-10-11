//go:build !windows

package state

import "syscall"

// processAlive reports whether pid is a live process.  Signal 0 performs
// existence and permission checks without delivering a signal.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	err := syscall.Kill(pid, 0)

	return err == nil || err == syscall.EPERM
}
