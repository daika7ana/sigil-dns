//go:build gui && !windows

package gui

// markDownloaded is a no-op off Windows, where there is no Mark-of-the-Web.
func markDownloaded(string) {}
