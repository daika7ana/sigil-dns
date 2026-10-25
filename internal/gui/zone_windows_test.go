//go:build gui && windows

package gui

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMarkDownloadedAddsZoneIdentifier proves the saved zip gets the Internet
// zone tag that SmartScreen and PowerShell read.
func TestMarkDownloadedAddsZoneIdentifier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns_v1.2.3_windows_amd64.zip")
	if err := os.WriteFile(path, []byte("PK"), 0o644); err != nil {
		t.Fatal(err)
	}

	markDownloaded(path)

	got, err := os.ReadFile(path + ":Zone.Identifier")
	if err != nil {
		t.Fatalf("read zone identifier: %v", err)
	}
	if !strings.Contains(string(got), "ZoneId=3") {
		t.Errorf("zone identifier = %q, want ZoneId=3", got)
	}
}

// TestDownloadUpdateWritesZoneIdentifier proves the real download path tags
// the saved zip, not just the helper in isolation.
func TestDownloadUpdateWritesZoneIdentifier(t *testing.T) {
	home := t.TempDir()
	useTempDownloads(t, home)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "PK")
	}))
	defer srv.Close()

	asset := releaseAsset{Name: "sigildns_v1.2.3_windows_amd64.zip", URL: srv.URL}
	path, err := downloadUpdateDefault(context.Background(), asset, checksumServer(t, asset.Name, []byte("PK")))
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if want := filepath.Join(home, "Downloads", asset.Name); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}

	got, err := os.ReadFile(path + ":Zone.Identifier")
	if err != nil {
		t.Fatalf("read zone identifier: %v", err)
	}
	if !strings.Contains(string(got), "ZoneId=3") {
		t.Errorf("zone identifier = %q, want ZoneId=3", got)
	}
}
