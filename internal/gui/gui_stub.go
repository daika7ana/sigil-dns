//go:build !gui

package gui

// Run starts the status window.  Stub: the default build has no GUI, so this
// returns ErrNotBuilt and the caller stays headless (SPEC §15).
func Run() (err error) { return ErrNotBuilt }

// RunTrayHost starts the native tray host.  Stub: the default build has no GUI
// and no tray host.
func RunTrayHost() (err error) { return ErrNotBuilt }
