package logging

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpenOffOpensNoFile verifies level off returns a discard logger and never
// creates the file (LOGGING_PLAN §1.5).
func TestOpenOffOpensNoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	l, closer, err := Open(Options{Level: "off", Path: path})
	if err != nil {
		t.Fatalf("Open(off): %v", err)
	}
	if closer != nil {
		t.Fatal("off returned a file closer")
	}

	l.Error("must not create a file")

	if _, statErr := os.Stat(path); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("off created a file: %v", statErr)
	}
}

// TestOpenFailOpenUnwritable verifies an unopenable path falls back to a usable
// logger with an error, never a panic (LOGGING_PLAN §1.5).
func TestOpenFailOpenUnwritable(t *testing.T) {
	// The parent directory does not exist, so OpenFile fails.
	path := filepath.Join(t.TempDir(), "missing", "sigildns.log")

	l, closer, err := Open(Options{Level: "info", Path: path})
	if err == nil {
		t.Fatal("expected an error for an unwritable path")
	}
	if l == nil {
		t.Fatal("fail-open returned a nil logger")
	}
	if closer != nil {
		t.Fatal("fail-open returned a file closer")
	}

	l.Info("still logs to stderr")
}

// TestRotateKeepsNewestLinesOnBoundary verifies rotation keeps the newest
// keep_bytes trimmed to a line boundary (LOGGING_PLAN §1.7).
func TestRotateKeepsNewestLinesOnBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	// Mirrors Open: no O_APPEND, since an append handle cannot be truncated on
	// Windows (H1).
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	w := &rotatingWriter{file: f, path: path, maxBytes: 25, keepBytes: 12}
	for _, line := range []string{"aaaa\n", "bbbb\n", "cccc\n", "dddd\n", "eeee\n", "ffff\n"} {
		if _, err = w.Write([]byte(line)); err != nil {
			t.Fatalf("Write(%q): %v", line, err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if want := "dddd\neeee\nffff\n"; string(got) != want {
		t.Fatalf("rotated content = %q, want %q", got, want)
	}
}

// TestOpenWritesAndAcls verifies a normal open writes through a redacting
// handler without error.
func TestOpenWritesAndAcls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	l, closer, err := Open(Options{Level: "debug", Path: path, Secrets: []string{"SECRETID"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	l.Debug("query https://user:pass@dns.nextdns.io/SECRETID")

	if err = closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	for _, leak := range []string{"SECRETID", "user", "pass"} {
		if strings.Contains(string(got), leak) {
			t.Fatalf("file leaked %q: %s", leak, got)
		}
	}
}

// TestRotateResyncsSizeAndTruncates verifies a rotation re-stats the file
// first (L1: no short, zero-filled ReadAt) and still truncates+rewrites (H1:
// the handle is writable, unlike an O_APPEND handle on Windows).
func TestRotateResyncsSizeAndTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	if _, err = f.WriteString("1111111111\n2222222222\n3333333333\n"); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	// A stale cached size far past EOF would make ReadAt return nothing; the
	// resync must recover the real size instead.
	w := &rotatingWriter{file: f, path: path, maxBytes: 100, keepBytes: 20, size: 1 << 30}
	if err = w.rotate(); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if bytes.ContainsRune(got, 0) {
		t.Fatalf("rotated file contains NUL bytes: %q", got)
	}
	if want := "3333333333\n"; string(got) != want {
		t.Fatalf("rotated content = %q, want %q", got, want)
	}
}

// TestWriteAppendsAtEOF verifies an existing file is appended to, not
// overwritten from offset 0, after the O_APPEND removal.
func TestWriteAppendsAtEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	if err := os.WriteFile(path, []byte("first\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	w := &rotatingWriter{file: f, path: path, maxBytes: 1 << 20, keepBytes: 1, size: int64(len("first\n"))}
	if _, err = w.Write([]byte("second\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err = w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if want := "first\nsecond\n"; string(got) != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

// TestUpdateSecretsReseeds verifies M1: a logger built by Open can be reseeded
// at runtime so a reload's new id is scrubbed.
func TestUpdateSecretsReseeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	l, closer, err := Open(Options{Level: "debug", Path: path, Secrets: []string{"OLDID"}})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	UpdateSecrets(l, []string{"NEWID"})
	l.Debug("https://dns.nextdns.io/NEWID")
	l.Debug("https://dns.nextdns.io/OLDID")

	if err = closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(got), "NEWID") {
		t.Fatalf("updated id leaked: %s", got)
	}
	// The replaced seed is no longer active.
	if !strings.Contains(string(got), "OLDID") {
		t.Fatalf("replaced seed unexpectedly scrubbed: %s", got)
	}
}

// TestOpenExpandsTabsInStringValues verifies dnsproxy's tab-separated message
// dumps are rendered with spaces, not the TextHandler's \t escape, while an
// ordinary value is left unchanged.
func TestOpenExpandsTabsInStringValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	l, closer, err := Open(Options{Level: "debug", Path: path})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	l.Debug("out", "line_num", 8, "line", "api.github.com.\t48\tIN\tA\t140.82.121.6")
	l.Debug("plain", "value", "no-tabs-here")

	if err = closer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	s := string(got)
	if strings.Contains(s, `\t`) || strings.ContainsRune(s, '\t') {
		t.Fatalf("tab not expanded: %q", s)
	}

	want := "api.github.com." + tabReplacement + "48" + tabReplacement + "IN" +
		tabReplacement + "A" + tabReplacement + "140.82.121.6"
	if !strings.Contains(s, want) {
		t.Fatalf("expanded line missing, got: %q", s)
	}
	if !strings.Contains(s, "value=no-tabs-here") {
		t.Fatalf("ordinary value changed: %q", s)
	}
}

// TestUpdateSecretsOffIsNoop verifies a no-op on the discard logger.
func TestUpdateSecretsOffIsNoop(t *testing.T) {
	l, _, err := Open(Options{Level: "off"})
	if err != nil {
		t.Fatalf("Open(off): %v", err)
	}

	UpdateSecrets(l, []string{"NEWID"})
	l.Info("still discarded")
}
