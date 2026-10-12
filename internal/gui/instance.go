//go:build gui

package gui

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/daika7ana/dns-client/internal/state"
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
	l, err := state.AcquireLock(guiLockPath(guiPath))
	if errors.Is(err, state.ErrLocked) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gui single-instance lock: %w", err)
	}

	return l, nil
}
