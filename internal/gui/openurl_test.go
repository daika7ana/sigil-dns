//go:build gui

package gui

import "testing"

// TestValidatePathRejectsDevicePaths proves the open-path guard mirrors
// config.ValidateLogPath's device-namespace refusal while keeping real local
// paths valid.
func TestValidatePathRejectsDevicePaths(t *testing.T) {
	for _, p := range []string{
		`\\.\PhysicalDrive0`,
		`\\?\C:\Windows\System32\config`,
	} {
		if _, err := validatePath(p); err == nil {
			t.Errorf("validatePath(%q) = nil, want an error for a device-namespace path", p)
		}
	}

	for _, p := range []string{
		`C:\ProgramData\sigildns\sigildns.log`,
		`\\server\share\sigildns.log`,
	} {
		if _, err := validatePath(p); err != nil {
			t.Errorf("validatePath(%q) = %v, want accepted", p, err)
		}
	}
}
