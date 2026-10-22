//go:build gui

package gui

import (
	"sync/atomic"

	gogui "github.com/go-gui-org/go-gui/gui"
)

// Tray menu action IDs.  The empty ID is go-gui's default action (a click on
// the icon itself).
const (
	trayActionOpen  = "open"
	trayActionLog   = "log"
	trayActionFlush = "flush"
	trayActionExit  = "exit"
)

// hostStart and hostStop are seams for the headless tests: a test run must
// never launch or stop the real tray-host process.
var (
	hostStart = startTrayHost
	hostStop  = stopTrayHost
)

// trayState is the poller's cross-thread view of the tray.  The icon lives in
// the separate tray-host process, so the window only needs to know whether a
// minimize should quit it.  The poller reads only the atomic, so it never
// touches UI-thread state.
type trayState struct {
	minimize atomic.Bool // quit the window to the tray when it is minimized

	// minimized is a seam for the headless poller tests; nil means the Win32
	// check.
	minimized func() bool
}

// isMinimized reports whether the window is minimized.
func (t *trayState) isMinimized() bool {
	if t.minimized != nil {
		return t.minimized()
	}

	return windowMinimized(windowTitle)
}

// trayMenu is the right-click menu.  The status row leads and is disabled: it
// reports the service state, so a stopped or unverified service is never shown
// as protected.
//
// shortcut: go-gui v0.89.1 (through v0.92.0) resolves a Win32 tray click
// through its menuNodes slice, whose element 0 is the root.  A click on item i
// therefore fires item i-1's ID, and item 0 fires nothing.  The IDs below are
// shifted one item down, with the disabled status row first and a separator
// after it, so each action item fires the ID it appears to own.  Re-check this
// mapping on a go-gui upgrade; a fixed library would need the natural IDs (and
// would let Open lead again).
func trayMenu(status string) []gogui.NativeMenuItemCfg {
	return []gogui.NativeMenuItemCfg{
		// Status is disabled: index 0 fires nothing.
		{Text: status, Disabled: true},
		// The click on Open Sigil DNS (index 2) reads this separator's ID.
		{ID: trayActionOpen, Separator: true},
		// The click on Open log file (index 3) reads this row's ID.
		{ID: trayActionLog, Text: "Open Sigil DNS"},
		// The click on Clear DNS cache (index 4) reads this row's ID.
		{ID: trayActionFlush, Text: "Open log file"},
		{Text: "Clear DNS cache"},
		// The click on Exit (index 6) reads this separator's ID.
		{ID: trayActionExit, Separator: true},
		{Text: "Exit"},
	}
}

// trayStatusText is the status row, matching the Settings service badge.
func trayStatusText(s snapshot) string {
	switch {
	case s.scmState == "":
		return "Service: Unknown" // SCM query failed, or no read yet
	case !s.installed:
		return "Service: Not installed"
	case s.trust == trustUnverified:
		return "Service: Unverified"
	default:
		return "Service: " + displayState(s.scmState)
	}
}

// trayConfig builds the icon and menu.  The tray host uses it for Create and
// Update, so the icon stays in place when only the status changes.
func trayConfig(status string) gogui.SystemTrayCfg {
	return gogui.SystemTrayCfg{
		Tooltip: windowTitle,
		IconPNG: appIconPNG,
		Menu:    trayMenu(status),
	}
}

// quitWindow quits this process to the tray host: save the geometry while the
// native window still exists, then close.  The host keeps the icon, so a later
// Open relaunches the window.
func (a *App) quitWindow(w *gogui.Window) {
	a.savePosition()
	a.saveSize(w)
	a.persist()
	_ = hostStart() // no-op when it is already running
	w.Close()
	// Close only sets a flag.  From a queued command the run loop can be about
	// to block in waitMessage, so it would not see the flag until another
	// message arrives; the process then keeps gui.lock and the next launch
	// exits.  InvalidateRender wakes the loop.
	w.InvalidateRender()
}

// windowNormal reports whether the window is neither minimized nor maximized.
// Only a normal window's size is the user's chosen size; a maximized or
// minimized window reports the state's size, which must never become the
// startup size.  A variable so the headless tests can drive it.
var windowNormal = func(title string) bool {
	return !windowMinimized(title) && !windowZoomed(title)
}

// closeWindow is the one close path for the title-bar button.  It saves
// geometry while the window still exists, then quits this process.  With
// close-to-tray on the separate tray host keeps the icon; otherwise the icon
// goes too, so closing exits the app entirely.
func (a *App) closeWindow(w *gogui.Window) {
	a.savePosition()
	a.saveSize(w)
	// Persist now, not only after RunApp returns: the window's size must
	// survive the process anyway.
	a.persist()
	// With close-to-tray on, the host is already running: it was started in
	// OnInit (or when the switch was turned on).  Starting one here would race
	// the tray Exit action, which closes this window while the host is
	// exiting; the host would be gone and this would create a replacement,
	// leaving an icon after "Exit".  Only stop the host when the user does not
	// want to stay in the tray.
	if !a.settings.Tray || !a.settings.CloseToTray {
		hostStop()
	}

	w.Close()
	// Close only sets a flag.  The run loop can be about to block in
	// waitMessage, so it would not see the flag until another message arrives;
	// the process then keeps gui.lock and the next launch exits.
	// InvalidateRender wakes the loop.
	w.InvalidateRender()
}

// persist writes gui.json after a tray preference change.  Best-effort, like
// every other gui.json write: it is not a security boundary.
func (a *App) persist() {
	if a.guiPath != "" {
		_ = saveSettings(a.guiPath, a.settings)
	}
}

// savePosition remembers where the window is, for the next launch.  It only
// reads a visible, normal window: a minimized window's rect is not the position
// the user chose, and keeping the last normal value is correct.
func (a *App) savePosition() {
	if !windowVisible(windowTitle) || windowMinimized(windowTitle) || windowZoomed(windowTitle) {
		return
	}
	if x, y, ok := windowPosition(windowTitle); ok {
		a.settings.X, a.settings.Y = x, y
	}
}

// saveSize remembers the window size.  Call it while the native window still
// exists (OnCloseRequest): after RunApp returns the size is gone.  A maximized
// or minimized window is ignored so its transient size cannot replace the size
// the user chose in the normal state.
func (a *App) saveSize(w *gogui.Window) {
	if !windowNormal(windowTitle) {
		return
	}
	if ww, wh := w.WindowSize(); ww > 0 && wh > 0 {
		a.settings.Width, a.settings.Height = ww, wh
	}
}

// setTrayEnabled toggles the tray host process.  A platform that cannot start
// one leaves the switch off and says so.
func setTrayEnabled(ctx gogui.EventCtx, on bool) {
	app := gogui.State[App](ctx.Window)

	if on {
		if err := hostStart(); err != nil {
			app.showToast(ctx.Window, noticeMessage{
				kind:  noticeWarning,
				title: "System tray unavailable",
				body:  "The tray host could not be started.",
			})
			on = false
		}
	} else {
		hostStop()
		// No tray to start into: drop the logon task too, or the next
		// sign-in would run an invisible host.  If the task survives,
		// keep the preference and say so rather than pretending.
		if app.settings.StartWithWindows {
			if err := setStartWithWindows(false); err != nil {
				app.showToast(ctx.Window, noticeMessage{
					kind:  noticeWarning,
					title: "Startup task not removed",
					body:  "The scheduled task could not be deleted.",
				})
			} else {
				app.settings.StartWithWindows = false
			}
		}
	}

	app.settings.Tray = on
	app.tray.minimize.Store(on && app.settings.MinimizeToTray)
	app.persist()
	ctx.Window.InvalidateLayout()
}

// toggleStartWithWindows registers or removes the per-user logon task.  A
// failed write leaves the stored preference unchanged, so the switch never
// promises a start that cannot happen.
func toggleStartWithWindows(ctx gogui.EventCtx, on bool) {
	app := gogui.State[App](ctx.Window)

	if err := setStartWithWindows(on); err != nil {
		app.showToast(ctx.Window, noticeMessage{
			kind:  noticeWarning,
			title: "Startup unavailable",
			body:  "The startup task could not be changed.",
		})
		ctx.Window.InvalidateLayout()
		return
	}

	app.settings.StartWithWindows = on
	app.persist()
	ctx.Window.InvalidateLayout()
}

// toggleTrayBehaviour flips a close/minimize preference and re-syncs the
// poller's copy.
func toggleTrayBehaviour(ctx gogui.EventCtx, fn func(*settings)) {
	app := gogui.State[App](ctx.Window)

	fn(&app.settings)
	app.tray.minimize.Store(app.settings.Tray && app.settings.MinimizeToTray)
	app.persist()
	ctx.Window.InvalidateLayout()
}

// trayCard is the opt-in tray section.  The two behaviour switches only appear
// while the icon is on, because neither does anything without it.
func trayCard(t gogui.Theme, app *App) gogui.View {
	rows := []gogui.View{
		settingRow(t, "System tray icon",
			"Keep Sigil DNS in the notification area when the window is closed.",
			gogui.Switch(gogui.SwitchCfg{
				ID:       "gui:tray:enabled",
				A11YCfg:  gogui.A11YCfg{A11YLabel: "System tray icon"},
				Selected: app.settings.Tray,
				OnClick: func(ctx gogui.EventCtx) {
					setTrayEnabled(ctx, !gogui.State[App](ctx.Window).settings.Tray)
				},
			})),
	}

	if app.settings.Tray {
		rows = append(rows,
			settingRow(t, "Start tray icon on startup",
				"Launch the tray icon when you sign in to Windows, elevated with no UAC prompt.",
				gogui.Switch(gogui.SwitchCfg{
					ID:       "gui:tray:autostart",
					A11YCfg:  gogui.A11YCfg{A11YLabel: "Start tray icon on startup"},
					Selected: app.settings.StartWithWindows,
					OnClick: func(ctx gogui.EventCtx) {
						toggleStartWithWindows(ctx, !gogui.State[App](ctx.Window).settings.StartWithWindows)
					},
				})),
			settingRow(t, "Minimize to tray",
				"Quit the window to the tray when it is minimized.",
				gogui.Switch(gogui.SwitchCfg{
					ID:       "gui:tray:minimize",
					A11YCfg:  gogui.A11YCfg{A11YLabel: "Minimize to tray"},
					Selected: app.settings.MinimizeToTray,
					OnClick: func(ctx gogui.EventCtx) {
						toggleTrayBehaviour(ctx, func(s *settings) {
							s.MinimizeToTray = !s.MinimizeToTray
						})
					},
				})),
			settingRow(t, "Close to tray",
				"Quit the window to the tray instead of stopping the icon.",
				gogui.Switch(gogui.SwitchCfg{
					ID:       "gui:tray:close",
					A11YCfg:  gogui.A11YCfg{A11YLabel: "Close to tray"},
					Selected: app.settings.CloseToTray,
					OnClick: func(ctx gogui.EventCtx) {
						toggleTrayBehaviour(ctx, func(s *settings) {
							s.CloseToTray = !s.CloseToTray
						})
					},
				})),
		)
	}

	return card(t, "gui:card:tray", "Window & tray", rows)
}
