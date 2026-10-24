//go:build gui && windows

package gui

import "os"

// markDownloaded tags the saved zip with the Internet zone, the same hidden
// Mark-of-the-Web a browser adds, so SmartScreen and PowerShell still treat the
// extracted binaries as downloaded.  Best-effort: a filesystem without alternate
// data streams must not fail the download.
func markDownloaded(path string) {
	_ = os.WriteFile(path+":Zone.Identifier", []byte("[ZoneTransfer]\r\nZoneId=3\r\n"), 0o644)
}
