package service

import (
	"path/filepath"
	"testing"
)

// TestCanonicalServiceExe pins the two-binary (dns-client.exe +
// dns-client-gui.exe) resolution used for install and the anti-hijack check.
func TestCanonicalServiceExe(t *testing.T) {
	dir := filepath.Join("C:", "app")
	gui := filepath.Join(dir, "dns-client-gui.exe")
	plain := filepath.Join(dir, "dns-client.exe")

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
