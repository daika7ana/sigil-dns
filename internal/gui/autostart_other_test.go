//go:build gui && !windows

package gui

import (
	"testing"

	gogui "github.com/go-gui-org/go-gui/gui"
)

// TestStartWithWindowsUnsupported leaves the preference unchanged when the
// platform cannot register the logon task.
func TestStartWithWindowsUnsupported(t *testing.T) {
	app := settingsApp()
	app.settings.Tray = true
	w := newSettingsWindow(t, app)

	toggleStartWithWindows(gogui.EventCtx{Window: w}, true)

	if app.settings.StartWithWindows {
		t.Error("settings.StartWithWindows = true on a non-Windows build")
	}
}
