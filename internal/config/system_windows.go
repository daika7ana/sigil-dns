//go:build windows

package config

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// SystemDir returns the fixed, machine-wide data directory derived from the
// ProgramData KnownFolder.  Unlike DefaultPath it never trusts the ProgramData
// environment variable and never falls back to a user-writable directory.  It
// returns "" when the KnownFolder cannot be resolved.
func SystemDir() (dir string) {
	root, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil || root == "" {
		return ""
	}

	return filepath.Join(root, "dns-client")
}

// SystemPath returns the fixed, machine-wide config path derived from the
// ProgramData KnownFolder.  It returns "" when the KnownFolder cannot be
// resolved.
func SystemPath() (p string) {
	dir := SystemDir()
	if dir == "" {
		return ""
	}

	return filepath.Join(dir, "config.yaml")
}

// SystemStatePath returns the fixed, machine-wide state path next to the config
// under the ProgramData KnownFolder.  It returns "" when the KnownFolder cannot
// be resolved.  The service and the elevated helper must resolve the same path
// or the state-lock exclusion is defeated.
func SystemStatePath() (p string) {
	dir := SystemDir()
	if dir == "" {
		return ""
	}

	return filepath.Join(dir, "state.json")
}
