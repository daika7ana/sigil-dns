//go:build gui

package gui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/daika7ana/sigil-dns/internal/config"
	"github.com/daika7ana/sigil-dns/internal/elevate"
)

// goGlyphMaxTextLength is go-glyph's hard single-text cap (validation_common.go
// MaxTextLength). A text over it fails to shape and go-gui paints nothing, so
// the viewer's retained tail must stay below it.
const goGlyphMaxTextLength = 10_240

// TestMaxTailBytesUnderGlyphCap guards against a future bump that would feed the
// shaper a text it refuses, blanking the viewer.
func TestMaxTailBytesUnderGlyphCap(t *testing.T) {
	if maxTailBytes > goGlyphMaxTextLength {
		t.Fatalf("maxTailBytes = %d exceeds go-glyph MaxTextLength %d; the viewer would blank",
			maxTailBytes, goGlyphMaxTextLength)
	}
}

// TestLogViewerHeightClamped proves a long tail scrolls inside the fixed log box
// rather than growing it: the viewer's height stays at logViewerHeight while the
// text is far taller.
func TestLogViewerHeightClamped(t *testing.T) {
	app := loggingApp()
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "line %03d\n", i)
	}
	app.logText = b.String() // well under the glyph cap, far taller than 240 px

	w, root := layoutFor(t, app, defaultWidth, defaultHeight)

	view := mustFindLayout(t, w, root, "gui:log:view")
	if view.Shape.Height > logViewerHeight+1 {
		t.Fatalf("log viewer height %.0f, want <= %.0f (it grew with content)",
			view.Shape.Height, logViewerHeight)
	}
	if view.Shape.Height < 1 {
		t.Fatalf("log viewer collapsed to %.0f", view.Shape.Height)
	}
	assertNoFindings(t, w)
}

// loggingApp is a verified App on the Logging page with a healthy snapshot and
// a live viewer cadence, so the page renders its controls and viewer.
func loggingApp() *App {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()
	app.snap.summary.LogLevel = config.LogLevelInfo
	app.snap.summary.LogPath = filepath.Join("ProgramData", "SigilDNS", "sigildns.log")
	app.page = pageLogging
	app.logView.interval.Store(logViewerDefaultInterval)

	return app
}

// TestLogViewerShowsTailText proves the declarative viewer reads app.logText on
// a layout build: the text set on the App is present inside gui:log:view.
func TestLogViewerShowsTailText(t *testing.T) {
	const tail = "2026-10-16T12:00:00 INF resolver started\n2026-10-16T12:00:01 DBG query example.com\n"

	app := loggingApp()
	app.logText = tail

	w, root := layoutFor(t, app, defaultWidth, defaultHeight)

	texts := textSet(mustFindLayout(t, w, root, "gui:log:view"))
	if !containsText(texts, "resolver started") || !containsText(texts, "query example.com") {
		t.Fatalf("viewer does not show the tail %q; texts=%v", tail, texts)
	}
	assertNoFindings(t, w)
}

// TestLogViewerRepaintsOnLayoutRebuild is the regression guard: after app.logText
// changes, a layout rebuild must repaint the viewer (the bug was a render-only
// refresh, which never re-reads the declarative text).
func TestLogViewerRepaintsOnLayoutRebuild(t *testing.T) {
	app := loggingApp()
	app.logText = "first line\n"

	w, root := layoutFor(t, app, defaultWidth, defaultHeight)
	if texts := textSet(mustFindLayout(t, w, root, "gui:log:view")); !containsText(texts, "first line") {
		t.Fatalf("initial text not shown; texts=%v", texts)
	}

	app.logText = "first line\nappended line\n"
	w.InvalidateLayout()
	root = w.TestRender(nil)

	if texts := textSet(mustFindLayout(t, w, root, "gui:log:view")); !containsText(texts, "appended line") {
		t.Fatalf("viewer did not repaint after a layout rebuild; texts=%v", texts)
	}
	assertNoFindings(t, w)
}

// TestLogViewerUpdatesThroughPoller drives the real poller off-thread and proves
// the appended bytes reach the viewer and the box is pinned to the newest line.
// It is bounded (a generous deadline, no fixed sleep) so it cannot be flaky.
func TestLogViewerUpdatesThroughPoller(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "sigildns.log")
	var b strings.Builder
	for i := 0; i < 150; i++ {
		fmt.Fprintf(&b, "2026-10-16T12:00:%02d INF resolver line %03d\n", i%60, i)
	}
	b.WriteString("2026-10-16T12:00:00 INF resolver started\n")
	if err := os.WriteFile(logPath, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	app := loggingApp()
	app.snap.summary.LogPath = logPath
	app.syncLogViewer()
	app.logView.interval.Store(1)
	app.logText = ""

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	startPoller(w, fakeSource{snap: app.snap}, make(chan struct{}, 1), &app.logView, alwaysFocused(), &app.tray)

	deadline := time.Now().Add(3 * time.Second)
	for app.logText == "" && time.Now().Before(deadline) {
		w.FrameFn()
		time.Sleep(5 * time.Millisecond)
	}
	if app.logText == "" {
		t.Fatal("poller never delivered the log tail")
	}

	root := w.TestRender(nil)
	if texts := textSet(mustFindLayout(t, w, root, logViewID)); !containsText(texts, "resolver started") {
		t.Fatalf("viewer does not show the polled tail; texts=%v", texts)
	}

	ids := w.ResolveID(logViewID)
	if len(ids) == 0 {
		t.Fatalf("viewer %q not rendered", logViewID)
	}
	overflow, ok := w.ScrollOverflowY(ids[0])
	if !ok || overflow <= 0 {
		t.Fatalf("viewer does not overflow: overflow=%v ok=%v", overflow, ok)
	}
	if got := w.ScrollVerticalOffset(ids[0]); absF(got+overflow) > 1 {
		t.Fatalf("poller did not pin to bottom: offset %.1f, overflow %.1f", got, overflow)
	}
	assertNoFindings(t, w)
}

// containsText reports whether any rendered string contains needle.
func containsText(set map[string]bool, needle string) (ok bool) {
	for s := range set {
		if strings.Contains(s, needle) {
			return true
		}
	}

	return false
}

// TestLogViewerAutoScrollsToBottom proves pinLogToBottom lands the viewer at the
// true bottom of a tall tail: after the rebuild its scroll offset equals the
// most-negative reachable offset (the hidden height), so the newest lines show.
func TestLogViewerAutoScrollsToBottom(t *testing.T) {
	app := loggingApp()
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "line %03d\n", i)
	}
	app.logText = b.String()

	w, _ := layoutFor(t, app, defaultWidth, defaultHeight)

	pinLogToBottom(w)
	w.TestRender(nil)

	ids := w.ResolveID(logViewID)
	if len(ids) == 0 {
		t.Fatalf("viewer %q not rendered", logViewID)
	}
	overflow, ok := w.ScrollOverflowY(ids[0])
	if !ok || overflow <= 0 {
		t.Fatalf("viewer does not overflow: overflow=%v ok=%v", overflow, ok)
	}
	if got := w.ScrollVerticalOffset(ids[0]); absF(got+overflow) > 1 {
		t.Fatalf("scroll offset %.1f, want bottom %.1f", got, -overflow)
	}
	assertNoFindings(t, w)
}

// TestLoggingNextStartNoteAboveApply proves the exact next-start wording sits
// above the Apply button and carries the info-accent stripe.
func TestLoggingNextStartNoteAboveApply(t *testing.T) {
	app := loggingApp()
	w, root := layoutFor(t, app, defaultWidth, defaultHeight)

	note := mustFindLayout(t, w, root, "gui:log:apply:note")
	apply := mustFindLayout(t, w, root, "gui:log:apply")

	if note.Shape.Y+note.Shape.Height > apply.Shape.Y+0.5 {
		t.Fatalf("note (y=%.0f h=%.0f) is not above Apply (y=%.0f)",
			note.Shape.Y, note.Shape.Height, apply.Shape.Y)
	}
	if !textSet(note)["Takes effect on next service start."] {
		t.Fatal("next-start note wording changed")
	}

	accent := mustFindLayout(t, w, root, "gui:log:apply:note:accent")
	if want := noticeAccent(gogui.CurrentTheme(), noticeInfo); accent.Shape.Color != want {
		t.Fatalf("note accent color = %+v, want the info accent %+v", accent.Shape.Color, want)
	}
	assertNoFindings(t, w)
}

// TestRulesPageOmitsResolverIDHint proves the resolver-ID line lives on the
// profile editor's Resolver ID field, not on the Rules page (which has no such
// field): a live field names the value to enter, and an inert field says the
// provider uses none.
func TestRulesPageOmitsResolverIDHint(t *testing.T) {
	const (
		liveHint = "Enter your NextDNS configuration ID."
		noIDHint = "This provider does not use a Resolver ID."
	)

	t.Run("rules page omits both", func(t *testing.T) {
		w, root := layoutFor(t, rulesApp(), defaultWidth, defaultHeight)
		text := textSet(mustFindLayout(t, w, root, "gui:rules"))
		if text[liveHint] || text[noIDHint] {
			t.Fatalf("Rules page shows a profile-editor resolver-ID hint")
		}
		assertNoFindings(t, w)
	})

	t.Run("live id field names the value", func(t *testing.T) {
		app := settingsApp()
		app.draft.provider = "nextdns" // a provider that uses a resolver id
		app.draft.variant = "ultralow"
		app.seeded = true // keep the hand-built draft; do not reseed

		w, root := layoutFor(t, app, defaultWidth, defaultHeight)
		editor := textSet(mustFindLayout(t, w, root, "gui:card:editor"))
		if !editor[liveHint] {
			t.Fatalf("profile editor is missing the hint %q", liveHint)
		}
		if editor[noIDHint] {
			t.Errorf("editor shows %q beside a live Resolver ID field", noIDHint)
		}
		if n := len(w.ResolveID("gui:edit:id:hint")); n != 1 {
			t.Errorf("gui:edit:id:hint rendered %d times, want 1", n)
		}
		assertNoFindings(t, w)
	})

	t.Run("no-id field says why", func(t *testing.T) {
		w, root := layoutFor(t, settingsApp(), defaultWidth, defaultHeight)
		editor := textSet(mustFindLayout(t, w, root, "gui:card:editor"))
		if !editor[noIDHint] {
			t.Fatalf("profile editor is missing %q", noIDHint)
		}
		assertNoFindings(t, w)
	})
}

// TestLoggingLevelLabelNotDuplicated proves the Level group prints its label
// once — the fieldGroup header only — matching the File group, which has no
// inner label.
func TestLoggingLevelLabelNotDuplicated(t *testing.T) {
	app := loggingApp()
	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	card := mustFindLayout(t, w, root, "gui:card:logcfg")
	if n := countText(card, "Level"); n != 1 {
		t.Fatalf(`"Level" rendered %d times, want 1`, n)
	}
	assertNoFindings(t, w)
}

// TestOpenLogCallsOpener proves Open hands the resolved path to the opener, and
// refuses to launch when the file is missing.
func TestOpenLogCallsOpener(t *testing.T) {
	orig := openPath
	defer func() { openPath = orig }()

	t.Run("opens the resolved file", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "sigildns.log")
		if err := os.WriteFile(logPath, []byte("x\n"), 0o600); err != nil {
			t.Fatalf("seed log: %v", err)
		}

		var got string
		openPath = func(p string) error { got = p; return nil }

		app := loggingApp()
		app.snap.summary.LogPath = logPath
		w := newSettingsWindow(t, app)
		w.TestRender(nil)

		if err := w.TestClick("gui:log:open"); err != nil {
			t.Fatalf("click open: %v", err)
		}
		if got != logPath {
			t.Fatalf("opener got %q, want %q", got, logPath)
		}
	})

	t.Run("missing file does not launch", func(t *testing.T) {
		called := false
		openPath = func(p string) error { called = true; return nil }

		app := loggingApp()
		app.snap.summary.LogPath = filepath.Join(t.TempDir(), "absent.log")
		w := newSettingsWindow(t, app)
		w.TestRender(nil)

		if err := w.TestClick("gui:log:open"); err != nil {
			t.Fatalf("click open: %v", err)
		}
		if called {
			t.Fatal("opener launched for a missing file")
		}
		if app.toast == nil || app.toast.kind != noticeWarning {
			t.Fatalf("want a warning toast for a missing file, got %+v", app.toast)
		}
	})
}

// TestClearLogRequiresConfirm proves Clear truncates the file only after Yes, and
// No leaves it intact.
func TestClearLogRequiresConfirm(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "sigildns.log")

	newApp := func(t *testing.T) *App {
		t.Helper()
		if err := os.WriteFile(logPath, []byte("some log lines\n"), 0o600); err != nil {
			t.Fatalf("seed log: %v", err)
		}
		app := loggingApp()
		app.snap.summary.LogPath = logPath

		return app
	}

	t.Run("yes truncates", func(t *testing.T) {
		app := newApp(t)
		w := newSettingsWindow(t, app)
		w.TestRender(nil)

		if err := w.TestClick("gui:log:clear"); err != nil {
			t.Fatalf("click clear: %v", err)
		}
		w.TestRender(nil)
		if n := len(w.ResolveID("gui:log:clear:confirm")); n != 1 {
			t.Fatalf("confirm dialog missing (%d)", n)
		}
		if fi, err := os.Stat(logPath); err != nil || fi.Size() == 0 {
			t.Fatalf("file truncated before confirming: %v %v", fi, err)
		}

		if err := w.TestClick("gui:log:clear:confirm:1"); err != nil {
			t.Fatalf("click confirm yes: %v", err)
		}
		w.TestRender(nil)

		fi, err := os.Stat(logPath)
		if err != nil {
			t.Fatalf("stat after clear: %v", err)
		}
		if fi.Size() != 0 {
			t.Fatalf("file size after clear = %d, want 0", fi.Size())
		}
		if app.logText != "" {
			t.Fatalf("viewer tail was not reset: %q", app.logText)
		}
		if app.toast == nil || app.toast.kind != noticeSuccess {
			t.Fatalf("want a success toast, got %+v", app.toast)
		}
		assertNoFindings(t, w)
	})

	t.Run("no keeps", func(t *testing.T) {
		app := newApp(t)
		w := newSettingsWindow(t, app)
		w.TestRender(nil)

		if err := w.TestClick("gui:log:clear"); err != nil {
			t.Fatalf("click clear: %v", err)
		}
		w.TestRender(nil)
		if err := w.TestClick("gui:log:clear:confirm"); err != nil {
			t.Fatalf("click confirm no: %v", err)
		}
		w.TestRender(nil)

		fi, err := os.Stat(logPath)
		if err != nil {
			t.Fatalf("stat after cancel: %v", err)
		}
		if fi.Size() == 0 {
			t.Fatal("file cleared on cancel")
		}
		assertNoFindings(t, w)
	})
}

// TestValidatePath proves the open-path guard accepts local absolute paths and
// rejects empty, option-like, relative and NUL-bearing values.
func TestValidatePath(t *testing.T) {
	for _, p := range []string{
		filepath.Join(t.TempDir(), "sigildns.log"),
		`C:\ProgramData\sigildns\sigildns.log`,
		`\\server\share\sigildns.log`,
	} {
		if got, err := validatePath(p); err != nil || got != p {
			t.Errorf("validatePath(%q) = (%q, %v), want the path", p, got, err)
		}
	}
	for _, p := range []string{"", "   ", "-option", "relative/log", "log\x00name"} {
		if _, err := validatePath(p); err == nil {
			t.Errorf("validatePath(%q) = nil, want error", p)
		}
	}
}

// countText counts rendered text shapes whose text equals want in a layout tree.
func countText(ly *gogui.Layout, want string) (n int) {
	var walk func(*gogui.Layout)
	walk = func(l *gogui.Layout) {
		if l == nil {
			return
		}
		if l.Shape != nil && l.Shape.TC != nil && l.Shape.TC.Text == want {
			n++
		}
		for i := range l.Children {
			walk(&l.Children[i])
		}
	}
	walk(ly)

	return n
}

// TestLoggingLayout proves the Logging page lays out: both cards present,
// content-sized, on one column, with no overlapping blocks.
func TestLoggingLayout(t *testing.T) {
	ids := []string{"gui:card:logcfg", "gui:card:logview"}

	for _, size := range []struct {
		name string
		w, h int
	}{{"default", defaultWidth, defaultHeight}, {"large", wideWidth, wideHeight}} {
		t.Run(size.name, func(t *testing.T) {
			assertPageLayout(t, loggingApp(), size.w, size.h, ids)
		})
	}
}

// TestLoggingPageControls proves every Logging control renders exactly once,
// the Apply caption is the exact next-start wording, and the viewer is a
// scrollable read-only field.
func TestLoggingPageControls(t *testing.T) {
	app := loggingApp()
	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	for _, id := range []string{
		"gui:nav:logging",
		"gui:log:level",
		"gui:log:path", "gui:log:browse",
		"gui:log:apply",
		"gui:log:interval", "gui:log:view",
		"gui:log:open", "gui:log:clear",
	} {
		if n := len(w.ResolveID(id)); n != 1 {
			t.Errorf("%s rendered %d times, want 1", id, n)
		}
	}

	texts := textSet(mustFindLayout(t, w, root, "gui:card:logcfg"))
	if !texts["Takes effect on next service start."] {
		t.Error("Apply caption is missing the exact next-start wording")
	}
	// The path field stays blank and shows the effective default as a hint.
	if app.log.path != "" {
		t.Errorf("path field seeded with %q, want blank", app.log.path)
	}
	if !texts["Default: "+app.snap.summary.LogPath] {
		t.Errorf("path hint does not show the effective default %q", app.snap.summary.LogPath)
	}

	view := mustFindLayout(t, w, root, "gui:log:view")
	if !view.Shape.Scrollable {
		t.Error("log viewer is not scrollable")
	}
	if view.Shape.Height < logViewerHeight-1 {
		t.Errorf("log viewer height %.0f, want >= %.0f", view.Shape.Height, logViewerHeight)
	}

	assertNoFindings(t, w)
}

// TestNavLoggingPage proves the Logging nav item switches to the page.
func TestNavLoggingPage(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if err := w.TestClick("gui:nav:logging"); err != nil {
		t.Fatalf("click logging nav: %v", err)
	}
	w.TestRender(nil)

	if app.page != pageLogging {
		t.Fatalf("page = %v, want logging", app.page)
	}
	if len(w.ResolveID("gui:log:view")) == 0 {
		t.Fatal("Logging page did not render the viewer")
	}
}

// TestNavLoggingPagePinsToNewestLine proves the nav click lands the viewer on
// its newest line: the page is switched from Status with a tall tail already
// read, and the first layout after the click must show the bottom.
func TestNavLoggingPagePinsToNewestLine(t *testing.T) {
	app := loggingApp()
	app.page = pageStatus

	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "line %03d\n", i)
	}
	app.logText = b.String()

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if err := w.TestClick("gui:nav:logging"); err != nil {
		t.Fatalf("click logging nav: %v", err)
	}
	w.TestRender(nil)

	ids := w.ResolveID(logViewID)
	if len(ids) == 0 {
		t.Fatalf("viewer %q not rendered", logViewID)
	}
	overflow, ok := w.ScrollOverflowY(ids[0])
	if !ok || overflow <= 0 {
		t.Fatalf("viewer does not overflow: overflow=%v ok=%v", overflow, ok)
	}
	if got := w.ScrollVerticalOffset(ids[0]); absF(got+overflow) > 1 {
		t.Fatalf("scroll offset %.1f, want bottom %.1f", got, -overflow)
	}
	assertNoFindings(t, w)
}

// TestLoggingSeedsFromSummary proves the buffer takes the configured level but
// leaves the path EMPTY when the summary reports a default: blank means "use the
// default location", so the resolved default must not be seeded (an unedited
// Apply would bake it in).
func TestLoggingSeedsFromSummary(t *testing.T) {
	app := loggingApp()
	app.page = pageStatus

	app.ensureLogDraft()

	if app.log.level != config.LogLevelInfo {
		t.Errorf("level = %q, want info", app.log.level)
	}
	if app.log.path != "" {
		t.Errorf("path = %q, want empty (blank means default)", app.log.path)
	}
	if app.effectiveLogPath() != app.snap.summary.LogPath {
		t.Errorf("effective default = %q, want %q", app.effectiveLogPath(), app.snap.summary.LogPath)
	}
}

// TestLoggingSeedsConfiguredPath proves a CONFIGURED path seeds the field, so the
// user can see and clear it (clearing the field and applying resets the default).
func TestLoggingSeedsConfiguredPath(t *testing.T) {
	app := loggingApp()
	custom := filepath.Join(t.TempDir(), "custom.log")
	app.snap.summary.LogPath = custom
	app.snap.summary.LogPathSet = true
	app.page = pageStatus

	app.ensureLogDraft()

	if app.log.path != custom {
		t.Fatalf("seed path = %q, want the configured %q", app.log.path, custom)
	}
}

// TestLoggingViewerDefaultInterval pins the viewer's initial cadence at about a
// second; 0 would mean Disabled.
func TestLoggingViewerDefaultInterval(t *testing.T) {
	if logViewerDefaultInterval != 1 {
		t.Fatalf("logViewerDefaultInterval = %d, want 1", logViewerDefaultInterval)
	}
}

// TestApplyLogCarriesPatch drives Apply and checks the request builder carries
// the level and path as an elevate patch on ActionApply.
func TestApplyLogCarriesPatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")

	req, ok := buildElevateRequest(applyRequest{
		action: actionSaveLog,
		log:    &logDraft{level: config.LogLevelDebug, path: "  " + path + "  "},
	})
	if !ok {
		t.Fatal("buildElevateRequest returned !ok")
	}
	if req.Action != elevate.ActionApply {
		t.Errorf("action = %q, want apply", req.Action)
	}
	if req.Patch.LogLevel == nil || *req.Patch.LogLevel != config.LogLevelDebug {
		t.Errorf("log level = %v, want debug", req.Patch.LogLevel)
	}
	if req.Patch.LogPath == nil || *req.Patch.LogPath != path {
		t.Errorf("log path = %v, want %q", req.Patch.LogPath, path)
	}
	if err := req.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestApplyLogBlankFields proves a blank level falls back to off and a blank
// path travels as an EXPLICIT empty string, which the helper applies as "reset
// to the default location".
func TestApplyLogBlankFields(t *testing.T) {
	req, ok := buildElevateRequest(applyRequest{action: actionSaveLog, log: &logDraft{}})
	if !ok {
		t.Fatal("buildElevateRequest returned !ok")
	}
	if req.Patch.LogLevel == nil || *req.Patch.LogLevel != config.LogLevelOff {
		t.Errorf("blank level = %v, want off", req.Patch.LogLevel)
	}
	if req.Patch.LogPath == nil {
		t.Fatal("blank path must travel as an explicit empty string, got nil")
	}
	if *req.Patch.LogPath != "" {
		t.Errorf("blank path = %q, want empty (reset to default)", *req.Patch.LogPath)
	}
}

// TestLogTailAppendsOnlyNewBytes proves the reader tracks its byte offset: an
// interval that has not elapsed is a no-op, and a later read appends only the
// bytes written since.
func TestLogTailAppendsOnlyNewBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")
	if err := os.WriteFile(path, []byte("one\n"), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	base := time.Now()
	tail := &logTail{}

	if !tail.read(path, 5, base) {
		t.Fatal("first read reported no change")
	}
	if tail.text != "one\n" || tail.offset != 4 {
		t.Fatalf("after first read: text=%q offset=%d", tail.text, tail.offset)
	}

	appendFile(t, path, "two\n")

	// Before the interval elapses nothing is read, even though the file grew.
	if tail.read(path, 5, base.Add(2*time.Second)) {
		t.Fatal("read ran before the interval elapsed")
	}
	if tail.text != "one\n" || tail.offset != 4 {
		t.Fatalf("premature read changed the tail: text=%q offset=%d", tail.text, tail.offset)
	}

	// After the interval only the appended bytes are added.
	if !tail.read(path, 5, base.Add(5*time.Second)) {
		t.Fatal("read after the interval reported no change")
	}
	if tail.text != "one\ntwo\n" || tail.offset != 8 {
		t.Fatalf("appended read: text=%q offset=%d", tail.text, tail.offset)
	}

	// An unchanged file reports no change (no redundant repaint).
	if tail.read(path, 5, base.Add(10*time.Second)) {
		t.Fatal("unchanged file reported a change")
	}
}

// TestLogTailRestartsOnRotation proves a file that shrank (rotated/truncated)
// is re-read from the top instead of being skipped.
func TestLogTailRestartsOnRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")
	if err := os.WriteFile(path, []byte("old-a\nold-b\n"), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	base := time.Now()
	tail := &logTail{}
	tail.read(path, 1, base)

	// Rewrite shorter: the size dropped below the tracked offset.
	if err := os.WriteFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatalf("rotate log: %v", err)
	}
	if !tail.read(path, 1, base.Add(time.Second)) {
		t.Fatal("rotation reported no change")
	}
	if tail.text != "new\n" {
		t.Fatalf("after rotation text = %q, want %q", tail.text, "new\n")
	}
}

// TestLogTailDisabledAtZero proves the slider's 0 reads nothing at all: the
// file is never opened and the tail stays empty.
func TestLogTailDisabledAtZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sigildns.log")
	if err := os.WriteFile(path, []byte("data\n"), 0o600); err != nil {
		t.Fatalf("seed log: %v", err)
	}

	tail := &logTail{}
	if tail.read(path, 0, time.Now()) {
		t.Fatal("interval 0 reported a change")
	}
	if tail.text != "" || tail.offset != 0 {
		t.Fatalf("disabled reader touched the file: text=%q offset=%d", tail.text, tail.offset)
	}
}

// TestCapTailKeepsNewestLines proves a large buffer keeps only its newest
// window and starts on a line boundary.
func TestCapTailKeepsNewestLines(t *testing.T) {
	big := ""
	for i := 0; i < maxTailBytes; i += 6 {
		big += "aaaaa\n"
	}
	big += "last\n"

	got := capTail(big)
	if len(got) > maxTailBytes {
		t.Fatalf("capTail length %d exceeds cap %d", len(got), maxTailBytes)
	}
	if got[len(got)-5:] != "last\n" {
		t.Fatalf("capTail dropped the newest line: %q", got[len(got)-10:])
	}
}

// appendFile appends s to path.
func appendFile(t *testing.T, path, s string) {
	t.Helper()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatalf("append %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}
