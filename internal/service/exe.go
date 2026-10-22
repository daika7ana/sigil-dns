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

// binaryMatches reports whether a registered service BinaryPathName still
// points at exe followed by the fixed "--service" argument.  It is pure so the
// anti-hijack check is unit-testable off Windows.
func binaryMatches(binPath, exe string) (ok bool) {
	s := strings.TrimSpace(binPath)
	if s == "" {
		return false
	}

	if strings.HasPrefix(s, `"`) {
		bin, after, found := strings.Cut(s[1:], `"`)
		if !found {
			return false
		}

		return strings.EqualFold(bin, exe) && strings.TrimSpace(after) == "--service"
	}

	bin, args, found := strings.Cut(s, " ")
	if !found {
		return false
	}

	return strings.EqualFold(bin, exe) && strings.TrimSpace(args) == "--service"
}
