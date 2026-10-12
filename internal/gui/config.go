// Package gui hosts the go-gui client window (SPEC §15, phase 6/2): a Status
// screen and a Settings screen.  With the "gui" build tag it is a real client
// window; without it Run is a stub so the default binary stays GUI-free
// (mirrors internal/tray).
package gui

import "errors"

// ErrNotBuilt is returned when the binary was built without GUI support.
var ErrNotBuilt = errors.New("gui support is not built into this binary (rebuild with -tags gui)")
