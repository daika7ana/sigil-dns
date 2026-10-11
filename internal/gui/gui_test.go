//go:build gui

package gui

import (
	"context"
	"image"
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

// TestWindowRendersEachState renders every UI state headlessly and asserts the
// frame is a real image with no widget-ID or event findings.
func TestWindowRendersEachState(t *testing.T) {
	cases := map[string]func() snapshot{
		"running": func() snapshot { return sampleSnapshot() },
		"degraded": func() snapshot {
			s := sampleSnapshot()
			s.summary.ConfigValid = false
			s.summary.ConfigError = "config could not be loaded or validated"

			return s
		},
		"dirty": func() snapshot {
			s := sampleSnapshot()
			s.status.Dirty = true

			return s
		},
		"stopped": func() snapshot {
			return snapshot{trust: trustVerified, updated: time.Now()}
		},
		"unverified": func() snapshot {
			return snapshot{trust: trustUnverified, running: true, updated: time.Now()}
		},
	}

	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			// Exercise the real read path (fake source -> snapshot).
			snap := readOnce(context.Background(), fakeSource{snap: mk()})
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
