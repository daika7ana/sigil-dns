//go:build windows

package state

import (
	"os"
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

// TestAcquireLockSecureCreatesDir asserts the service state lock creates and
// secures its directory: the service takes the lock before any config save has
// created %ProgramData%\SigilDNS, and ERROR_PATH_NOT_FOUND used to abort start.
func TestAcquireLockSecureCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "state")
	lockPath := filepath.Join(dir, "state.json.lock")

	l, err := AcquireLockSecure(lockPath)
	if err != nil {
		t.Fatalf("AcquireLockSecure: %v", err)
	}
	defer func() { _ = l.Release() }()

	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("state dir not created: %v", err)
	}
}
