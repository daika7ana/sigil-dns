//go:build gui

package gui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/daika7ana/sigil-dns/internal/state"
)

// guiLockPath is the user-scope single-instance lock, kept beside gui.json so
// it never depends on ProgramData.  Derived from the gui.json path.
func guiLockPath(guiPath string) (lock string) {
	return filepath.Join(filepath.Dir(guiPath), "gui.lock")
}

// acquireInstance takes the GUI single-instance lock.  A (nil, nil) result
// means another instance already holds it, so Run exits quietly without opening
// a second window; a non-nil lock must be released by the caller.  Any other
// failure is returned.  Only the GUI path calls this — the CLI commands never
// take or check this lock.
func acquireInstance(guiPath string) (lock *state.Lock, err error) {
	// The lock file is opened with O_CREATE, which will not create the
	// directory.  saveSettings creates it too, but only after the window is
	// open, so a first launch has to create it here.
	if err = os.MkdirAll(filepath.Dir(guiPath), 0o700); err != nil {
		return nil, fmt.Errorf("gui state dir: %w", err)
	}

	l, err := state.AcquireLock(guiLockPath(guiPath))
	if errors.Is(err, state.ErrLocked) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gui single-instance lock: %w", err)
	}

	return l, nil
}
