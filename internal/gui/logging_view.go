//go:build gui

package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/daika7ana/sigil-dns/internal/config"
)

// logViewerDefaultInterval is the initial refresh cadence of the live viewer,
// in seconds.  0 would mean Disabled; the page opens live, refreshed about once
// a second.
const logViewerDefaultInterval = 1

// logViewerHeight is the fixed height of the read-only log view.  A fixed box
// (rather than a content-sized one) gives the viewer its own scroll region, so
// a long tail never grows the page.  The height is about five text lines taller
// than the original 240 px; the toolkit exposes no line-height metric, so the
// bump is a fixed 100 px derived from the tuned body size (14 px) times the
// toolkit's 1.4 line factor (≈20 px/line) times five.
const logViewerHeight float32 = 340

// logViewID is the viewer's widget ID.  It contains the id separator, so it is
// its own scope root: its effective ID — and therefore its scroll-region ID —
// is exactly this string.
const logViewID = "gui:log:view"

// logScrollBottom is a finite offset below any real content.  Writing it (via
// ScrollY) and invalidating the layout lets the next position pass clamp it to
// the true bottom of the just-read text.  ScrollVerticalToPct cannot be used:
// it clamps against the previous frame's geometry, so it would lag a poll
// behind the newest lines.
const logScrollBottom float32 = -1e9

// maxTailBytes caps the tail kept in the viewer: a log larger than this keeps
// only its newest window (LOGGING_PLAN §4.7).  It stays under go-glyph's
// MaxTextLength (10240 bytes), a hard cap the shaper enforces: a single text
// over it fails to shape and go-gui then paints nothing, so a tail at or above
// the cap would blank the whole viewer.  8 KiB keeps a margin below the limit.
// The service, not the GUI, owns the file; the GUI never holds a write handle.
const maxTailBytes = 8 << 10

// logDraft is the Logging page's edit buffer: the level and the file path.
// A blank path means the platform default.  It is deliberately decoupled from
// internal/config so the page and its tests build and run without the backend.
type logDraft struct {
	level string
	path  string
}

// logViewCtl is the cross-thread control for the live viewer: the refresh
// interval in seconds (0 = Disabled, no reading) and the file being tailed.
// The main thread writes both and the poller goroutine reads them, so they are
// atomics and never locked.
type logViewCtl struct {
	interval atomic.Int32
	path     atomic.Pointer[string]
}

// logTail is the incremental reader behind the viewer.  It remembers the byte
// offset it has already shown and appends only the bytes written since, so a
// refresh never re-reads the whole file.  One goroutine (the poller) owns it.
type logTail struct {
	path   string
	text   string
	offset int64
	last   time.Time
}

// read appends the bytes written to path since the previous read.  interval is
// the refresh cadence in seconds; 0 disables reading entirely, and a call
// before the interval has elapsed is a no-op.  It reports whether the tail
// changed.  A file that shrank (rotated away) restarts from the top; a missing
// or unreadable file leaves the tail untouched.
func (t *logTail) read(path string, interval int, now time.Time) (changed bool) {
	if path == "" || interval <= 0 {
		return false
	}
	if now.Sub(t.last) < time.Duration(interval)*time.Second {
		return false
	}
	t.last = now

	if path != t.path {
		t.path = path
		t.offset = 0
		t.text = ""
	}

	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return false
	}
	if info.Size() < t.offset {
		t.offset = 0
		t.text = ""
	}
	if info.Size() == t.offset {
		return false
	}

	buf := make([]byte, info.Size()-t.offset)
	n, _ := f.ReadAt(buf, t.offset)
	if n <= 0 {
		return false
	}
	t.offset += int64(n)
	t.text = capTail(t.text + string(buf[:n]))

	return true
}

// capTail trims the buffer to its newest maxTailBytes, dropping a partial
// leading line so the view starts on a line boundary.
func capTail(s string) (out string) {
	if len(s) <= maxTailBytes {
		return s
	}
	s = s[len(s)-maxTailBytes:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}

	return s
}

// loggingPage is the Logging screen: the level and file controls that write
// through the elevated apply path, and a live read-only viewer of the log tail.
// It mirrors the Settings editor's card/field-group language.
func loggingPage(t gogui.Theme, app *App, windowWidth int) gogui.View {
	app.ensureLogDraft()
	app.syncLogViewer()

	floor := selectFloor(windowWidth)

	content := withEditNotice(t, app, nil)
	content = append(content,
		logConfigCard(t, app, floor),
		logViewCard(t, app),
	)

	return pageColumn("gui:logging", content)
}

// logConfigCard is the writable half: the level selector, the file path with a
// Browse picker, and the Apply action with its next-start caption.
func logConfigCard(t gogui.Theme, app *App, floor float32) gogui.View {
	d := app.log
	disabled := app.busy || !canEdit(app.snap)

	return card(t, "gui:card:logcfg", "Logging", []gogui.View{
		fieldGroup(t, "gui:log:group:level", "Level",
			formSelect(gogui.SelectCfg{
				ID:      "gui:log:level",
				Options: logLevelOptions(), Selected: one(d.level),
				Disabled: disabled,
				OnSelect: func(vals []string, ctx gogui.EventCtx) { setLogLevel(ctx, vals) },
			}, floor),
			caption(t, "Verbose records DNS query names and is only for troubleshooting."),
		),
		gogui.Separator(gogui.SeparatorCfg{
			ID:    "gui:log:divider:1",
			Color: t.ColorBorder,
		}),
		fieldGroup(t, "gui:log:group:file", "File",
			gogui.InputGroup(gogui.InputGroupCfg{
				ID:     "gui:log:pathrow",
				Sizing: gogui.FillFit,
				Segments: []gogui.InputGroupSegment{
					gogui.InputGroupInput(gogui.InputCfg{
						ID: "gui:log:path", Sizing: gogui.FillFit,
						Text: d.path, Placeholder: defaultPathHint(app),
						Disabled:      disabled,
						OnTextChanged: func(text string, ctx gogui.EventCtx) { setLogPathText(ctx, text) },
					}),
					gogui.InputGroupButton(gogui.ButtonCfg{
						ID: "gui:log:browse", Variant: gogui.ButtonSecondary, Label: "Browse",
						Sizing: gogui.FitFit, Disabled: disabled, OnClick: browseLogPath,
					}),
				},
			}),
			caption(t, "Leave blank to use the default location."),
		),
		logApplyRow(t, disabled),
	})
}

// logApplyRow is the Apply button and its exact next-start caption.  The
// caption is deliberate and sits ABOVE the button: a log change is saved
// immediately but the service only picks it up on its next start (LOGGING_PLAN
// §1.6), so the note reads before the action.
func logApplyRow(t gogui.Theme, disabled bool) gogui.View {
	return primaryActionRow("gui:log:apply", "Apply", disabled, runApplyLog,
		logInfoNote(t, "Takes effect on next service start."))
}

// logInfoNote is a compact single-line inline note: a thin leading info-accent
// stripe plus one line, the same visual language as the `notice` banner (see
// notice/noticeAccent) without its title + detail stack.
func logInfoNote(t gogui.Theme, text string) gogui.View {
	return gogui.Row(gogui.ContainerCfg{
		ID:         "gui:log:apply:note",
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingSmall,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		VAlign:     gogui.VAlignMiddle,
		Content: []gogui.View{
			// The info stripe: a 3px rounded bar spanning the note's height,
			// the same accent the notice banner uses for an informational note.
			severityStripe(t, "gui:log:apply:note:accent", noticeInfo),
			gogui.Text(gogui.TextCfg{
				Text:      text,
				TextStyle: t.TextStyleSecondary,
				Sizing:    gogui.FillFit,
				Mode:      gogui.TextModeWrap,
			}),
		},
	})
}

// logViewCard is the read-only half: the refresh-interval slider and the fixed
// log view it feeds.
func logViewCard(t gogui.Theme, app *App) gogui.View {
	return card(t, "gui:card:logview", "Live log", []gogui.View{
		logRefreshRow(t, app),
		gogui.Input(gogui.InputCfg{
			// FillFixed (fill width, Fixed height) is what actually clamps the
			// box: a Fixed height pins Min = Max = Height, so the viewer stays
			// logViewerHeight tall and its text scrolls inside it.  A Fill/Fit
			// height is content-summed by the layout pass and ignores the stated
			// Height, so the box grew with the log.
			ID: logViewID, Sizing: gogui.FillFixed, Height: logViewerHeight,
			Mode: gogui.InputMultiline, Scrollable: true, ReadOnly: true,
			TextStyle: t.TextStyleCodeSmall,
			Text:      app.logText, Placeholder: "No log output yet.",
		}),
		logActionsRow(t, app),
	})
}

// logActionsRow holds the two file actions under the viewer: Open (hand the log
// to the OS default handler) and Clear (empty it in place, after a confirm).
// Both are inert until there is a resolved path to act on.
func logActionsRow(t gogui.Theme, app *App) gogui.View {
	havePath := app.effectiveLogPath() != ""

	return gogui.Row(gogui.ContainerCfg{
		Sizing:     gogui.FillFit,
		Spacing:    gogui.SpacingSmall,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		VAlign:     gogui.VAlignMiddle,
		Content: []gogui.View{
			gogui.Button(gogui.ButtonCfg{
				ID: "gui:log:open", Variant: gogui.ButtonSecondary, Label: "Open log file",
				Sizing: gogui.FitFit, Disabled: !havePath, OnClick: runOpenLog,
			}),
			// Emptying the log destroys data: the subordinate danger outline,
			// matching the per-row Remove controls.
			dangerOutlineButton(t, "gui:log:clear", "Clear log file", !havePath, confirmClearLog),
		},
	})
}

// pinLogToBottom sticks the viewer to its newest line.  It stores a
// far-past-bottom offset and asks for a layout rebuild, so layoutAdjustScroll-
// Offsets clamps the stored value to the true bottom of the just-read text and
// the position pass shows the newest lines.  Only the Logging page has the
// viewer, so callers gate on it.
func pinLogToBottom(w *gogui.Window) {
	w.ScrollY().Set(logViewID, logScrollBottom)
	w.InvalidateLayout()
}

// logRefreshRow is the refresh-interval control: a 0-30 s slider whose 0 stops
// all reading, and a caption naming the selected cadence.
func logRefreshRow(t gogui.Theme, app *App) gogui.View {
	sec := int(app.logView.interval.Load())

	control := gogui.Column(gogui.ContainerCfg{
		Sizing:     gogui.FitFit,
		Spacing:    gogui.SpacingTight,
		Padding:    gogui.PaddingNone,
		SizeBorder: gogui.NoBorder,
		Content: []gogui.View{
			gogui.Slider(gogui.SliderCfg{
				ID: "gui:log:interval", Sizing: gogui.FitFit, Width: 220,
				Min: 0, Max: 30, Step: 1, RoundValue: true,
				Value: float32(sec), OnChange: setLogInterval,
			}),
			gogui.Text(gogui.TextCfg{
				Text: logIntervalLabel(sec), TextStyle: t.TextStyleCaption, Sizing: gogui.FitFit,
			}),
		},
	})

	return settingRow(t, "Refresh interval",
		"How often the view reads new log lines. Off stops reading.", control)
}

// --- draft seeding + cross-thread sync ---

// ensureLogDraft seeds the Logging page's buffer once from the summary, so a
// poll never clobbers a pending edit.  The level defaults to off when the
// summary carries none.  The path is seeded from the CONFIGURED value only: when
// the summary reports a default (LogPathSet false) the field stays blank, so an
// unedited Apply does not bake the resolved default into config.yaml.  Blank
// means "use the default location"; the effective default is shown as a hint
// (see defaultPathHint).
func (a *App) ensureLogDraft() {
	if a.logSeeded {
		return
	}

	level := config.LogLevelOff
	path := ""
	if sum := a.summary(); sum != nil {
		if sum.LogLevel != "" {
			level = sum.LogLevel
		}
		if sum.LogPathSet {
			path = sum.LogPath
		}
	}
	a.log = logDraft{level: level, path: path}
	a.logSeeded = true
}

// syncLogViewer publishes the effective log path to the poller's tail reader.
// It runs on the main thread each poll, so a path changed elsewhere is picked
// up.  The pointer is only replaced when the value changes.
func (a *App) syncLogViewer() {
	path := a.effectiveLogPath()
	if cur := a.logView.path.Load(); cur != nil && *cur == path {
		return
	}
	a.logView.path.Store(&path)
}

// effectiveLogPath is the resolved log file path from the summary — the
// configured override, or the platform default the summary already fills in.
func (a *App) effectiveLogPath() (path string) {
	if sum := a.summary(); sum != nil {
		return sum.LogPath
	}

	return ""
}

// defaultPathHint is the path field's placeholder: the effective default the
// helper resolves for a blank path, so the user can see where the log will go
// without that path being written into the config.
func defaultPathHint(app *App) (hint string) {
	if p := app.effectiveLogPath(); p != "" {
		return "Default: " + p
	}

	return "Default location"
}

// --- controls ---

// logLevelOptions lists the four levels as label/value pairs; the value is the
// config level string the helper validates.
func logLevelOptions() (opts []gogui.SelectOption) {
	return []gogui.SelectOption{
		gogui.NewSelectOption("Off", config.LogLevelOff),
		gogui.NewSelectOption("Errors only", config.LogLevelError),
		gogui.NewSelectOption("Normal", config.LogLevelInfo),
		gogui.NewSelectOption("Verbose", config.LogLevelDebug),
	}
}

// logIntervalLabel renders the refresh cadence for the slider's value.
func logIntervalLabel(sec int) (s string) {
	switch {
	case sec <= 0:
		return "Off"
	case sec == 1:
		return "1 second"
	default:
		return fmt.Sprintf("%d seconds", sec)
	}
}

// setLogLevel stores the chosen level in the buffer.
func setLogLevel(ctx gogui.EventCtx, vals []string) {
	app := gogui.State[App](ctx.Window)
	app.log.level = first(vals)
	app.logSeeded = true
	ctx.Window.InvalidateLayout()
}

// setLogPathText stores the typed path in the buffer.
func setLogPathText(ctx gogui.EventCtx, text string) {
	app := gogui.State[App](ctx.Window)
	app.log.path = text
	app.logSeeded = true
	ctx.Window.InvalidateLayout()
}

// setLogInterval applies a slider change.  0 stores Disabled, which the poller
// reads as "do not read".
func setLogInterval(v float32, ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	app.logView.interval.Store(int32(v))
	ctx.Window.InvalidateLayout()
}

// runApplyLog launches the log-save action through the elevated apply seam,
// carrying the edited level and path.
func runApplyLog(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	d := app.log
	runRequest(ctx.Window, applyRequest{action: actionSaveLog, log: &d})
}

// browseLogPath opens the native save picker to choose the log file.  A blank
// field opens in the platform default directory; the file need not exist yet.
func browseLogPath(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	dir, name := filepath.Split(strings.TrimSpace(app.log.path))

	ctx.Window.NativeSaveDialog(gogui.NativeSaveDialogCfg{
		Title:       "Choose log file",
		StartDir:    dir,
		DefaultName: fallback(name, "sigildns.log"),
		Filters:     []gogui.NativeFileFilter{{Name: "Log files", Extensions: []string{"log"}}},
		OnDone: func(res gogui.NativeDialogResult, w *gogui.Window) {
			if res.Status != gogui.DialogOK {
				return
			}
			if paths := res.PathStrings(); len(paths) > 0 {
				setLogPath(w, paths[0])
			}
		},
	})
}

// setLogPath stores a path chosen from the native picker.
func setLogPath(w *gogui.Window, path string) {
	app := gogui.State[App](w)
	app.log.path = path
	app.logSeeded = true
	w.InvalidateLayout()
}

// runOpenLog opens the effective log file with the OS default handler.  A
// missing file is reported instead of launching; the opener validates the path
// before the shell sees it.
func runOpenLog(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	path := app.effectiveLogPath()
	if path == "" {
		showLogNotice(ctx, noticeWarning, "No log file", "Set a log location first.")

		return
	}
	if _, err := os.Stat(path); err != nil {
		showLogNotice(ctx, noticeWarning, "Log file not found", "There is no log file at that location yet.")

		return
	}
	if err := openPath(path); err != nil {
		showLogNotice(ctx, noticeError, "Could not open the log file", err.Error())
	}
}

// confirmClearLog asks before emptying the log file.  The file is only cleared
// once the confirm dialog is accepted.
func confirmClearLog(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	path := app.effectiveLogPath()
	if path == "" {
		showLogNotice(ctx, noticeWarning, "No log file", "Set a log location first.")

		return
	}

	ctx.Window.Dialog(gogui.DialogCfg{
		DialogType: gogui.DialogConfirm,
		Title:      "Clear log file",
		Body:       "Delete the log file's contents? This cannot be undone.",
		FocusID:    "gui:log:clear:confirm",
		OnOkYes:    func(w *gogui.Window) { clearLogFile(w, path) },
	})
}

// clearLogFile empties the log in place.  os.Truncate, not remove-and-recreate:
// the running service holds the file open and only creates it at service start,
// so truncating is what keeps it writing to the same path.
func clearLogFile(w *gogui.Window, path string) {
	if _, err := os.Stat(path); err != nil {
		showLogNotice(gogui.EventCtx{Window: w}, noticeWarning, "Log file not found",
			"There is no log file at that location yet.")

		return
	}
	if err := os.Truncate(path, 0); err != nil {
		showLogNotice(gogui.EventCtx{Window: w}, noticeError, "Could not clear the log file", err.Error())

		return
	}

	app := gogui.State[App](w)
	// Drop the shown tail now.  The poller's reader restarts at the top when it
	// next sees the shrunken size, so the view stays empty until the service
	// appends again.
	app.logText = ""
	if app.page == pageLogging {
		pinLogToBottom(w)
	}
	showLogNotice(gogui.EventCtx{Window: w}, noticeSuccess, "Log file cleared", "")
}

// showLogNotice shows a file-action toast and repaints it.
func showLogNotice(ctx gogui.EventCtx, kind noticeKind, title, body string) {
	gogui.State[App](ctx.Window).showToast(ctx.Window, noticeMessage{kind: kind, title: title, body: body})
	ctx.Window.InvalidateLayout()
}
