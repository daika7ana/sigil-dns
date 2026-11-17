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
// Users access, and that the owner was seized by Administrators/SYSTEM.  This
// runs in the windows-latest CI job.
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

		owner, err := readOwner(t, path)
		if err != nil {
			t.Fatalf("%s: reading owner: %v", path, err)
		}
		if !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) &&
			!owner.IsWellKnown(windows.WinLocalSystemSid) {
			t.Errorf("%s: owner is %s, want Administrators/SYSTEM", path, owner)
		}
	}
}

// TestEnableRestorePrivilege asserts the privilege helper round-trips: it must
// enable SeRestorePrivilege (the CI runner is an elevated admin), report it as
// enabled, and restore the previous state.  This catches a broken success check
// that would make every SecureDir/SecureFile fail closed.
func TestEnableRestorePrivilege(t *testing.T) {
	var tok windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &tok); err != nil {
		t.Fatalf("open process token: %v", err)
	}
	defer tok.Close()

	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr("SeRestorePrivilege"), &luid); err != nil {
		t.Fatalf("lookup SeRestorePrivilege: %v", err)
	}
	initial, err := privilegeEnabled(tok, luid)
	if err != nil {
		t.Fatalf("privilegeEnabled (initial): %v", err)
	}

	restore, err := enableRestorePrivilege()
	if err != nil {
		t.Fatalf("enableRestorePrivilege: %v", err)
	}
	if enabled, pErr := privilegeEnabled(tok, luid); pErr != nil || !enabled {
		t.Errorf("SeRestorePrivilege not enabled after enableRestorePrivilege (enabled=%v err=%v)", enabled, pErr)
	}
	restore()
	if enabled, pErr := privilegeEnabled(tok, luid); pErr != nil || enabled != initial {
		t.Errorf("SeRestorePrivilege state not restored (got %v want %v err=%v)", enabled, initial, pErr)
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

// readOwner returns the owner of path.
func readOwner(t *testing.T, path string) (owner *windows.SID, err error) {
	t.Helper()

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err = sd.Owner()

	return owner, err
}
