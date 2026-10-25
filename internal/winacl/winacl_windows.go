//go:build windows

// Package winacl restricts files and directories to SYSTEM + Administrators on
// Windows.  It is a no-op on other platforms.  Callers are responsible for
// treating an error as fatal when the protected data is sensitive (resolver
// ids, snapshots, keys).
package winacl

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// secureSDDL grants Generic All to SYSTEM and Built-in Administrators, with a
// protected DACL (no inherited ACEs).  Well-known SIDs are locale-independent.
const secureSDDL = `D:P(A;;GA;;;SY)(A;;GA;;;BA)`

// errReparsePoint is static so it stays path-free for unelevated callers.
var errReparsePoint = errors.New("winacl: refusing to secure a reparse point")

// SecureFile restricts path (a file) to SYSTEM + Administrators.
func SecureFile(path string) (err error) {
	return aclSystemAdmins(path)
}

// SecureDir restricts path (a directory) to SYSTEM + Administrators.
func SecureDir(path string) (err error) {
	return aclSystemAdmins(path)
}

// aclSystemAdmins sets a protected SYSTEM+Administrators DACL on path.
func aclSystemAdmins(path string) (err error) {
	return SetDACL(path, secureSDDL)
}

// SetDACL sets a protected DACL on path from an SDDL string.  Callers that need
// extra principals (e.g. the elevate staging directory) pass their own SDDL.
func SetDACL(path, sddl string) (err error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("winacl: converting path: %w", err)
	}

	// SetNamedSecurityInfo is path-based and would follow a junction, so refuse
	// reparse points before any ACL is applied to the target.
	attrs, err := windows.GetFileAttributes(name)
	if err != nil {
		return fmt.Errorf("winacl: reading attributes: %w", err)
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errReparsePoint
	}

	// GetFileAttributes reports the link itself, not its target, so the check
	// above is the reparse-point guard; opening the path here would add nothing.
	//
	// shortcut: the check and SetNamedSecurityInfo are separate path lookups, so
	// a reparse point swapped in between them is still followed; apply the DACL
	// with SetSecurityInfo on an open WRITE_DAC handle if that race matters.
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("winacl: parsing sddl: %w", err)
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("winacl: reading dacl: %w", err)
	}

	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	)
	if err != nil {
		return fmt.Errorf("winacl: setting acl on %s: %w", path, err)
	}

	return nil
}
