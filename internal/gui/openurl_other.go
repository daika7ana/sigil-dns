//go:build gui && !windows

package gui

import "errors"

// openURLPlatform is a no-op that reports the platform has no browser opener.
// No shell association exists off Windows, so the About links simply do nothing.
func openURLPlatform(rawURL string) error {
	return errors.New("opening a URL is only supported on Windows")
}

// openPathPlatform is a no-op that reports the platform has no file opener.
func openPathPlatform(path string) error {
	return errors.New("opening a file is only supported on Windows")
}
