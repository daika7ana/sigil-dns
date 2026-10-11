package filter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// MaxListSize is the maximum size of a single filter list.
const MaxListSize = 64 << 20

// fetchTimeout bounds a single HTTP(S) list fetch.
const fetchTimeout = 30 * time.Second

// Loading errors.
var (
	ErrTooBig = errors.New("filter list exceeds size cap")
	ErrHTML   = errors.New("filter list looks like an HTML page")
	ErrBinary = errors.New("filter list looks like binary data")
)

// List is one filter-list source to load.
type List struct {
	// ID is a human-readable identifier (URL or path).
	ID string

	// Source is an http(s):// URL or a local file path.
	Source string
}

// LoadAll reads every list and returns the raw contents plus a combined content
// checksum.  Lists with identical content are parsed once.
//
// Note: per SPEC §6 the plan mentioned ETag/If-None-Match refresh, but AdGuard
// Home does not use conditional requests; its pattern is an unconditional GET
// plus a content checksum.  We follow that: fetch unconditionally and derive a
// sha256 so callers can skip re-parsing unchanged content.
func LoadAll(ctx context.Context, lists []List) (raw []RawList, sum [32]byte, err error) {
	h := sha256.New()
	seen := map[[32]byte]struct{}{}

	for _, l := range lists {
		if strings.TrimSpace(l.Source) == "" {
			continue
		}

		data, lErr := loadOne(ctx, l.Source)
		if lErr != nil {
			return nil, [32]byte{}, fmt.Errorf("list %q: %w", l.Source, lErr)
		}

		c := sha256.Sum256(data)
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}

		_, _ = h.Write(c[:])
		raw = append(raw, RawList{ID: l.ID, Text: data})
	}

	copy(sum[:], h.Sum(nil))

	return raw, sum, nil
}

// loadOne loads and validates a single source.
func loadOne(ctx context.Context, source string) (data []byte, err error) {
	if isURL(source) {
		data, err = fetch(ctx, source)
	} else {
		data, err = readFile(source)
	}
	if err != nil {
		return nil, err
	}

	if err = validate(data); err != nil {
		return nil, err
	}

	return data, nil
}

// fetch downloads a list over HTTP(S).
func fetch(ctx context.Context, url string) (data []byte, err error) {
	fctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(fctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}

	data, err = readCapped(resp.Body, MaxListSize)
	if err != nil {
		return nil, err
	}

	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
		return nil, ErrHTML
	}

	return data, nil
}

// readFile reads a local list, enforcing the size cap before reading it all.
func readFile(path string) (data []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	if info, sErr := f.Stat(); sErr == nil && info.Size() > MaxListSize {
		return nil, ErrTooBig
	}

	return readCapped(f, MaxListSize)
}

// readCapped reads at most limit bytes and errors if the source is larger.
func readCapped(r io.Reader, limit int64) (data []byte, err error) {
	data, err = io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("reading: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, ErrTooBig
	}

	return data, nil
}

// validate rejects payloads that are obviously not filter lists.
func validate(data []byte) (err error) {
	if int64(len(data)) > MaxListSize {
		return ErrTooBig
	}
	if isHTML(data) {
		return ErrHTML
	}
	if isBinary(data) {
		return ErrBinary
	}

	return nil
}

// isHTML reports whether data looks like an HTML/XML document.
func isHTML(data []byte) (ok bool) {
	head := bytes.TrimSpace(data)
	if len(head) > 64 {
		head = head[:64]
	}
	head = bytes.ToLower(head)

	for _, marker := range [][]byte{
		[]byte("<!doctype html"),
		[]byte("<html"),
		[]byte("<head"),
		[]byte("<?xml"),
	} {
		if bytes.HasPrefix(head, marker) {
			return true
		}
	}

	return false
}

// isBinary reports whether the first 512 bytes contain a NUL byte.
func isBinary(data []byte) (ok bool) {
	if len(data) > 512 {
		data = data[:512]
	}

	return bytes.IndexByte(data, 0) >= 0
}

// isURL reports whether source is an HTTP(S) URL.
func isURL(source string) (ok bool) {
	return strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://")
}
