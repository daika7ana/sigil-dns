package winacl

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSecureNoOpOrReal runs on every platform.  On Windows it exercises the
// real SetSecurityInfo path; elsewhere it asserts the no-op path returns nil.
func TestSecureNoOpOrReal(t *testing.T) {
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
}
