//go:build gui && !windows

package gui

import (
	"os"
	"path/filepath"
)

// downloadsFolderDefault is the home-relative folder off Windows, where the
// known-folder API does not exist.
func downloadsFolderDefault() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", err
	}

	return filepath.Join(home, "Downloads"), nil
}
