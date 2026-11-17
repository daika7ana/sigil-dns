//go:build windows

// Package winacl restricts files and directories to SYSTEM + Administrators on
// Windows.  It is a no-op on other platforms.  Callers are responsible for
// treating an error as fatal when the protected data is sensitive (resolver
// ids, snapshots, keys).
package winacl

import (
	"errors"
	"fmt"
	"unsafe"

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

// aclSystemAdmins makes Built-in Administrators the owner and applies a
// protected SYSTEM+Administrators DACL.  The DACL alone is not enough: an owner
// holds implicit WRITE_DAC, so a standard user who pre-created the data
// directory could restore their own DACL and read the resolver ids.
func aclSystemAdmins(path string) (err error) {
	owner, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("winacl: building owner sid: %w", err)
	}

	restore, err := enableRestorePrivilege()
	if err != nil {
		return fmt.Errorf("winacl: enabling SeRestorePrivilege: %w", err)
	}
	defer restore()

	return setSecurity(path, secureSDDL, owner)
}

// enableRestorePrivilege enables SeRestorePrivilege on the process token, which
// SetNamedSecurityInfo needs to assign an owner other than the caller.  SYSTEM
// and elevated admins hold it (disabled); a token without it fails closed.  The
// returned func restores the previous state and is never nil on success.
func enableRestorePrivilege() (restore func(), err error) {
	var t windows.Token
	if err = windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES,
		&t,
	); err != nil {
		return nil, fmt.Errorf("opening process token: %w", err)
	}

	var luid windows.LUID
	if err = windows.LookupPrivilegeValue(
		nil, windows.StringToUTF16Ptr("SeRestorePrivilege"), &luid,
	); err != nil {
		_ = t.Close()

		return nil, fmt.Errorf("looking up SeRestorePrivilege: %w", err)
	}

	wasEnabled, err := privilegeEnabled(t, luid)
	if err != nil {
		_ = t.Close()

		return nil, fmt.Errorf("reading token privileges: %w", err)
	}

	state := windows.Tokenprivileges{PrivilegeCount: 1}
	state.Privileges[0].Luid = luid
	state.Privileges[0].Attributes = windows.SE_PRIVILEGE_ENABLED
	// AdjustTokenPrivileges returns success even when it adjusted nothing (the
	// privilege is absent from the token), and GetLastError is unreliable here.
	// Re-read the token to confirm the privilege is actually enabled.
	if err = windows.AdjustTokenPrivileges(t, false, &state, 0, nil, nil); err != nil {
		_ = t.Close()

		return nil, fmt.Errorf("adjusting token privileges: %w", err)
	}
	nowEnabled, err := privilegeEnabled(t, luid)
	if err != nil {
		_ = t.Close()

		return nil, fmt.Errorf("reading adjusted token privileges: %w", err)
	}
	if !nowEnabled {
		_ = t.Close()

		return nil, errors.New("token does not hold SeRestorePrivilege")
	}

	if wasEnabled {
		return func() { _ = t.Close() }, nil
	}

	return func() {
		state.Privileges[0].Attributes = 0
		_ = windows.AdjustTokenPrivileges(t, false, &state, 0, nil, nil)
		_ = t.Close()
	}, nil
}

// privilegeEnabled reports whether the token currently has luid enabled.
func privilegeEnabled(t windows.Token, luid windows.LUID) (enabled bool, err error) {
	var size uint32
	if err = windows.GetTokenInformation(t, windows.TokenPrivileges, nil, 0, &size); err != nil &&
		err != windows.ERROR_INSUFFICIENT_BUFFER {
		return false, err
	}

	buf := make([]byte, size)
	if err = windows.GetTokenInformation(t, windows.TokenPrivileges, &buf[0], size, &size); err != nil {
		return false, err
	}

	tp := (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0]))
	for _, p := range tp.AllPrivileges() {
		if p.Luid == luid {
			return p.Attributes&windows.SE_PRIVILEGE_ENABLED != 0, nil
		}
	}

	return false, nil
}

// SetDACL sets a protected DACL on path from an SDDL string.  Callers that need
// extra principals (e.g. the elevate staging directory) pass their own SDDL.
func SetDACL(path, sddl string) error {
	return setSecurity(path, sddl, nil)
}

// setSecurity applies sddl (and owner, when non-nil) to path.
func setSecurity(path, sddl string, owner *windows.SID) (err error) {
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

	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	if owner != nil {
		info |= windows.OWNER_SECURITY_INFORMATION
	}

	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		info,
		owner, nil, dacl, nil,
	)
	if err != nil {
		return fmt.Errorf("winacl: setting acl on %s: %w", path, err)
	}

	return nil
}
