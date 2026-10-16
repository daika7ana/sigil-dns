//go:build !windows

package elevate

import "context"

// Apply is unsupported off Windows: there is no UAC and no ACL'd config.
func Apply(context.Context, Request) (Result, error) {
	return Result{}, ErrUnsupported
}

// HelperMain is unsupported off Windows.
func HelperMain(string) error {
	return ErrUnsupported
}

// ServiceUninstall is unsupported off Windows.
func ServiceUninstall() error {
	return ErrUnsupported
}
