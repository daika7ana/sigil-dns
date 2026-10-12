//go:build gui

package gui

import (
	"fmt"
	"net/url"
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
