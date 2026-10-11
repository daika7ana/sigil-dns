//go:build gui

package gui

import (
	"context"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend"
)

// Window defaults.  A saved geometry in gui.json wins.
const (
	defaultWidth  = 560
	defaultHeight = 700
	minWidth      = 420
	minHeight     = 460
)

// pollInterval is the status refresh cadence (SPEC §15.4, phase 2: ~2 s).
const pollInterval = 2 * time.Second

// App is the window state the view reads each frame.  It is written only on the
// main thread; the poller posts updates through Window.QueueCommand.
type App struct {
	snap      snapshot
	refresh   chan struct{}
	apply     applyFunc
	guiPath   string
	themePref string

	// busy is true while an elevated action is in flight; busyNote explains
	// what it is waiting on.
	busy     bool
	busyNote string
}

// Run opens the read-only status window and blocks until it is closed.
func Run(cfg Config) (err error) {
	path := cfg.GuiPath
	if path == "" {
		path = defaultGuiPath()
	}

	s := loadSettings(path)
	if s.Width <= 0 {
		s.Width = defaultWidth
	}
	if s.Height <= 0 {
		s.Height = defaultHeight
	}
	if s.Theme == "" {
		s.Theme = themeSystem
	}

	app := &App{
		guiPath:   path,
		themePref: s.Theme,
		refresh:   make(chan struct{}, 1),
		apply:     elevateApply,
	}
	src := newSource(cfg.Endpoint)

	w := gogui.NewWindow(gogui.WindowCfg{
		Title:     "dns-client",
		Width:     s.Width,
		Height:    s.Height,
		MinWidth:  minWidth,
		MinHeight: minHeight,
		State:     app,
		OnInit: func(w *gogui.Window) {
			applyTheme(w, app.themePref)
			w.SetView(view)
			startPoller(w, src, app.refresh)
		},
	})

	// Blocks on the main thread until the window closes.
	backend.Run(w)

	// Persist geometry + theme preference.  Best-effort: gui.json is not a
	// security boundary and a write failure must not fail the run.
	if ww, wh := w.WindowSize(); ww > 0 && wh > 0 {
		s.Width, s.Height = ww, wh
	}
	s.Theme = app.themePref
	_ = saveSettings(path, s)

	return nil
}

// startPoller reads the status immediately and then every pollInterval, posting
// each snapshot to the main thread (SPEC §15.4).  It stops with the window.
func startPoller(w *gogui.Window, src statusSource, refresh chan struct{}) {
	ctx := w.Ctx()

	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		for {
			snap := readOnce(ctx, src)
			w.QueueCommand(func(win *gogui.Window) {
				gogui.State[App](win).snap = snap
				win.InvalidateLayout()
			})

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-refresh:
			}
		}
	}()
}

// readOnce performs one bounded read.  A read error becomes an empty snapshot
// carrying a secret-free message rather than an error the poller would drop.
func readOnce(ctx context.Context, src statusSource) (snap snapshot) {
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	snap, err := src.Read(rctx)
	if err != nil {
		return snapshot{trust: trustVerified, updated: time.Now(),
			err: "could not read status"}
	}
	if snap.updated.IsZero() {
		snap.updated = time.Now()
	}

	return snap
}

// applyTheme pins the window theme from the stored preference.
func applyTheme(w *gogui.Window, pref string) {
	switch pref {
	case themeLight:
		w.SetTheme(gogui.ThemeLight)
	case themeDark:
		w.SetTheme(gogui.ThemeDark)
	default:
		w.FollowSystemAppearance(gogui.ThemeLight, gogui.ThemeDark)
	}
}

// cycleTheme advances the theme preference and applies it.  Runs on the main
// thread from the button callback.
func cycleTheme(ctx gogui.EventCtx) {
	w := ctx.Window
	app := gogui.State[App](w)

	switch app.themePref {
	case themeSystem:
		app.themePref = themeLight
	case themeLight:
		app.themePref = themeDark
	default:
		app.themePref = themeSystem
	}

	applyTheme(w, app.themePref)
	w.InvalidateLayout()
}

// requestRefresh asks the poller for an immediate read without blocking.
func requestRefresh(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)

	select {
	case app.refresh <- struct{}{}:
	default:
	}
}

// themeLabel names the current theme preference for the toggle button.
func themeLabel(pref string) (label string) {
	switch pref {
	case themeLight:
		return "Theme: Light"
	case themeDark:
		return "Theme: Dark"
	default:
		return "Theme: Auto"
	}
}
