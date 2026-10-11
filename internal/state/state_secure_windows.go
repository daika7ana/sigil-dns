//go:build windows

package state

import (
	"fmt"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// stateSDDL grants Generic All to SYSTEM and Built-in Administrators, with a
// protected DACL (no inherited ACEs).  Well-known SIDs are locale-independent.
const stateSDDL = `D:P(A;;GA;;;SY)(A;;GA;;;BA)`

// secure ACLs the state file and its directory to SYSTEM + Administrators using
// SetSecurityInfo.  Failures are returned so they are visible, not swallowed.
func secure(path string) (err error) {
	dir := filepath.Dir(path)

	for _, p := range []string{dir, path} {
		if err = aclSystemAdmins(p); err != nil {
			return fmt.Errorf("acl %s: %w", p, err)
		}
	}

	return nil
}

// aclSystemAdmins sets a protected SYSTEM+Administrators DACL on path.
func aclSystemAdmins(path string) (err error) {
	sd, err := windows.SecurityDescriptorFromString(stateSDDL)
	if err != nil {
		return err
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}

	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	)
}
