//go:build windows

package winacl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestSecureWindowsDACL asserts the DACL on both a secured directory and file is
// SYSTEM + Administrators only, with no Authenticated Users/Everyone/Builtin
// Users access.  This runs in the windows-latest CI job.
func TestSecureWindowsDACL(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SecureDir(dir); err != nil {
		t.Fatalf("SecureDir: %v", err)
	}
	if err := SecureFile(file); err != nil {
		t.Fatalf("SecureFile: %v", err)
	}

	for _, path := range []string{dir, file} {
		sddl := readSDDL(t, path)

		if !strings.Contains(sddl, ";SY)") {
			t.Errorf("%s: DACL missing SYSTEM ACE: %s", path, sddl)
		}
		if !strings.Contains(sddl, ";BA)") {
			t.Errorf("%s: DACL missing Administrators ACE: %s", path, sddl)
		}
		for _, bad := range []string{";AU)", ";WD)", ";BU)"} {
			if strings.Contains(sddl, bad) {
				t.Errorf("%s: DACL grants access to %s: %s", path, bad, sddl)
			}
		}
	}
}

// readSDDL returns the SDDL of path's security descriptor.
func readSDDL(t *testing.T, path string) (sddl string) {
	t.Helper()

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo(%s): %v", path, err)
	}

	return sd.String()
}
