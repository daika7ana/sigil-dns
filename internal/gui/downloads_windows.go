//go:build gui && windows

package gui

import "golang.org/x/sys/windows"

// downloadsFolderDefault resolves the user's Downloads known folder.  Windows
// lets the user relocate it, so %USERPROFILE%\Downloads is not reliable.
func downloadsFolderDefault() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_DEFAULT)
}
