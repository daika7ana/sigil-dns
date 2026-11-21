package winacl

import (
	"errors"
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

// TestRefuseHardlink accepts a single-link file and rejects a planted hardlink.
func TestRefuseHardlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	if err = RefuseHardlink(f); err != nil {
		t.Fatalf("RefuseHardlink(single link): %v", err)
	}
	_ = f.Close()

	link := filepath.Join(dir, "link.txt")
	if err := os.Link(target, link); err != nil {
		t.Skipf("hardlinks unsupported: %v", err)
	}
	if f, err = os.Open(link); err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err = RefuseHardlink(f); !errors.Is(err, ErrHardlink) {
		t.Fatalf("RefuseHardlink(link) = %v, want ErrHardlink", err)
	}
}
