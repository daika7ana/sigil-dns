//go:build gui

package gui

import (
	"context"
	"errors"
	"image"
	"image/color"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend/soft"

	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/version"
)

// fakeSource is a statusSource that returns a fixed snapshot, so the window can
// be exercised with no service, pipe, or display.
type fakeSource struct{ snap snapshot }

func (f fakeSource) Read(context.Context) (snapshot, error)     { return f.snap, nil }
func (f fakeSource) ReadLive(context.Context) (snapshot, error) { return f.snap, nil }

// alwaysFocused is a focused flag for tests that drive the poller.
func alwaysFocused() *atomic.Bool {
	f := &atomic.Bool{}
	f.Store(true)

	return f
}

// sampleSnapshot is a healthy, fully-populated read.  Dirty is deliberately
// true: the lifecycle sets it for the duration of a normal enabled session, so
// a realistic enabled snapshot carries it and must still render "Protected".
func sampleSnapshot() snapshot {
	return snapshot{
		trust:     trustVerified,
		running:   true,
		installed: true,
		scmState:  "running",
		updated:   time.Date(2026, 10, 12, 14, 3, 22, 0, time.UTC),
		status: &ipc.StatusDTO{
			Mode:      "on",
			Enabled:   true,
			Dirty:     true,
			Mechanism: "nrpt",
			Health:    &ipc.HealthSummary{Ours: true, IPv4: true, IPv6: true},
		},
		summary: &ipc.ConfigSummary{
			Profile:          "default",
			Provider:         "controld",
			Variant:          "p2",
			Protocol:         "doh",
			IDSet:            true,
			ConfigValid:      true,
			FilteringEnabled: true,
			ListCount:        2,
			PerDomainCount:   3,
			CacheEnabled:     true,
			CacheSizeBytes:   4 << 20,
			BlockResponse:    "0.0.0.0",
			Bootstrap:        []string{"76.76.2.22", "76.76.10.22"},
			Lists: []ipc.ListSummary{
				{Host: "ads.example.net", Enabled: true},
				{Host: "trackers.example.org", Enabled: false},
			},
		},
		info: &ipc.Info{
			AppVersion:      "v1.0.0",
			ProtocolVersion: 1,
			Capabilities:    ipc.Capabilities,
		},
	}
}

// stateCase is one render-state case: whether the Restore button is offered.
type stateCase struct {
	mk      func() snapshot
	restore bool
}

// TestWindowRendersEachState renders every UI state headlessly and asserts the
// frame is a real image, shows the single power control (and Restore only for
// the crash-leftover state), and has no widget-ID or event findings.
func TestWindowRendersEachState(t *testing.T) {
	cases := map[string]stateCase{
		"running": {
			// Enabled and healthy, with the lifecycle's Dirty marker set.
			mk: func() snapshot { return sampleSnapshot() },
		},
		"degraded-config": {
			mk: func() snapshot {
				s := sampleSnapshot()
				s.summary.ConfigValid = false
				s.summary.ConfigError = "config could not be loaded or validated"

				return s
			},
		},
		"degraded-health": {
			mk: func() snapshot {
				s := sampleSnapshot()
				s.status.Health = &ipc.HealthSummary{Ours: true, IPv4: false, IPv6: false}

				return s
			},
		},
		"dirty-leftover": {
			// Interception applied, but interception is off: the genuine
			// crash-recovery case, the only one that offers Restore.
			mk: func() snapshot {
				s := sampleSnapshot()
				s.status.Enabled = false
				s.status.Mode = "off"

				return s
			},
			restore: true,
		},
		"stopped-not-installed": {
			mk: func() snapshot { return snapshot{trust: trustVerified, updated: time.Now()} },
		},
		"stopped-installed": {
			mk: func() snapshot {
				s := sampleSnapshot()
				s.running = false
				s.status = nil

				return s
			},
		},
		"unverified": {
			mk: func() snapshot { return snapshot{trust: trustUnverified, running: true, updated: time.Now()} },
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Exercise the real read path (fake source -> snapshot).
			snap := readOnce(context.Background(), fakeSource{snap: tc.mk()})
			if snap.updated.IsZero() {
				t.Fatal("readOnce left timestamp zero")
			}

			app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
			app.snap = snap

			w := gogui.NewTestWindow(t, gogui.WindowCfg{
				Title:  "dns-client",
				Width:  defaultWidth,
				Height: defaultHeight,
				State:  app,
			})
			w.SetView(view)

			// Real software render: installs a text measurer and produces pixels.
			img, err := soft.RenderToImage(w, 1)
			if err != nil {
				t.Fatalf("RenderToImage: %v", err)
			}
			if img == nil || img.Bounds().Empty() {
				t.Fatal("render produced an empty image")
			}
			if n := distinctColors(img); n < 4 {
				t.Fatalf("render looks blank: %d distinct colors", n)
			}

			root := w.TestRender(nil)

			// No lifecycle control lives on the Status page: Start/Stop and
			// Install/Uninstall are in Settings, and the header no longer
			// carries a power button.
			for _, id := range []string{
				"gui:power",
				"gui:action:enable", "gui:action:disable",
				"gui:service:install", "gui:service:uninstall",
				"gui:service:start", "gui:service:stop",
			} {
				if len(w.ResolveID(id)) != 0 {
					t.Errorf("Status page must not render %q", id)
				}
			}

			if tc.restore != (len(w.ResolveID("gui:action:restore")) > 0) {
				t.Errorf("restore present = %v, want %v", !tc.restore, tc.restore)
			}

			// The SCM state badge now sits in the header, beside the app name.
			if !textSet(mustFindLayout(t, w, root, "gui:header"))[displayState(snap.scmState)] {
				t.Errorf("header does not show the service state badge %q", displayState(snap.scmState))
			}

			// ID and event findings: duplicates, missing IDs, unknown focus,
			// stamp drift, unconsumed events.
			findings := w.TestFindings(gogui.DebugDuplicates | gogui.DebugMissingIDs |
				gogui.DebugUnconsumed | gogui.DebugUnknownFocus |
				gogui.DebugStampDrift | gogui.DebugAutoIDs)
			if len(findings) > 0 {
				t.Fatalf("widget findings: %v", findings)
			}
		})
	}
}

// TestStatusVisualPrecedence pins the state mapping, above all that an enabled
// session is never labeled "Restore needed" just because Dirty is set.
func TestStatusVisualPrecedence(t *testing.T) {
	th := gogui.ThemeLight

	tests := []struct {
		name     string
		snap     snapshot
		kind     statusKind
		headline string
	}{
		{"enabled-dirty-is-protected", sampleSnapshot(), kindRunning, "Protected"},
		{"unverified", snapshot{trust: trustUnverified, running: true}, kindUnverified, "Unverified"},
		{"not-installed", snapshot{trust: trustVerified}, kindStopped, "Not installed"},
		{"installed-stopped", func() snapshot {
			s := sampleSnapshot()
			s.running = false
			s.status = nil

			return s
		}(), kindStopped, "Stopped"},
		{"dirty-leftover", func() snapshot {
			s := sampleSnapshot()
			s.status.Enabled = false

			return s
		}(), kindDirty, "Restore needed"},
		{"enabled-off", func() snapshot {
			s := sampleSnapshot()
			s.status.Enabled = false
			s.status.Dirty = false

			return s
		}(), kindStopped, "Interception off"},
		{"enabled-invalid", func() snapshot {
			s := sampleSnapshot()
			s.summary.ConfigValid = false

			return s
		}(), kindDegraded, "Needs attention"},
		{"enabled-unhealthy", func() snapshot {
			s := sampleSnapshot()
			s.status.Health = &ipc.HealthSummary{Ours: false}

			return s
		}(), kindDegraded, "Needs attention"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusVisualFor(tt.snap, th)
			if got.kind != tt.kind {
				t.Errorf("kind = %v, want %v", got.kind, tt.kind)
			}
			if got.headline != tt.headline {
				t.Errorf("headline = %q, want %q", got.headline, tt.headline)
			}
		})
	}
}

// TestPlanPower covers the header's Restore/note state matrix as data.
func TestPlanPower(t *testing.T) {
	tests := []struct {
		name      string
		kind      statusKind
		installed bool
		running   bool
		busy      bool
		trusted   bool
		restore   bool
		wantNote  bool
	}{
		{"running", kindRunning, true, true, false, true, false, false},
		{"stopped-not-installed", kindStopped, false, false, false, true, false, true},
		{"stopped-installed", kindStopped, true, false, false, true, false, true},
		{"interception-off", kindStopped, true, true, false, true, false, true},
		{"dirty-leftover", kindDirty, true, true, false, true, true, true},
		{"degraded", kindDegraded, true, true, false, true, false, true},
		{"unverified", kindUnverified, true, true, false, false, false, true},
		{"busy", kindRunning, true, true, true, true, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planPower(tt.kind, tt.installed, tt.running, tt.busy, tt.trusted)
			if got.restore != tt.restore {
				t.Errorf("restore = %v, want %v", got.restore, tt.restore)
			}
			if tt.wantNote != (got.note != "") {
				t.Errorf("note = %q, wantNote = %v", got.note, tt.wantNote)
			}
			// Restore is offered only for the crash leftover, never for a
			// healthy running state.
			if tt.kind == kindRunning && got.restore {
				t.Fatal("a healthy running state must never offer Restore")
			}
		})
	}
}

// TestNoticeForOutcome covers success, helper failure, UAC cancel (both as a
// returned error and as a helper result), and unsupported.
func TestNoticeForOutcome(t *testing.T) {
	tests := []struct {
		name    string
		action  guiAction
		res     applyResult
		err     error
		kind    noticeKind
		title   string
		bodyHas string
	}{
		{
			name: "success", action: actionEnable,
			res:  applyResult{ok: true, message: "Resolver enabled."},
			kind: noticeSuccess, title: "Enabled", bodyHas: "Resolver enabled.",
		},
		{
			name: "failure", action: actionEnable,
			res:  applyResult{ok: false, errText: "bind 127.0.0.1:53: access denied"},
			kind: noticeError, title: "Could not enable", bodyHas: "access denied",
		},
		{
			name: "stop-service-success", action: actionServiceStop,
			res:  applyResult{ok: true, message: "Service stopped."},
			kind: noticeSuccess, title: "Service stopped", bodyHas: "Service stopped.",
		},
		{
			name: "stop-service-failure", action: actionServiceStop,
			res:  applyResult{ok: false, errText: "the service did not stop"},
			kind: noticeError, title: "Could not stop the service", bodyHas: "did not stop",
		},
		{
			name: "cancel-as-error", action: actionServiceStop,
			err:  errors.New("UAC declined"),
			kind: noticeWarning, title: "Elevation cancelled", bodyHas: "Nothing changed",
		},
		{
			name: "cancel-as-result", action: actionRestore,
			res:  applyResult{ok: false, errText: "user cancelled"},
			kind: noticeWarning, title: "Elevation cancelled", bodyHas: "Nothing changed",
		},
		{
			name: "unsupported", action: actionEnable,
			err:  errUnsupported,
			kind: noticeWarning, title: "Not supported", bodyHas: "not available on this platform",
		},
		{
			name: "revision-conflict", action: actionEnable,
			err:  errRevision,
			kind: noticeWarning, title: "Configuration changed", bodyHas: "Try again",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := noticeFor(tt.action, tt.res, tt.err)
			if got.kind != tt.kind {
				t.Errorf("kind = %v, want %v", got.kind, tt.kind)
			}
			if got.title != tt.title {
				t.Errorf("title = %q, want %q", got.title, tt.title)
			}
			if !strings.Contains(got.body, tt.bodyHas) {
				t.Errorf("body = %q, want substring %q", got.body, tt.bodyHas)
			}
		})
	}
}

// TestNoticePaletteOpaque proves the toast fill is composited onto an opaque
// ground in both themes (the raw *Subtle colors are translucent, which let the
// page show through), while the four severities stay visually distinct.
func TestNoticePaletteOpaque(t *testing.T) {
	severities := []noticeKind{noticeInfo, noticeSuccess, noticeWarning, noticeError}

	for _, th := range []gogui.Theme{gogui.ThemeLight, gogui.ThemeDark} {
		seen := make(map[gogui.Color]noticeKind, len(severities))
		for _, k := range severities {
			_, bg := noticePalette(th, k)
			if bg.A != 255 {
				t.Errorf("%s: %v toast fill is translucent (alpha=%d)", th.Name, k, bg.A)
			}
			if prev, dup := seen[bg]; dup {
				t.Errorf("%s: severities %v and %v share fill %v", th.Name, prev, k, bg)
			}
			seen[bg] = k
		}
	}
}

// TestServiceActionClickInvokesApply drives a click on the Settings Start/Stop
// control through real dispatch and asserts the injected apply func runs with
// the mapped action, off the frame thread: Start maps to Enable (start service
// + protection), Stop to the service's graceful shutdown.
func TestServiceActionClickInvokesApply(t *testing.T) {
	tests := []struct {
		name string
		snap snapshot
		id   string
		want guiAction
	}{
		{"start", snapshot{trust: trustVerified, installed: true, updated: time.Now()}, "gui:service:start", actionEnable},
		{"stop", sampleSnapshot(), "gui:service:stop", actionServiceStop},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make(chan guiAction, 1)

			app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
			app.snap = tt.snap
			app.page = pageSettings
			app.apply = func(_ context.Context, req applyRequest) (applyResult, error) {
				got <- req.action

				return applyResult{ok: true, message: "Done."}, nil
			}

			w := gogui.NewTestWindow(t, gogui.WindowCfg{
				Width: defaultWidth, Height: defaultHeight, State: app,
			})
			w.SetView(view)
			w.TestRender(nil)

			if err := w.TestClick(tt.id); err != nil {
				t.Fatalf("click %s: %v", tt.id, err)
			}

			select {
			case action := <-got:
				if action != tt.want {
					t.Fatalf("apply called with %q, want %q", action, tt.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("apply was not invoked")
			}

			// Busy is cleared by the queued callback; flushing it also exercises
			// the toast + re-poll path without touching the window from the
			// goroutine.
			waitIdle(t, w, app)
		})
	}
}

// waitIdle drives frames until the in-flight action finishes.  FrameFn flushes
// the command queue (TestRender does not), so the queued completion callback --
// which clears busy, sets the inline notice and requests a re-poll -- actually
// runs.
func waitIdle(t *testing.T, w *gogui.Window, app *App) {
	t.Helper()

	for i := 0; i < 400; i++ {
		w.FrameFn()
		if !app.busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("action did not settle")
}

// TestWindowsThemeTunedTokens pins the app's WinUI calibration so the numbers
// are visible and further tunable: a thinner scrollbar with a larger edge
// inset, and a two-step font-ladder bump (body 12 -> 14).
func TestWindowsThemeTunedTokens(t *testing.T) {
	for _, name := range []string{"windows", "windows-dark"} {
		th := windowsTheme(name, gogui.ThemeDark)
		if th.Cfg.SizeScrollbar != themeScrollbarSize {
			t.Errorf("%s SizeScrollbar = %v, want %v", name, th.Cfg.SizeScrollbar, themeScrollbarSize)
		}
		if !th.Cfg.SizeScrollbarGap.IsSet() || th.Cfg.SizeScrollbarGap.Get(0) != themeScrollbarGap {
			t.Errorf("%s SizeScrollbarGap = (%v,set=%v), want %v", name, th.Cfg.SizeScrollbarGap.Get(0), th.Cfg.SizeScrollbarGap.IsSet(), themeScrollbarGap)
		}
		if !th.Cfg.SizeScrollbarGapEnd.IsSet() || th.Cfg.SizeScrollbarGapEnd.Get(0) != themeScrollbarGapEnd {
			t.Errorf("%s SizeScrollbarGapEnd = (%v,set=%v), want %v", name, th.Cfg.SizeScrollbarGapEnd.Get(0), th.Cfg.SizeScrollbarGapEnd.IsSet(), themeScrollbarGapEnd)
		}
		// Body 12 + 2
		if want := float32(12) + themeFontDelta; th.Cfg.TextStyleDef.Size != want {
			t.Errorf("%s body size = %v, want %v", name, th.Cfg.TextStyleDef.Size, want)
		}
		// The family stays empty, so Windows resolves its own system face.
		if th.Cfg.TextStyleDef.Family != "" {
			t.Errorf("%s pinned a font family %q", name, th.Cfg.TextStyleDef.Family)
		}
	}

	// The fallback is calibrated too.
	fb := windowsTheme("no-such-theme", gogui.ThemeDark)
	if fb.Cfg.SizeScrollbar != themeScrollbarSize {
		t.Errorf("fallback SizeScrollbar = %v, want %v", fb.Cfg.SizeScrollbar, themeScrollbarSize)
	}
}

// TestAboutPageRenders proves the About page renders its card with the real
// identity values, and no widget findings.
func TestAboutPageRenders(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()
	app.page = pageAbout

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	root := w.TestRender(nil)

	if n := len(w.ResolveID("gui:nav:about")); n != 1 {
		t.Fatalf("About nav button = %d, want 1", n)
	}
	mustFindLayout(t, w, root, "gui:card:about")

	for _, id := range []string{"gui:about:author", "gui:about:repo"} {
		if n := len(w.ResolveID(id)); n != 1 {
			t.Errorf("%s rendered %d times, want 1", id, n)
		}
	}

	texts := textSet(root)
	for _, want := range []string{
		appName, version.Version, appAuthor, appRepoURL, appLicense,
	} {
		if !texts[want] {
			t.Errorf("About page does not show %q", want)
		}
	}

	assertNoFindings(t, w)
}

// TestAboutLinksOpen proves the About Author and Repository controls are
// clickable and each opens its URL through the openURL seam.
func TestAboutLinksOpen(t *testing.T) {
	var got []string
	orig := openURL
	openURL = func(u string) error {
		got = append(got, u)

		return nil
	}
	defer func() { openURL = orig }()

	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()
	app.page = pageAbout

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	for _, id := range []string{"gui:about:author", "gui:about:repo"} {
		if n := len(w.ResolveID(id)); n != 1 {
			t.Fatalf("%s rendered %d times, want 1", id, n)
		}
	}

	if err := w.TestClick("gui:about:author"); err != nil {
		t.Fatalf("click author link: %v", err)
	}
	if err := w.TestClick("gui:about:repo"); err != nil {
		t.Fatalf("click repository link: %v", err)
	}

	want := []string{appAuthorURL, appRepoURL}
	if len(got) != len(want) {
		t.Fatalf("openURL called %d times (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("openURL call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestValidateURLAcceptsOnlyHTTP proves the opener's scheme guard accepts only
// absolute http/https URLs with a host, so the OS shell never sees anything else.
func TestValidateURLAcceptsOnlyHTTP(t *testing.T) {
	for _, ok := range []string{
		"http://example.com",
		"https://github.com/daika7ana",
		appAuthorURL,
		appRepoURL,
	} {
		if _, err := validateURL(ok); err != nil {
			t.Errorf("validateURL(%q) = %v, want nil", ok, err)
		}
	}

	for _, bad := range []string{
		"",
		"file:///etc/passwd",
		"javascript:alert(1)",
		"ftp://example.com",
		"mailto:x@y.example",
		"https://",
		"//example.com",
	} {
		if _, err := validateURL(bad); err == nil {
			t.Errorf("validateURL(%q) = nil, want error", bad)
		}
	}
}

// TestDisplayCapitalization pins the render-layer capitalization of state and
// boolean strings; the stored values stay lowercase.
func TestDisplayCapitalization(t *testing.T) {
	states := map[string]string{
		"running":       "Running",
		"stopped":       "Stopped",
		"starting":      "Starting",
		"stopping":      "Stopping",
		"not installed": "Not installed",
		"unsupported":   "Unsupported",
		"":              "Unknown",
	}
	for in, want := range states {
		if got := displayState(in); got != want {
			t.Errorf("displayState(%q) = %q, want %q", in, got, want)
		}
	}
	if yesNo(true) != "Yes" || yesNo(false) != "No" {
		t.Errorf("yesNo = %q/%q, want Yes/No", yesNo(true), yesNo(false))
	}
	if got := resolverID(false); got != "Not set" {
		t.Errorf("resolverID(false) = %q, want Not set", got)
	}
}

// TestServiceStateShownCapitalized proves the header shows the capitalized
// state while the snapshot keeps the raw lowercase value.
func TestServiceStateShownCapitalized(t *testing.T) {
	app := settingsApp()
	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	if app.snap.scmState != "running" {
		t.Fatalf("sample snapshot state changed: %q", app.snap.scmState)
	}
	texts := textSet(mustFindLayout(t, w, root, "gui:header"))
	if !texts["Running"] {
		t.Error(`header does not show the capitalized state "Running"`)
	}
	if texts["running"] {
		t.Error("header still shows the raw lowercase state")
	}
}

// textSet collects every rendered text string in a layout tree.
func textSet(ly *gogui.Layout) (set map[string]bool) {
	set = map[string]bool{}

	var walk func(*gogui.Layout)
	walk = func(l *gogui.Layout) {
		if l == nil {
			return
		}
		if l.Shape != nil && l.Shape.TC != nil && l.Shape.TC.Text != "" {
			set[l.Shape.TC.Text] = true
		}
		for i := range l.Children {
			walk(&l.Children[i])
		}
	}
	walk(ly)

	return set
}

// distinctColors counts distinct pixel colors on a coarse grid.
func distinctColors(img *image.RGBA) (n int) {
	b := img.Bounds()
	seen := make(map[color.RGBA]struct{})
	step := 8
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			seen[img.RGBAAt(x, y)] = struct{}{}
		}
	}

	return len(seen)
}
