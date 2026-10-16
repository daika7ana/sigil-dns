//go:build gui

package gui

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// openURL opens rawURL in the OS default browser.  It is a package-level var so
// a headless test can stub it (mirroring newSource in source.go) and capture the
// URL without launching anything.  Production points at openURLDefault.
var openURL = openURLDefault

// openURLDefault is the trust boundary before the OS shell sees a string: it
// accepts only an absolute http/https URL with a host, then hands it to the
// platform opener.  Rejecting every other scheme keeps the shell from ever being
// asked to handle e.g. "file:" or "javascript:".
func openURLDefault(rawURL string) error {
	u, err := validateURL(rawURL)
	if err != nil {
		return err
	}

	return openURLPlatform(u.String())
}

// validateURL parses rawURL and requires an absolute http/https URL with a host.
// It is split out from the platform call so the guard is unit-testable with no
// browser and no OS.
func validateURL(rawURL string) (*url.URL, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("refusing to open non-http(s) URL %q", rawURL)
	}

	return u, nil
}

// openPath opens a local file with the OS default handler (the shell "open this"
// verb).  Like openURL it is a package-level var so a headless test can stub it
// and capture the path without launching anything.  Production points at
// openPathDefault.
var openPath = openPathDefault

// openPathDefault is the trust boundary before the OS opener sees the value: it
// accepts only a non-empty local filesystem path, then hands it to the platform
// opener as a single argv element (never a shell string).
func openPathDefault(path string) error {
	p, err := validatePath(path)
	if err != nil {
		return err
	}

	return openPathPlatform(p)
}

// validatePath trims and checks a candidate log path, returning the cleaned
// value.  It accepts only a local absolute path (drive or UNC); it rejects an
// empty value, a NUL byte, and a leading "-" so the value can never be read as
// an option by the shell or the helper.  It is split out from the platform call
// so the guard is unit-testable with no OS and no process launch.
func validatePath(path string) (string, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return "", errors.New("empty path")
	}
	if strings.ContainsRune(p, 0) || strings.HasPrefix(p, "-") {
		return "", fmt.Errorf("refusing to open unsafe path %q", path)
	}
	if !isLocalAbsPath(p) {
		return "", fmt.Errorf("refusing to open non-local path %q", path)
	}

	return p, nil
}

// isLocalAbsPath reports whether p is an absolute local filesystem path.  It
// accepts the host's own absolute form, a UNC path, and a Windows drive path
// even when checked off Windows, so the guard cannot depend on the OS the tests
// run on.  It rejects the Win32 device namespace prefixes (\\.\ and \\?\), the
// same ones config.ValidateLogPath refuses.
func isLocalAbsPath(p string) bool {
	if strings.HasPrefix(p, `\\.\`) || strings.HasPrefix(p, `\\?\`) {
		return false
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, `\\`) {
		return true
	}
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		c := p[0]

		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}

	return false
}
