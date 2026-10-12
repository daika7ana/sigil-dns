//go:build gui && !windows

package gui

import "errors"

// errOpenURLUnsupported is returned off Windows, where there is no shell
// association to open a URL with.  The build and its headless tests still run
// without a browser; the About links simply do nothing.
var errOpenURLUnsupported = errors.New("opening a URL is only supported on Windows")

// openURLPlatform is a no-op that reports the platform has no browser opener.
func openURLPlatform(rawURL string) error {
	return errOpenURLUnsupported
}
