//go:build !windows

package intercept

import "context"

// New returns the unsupported stub on non-Windows platforms.
func New() Controller { return stub{} }

type stub struct{}

// Enable implements Controller.
func (stub) Enable(context.Context) error { return ErrUnsupported }

// Disable implements Controller.
func (stub) Disable(context.Context) error { return ErrUnsupported }

// SnapshotAdapters is unsupported off Windows.
func SnapshotAdapters(context.Context) (entries []AdapterEntry, err error) {
	return nil, ErrUnsupported
}

// ApplyAdapters is unsupported off Windows.
func ApplyAdapters(context.Context, []AdapterEntry) (err error) { return ErrUnsupported }

// ResetAdapters is unsupported off Windows.
func ResetAdapters(context.Context, []AdapterEntry) (err error) { return ErrUnsupported }

// ProbeNRPT is unsupported off Windows.
func ProbeNRPT(context.Context) (st NRPTState, err error) { return NRPTState{}, ErrUnsupported }
