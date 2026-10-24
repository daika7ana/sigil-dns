package filter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/daika7ana/sigil-dns/internal/redact"
)

// maxListSize is the maximum size of a single filter list.
const maxListSize = 64 << 20

// fetchTimeout bounds a single HTTP(S) list fetch.
const fetchTimeout = 30 * time.Second

// Loading errors.
var (
	ErrTooBig = errors.New("filter list exceeds size cap")
	ErrHTML   = errors.New("filter list looks like an HTML page")
	ErrBinary = errors.New("filter list looks like binary data")
)

// statusError is an unexpected HTTP status.  It carries the code only, so the
// message can reach a user without the URL.
type statusError int

func (e statusError) Error() string { return fmt.Sprintf("unexpected status %d", int(e)) }

// List is one filter-list source to load.
type List struct {
	// Source is an http(s):// URL or a local file path.
	Source string
}

// LoadAll reads every list and returns the raw contents.  Lists with identical
// content are parsed once.
//
// Note: per SPEC §6 the plan mentioned ETag/If-None-Match refresh, but AdGuard
// Home does not use conditional requests; its pattern is an unconditional GET
// plus a content checksum.  We follow that: fetch unconditionally and dedupe by
// a per-list sha256.
func LoadAll(ctx context.Context, lists []List) (raw []RawList, err error) {
	seen := map[[32]byte]struct{}{}

	for i, l := range lists {
		if strings.TrimSpace(l.Source) == "" {
			continue
		}

		data, lErr := loadOne(ctx, l.Source)
		if lErr != nil {
			return nil, listError(i, l.Source, lErr)
		}

		c := sha256.Sum256(data)
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}

		raw = append(raw, RawList{Text: data})
	}

	return raw, nil
}

// listFailure carries the static, source-free message while keeping the
// loading sentinel visible to errors.Is.
type listFailure struct {
	msg   string
	cause error
}

func (e listFailure) Error() string { return e.msg }
func (e listFailure) Unwrap() error { return e.cause }

// listError labels a failure without exposing the source URL or path.  The
// reason is one of a few fixed strings; a loading sentinel stays wrapped so
// errors.Is still classifies the failure.
func listError(i int, source string, err error) error {
	reason := "other failure"
	var sentinel error

	var netErr net.Error
	var status statusError
	switch {
	case errors.Is(err, ErrHTML):
		reason, sentinel = "looks like an HTML page", ErrHTML
	case errors.Is(err, ErrBinary):
		reason, sentinel = "looks like binary data", ErrBinary
	case errors.Is(err, ErrTooBig):
		reason, sentinel = "too large", ErrTooBig
	case errors.As(err, &status):
		switch status {
		case http.StatusNotFound:
			reason = "not found (404)"
		case http.StatusForbidden:
			reason = "permission denied (403)"
		default:
			reason = status.Error()
		}
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		reason = "timed out"
	case errors.Is(err, fs.ErrNotExist):
		reason = "not found"
	case errors.Is(err, fs.ErrPermission):
		reason = "permission denied"
	}

	msg := fmt.Sprintf("filter list %d (%s): %s", i, redact.Source(source), reason)
	if sentinel != nil {
		return listFailure{msg: msg, cause: sentinel}
	}

	return errors.New(msg)
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
		return nil, statusError(resp.StatusCode)
	}

	data, err = readCapped(resp.Body, maxListSize)
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

	if info, sErr := f.Stat(); sErr == nil && info.Size() > maxListSize {
		return nil, ErrTooBig
	}

	return readCapped(f, maxListSize)
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
