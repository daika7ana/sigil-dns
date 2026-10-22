package service

import (
	"path/filepath"
	"testing"
)

// TestCanonicalServiceExe pins the two-binary (sigildns.exe +
// sigildns-gui.exe) resolution used for install and the anti-hijack check.
func TestCanonicalServiceExe(t *testing.T) {
	dir := filepath.Join("C:", "app")
	gui := filepath.Join(dir, "sigildns-gui.exe")
	plain := filepath.Join(dir, "sigildns.exe")

	exists := func(string) bool { return true }
	missing := func(string) bool { return false }

	if got := canonicalServiceExe(gui, exists); got != plain {
		t.Fatalf("gui with sibling = %q, want %q", got, plain)
	}
	if got := canonicalServiceExe(gui, missing); got != gui {
		t.Fatalf("gui without sibling = %q, want %q", got, gui)
	}
	if got := canonicalServiceExe(plain, exists); got != plain {
		t.Fatalf("plain = %q, want %q", got, plain)
	}
}

// TestBinaryMatches pins the anti-hijack registration check.
func TestBinaryMatches(t *testing.T) {
	exe := `C:\Program Files\sigildns\sigildns.exe`
	plain := `C:\sigildns\sigildns.exe`

	cases := []struct {
		name    string
		binPath string
		exe     string
		want    bool
	}{
		{"quoted with arg", `"` + exe + `" --service`, exe, true},
		{"unquoted with arg", plain + ` --service`, plain, true},
		{"wrong binary", `"C:\evil\evil.exe" --service`, exe, false},
		{"missing arg", `"` + exe + `"`, exe, false},
		{"extra arg", `"` + exe + `" --service --evil`, exe, false},
		{"empty", ``, exe, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := binaryMatches(tc.binPath, tc.exe); got != tc.want {
				t.Fatalf("binaryMatches(%q) = %v, want %v", tc.binPath, got, tc.want)
			}
		})
	}
}
