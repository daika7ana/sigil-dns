//go:build windows

package state

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestAcquireLockSecureAppliesAdminACL: the service lock is the one state file
// that is not written through winacl.WriteFileAtomic, so it must apply the same
// SYSTEM + Administrators DACL itself.
func TestAcquireLockSecureAppliesAdminACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json.lock")

	l, err := AcquireLockSecure(path)
	if err != nil {
		t.Fatalf("AcquireLockSecure: %v", err)
	}
	defer func() { _ = l.Release() }()

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	sddl := sd.String()

	for _, want := range []string{";SY)", ";BA)"} {
		if !strings.Contains(sddl, want) {
			t.Errorf("lock DACL missing %s: %s", want, sddl)
		}
	}
}
