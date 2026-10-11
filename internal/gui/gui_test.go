//go:build gui

package gui

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend/soft"

	"github.com/daika7ana/dns-client/internal/ipc"
)

// fakeSource is a statusSource that returns a fixed snapshot, so the window can
// be exercised with no service, pipe, or display.
type fakeSource struct{ snap snapshot }

func (f fakeSource) Read(context.Context) (snapshot, error) { return f.snap, nil }

// sampleSnapshot is a healthy, fully-populated read.
func sampleSnapshot() snapshot {
	return snapshot{
		trust:   trustVerified,
		running: true,
		updated: time.Date(2026, 10, 12, 14, 3, 22, 0, time.UTC),
		status: &ipc.StatusDTO{
			Mode:      "on",
			Enabled:   true,
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

// stateCase is one render-state case with the action buttons it must show.
type stateCase struct {
	mk         func() snapshot
	show       []guiAction // action buttons expected present
	unverified bool
}

// TestWindowRendersEachState renders every UI state headlessly and asserts the
// frame is a real image, shows the right actions, and has no widget-ID or event
// findings.
func TestWindowRendersEachState(t *testing.T) {
	cases := map[string]stateCase{
		"running": {
			mk:   func() snapshot { return sampleSnapshot() },
			show: []guiAction{actionDisable},
		},
		"degraded": {
			mk: func() snapshot {
				s := sampleSnapshot()
				s.summary.ConfigValid = false
				s.summary.ConfigError = "config could not be loaded or validated"

				return s
			},
			show: []guiAction{actionRestore, actionDisable},
		},
		"dirty": {
			mk: func() snapshot {
				s := sampleSnapshot()
				s.status.Dirty = true

				return s
			},
			show: []guiAction{actionRestore, actionDisable},
		},
		"stopped": {
			mk:   func() snapshot { return snapshot{trust: trustVerified, updated: time.Now()} },
			show: []guiAction{actionEnable},
		},
		"unverified": {
			mk:         func() snapshot { return snapshot{trust: trustUnverified, running: true, updated: time.Now()} },
			unverified: true,
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

			// The right action buttons are rendered; the others are not.
			for _, a := range tc.show {
				if len(w.ResolveID("gui:action:"+string(a))) == 0 {
					t.Errorf("expected action button %q to be rendered", a)
				}
			}
			if tc.unverified {
				for _, a := range []guiAction{actionEnable, actionDisable, actionRestore} {
					if len(w.ResolveID("gui:action:"+string(a))) != 0 {
						t.Errorf("unverified state must not render %q", a)
					}
				}
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

// TestActionPlan covers the state -> action matrix as data.
func TestActionPlan(t *testing.T) {
	tests := []struct {
		name      string
		kind      statusKind
		primary   guiAction
		secondary guiAction
		blocked   bool
	}{
		{"running", kindRunning, actionDisable, "", false},
		{"stopped", kindStopped, actionEnable, "", false},
		{"dirty", kindDirty, actionRestore, actionDisable, false},
		{"degraded", kindDegraded, actionRestore, actionDisable, false},
		{"unverified", kindUnverified, "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := planActions(tt.kind)
			if got.primary != tt.primary || got.secondary != tt.secondary || got.blocked != tt.blocked {
				t.Fatalf("planActions(%v) = {primary:%q secondary:%q blocked:%v}, want {%q %q %v}",
					tt.name, got.primary, got.secondary, got.blocked, tt.primary, tt.secondary, tt.blocked)
			}
			if tt.blocked && got.reason == "" {
				t.Fatal("blocked plan must carry a reason")
			}
		})
	}
}

// TestToastForOutcome covers success, helper failure, UAC cancel (both as a
// returned error and as a helper result), and unsupported.
func TestToastForOutcome(t *testing.T) {
	tests := []struct {
		name     string
		action   guiAction
		res      applyResult
		err      error
		severity gogui.ToastSeverity
		title    string
		bodyHas  string
	}{
		{
			name: "success", action: actionEnable,
			res:      applyResult{ok: true, message: "Resolver enabled."},
			severity: gogui.ToastSuccess, title: "Enabled", bodyHas: "Resolver enabled.",
		},
		{
			name: "failure", action: actionEnable,
			res:      applyResult{ok: false, errText: "bind 127.0.0.1:53: access denied"},
			severity: gogui.ToastError, title: "Could not enable", bodyHas: "access denied",
		},
		{
			name: "cancel-as-error", action: actionDisable,
			err:      errors.New("UAC declined"),
			severity: gogui.ToastWarning, title: "Elevation cancelled", bodyHas: "Nothing changed",
		},
		{
			name: "cancel-as-result", action: actionRestore,
			res:      applyResult{ok: false, errText: "user cancelled"},
			severity: gogui.ToastWarning, title: "Elevation cancelled", bodyHas: "Nothing changed",
		},
		{
			name: "unsupported", action: actionEnable,
			err:      errUnsupported,
			severity: gogui.ToastWarning, title: "Not supported", bodyHas: "not available on this platform",
		},
		{
			name: "revision-conflict", action: actionEnable,
			err:      errRevision,
			severity: gogui.ToastWarning, title: "Configuration changed", bodyHas: "Refresh and try again",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toastFor(tt.action, tt.res, tt.err)
			if got.Severity != tt.severity {
				t.Errorf("severity = %v, want %v", got.Severity, tt.severity)
			}
			if got.Title != tt.title {
				t.Errorf("title = %q, want %q", got.Title, tt.title)
			}
			if !strings.Contains(got.Body, tt.bodyHas) {
				t.Errorf("body = %q, want substring %q", got.Body, tt.bodyHas)
			}
		})
	}
}

// TestActionClickInvokesApply drives a click through real dispatch and asserts
// the injected apply func runs with the planned action, off the frame thread.
func TestActionClickInvokesApply(t *testing.T) {
	got := make(chan guiAction, 1)

	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = snapshot{trust: trustVerified, updated: time.Now()} // stopped -> Enable
	app.apply = func(_ context.Context, action guiAction, _ string) (applyResult, error) {
		got <- action

		return applyResult{ok: true, message: "Resolver enabled."}, nil
	}

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	if err := w.TestClick("gui:action:enable"); err != nil {
		t.Fatalf("click enable: %v", err)
	}

	select {
	case action := <-got:
		if action != actionEnable {
			t.Fatalf("apply called with %q, want %q", action, actionEnable)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("apply was not invoked")
	}

	// Busy is cleared by the queued callback; flushing it also exercises the
	// toast + re-poll path without touching the window from the goroutine.
	waitIdle(t, w, app)
}

// waitIdle drives frames until the in-flight action finishes.  FrameFn flushes
// the command queue (TestRender does not), so the queued completion callback --
// which clears busy, shows the toast and requests a re-poll -- actually runs.
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

// TestRefreshActionFires checks the Refresh button is reachable and requests a
// poll without blocking.
func TestRefreshActionFires(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	if err := w.TestClick("gui:refresh"); err != nil {
		t.Fatalf("click refresh: %v", err)
	}
	select {
	case <-app.refresh:
	default:
		t.Fatal("refresh click did not request a poll")
	}
}

// distinctColors counts distinct pixel colors on a coarse grid.
func distinctColors(img *image.RGBA) (n int) {
	b := img.Bounds()
	seen := make(map[uint32]struct{})
	step := 8
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			c := img.RGBAAt(x, y)
			key := uint32(c.R)<<24 | uint32(c.G)<<16 | uint32(c.B)<<8 | uint32(c.A)
			seen[key] = struct{}{}
		}
	}

	return len(seen)
}
