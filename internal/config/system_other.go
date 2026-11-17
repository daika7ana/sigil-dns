//go:build !windows

package config

import (
	"os"
	"path/filepath"
)

// SystemDir is the directory of the platform default off Windows: there is no
// ProgramData KnownFolder and no ACL'd admin path.
func SystemDir() (dir string) { return filepath.Dir(defaultPath()) }

// SystemPath is the platform default off Windows: there is no ProgramData
// KnownFolder and no ACL'd admin path.
func SystemPath() (p string) { return defaultPath() }

// defaultPath returns the platform-appropriate config file path.  It is
// non-Windows only: the Windows build resolves ProgramData through the
// KnownFolder API in system_windows.go instead.
func defaultPath() (p string) {
	if dir := os.Getenv("ProgramData"); dir != "" {
		return filepath.Join(dir, "SigilDNS", "config.yaml")
	}

	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "sigildns", "config.yaml")
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "sigildns", "config.yaml")
	}

	return filepath.Join(os.TempDir(), "sigildns", "config.yaml")
}

// SystemStatePath is the platform default state path off Windows.  It keeps the
// historical env-based resolution (the service and CLI shared it before the
// ProgramData hardening).
func SystemStatePath() (p string) {
	if dir := os.Getenv("ProgramData"); dir != "" {
		return filepath.Join(dir, "sigildns", "state.json")
	}

	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "sigildns", "state.json")
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "sigildns", "state.json")
	}

	return filepath.Join(os.TempDir(), "sigildns", "state.json")
}
