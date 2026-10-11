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
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("GetTokenUser: %v", err)
	}
	if !strings.Contains(sddl, user.User.Sid.String()) {
		t.Fatalf("DACL %q does not grant the current user", sddl)
	}
	if strings.Contains(sddl, ";;;WD)") || strings.Contains(sddl, ";;;AU)") {
		t.Fatalf("DACL %q grants other users", sddl)
	}
}
