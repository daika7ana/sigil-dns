//go:build tray

// Package tray hosts the tray UI (phase 4b).  This file is the build-tagged
// placeholder; the real systray implementation will replace the body.
package tray

import (
	"context"
	"errors"

	"github.com/daika7ana/dns-client/internal/ipc"
)

// Run starts the tray UI.  TODO(phase 4b): implement with fyne.io/systray,
// reading status from and toggling the daemon through client.
func Run(ctx context.Context, client *ipc.Client) (err error) {
	return errors.New("tray UI is not implemented yet")
}
