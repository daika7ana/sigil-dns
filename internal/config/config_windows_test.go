//go:build windows

package config

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestSaveAndInitACLWindows asserts config.Save/Init ACL the file and its
// directory to SYSTEM + Administrators (the config holds resolver ids).
func TestSaveAndInitACLWindows(t *testing.T) {
	dir := t.TempDir()

	saved := filepath.Join(dir, "config.yaml")
	if err := Default().Save(saved); err != nil {
		t.Fatalf("Save: %v", err)
	}
	assertConfigACL(t, saved)
	assertConfigACL(t, dir)

	inited := filepath.Join(dir, "sub", "config.yaml")
	if err := Init(inited); err != nil {
		t.Fatalf("Init: %v", err)
	}
	assertConfigACL(t, inited)
	assertConfigACL(t, filepath.Dir(inited))
}

// assertConfigACL checks path's DACL is SYSTEM + Administrators only.
func assertConfigACL(t *testing.T, path string) {
	t.Helper()

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo(%s): %v", path, err)
	}
	sddl := sd.String()

	if !strings.Contains(sddl, ";SY)") || !strings.Contains(sddl, ";BA)") {
		t.Errorf("%s: DACL missing SYSTEM/Administrators: %s", path, sddl)
	}
	for _, bad := range []string{";AU)", ";WD)", ";BU)"} {
		if strings.Contains(sddl, bad) {
			t.Errorf("%s: DACL grants %s: %s", path, bad, sddl)
		}
	}
}
