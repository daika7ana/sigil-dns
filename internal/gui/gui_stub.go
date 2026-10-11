//go:build !gui

package gui

// Run starts the status window.  Stub: the default build has no GUI, so this
// returns ErrNotBuilt and the caller stays headless (SPEC §15).
func Run(cfg Config) (err error) { return ErrNotBuilt }
