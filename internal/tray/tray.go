//go:build !tray

// Package tray hosts the tray UI.  Without the "tray" build tag it is a stub so
// Linux and the default Windows build do not need cgo (SPEC §10).
package tray

import (
	"context"
	"errors"

	"github.com/daika7ana/sigil-dns/internal/ipc"
)

// ErrNotBuilt is returned when the binary was built without tray support.
var ErrNotBuilt = errors.New("tray support is not built into this binary (the tray host ships with the GUI build)")

// Run starts the retired tray UI stub; returns ErrNotBuilt.  The shipped tray is
// the GUI build's separate `--tray` host in internal/gui.
func Run(ctx context.Context, client *ipc.Client) (err error) { return ErrNotBuilt }
