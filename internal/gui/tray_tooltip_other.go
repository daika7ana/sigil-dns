//go:build gui && !windows

package gui

// The headless/test platforms have no notification-area tooltip to repair.
func applyTrayTooltip(string) {}
