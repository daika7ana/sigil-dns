//go:build !windows

package drift

import (
	"context"
	"log/slog"
	"sync"
)

// watchRegistry is a no-op off Windows (no SCM/registry interception).
func watchRegistry(ctx context.Context, nudge chan<- struct{}) {}

// NewSystemReconciler returns a reconciler with no platform hooks, so builds
// and tests off Windows are unaffected.
func NewSystemReconciler(statePath string, logger *slog.Logger, onDisable func(), _ *sync.Mutex) (r *Reconciler) {
	return NewReconciler(Options{Logger: logger, OnDisable: onDisable})
}
