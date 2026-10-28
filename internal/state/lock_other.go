//go:build !windows

package state

import (
	"errors"
	"os"
	"syscall"
)

// lockFile takes a non-blocking exclusive flock on f.  flock is tied to the
// open file description, so a second process (or a second open in this
// process) that tries to lock the same file fails.
func lockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return errLockHeld
	}

	return err
}

// unlockFile releases the flock; closing the handle also releases it.
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
