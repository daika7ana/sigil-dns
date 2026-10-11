//go:build windows

package elevate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestDPAPIRoundTrip(t *testing.T) {
	secret := []byte(`{"action":"apply","uuid":"abc","patch":{"mode":"on"}}`)

	sealed, err := dpapiProtect(secret)
	if err != nil {
		t.Fatalf("dpapiProtect: %v", err)
	}
	if string(sealed) == string(secret) {
		t.Fatal("ciphertext equals plaintext")
	}

	plain, err := dpapiUnprotect(sealed)
	if err != nil {
		t.Fatalf("dpapiUnprotect: %v", err)
	}
	if string(plain) != string(secret) {
		t.Fatalf("round-trip = %q, want %q", plain, secret)
	}
}

// TestStagingSDDL pins the exact SDDL string (the real security invariant),
// which is immune to Windows DACL canonicalisation on a file object.
func TestStagingSDDL(t *testing.T) {
	got := stagingSDDL("S-1-5-21-1-2-3-1001")
	want := "D:P(A;;GA;;;S-1-5-21-1-2-3-1001)(A;;GA;;;SY)(A;;GA;;;BA)"
	if got != want {
		t.Fatalf("stagingSDDL = %q, want %q", got, want)
	}
}

// TestSecureUserOnlyACL asserts the DACL *effect* rather than its exact text:
// Windows rewrites GENERIC_ALL to FILE_ALL_ACCESS (GA -> FA) on a file object
// and abbreviates well-known SIDs, so a literal SID/GSDD comparison is brittle.
func TestSecureUserOnlyACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "req")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := secureUserOnly(path); err != nil {
		t.Fatalf("secureUserOnly: %v", err)
	}

	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}

	sddl := sd.String()
	if !strings.HasPrefix(sddl, "D:P") {
		t.Fatalf("DACL is not protected: %q", sddl)
	}
	// No broad principals.
	for _, bad := range []string{";;;WD)", ";;;AU)", ";;;BU)", ";;;AN)"} {
		if strings.Contains(sddl, bad) {
			t.Fatalf("DACL %q grants a broad principal (%s)", sddl, bad)
		}
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatalf("DACL: %v", err)
	}
	if dacl.AceCount != 3 {
		t.Fatalf("DACL has %d ACEs, want 3 (user, SYSTEM, Administrators): %q", dacl.AceCount, sddl)
	}

	// Effect: the current user (the same identity the elevated helper runs as)
	// must still be able to read and write the staging file.
	h, err := windows.CreateFile(
		windows.StringToUTF16Ptr(path),
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		uint32(windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE),
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("current user cannot open %s for read/write: %v", path, err)
	}
	_ = windows.CloseHandle(h)
}
