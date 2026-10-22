//go:build !windows

package intercept

import (
	"context"
	"log/slog"
)

// New returns the unsupported stub on non-Windows platforms.
func New(*slog.Logger) Controller { return stub{} }

type stub struct{}

// Enable implements Controller.
func (stub) Enable(context.Context, []string) error { return ErrUnsupported }

// Disable implements Controller.
func (stub) Disable(context.Context) error { return ErrUnsupported }

// ProbeNRPT is unsupported off Windows.
func ProbeNRPT(context.Context) (st NRPTState, err error) { return NRPTState{}, ErrUnsupported }
