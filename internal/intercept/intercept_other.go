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

// Status implements Controller.
func (stub) Status(context.Context) (Status, error) { return Status{}, ErrUnsupported }
