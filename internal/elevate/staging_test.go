package elevate

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestValidateStagingRequest pins the --apply containment rule: only a
// "<name>.req" file directly under the staging dir is accepted.
func TestValidateStagingRequest(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "staging")
	other := filepath.Join(base, "other")

	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"inside", filepath.Join(dir, "abc.req"), false},
		{"outside", filepath.Join(other, "abc.req"), true},
		{"traversal", filepath.Join(dir, "..", "abc.req"), true},
		{"wrong extension", filepath.Join(dir, "abc.txt"), true},
		{"subdirectory", filepath.Join(dir, "sub", "abc.req"), true},
		{"empty base", filepath.Join(dir, ".req"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStagingRequest(tc.path, dir)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateStagingRequest(%q) = %v, wantErr %v", tc.path, err, tc.wantErr)
			}
		})
	}
}

// TestReadStagingRequestContainment proves a rejected path is never read or
// deleted, and that an accepted request is consumed (read + removed).
func TestReadStagingRequestContainment(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "staging")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	outside := filepath.Join(base, "outside.req")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := readStagingRequest(outside, dir); err == nil {
		t.Fatal("path outside the staging dir was accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("rejected path was deleted: %v", err)
	}

	inside := filepath.Join(dir, "abc.req")
	if err := os.WriteFile(inside, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	data, err := readStagingRequest(inside, dir)
	if err != nil {
		t.Fatalf("readStagingRequest: %v", err)
	}
	if string(data) != "payload" {
		t.Fatalf("data = %q, want %q", data, "payload")
	}
	if _, err := os.Stat(inside); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("accepted request was not removed: %v", err)
	}
}
