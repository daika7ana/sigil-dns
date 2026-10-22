//go:build gui && !windows

package gui

import "errors"

// setStartWithWindows is Windows-only; the tray logon task has no meaning here.
// Removing it succeeds trivially: there is nothing to remove.
func setStartWithWindows(on bool) error {
	if !on {
		return nil
	}

	return errors.New("start with Windows is only supported on Windows")
}
