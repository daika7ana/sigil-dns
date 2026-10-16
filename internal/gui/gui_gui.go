//go:build gui

package gui

import (
	"context"
	_ "embed"
	"sync/atomic"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend"

	"github.com/daika7ana/dns-client/internal/ipc"
)

// appIconPNG is the window icon (title bar + taskbar), embedded so the run
// time has it without a file beside the exe.  The toolkit otherwise paints its
// own default icon on the window even though the exe resource carries ours.
//
//go:embed appicon.png
var appIconPNG []byte

// Window defaults.  A saved geometry in gui.json wins.  The window opens at
// 1280x720 out of the box; the taller Settings page scrolls when its content
// does not fit.
const (
	defaultWidth  = 1280
	defaultHeight = 720
	minWidth      = 980
	minHeight     = 620
)

// windowTitle is the native window title.  The position shim finds this
// process's window by it.
const windowTitle = "dns-client"

// pollInterval is the live status refresh cadence while the window is focused
// (SPEC §15.4: 1 s).
const pollInterval = 1 * time.Second

// safetyInterval is the full-read cadence while focused: the live read only
// refreshes the SCM state and status DTO, so this picks up a changed config (or
// a missed service-state transition) without a manual refresh.
const safetyInterval = 30 * time.Second

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

	// listInput is the not-yet-added filter-list URL typed into the editor's
	// add-list field.  It is transient UI state: it becomes a draft row on Add,
	// and it never holds a value read from the config.
	listInput string

	// log is the Logging page's edit buffer (level + path), seeded once from
	// the summary; logSeeded guards that seeding so a poll never clobbers a
	// pending edit.  logView is the cross-thread live-viewer control and
	// logText is the tail the poller has read (written and read on the main
	// thread only).
	log       logDraft
	logSeeded bool
	logView   logViewCtl
	logText   string

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
func Run() (err error) {
	// Detach from the inherited console first: the binary is console-subsystem,
	// so a double-click would otherwise leave a blank cmd window open for the
	// whole GUI session.  GUI path only; the CLI commands never do this.
	detachConsole()

	path := defaultGuiPath()

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
	app.logView.interval.Store(logViewerDefaultInterval)
	src := newSource()

	// focused pauses the poller while the window is unfocused/minimized.  It is
	// shared with the poller goroutine (atomic) and flipped from OnEvent.
	var focused atomic.Bool
	focused.Store(true)

	// The position is captured on close, while the native window still exists
	// (OnCloseRequest runs before destroy), and applied on open from OnInit.
	var posX, posY int
	var havePos bool

	// go-gui shows the window's first frame before OnInit runs, so applying a
	// saved position there would visibly jump.  When there is one, start the
	// window fully transparent (the backend replays this before showing it),
	// apply the position while it is invisible, then reveal it — no jump.
	restoring := s.X != 0 || s.Y != 0

	w := gogui.NewWindow(gogui.WindowCfg{
		Title:     windowTitle,
		IconPNG:   appIconPNG,
		Width:     s.Width,
		Height:    s.Height,
		MinWidth:  minWidth,
		MinHeight: minHeight,
		State:     app,
		OnEvent: func(e *gogui.Event, _ *gogui.Window) {
			switch e.Type {
			case gogui.EventFocused:
				focused.Store(true)
				requestRefresh(app.refresh) // refetch at once; the data may be stale
			case gogui.EventUnfocused:
				focused.Store(false) // pause the poller while backgrounded
			}
		},
		OnInit: func(w *gogui.Window) {
			applyTheme(w, app.themePref)
			w.SetView(view)
			startPoller(w, src, app.refresh, &app.logView, &focused)
			if restoring {
				restoreWindowPosition(windowTitle, s.X, s.Y)
			}
			focusWindow(windowTitle)
			if restoring {
				w.SetWindowOpacity(1)
			}
		},
		OnCloseRequest: func(win *gogui.Window) {
			posX, posY, havePos = windowPosition(windowTitle)
			win.Close()
		},
	})

	if restoring {
		// Replayed by the backend before the window is shown; see OnInit.
		w.SetWindowOpacity(0)
	}

	// Blocks on the main thread until the window closes.
	backend.Run(w)

	// Persist geometry, position + theme preference.  Best-effort: gui.json is
	// not a security boundary and a write failure must not fail the run.
	if ww, wh := w.WindowSize(); ww > 0 && wh > 0 {
		s.Width, s.Height = ww, wh
	}
	if havePos {
		s.X, s.Y = posX, posY
	}
	s.Theme = app.themePref
	s.Summary = app.cached
	_ = saveSettings(path, s)

	return nil
}

// startPoller reads the status immediately, then cheaply (SCM + status DTO)
// every pollInterval while focused, with a full read every safetyInterval, on a
// refresh signal, and once at start (SPEC §15.4).  An unchanged read pushes
// nothing and requests no repaint, so an idle window leaves the on-demand render
// loop asleep.  It stops with the window.  It also tails the log file for the
// Logging page's live viewer, off the frame thread, while focused.
func startPoller(w *gogui.Window, src statusSource, refresh chan struct{}, lv *logViewCtl, focused *atomic.Bool) {
	ctx := w.Ctx()

	go func() {
		live := time.NewTicker(pollInterval)
		defer live.Stop()
		safety := time.NewTicker(safetyInterval)
		defer safety.Stop()

		tail := &logTail{}

		// last is the most recent read: the merge baseline and the source of the
		// heavy fields a live read does not fetch.  applied is what the UI
		// currently shows, so only a change against it is worth a repaint.
		var last, applied snapshot

		// process folds one read into the UI.  A live read whose service state
		// moved is replaced by a full read, because only that switches the
		// summary source (live vs offline) and refetches the config.
		process := func(snap snapshot, full bool) {
			if !full && liveStateChanged(last, snap) {
				snap = readOnce(ctx, src)
				full = true
			}
			if !full {
				snap.summary = last.summary
				snap.info = last.info
			}
			last = snap

			if samePoll(applied, snap) {
				return // unchanged: push nothing, so the render loop stays idle
			}
			snap.updated = time.Now()
			applied = snap

			w.QueueCommand(func(win *gogui.Window) {
				a := gogui.State[App](win)
				a.snap = snap
				a.seed(snap)
				a.syncLogViewer()
				a.cacheSummary(snap)
				win.InvalidateLayout()
			})
		}

		// tailLog appends newly written log bytes and posts them to the viewer.
		// The reader keeps its offset, so a pause while unfocused catches up.
		tailLog := func() {
			p := lv.path.Load()
			if p == nil || !tail.read(*p, int(lv.interval.Load()), time.Now()) {
				return
			}
			text := tail.text
			w.QueueCommand(func(win *gogui.Window) {
				a := gogui.State[App](win)
				a.logText = text
				// The viewer's text is declarative (Text: app.logText); only a
				// layout rebuild re-reads it, so InvalidateRender alone never
				// repaints it.  pinLogToBottom both asks for that rebuild and
				// pins the box to the newest line.  Gated to the Logging page so
				// other pages are not re-laid-out every tick.
				if a.page == pageLogging {
					pinLogToBottom(win)
				} else {
					win.InvalidateRender()
				}
			})
		}

		process(readOnce(ctx, src), true) // initial full read

		for {
			select {
			case <-ctx.Done():
				return
			case <-live.C:
				if !focused.Load() {
					continue // paused: no read, no tail, no repaint
				}
				tailLog()
				process(readLiveOnce(ctx, src), false)
			case <-safety.C:
				if !focused.Load() {
					continue
				}
				tailLog()
				process(readOnce(ctx, src), true)
			case <-refresh:
				tailLog()
				process(readOnce(ctx, src), true)
			}
		}
	}()
}

// liveStateChanged reports whether a live read moved the service lifecycle
// state, which needs a full read: only a full read refetches the summary (e.g.
// the service just stopped, so offlineSummary must replace the live one).
func liveStateChanged(a, b snapshot) bool {
	return a.installed != b.installed || a.running != b.running || a.scmState != b.scmState
}

// requestRefresh asks the poller for an immediate full read without blocking:
// the channel is buffered, and a request already pending covers this one.
func requestRefresh(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// readOnce performs one bounded full read.  A read error becomes an empty
// snapshot carrying a secret-free message rather than an error the poller would
// drop.
func readOnce(ctx context.Context, src statusSource) (snap snapshot) {
	return readWith(ctx, src.Read)
}

// readLiveOnce performs one bounded live read (SCM state + status DTO only).
func readLiveOnce(ctx context.Context, src statusSource) (snap snapshot) {
	return readWith(ctx, src.ReadLive)
}

// readWith runs one read under a timeout, normalising an error and a missing
// timestamp so the callers never drop a read.
func readWith(ctx context.Context, read func(context.Context) (snapshot, error)) (snap snapshot) {
	rctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()

	snap, err := read(rctx)
	if err != nil {
		return snapshot{trust: trustVerified, updated: time.Now()}
	}
	if snap.updated.IsZero() {
		snap.updated = time.Now()
	}

	return snap
}

// applyTheme pins the window theme from the stored preference.  The Windows
// WinUI pair is the token base for the whole UI: hairlines, the squarer radius
// ladder, the WinUI accent, and the Win11 blue-to-cyan selection ramp.  It
// falls back to the toolkit light/dark presets if the built-in Windows themes
// are ever unavailable.
func applyTheme(w *gogui.Window, pref string) {
	switch pref {
	case themeLight:
		w.SetTheme(windowsTheme("windows", gogui.ThemeLight))
	case themeDark:
		w.SetTheme(windowsTheme("windows-dark", gogui.ThemeDark))
	default:
		w.FollowSystemAppearance(
			windowsTheme("windows", gogui.ThemeLight),
			windowsTheme("windows-dark", gogui.ThemeDark))
	}
}

// WinUI base-theme calibration.  The built-in Windows theme is the token base;
// these tune it for the app without pinning a font family or changing structure.
const (
	// themeScrollbarSize is the bar thickness.  WinUI ships 10 (chunky); the
	// app asks for a thinner, quieter bar.
	themeScrollbarSize float32 = 6
	// themeScrollbarGap insets the bar from the edge it tracks, and
	// themeScrollbarGapEnd shortens the track at its ends, so the thumb reads
	// as floating in the gutter rather than hugging the card edge.
	themeScrollbarGap    float32 = 2
	themeScrollbarGapEnd float32 = 6
	// themeFontDelta bumps the whole ladder up two steps (body 12 -> 14); the
	// derived rungs follow.  Bounds are defensive; AdjustFontSize errors out of
	// range and the untuned theme is kept.
	themeFontDelta float32 = 2
	themeFontMin   float32 = 8
	themeFontMax   float32 = 28
)

// windowsTheme returns the registered Windows theme by name, calibrated for the
// app, or the toolkit preset as a fallback (also calibrated).
func windowsTheme(name string, fallback gogui.Theme) gogui.Theme {
	base, ok := gogui.ThemeGet(name)
	if !ok {
		base = fallback
	}

	return tuneTheme(base)
}

// tuneTheme applies the app's calibration to a base theme: a thinner scrollbar
// with a larger edge inset, and a two-step font-ladder bump.  Rebuilt from the
// theme's own Cfg so nothing else moves, and side-effect-free.
func tuneTheme(base gogui.Theme) gogui.Theme {
	cfg := base.Cfg
	cfg.SizeScrollbar = themeScrollbarSize
	cfg.SizeScrollbarGap = gogui.SomeF(themeScrollbarGap)
	cfg.SizeScrollbarGapEnd = gogui.SomeF(themeScrollbarGapEnd)

	t := gogui.ThemeMaker(cfg)
	if adjusted, err := t.AdjustFontSize(themeFontDelta, themeFontMin, themeFontMax); err == nil {
		return adjusted
	}

	return t
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
