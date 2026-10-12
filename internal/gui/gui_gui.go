//go:build gui

package gui

import (
	"context"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend"

	"github.com/daika7ana/dns-client/internal/ipc"
)

// Window defaults.  A saved geometry in gui.json wins.  The Status page is now
// a banner, one power control, and three read-only cards, so the default is
// shorter than it was; the taller Settings page scrolls.
const (
	defaultWidth  = 720
	defaultHeight = 600
	minWidth      = 520
	minHeight     = 480
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

	// page is the visible screen.
	page pageKind

	// summaries is the sanitized per-profile list from the last poll, and
	// profiles its name list (the selector's items).  active is the active
	// profile name, selected is the profile the editor is focused on, and
	// draft is the editor buffer for the selected profile.
	summaries []ipc.ProfileSummary
	profiles  []string
	active    string
	selected  string
	draft     profileDraft
	seeded    bool

	// cached is the last sanitized summary, persisted to gui.json so the
	// editor still works while the service is stopped.  cachedRev is the
	// revision token that produced it (compared to avoid rewriting the file
	// every poll).  It never holds an id or upstream URL — a ConfigSummary
	// has none (SPEC §15.2).
	cached    *ipc.ConfigSummary
	cachedRev string

	// settings is the loaded gui.json, kept so a cached-summary write
	// preserves geometry and theme.
	settings settings

	// busy is true while an elevated action is in flight; busyNote explains
	// what it is waiting on.  toast is the last action result, shown as a
	// transient floating toast; toastSeq guards its auto-dismiss timer, and
	// toastTTL overrides the default lifetime (tests shorten it).
	busy     bool
	busyNote string
	toast    *noticeMessage
	toastSeq uint64
	toastTTL time.Duration
}

// Run opens the client window and blocks until it is closed.
func Run(cfg Config) (err error) {
	// Detach from the inherited console first: the binary is console-subsystem,
	// so a double-click would otherwise leave a blank cmd window open for the
	// whole GUI session.  GUI path only; the CLI commands never do this.
	detachConsole()

	path := cfg.GuiPath
	if path == "" {
		path = defaultGuiPath()
	}

	// Single instance: a second launch must not open a second window.  The
	// lock is user-scope (beside gui.json) and taken before the window exists;
	// a conflict exits quietly.  GUI path only.
	lock, lockErr := acquireInstance(path)
	if lockErr != nil {
		return lockErr
	}
	if lock == nil {
		return nil // another instance is already open
	}
	defer func() { _ = lock.Release() }()

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
		settings:  s,
		cached:    s.Summary,
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
	s.Summary = app.cached
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
				a := gogui.State[App](win)
				a.snap = snap
				a.seed(snap)
				a.cacheSummary(snap)
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

// setThemePref applies a theme choice from the header control and remembers it.
func setThemePref(ctx gogui.EventCtx, pref string) {
	app := gogui.State[App](ctx.Window)
	app.themePref = pref
	app.settings.Theme = pref

	applyTheme(ctx.Window, pref)
	ctx.Window.InvalidateLayout()
}

// themeOptions lists the theme choices for the header segmented control.
func themeOptions() (opts []gogui.SegmentOption) {
	return []gogui.SegmentOption{
		gogui.NewSegmentOption("Auto", themeSystem),
		gogui.NewSegmentOption("Light", themeLight),
		gogui.NewSegmentOption("Dark", themeDark),
	}
}

// requestRefresh asks the poller for an immediate read without blocking.
func requestRefresh(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)

	select {
	case app.refresh <- struct{}{}:
	default:
	}
}

// cacheSummary remembers the last sanitized summary so the editor works while
// the service is stopped, persisting it to gui.json only when the config
// revision changes.  The revision token is stripped before caching: a cached
// summary has no live revision, so a save staged from it sends an empty
// ExpectedRevision.
func (a *App) cacheSummary(s snapshot) {
	if s.summary == nil || s.summary.Revision == a.cachedRev {
		return
	}
	a.cachedRev = s.summary.Revision

	sum := *s.summary
	sum.Revision = ""
	a.cached = &sum

	a.settings.Summary = a.cached
	if a.guiPath != "" {
		_ = saveSettings(a.guiPath, a.settings)
	}
}

// summary is the config summary the editor reads: the live one while the
// service is running, else the cached last-good one.
func (a *App) summary() *ipc.ConfigSummary {
	if a.snap.summary != nil {
		return a.snap.summary
	}

	return a.cached
}
