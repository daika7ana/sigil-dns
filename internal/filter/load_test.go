package filter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAllFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts.txt")
	if err := os.WriteFile(path, []byte("0.0.0.0 blocked.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	raw, err := LoadAll(context.Background(), []List{{Source: path}})
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(raw) != 1 || !strings.Contains(string(raw[0].Text), "blocked.test") {
		t.Fatalf("raw = %+v", raw)
	}

	// Same contents twice: deduplicated.
	raw, err = LoadAll(context.Background(), []List{{Source: path}, {Source: path}})
	if err != nil {
		t.Fatalf("LoadAll dup: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("expected duplicate list deduped, got %d", len(raw))
	}
}

func TestLoadAllHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("||remote.test^\n"))
	}))
	t.Cleanup(srv.Close)

	raw, err := LoadAll(context.Background(), []List{{Source: srv.URL}})
	if err != nil {
		t.Fatalf("LoadAll http: %v", err)
	}
	if len(raw) != 1 || !strings.Contains(string(raw[0].Text), "remote.test") {
		t.Fatalf("raw = %+v", raw)
	}
}

func TestLoadRejectsNonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	_, err := LoadAll(context.Background(), []List{{Source: srv.URL}})
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
	if strings.Contains(err.Error(), srv.URL) {
		t.Errorf("error leaked the source: %v", err)
	}
}

func TestLoadRejectsHTML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.txt")
	if err := os.WriteFile(path, []byte("<!DOCTYPE html>\n<html><body>oops</body></html>\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadAll(context.Background(), []List{{Source: path}}); !errors.Is(err, ErrHTML) {
		t.Fatalf("err = %v, want ErrHTML", err)
	} else if strings.Contains(err.Error(), path) {
		t.Errorf("error leaked the source: %v", err)
	}
}

func TestLoadRejectsBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadAll(context.Background(), []List{{Source: path}}); !errors.Is(err, ErrBinary) {
		t.Fatalf("err = %v, want ErrBinary", err)
	} else if strings.Contains(err.Error(), path) {
		t.Errorf("error leaked the source: %v", err)
	}
}

func TestReadCapped(t *testing.T) {
	if _, err := readCapped(strings.NewReader("0123456789"), 10); err != nil {
		t.Fatalf("exactly at cap: %v", err)
	}
	if _, err := readCapped(strings.NewReader("01234567890"), 10); !errors.Is(err, ErrTooBig) {
		t.Fatalf("over cap: err = %v, want ErrTooBig", err)
	}
}

func TestValidate(t *testing.T) {
	if err := validate([]byte("||ads.test^\n")); err != nil {
		t.Errorf("normal list rejected: %v", err)
	}
	if err := validate([]byte("   <html>\n")); !errors.Is(err, ErrHTML) {
		t.Errorf("html: err = %v, want ErrHTML", err)
	}
	if err := validate([]byte("ab\x00cd")); !errors.Is(err, ErrBinary) {
		t.Errorf("binary: err = %v, want ErrBinary", err)
	}
}

// TestListErrorStatus: an HTTP failure names the status code so a moved list
// (404) is distinguishable from a forbidden one (403), without the URL.
func TestListErrorStatus(t *testing.T) {
	cases := []struct {
		code statusError
		want string
	}{
		{http.StatusNotFound, "not found (404)"},
		{http.StatusForbidden, "permission denied (403)"},
		{http.StatusInternalServerError, "unexpected status 500"},
	}

	for _, tc := range cases {
		err := listError(2, "https://lists.example.com/ads.txt", tc.code)
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("listError(%d) = %q, want %q", tc.code, err, tc.want)
		}
		if strings.Contains(err.Error(), "ads.txt") {
			t.Errorf("listError(%d) leaked the path: %q", tc.code, err)
		}
	}
}
