//go:build tray

// Package tray hosts the tray UI (phase 4b).  This file is the build-tagged
// placeholder; the real systray implementation will replace the body.
package tray

import (
	"context"
	"errors"

	"github.com/daika7ana/sigil-dns/internal/ipc"
)

// Run is the retired tray UI stub.  The shipped tray is the separate no-window
// host in internal/gui (`--tray`, trayhost_windows.go); the `tray` tag and this
// package stay only so old build commands still compile.
func Run(ctx context.Context, client *ipc.Client) (err error) {
	return errors.New("tray UI is not implemented yet")
}
