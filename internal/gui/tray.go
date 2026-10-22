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

// trayStatusEvery is how many paused 1 s ticks pass between tray status
// refreshes while the window is hidden; the window poller is otherwise paused.
const trayStatusEvery = 5

// trayState is the cross-thread view of the tray.  The UI thread owns the
// handle and settings; the poller reads only these atomics, so it can hide a
// minimized window without touching App state.
//
// shortcut: go-gui does not re-add the icon after an Explorer restart, so a
// window hidden at that moment is recovered by relaunching (focusExistingWindow).
type trayState struct {
	active   atomic.Bool // a tray icon currently exists
	minimize atomic.Bool // hide the window when it is minimized
	hidden   atomic.Bool // hidden because it was minimized

	// minimized and statusEvery are seams for the headless poller tests; the
	// zero values mean the Win32 check and trayStatusEvery.
	minimized   func() bool
	statusEvery int
}

// isMinimized reports whether the window is minimized.
func (t *trayState) isMinimized() bool {
	if t.minimized != nil {
		return t.minimized()
	}

	return windowMinimized(windowTitle)
}

// trayEvery is the tray status refresh cadence in paused ticks.
func (t *trayState) trayEvery() int {
	if t.statusEvery > 0 {
		return t.statusEvery
	}

	return trayStatusEvery
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

// startTray creates the tray icon and its menu.  It reports false when the
// platform cannot create one, leaving the setting off.
func (a *App) startTray(w *gogui.Window) (ok bool) {
	gapp := w.App()
	if gapp == nil {
		return false
	}

	status := trayStatusText(a.snap)
	h, err := gapp.SetSystemTray(a.trayConfig(w, status))
	if err != nil {
		if h != nil {
			gapp.RemoveSystemTray(h) // a half-created icon must not linger
		}
		return false
	}
	if h == nil {
		return false
	}

	a.trayHandle = h
	a.tray.active.Store(true)
	a.trayStatus = status
	applyTrayTooltip(windowTitle) // go-gui does not set NIF_SHOWTIP

	return true
}

// trayConfig builds the icon, menu and callback.  Creation and updates share
// it so the callback stays in place when only the status text changes.
func (a *App) trayConfig(w *gogui.Window, status string) gogui.SystemTrayCfg {
	return gogui.SystemTrayCfg{
		Tooltip: windowTitle,
		IconPNG: appIconPNG,
		Menu:    trayMenu(status),
		// go-gui queues OnAction onto the UI thread, so the App fields below
		// may be touched directly (see App.SetSystemTray).
		OnAction: func(id string) { a.handleTrayAction(w, id) },
	}
}

// handleTrayAction runs one right-click choice on the UI thread.
func (a *App) handleTrayAction(w *gogui.Window, id string) {
	switch id {
	case "", trayActionOpen:
		a.showWindow(w)
	case trayActionLog:
		// Do not raise the window: opening a file is not a request to see
		// the app.  A missing log is a toast on the window if it happens to
		// be visible; the tray action stays out of the user's way.
		runOpenLog(gogui.EventCtx{Window: w})
	case trayActionFlush:
		// Same rule: clear the caches without bringing the window forward.
		runRequest(w, applyRequest{action: actionClearCache})
	case trayActionExit:
		a.exitFromTray(w)
	}
}

// syncTray updates the status row when the service state text changes.  The
// poller queues this onto the UI thread.
func (a *App) syncTray(w *gogui.Window, status string) {
	if a.trayHandle == nil || status == a.trayStatus {
		return
	}
	gapp := w.App()
	if gapp == nil {
		return
	}

	a.trayStatus = status
	gapp.UpdateSystemTray(a.trayHandle, a.trayConfig(w, status))
	applyTrayTooltip(windowTitle) // an update rewrites the tip flags
}

// stopTray removes the icon and forgets its handle.
func (a *App) stopTray(w *gogui.Window) {
	if gapp := w.App(); gapp != nil && a.trayHandle != nil {
		gapp.RemoveSystemTray(a.trayHandle)
	}
	a.trayHandle = nil
	a.tray.active.Store(false)
	a.tray.hidden.Store(false)
	a.trayStatus = ""
}

// showWindow restores the window from the tray, including after a minimize.
// Visibility and the maximized state are read from Win32, not from go-gui's
// hidden flag: a second launch can show the window without clearing it.
func (a *App) showWindow(w *gogui.Window) {
	a.tray.hidden.Store(false)
	// A tray start creates the window transparent; reveal it now.
	w.SetWindowOpacity(1)

	zoomed := windowZoomed(windowTitle)
	visible, minimized := windowVisible(windowTitle), windowMinimized(windowTitle)

	if visible && !minimized {
		ensureFocus(w) // already visible: raise it, don't restore

		return
	}

	w.Show()
	// Show does not request a frame, so ask for the layout pass it skips.
	ensureFocus(w)
	w.InvalidateLayout()
	if zoomed {
		// Show uses SW_RESTORE, which un-maximizes; put the window back.
		maximizeWindow(windowTitle)
	}
}

// exitFromTray leaves for good: the tray icon goes first, so closing the
// window cannot be diverted back into the tray by the close preference.
func (a *App) exitFromTray(w *gogui.Window) {
	a.stopTray(w)
	a.closeWindow(w)
}

// windowNormal reports whether the window is neither minimized nor maximized.
// Only a normal window's size is the user's chosen size; a maximized or
// minimized window reports the state's size, which must never become the
// startup size.  A variable so the headless tests can drive it.
var windowNormal = func(title string) bool {
	return !windowMinimized(title) && !windowZoomed(title)
}

// closeWindow is the one close path for the title-bar button and Tray Exit.
// It saves geometry while the window still exists, honours close-to-tray, and
// removes the icon before the window goes away.
func (a *App) closeWindow(w *gogui.Window) {
	a.savePosition()
	a.saveSize(w)
	// Persist now, not only after RunApp returns: close-to-tray keeps the
	// process alive, and the size the user just chose must survive a crash
	// or a forced exit while the window is hidden.
	a.persist()
	if a.closeToTray() {
		w.Hide()
		return
	}

	a.stopTray(w)
	w.Close()
	// Close only sets a flag.  From a queued tray action the run loop can be
	// about to block in waitMessage, so it would not see the flag until
	// another message arrives; the process then keeps gui.lock and the next
	// launch exits.  InvalidateRender wakes the loop.
	w.InvalidateRender()
}

// closeToTray reports whether the close button should hide the window.
func (a *App) closeToTray() bool {
	return a.tray.active.Load() && a.settings.CloseToTray
}

// persist writes gui.json after a tray preference change.  Best-effort, like
// every other gui.json write: it is not a security boundary.
func (a *App) persist() {
	if a.guiPath != "" {
		_ = saveSettings(a.guiPath, a.settings)
	}
}

// savePosition remembers where the window is, for the next launch.  It only
// reads a visible, normal window: a hidden or minimized window's rect is not
// the position the user chose, and keeping the last normal value is correct.
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

// setTrayEnabled toggles the icon.  A platform that cannot create one leaves
// the switch off and says so.
func setTrayEnabled(ctx gogui.EventCtx, on bool) {
	app := gogui.State[App](ctx.Window)

	if on && !app.startTray(ctx.Window) {
		app.showToast(ctx.Window, noticeMessage{
			kind:  noticeWarning,
			title: "System tray unavailable",
			body:  "The tray icon could not be created.",
		})
		on = false
	} else if !on {
		app.stopTray(ctx.Window)
		// No tray to start into: drop the logon task too, or the next
		// sign-in would run an invisible window.  If the task survives,
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
			"Keep Sigil DNS in the notification area when the window is hidden.",
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
				"Launch hidden to the tray when you sign in to Windows, elevated with no UAC prompt.",
				gogui.Switch(gogui.SwitchCfg{
					ID:       "gui:tray:autostart",
					A11YCfg:  gogui.A11YCfg{A11YLabel: "Start tray icon on startup"},
					Selected: app.settings.StartWithWindows,
					OnClick: func(ctx gogui.EventCtx) {
						toggleStartWithWindows(ctx, !gogui.State[App](ctx.Window).settings.StartWithWindows)
					},
				})),
			settingRow(t, "Minimize to tray",
				"Hide the window in the tray when it is minimized.",
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
				"Keep running in the tray instead of exiting when the window is closed.",
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
