//go:build gui && windows

package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteTaskXMLStagesInSecuredDir proves the staged XML lands in the given
// directory — the machine-wide data dir stageTaskXML resolves — and not in
// %TEMP%, and that the content is the UTF-16LE task XML.  stageTaskXML itself
// is one call to config.SystemDir(), which cannot be pointed at a temp dir
// without writing to the real ProgramData, so the directory argument carries
// the assertion.
func TestWriteTaskXMLStagesInSecuredDir(t *testing.T) {
	dir := t.TempDir()

	path, err := writeTaskXML(dir, "<Task/>")
	if err != nil {
		t.Fatalf("writeTaskXML: %v", err)
	}
	defer func() { _ = os.Remove(path) }()

	rel, err := filepath.Rel(dir, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("staged path %q is not under %q", path, dir)
	}
	if !strings.HasPrefix(filepath.Base(path), "sigildns-task-") {
		t.Fatalf("staged name %q does not use the task prefix", filepath.Base(path))
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged xml: %v", err)
	}
	if string(got) != string(utf16LE("<Task/>")) {
		t.Fatalf("staged content = %q, want the UTF-16LE task XML", got)
	}
}
