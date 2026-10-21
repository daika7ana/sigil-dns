package service

import (
	"path/filepath"
	"strings"
)

// canonicalServiceExe returns the executable the service is registered as and
// compared against: the non-GUI sibling sigildns.exe when exe is the GUI
// executable and that sibling exists, else exe.  stat is injected so the
// two-binary resolution is unit-testable.
func canonicalServiceExe(exe string, stat func(string) bool) (canonical string) {
	if strings.EqualFold(filepath.Base(exe), "sigildns-gui.exe") {
		sibling := filepath.Join(filepath.Dir(exe), "sigildns.exe")
		if stat(sibling) {
			return sibling
		}
	}

	return exe
}
