//go:build gui

package gui

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend/soft"

	"github.com/daika7ana/sigil-dns/internal/config"
	"github.com/daika7ana/sigil-dns/internal/ipc"
)

// A wide window, distinct from the default, so the pages are also exercised
// past their natural width.
const (
	wideWidth  = 1600
	wideHeight = 900
)

// cardRect is one card's arranged rectangle.
type cardRect struct {
	id string
	x  float32
	y  float32
	w  float32
	h  float32
}

// statusApp is a verified, healthy App on the Status page.
func statusApp() *App {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()

	return app
}

// TestStatusLayout proves the Status dashboard lays out: the hero and
// connection panel share the top row at normal widths, the three secondary
// cards share the bottom row, and at the minimum window the top row stacks and
// the cache card takes the full width — with no collapse, overlap, or
// horizontal overflow.
func TestStatusLayout(t *testing.T) {
	for _, size := range []struct {
		name    string
		w, h    int
		stacked bool
	}{
		{"default", defaultWidth, defaultHeight, false},
		{"large", wideWidth, wideHeight, false},
		{"minimum", minWidth, minHeight, true},
	} {
		t.Run(size.name, func(t *testing.T) {
			assertStatusDashboard(t, statusApp(), size.w, size.h, size.stacked)
		})
	}
}

// TestStatusLongBootstrapFits proves an uncompressed IPv6 bootstrap address
// stays inside the cache card instead of widening the dashboard and forcing a
// horizontal scrollbar on the Status page.  The grid gives the card a fixed
// column, so the literal is clipped rather than the card growing.
func TestStatusLongBootstrapFits(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int
	}{
		{"default", defaultWidth, defaultHeight},
		{"screenshot", 1320, 700},
		{"stack-breakpoint", statusStackWidth, 700},
	} {
		t.Run(size.name, func(t *testing.T) {
			app := statusApp()
			app.snap.summary.Bootstrap = []string{
				"2001:0db8:85a3:0000:0000:8a2e:0370:7334",
				"2606:4700:4700::1111",
			}

			w, root := layoutFor(t, app, size.w, size.h)
			page := mustFindLayout(t, w, root, "gui:content")
			if ov, ok := w.ScrollOverflowX(page.Shape.ID); !ok || ov > 0.5 {
				t.Errorf("Status page has %.1f px of horizontal overflow (ok=%v); want none", ov, ok)
			}

			// The long value must never push the card past its grid column or
			// its value block past the card.
			cache := mustFindLayout(t, w, root, "gui:card:cache")
			grid := mustFindLayout(t, w, root, "gui:status:grid")
			if cache.Shape.X+cache.Shape.Width > grid.Shape.X+grid.Shape.Width+0.5 {
				t.Errorf("cache card (%.0f..%.0f) extends past the dashboard (right %.0f)",
					cache.Shape.X, cache.Shape.X+cache.Shape.Width, grid.Shape.X+grid.Shape.Width)
			}
			value := mustFindLayout(t, w, root, "gui:status:bootstrap:value")
			if value.Shape.X+value.Shape.Width > cache.Shape.X+cache.Shape.Width-0.5 {
				t.Errorf("bootstrap value (%.0f..%.0f) extends past the cache card (right %.0f)",
					value.Shape.X, value.Shape.X+value.Shape.Width,
					cache.Shape.X+cache.Shape.Width)
			}
		})
	}
}

// TestStatusFilteringStatsAndPause proves the Filtering card shows the running
// session's counters and offers the pause control only while filtering is on;
// an active pause turns the control into Resume and shows the remaining minutes.
// A missing stats read shows the em dash and no control.
func TestStatusFilteringStatsAndPause(t *testing.T) {
	// No stats: the counters fall back to the em dash and the pause is not
	// offered, because there is nothing to confirm the pause against.
	empty := statusApp()
	ew, eroot := layoutFor(t, empty, defaultWidth, defaultHeight)
	for _, id := range []string{"gui:status:stats:queries", "gui:status:stats:blocked"} {
		if got := mustFindLayout(t, ew, eroot, id).Shape.TC.Text; got != "—" {
			t.Errorf("nil stats: %s = %q, want an em dash", id, got)
		}
	}
	if n := len(ew.ResolveID("gui:filtering:toggle")); n != 0 {
		t.Errorf("pause control rendered without stats (%d)", n)
	}

	// Running session with filtering on: the counters are real values and the
	// pause is offered.
	app := statusApp()
	app.snap.status.Stats = &ipc.RuntimeStats{Queries: 1234, Blocked: 56}

	w, root := layoutFor(t, app, defaultWidth, defaultHeight)
	for id, want := range map[string]string{
		"gui:status:stats:queries": "1,234",
		"gui:status:stats:blocked": "56",
	} {
		if got := mustFindLayout(t, w, root, id).Shape.TC.Text; got != want {
			t.Errorf("%s = %q, want %q", id, got, want)
		}
	}
	assertDisabled(t, root, w, "gui:filtering:toggle", false)
	if !textSet(mustFindLayout(t, w, root, "gui:filtering:toggle"))["Pause filtering"] {
		t.Error(`pause control does not read "Pause filtering"`)
	}
	// The copy must say the whole filter stage stops: hosts entries and
	// $dnsrewrite mappings stop too, not just blocklist matches.
	if !textSet(mustFindLayout(t, w, root, "gui:card:filtering"))["Skips all filtering for 15 minutes."] {
		t.Error("unpaused card does not say all filtering stops")
	}

	// The counters are a stat block, not another form row: the value leads its
	// caption at a larger size.
	stats := mustFindLayout(t, w, root, "gui:status:stats")
	queries := mustFindLayout(t, w, root, "gui:status:stats:queries")
	label := findTextLayout(stats, "Queries")
	if label == nil {
		t.Fatal("stats block has no Queries label")
	}
	if queries.Shape.Height <= label.Shape.Height {
		t.Errorf("counter value (%.0f high) is not larger than its label (%.0f high)",
			queries.Shape.Height, label.Shape.Height)
	}

	// A future expiry means the service is already paused: the control becomes
	// Resume, stays usable, and the countdown moves to the description.
	until := time.Now().Add(12 * time.Minute)
	app.snap.status.Stats.PausedUntil = &until
	pw, proot := layoutFor(t, app, defaultWidth, defaultHeight)
	assertDisabled(t, proot, pw, "gui:filtering:toggle", false)
	if !textSet(mustFindLayout(t, pw, proot, "gui:filtering:toggle"))["Resume filtering"] {
		t.Error("paused control does not offer Resume filtering")
	}
	if !textSet(mustFindLayout(t, pw, proot, "gui:card:filtering"))["Paused. Filtering resumes automatically in 12 minutes."] {
		t.Error("paused card does not show the remaining minutes")
	}

	// The control must still fit inside the card at the minimum width.
	mw, mroot := layoutFor(t, app, minWidth, minHeight)
	card := mustFindLayout(t, mw, mroot, "gui:card:filtering")
	if !layoutContains(card, mustFindLayout(t, mw, mroot, "gui:filtering:toggle")) {
		t.Error("pause control is outside the Filtering card at the minimum width")
	}
	page := mustFindLayout(t, mw, mroot, "gui:content")
	if ov, ok := mw.ScrollOverflowX(page.Shape.ID); !ok || ov > 0.5 {
		t.Errorf("Status page has %.1f px of horizontal overflow at the minimum width (ok=%v)", ov, ok)
	}

	// An in-flight action also disables it, like every other control, and says
	// which way it is going so the click is visibly acknowledged.
	busy := statusApp()
	busy.busy = true
	busy.busyAction = actionPauseFiltering
	busy.snap.status.Stats = &ipc.RuntimeStats{Queries: 1, Blocked: 0}
	bw, broot := layoutFor(t, busy, defaultWidth, defaultHeight)
	assertDisabled(t, broot, bw, "gui:filtering:toggle", true)
	if !textSet(mustFindLayout(t, bw, broot, "gui:filtering:toggle"))["Pausing\u2026"] {
		t.Error("in-flight pause does not read Pausing...")
	}

	pausedBusy := statusApp()
	pausedBusy.busy = true
	pausedBusy.busyAction = actionResumeFiltering
	pausedBusy.snap.status.Stats = &ipc.RuntimeStats{Queries: 1, Blocked: 0, PausedUntil: &until}
	rbw, rroot := layoutFor(t, pausedBusy, defaultWidth, defaultHeight)
	assertDisabled(t, rroot, rbw, "gui:filtering:toggle", true)
	if !textSet(mustFindLayout(t, rbw, rroot, "gui:filtering:toggle"))["Resuming\u2026"] {
		t.Error("in-flight resume does not read Resuming...")
	}
}

// TestStatusTrustGatesCards proves the whole dashboard is hidden only for an
// unverified source: a verified source whose summary has not arrived yet still
// renders the cards, with their em-dash fallbacks.
func TestStatusTrustGatesCards(t *testing.T) {
	open := statusApp()
	open.snap.summary = nil
	open.cached = nil

	w, _ := layoutFor(t, open, defaultWidth, defaultHeight)
	for _, id := range []string{"gui:card:connection", "gui:card:filtering", "gui:card:cache"} {
		if len(w.ResolveID(id)) == 0 {
			t.Errorf("%s missing for a verified source with no summary", id)
		}
	}

	unverified := statusApp()
	unverified.snap.trust = trustUnverified
	uw, _ := layoutFor(t, unverified, defaultWidth, defaultHeight)
	for _, id := range []string{"gui:card:connection", "gui:card:filtering", "gui:card:cache"} {
		if n := len(uw.ResolveID(id)); n != 0 {
			t.Errorf("unverified Status page rendered %d %s; want none", n, id)
		}
	}
	if len(uw.ResolveID("gui:banner")) == 0 {
		t.Error("unverified Status page did not render the state hero")
	}
}

// TestStatusGraphPaintsStateInk proves the hero illustration actually paints
// ink — not an empty canvas — and that a stopped session never carries the
// protected green.  It samples the graph's own bounds in the soft render, so
// the assertion follows pixels rather than the layout tree.
func TestStatusGraphPaintsStateInk(t *testing.T) {
	themes := []struct {
		name string
		th   gogui.Theme
	}{
		{"light", windowsTheme("windows", gogui.ThemeLight)},
		{"dark", windowsTheme("windows-dark", gogui.ThemeDark)},
	}
	cases := []struct {
		name    string
		snap    snapshot
		painted bool
	}{
		{"protected", sampleSnapshot(), true},
		{"stopped", stoppedInstalledSnapshot(), false},
	}

	for _, theme := range themes {
		for _, tc := range cases {
			t.Run(theme.name+"/"+tc.name, func(t *testing.T) {
				app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
				app.snap = tc.snap

				w := gogui.NewTestWindow(t, gogui.WindowCfg{
					Width: defaultWidth, Height: defaultHeight, State: app,
				})
				w.SetView(view)
				w.SetTheme(theme.th)
				root := w.TestRender(nil)

				graph := mustFindLayout(t, w, root, "gui:status:graph")
				img, err := soft.RenderToImage(w, 1)
				if err != nil {
					t.Fatalf("RenderToImage: %v", err)
				}

				// Always probe the protected ink: a stopped shield must not
				// carry it, whatever its own neutral stroke colour is.
				ink := statusVisualFor(sampleSnapshot(), theme.th).accent
				n := countInkPixels(img, graph, ink, 24)
				if tc.painted && n < 25 {
					t.Errorf("illustration paints %d protected-ink pixels, want a visible shield and check", n)
				}
				if !tc.painted && n != 0 {
					t.Errorf("stopped illustration paints %d protected-ink pixels: it must not read as protected", n)
				}
			})
		}
	}
}

// TestStatusGraphPaintsStateMarks covers the non-routine state marks: the
// exclamation (degraded/dirty) and the dash (unverified) must paint in their
// state ink, not silently fall back to a plain shield.
func TestStatusGraphPaintsStateMarks(t *testing.T) {
	themes := []struct {
		name string
		th   gogui.Theme
	}{
		{"light", windowsTheme("windows", gogui.ThemeLight)},
		{"dark", windowsTheme("windows-dark", gogui.ThemeDark)},
	}
	cases := []struct {
		name string
		snap snapshot
	}{
		{"degraded", func() snapshot {
			s := sampleSnapshot()
			s.summary.ConfigValid = false

			return s
		}()},
		{"dirty", func() snapshot {
			s := sampleSnapshot()
			s.status.Enabled = false
			s.status.Mode = "off"

			return s
		}()},
		{"unverified", snapshot{trust: trustUnverified, running: true, updated: time.Now()}},
	}

	for _, theme := range themes {
		for _, tc := range cases {
			t.Run(theme.name+"/"+tc.name, func(t *testing.T) {
				app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
				app.snap = tc.snap

				w := gogui.NewTestWindow(t, gogui.WindowCfg{
					Width: defaultWidth, Height: defaultHeight, State: app,
				})
				w.SetView(view)
				w.SetTheme(theme.th)
				root := w.TestRender(nil)

				graph := mustFindLayout(t, w, root, "gui:status:graph")
				img, err := soft.RenderToImage(w, 1)
				if err != nil {
					t.Fatalf("RenderToImage: %v", err)
				}

				ink := statusVisualFor(tc.snap, theme.th).accent
				n := countInkPixelsBox(img, graph, ink, 24,
					graph.Shape.Width*0.35, graph.Shape.Height*0.35,
					graph.Shape.Width*0.65, graph.Shape.Height*0.65)
				if n < 10 {
					t.Errorf("state mark paints %d centre ink pixels, want a visible mark", n)
				}
			})
		}
	}
}

// countInkPixels counts opaque pixels inside ly's bounds within tol of ink on
// every channel.  Antialiased edges fall outside the tolerance; a real stroke
// leaves a solid core inside it.
func countInkPixels(img *image.RGBA, ly *gogui.Layout, ink gogui.Color, tol int) (n int) {
	return countInkPixelsBox(img, ly, ink, tol, 0, 0, ly.Shape.Width, ly.Shape.Height)
}

// countInkPixelsBox is countInkPixels restricted to a box inside ly, measured
// from ly's top-left.  It lets a test probe the shield's centre (the state
// mark) without the outline dominating the count.
func countInkPixelsBox(img *image.RGBA, ly *gogui.Layout, ink gogui.Color, tol int, bx0, by0, bx1, by1 float32) (n int) {
	bounds := img.Bounds()
	x0 := max(int(ly.Shape.X+bx0), bounds.Min.X)
	y0 := max(int(ly.Shape.Y+by0), bounds.Min.Y)
	x1 := min(int(ly.Shape.X+bx1+0.5), bounds.Max.X)
	y1 := min(int(ly.Shape.Y+by1+0.5), bounds.Max.Y)

	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			c := img.RGBAAt(x, y)
			if c.A != 0xff {
				continue
			}
			if absI(int(c.R)-int(ink.R)) <= tol &&
				absI(int(c.G)-int(ink.G)) <= tol &&
				absI(int(c.B)-int(ink.B)) <= tol {
				n++
			}
		}
	}

	return n
}

// absI is an int absolute value.
func absI(v int) int {
	if v < 0 {
		return -v
	}

	return v
}

// TestStatusLabelsUseDisplayNames proves the hero chips and the Connection Info
// rows render the human provider, variant, and protocol labels, never the raw
// backend values from the summary.
func TestStatusLabelsUseDisplayNames(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	for _, ly := range []*gogui.Layout{
		mustFindLayout(t, w, root, "gui:banner:chips"),
		mustFindLayout(t, w, root, "gui:card:connection"),
	} {
		texts := textSet(ly)
		for _, want := range []string{"ControlD", "Ads and trackers blocked", "DoH"} {
			if !texts[want] {
				t.Errorf("%s missing display label %q; got %v", ly.Shape.ID, want, texts)
			}
		}
		for _, raw := range []string{"controld", "p2", "doh"} {
			if texts[raw] {
				t.Errorf("%s still renders raw value %q", ly.Shape.ID, raw)
			}
		}
	}
}

// assertStatusDashboard renders the Status page and asserts the dashboard
// geometry for the chosen layout.
func assertStatusDashboard(t *testing.T, app *App, width, height int, stacked bool) {
	t.Helper()

	w, root := layoutFor(t, app, width, height)

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

	hero := mustFindLayout(t, w, root, "gui:banner")
	conn := mustFindLayout(t, w, root, "gui:card:connection")
	filtering := mustFindLayout(t, w, root, "gui:card:filtering")
	lists := mustFindLayout(t, w, root, "gui:card:filterlists")
	cache := mustFindLayout(t, w, root, "gui:card:cache")

	for _, ly := range []*gogui.Layout{hero, conn, filtering, lists, cache} {
		if ly.Shape.Width <= 1 || ly.Shape.Height <= 1 {
			t.Errorf("%s collapsed to %.0fx%.0f", ly.Shape.ID, ly.Shape.Width, ly.Shape.Height)
		}
	}

	// The duplicated list count is gone: the Filter lists card owns that data.
	if n := countText(filtering, "Filter lists"); n != 0 {
		t.Errorf("Filtering card still renders a duplicated %q row (%d)", "Filter lists", n)
	}

	// Bootstrap is stacked (an IPv6 literal needs the full card width) but its
	// label and values share the Cache card's left label column and rhythm.
	cacheLabel := findTextLayout(cache, "Cache")
	bootstrapLabel := mustFindLayout(t, w, root, "gui:status:bootstrap:label")
	bootstrapValue := mustFindLayout(t, w, root, "gui:status:bootstrap:value")
	if cacheLabel == nil {
		t.Fatal("Cache card has no Cache label")
	}
	if absF(bootstrapLabel.Shape.X-cacheLabel.Shape.X) > 0.5 {
		t.Errorf("Bootstrap label x=%.1f != Cache label x=%.1f",
			bootstrapLabel.Shape.X, cacheLabel.Shape.X)
	}
	if absF(bootstrapValue.Shape.X-bootstrapLabel.Shape.X) > 0.5 {
		t.Errorf("Bootstrap values x=%.1f do not share the label column x=%.1f",
			bootstrapValue.Shape.X, bootstrapLabel.Shape.X)
	}
	if n := countText(cache, "Bootstrap"); n != 1 {
		t.Errorf("Bootstrap label rendered %d times, want 1", n)
	}

	// The hero carries the geometric illustration.
	graph := mustFindLayout(t, w, root, "gui:status:graph")
	if graph.Shape.Width < 100 || graph.Shape.Height < 100 {
		t.Errorf("hero illustration is %.0fx%.0f, want a real shape",
			graph.Shape.Width, graph.Shape.Height)
	}
	if !layoutContains(hero, graph) {
		t.Error("hero illustration is outside the hero card")
	}

	// The illustration is centred in the hero's inner height: equal breathing
	// room above and below, even when the paired card stretches the hero.
	innerTop := hero.Shape.Y + hero.Shape.SizeBorder + hero.Shape.Padding.Top
	innerBottom := hero.Shape.Y + hero.Shape.Height - hero.Shape.SizeBorder - hero.Shape.Padding.Bottom
	graphTopGap := graph.Shape.Y - innerTop
	graphBottomGap := innerBottom - (graph.Shape.Y + graph.Shape.Height)
	if absF(graphTopGap-graphBottomGap) > 1 {
		t.Errorf("illustration is %.1f px from the hero top and %.1f px from the bottom: not centred",
			graphTopGap, graphBottomGap)
	}

	grid := mustFindLayout(t, w, root, "gui:status:grid")
	page := mustFindLayout(t, w, root, "gui:content")
	if want := page.Shape.Width - page.Shape.Padding.Width(); absF(grid.Shape.Width-want) > 0.5 {
		t.Errorf("dashboard width %.1f != page content width %.1f", grid.Shape.Width, want)
	}
	if ov, ok := w.ScrollOverflowX(page.Shape.ID); !ok || ov > 0.5 {
		t.Errorf("Status page has %.1f px of horizontal overflow (ok=%v); want none", ov, ok)
	}

	assertCardHeaderRhythm(t, w, root)

	if stacked {
		if conn.Shape.Y <= hero.Shape.Y+hero.Shape.Height-0.5 {
			t.Errorf("connection panel y=%.0f does not sit below the hero (bottom %.0f)",
				conn.Shape.Y, hero.Shape.Y+hero.Shape.Height)
		}
		if absF(conn.Shape.X-hero.Shape.X) > 0.5 || absF(conn.Shape.Width-hero.Shape.Width) > 0.5 {
			t.Errorf("stacked connection panel is not hero-width: %.0f..%.0f vs %.0f..%.0f",
				conn.Shape.X, conn.Shape.X+conn.Shape.Width, hero.Shape.X, hero.Shape.X+hero.Shape.Width)
		}

		if absF(filtering.Shape.Y-lists.Shape.Y) > 1 {
			t.Errorf("filtering and filter lists are not on one row: y=%.0f/%.0f",
				filtering.Shape.Y, lists.Shape.Y)
		}
		if absF(filtering.Shape.Width-lists.Shape.Width) > 1 {
			t.Errorf("filtering width %.0f != filter lists width %.0f",
				filtering.Shape.Width, lists.Shape.Width)
		}
		if absF(filtering.Shape.Height-lists.Shape.Height) > 1 {
			t.Errorf("filtering height %.0f != filter lists height %.0f",
				filtering.Shape.Height, lists.Shape.Height)
		}
		if gap := lists.Shape.X - (filtering.Shape.X + filtering.Shape.Width); gap <= 0.5 {
			t.Errorf("filtering and filter lists have no gutter (%.1f)", gap)
		} else if absF(2*filtering.Shape.Width+gap-grid.Shape.Width) > 0.5 {
			t.Errorf("stacked pair (%.0f+%.0f+%.0f) does not fill the dashboard %.0f",
				filtering.Shape.Width, gap, lists.Shape.Width, grid.Shape.Width)
		}
		if absF(hero.Shape.Width-grid.Shape.Width) > 0.5 {
			t.Errorf("stacked hero width %.0f is not the dashboard width %.0f",
				hero.Shape.Width, grid.Shape.Width)
		}
		if cache.Shape.Y <= filtering.Shape.Y+filtering.Shape.Height-0.5 {
			t.Errorf("cache card y=%.0f does not sit below the first bottom row", cache.Shape.Y)
		}
		if absF(cache.Shape.Width-grid.Shape.Width) > 0.5 {
			t.Errorf("stacked cache width %.0f is not the dashboard width %.0f",
				cache.Shape.Width, grid.Shape.Width)
		}

		return
	}

	// Wide layout: hero and connection share the top row, hero wider, and the
	// pair ends on one baseline.
	if absF(conn.Shape.Y-hero.Shape.Y) > 1 {
		t.Errorf("top-row cards do not share a baseline: y=%.0f/%.0f", hero.Shape.Y, conn.Shape.Y)
	}
	if absF(conn.Shape.Height-hero.Shape.Height) > 1 {
		t.Errorf("connection height %.0f != hero height %.0f",
			conn.Shape.Height, hero.Shape.Height)
	}
	if conn.Shape.X <= hero.Shape.X {
		t.Errorf("connection panel x=%.0f is not right of the hero x=%.0f", conn.Shape.X, hero.Shape.X)
	}
	if conn.Shape.Width >= hero.Shape.Width {
		t.Errorf("connection panel width %.0f is not narrower than the hero %.0f",
			conn.Shape.Width, hero.Shape.Width)
	}
	topGap := conn.Shape.X - (hero.Shape.X + hero.Shape.Width)
	if topGap <= 0.5 {
		t.Errorf("top-row cards have no gutter (%.1f)", topGap)
	}

	bottomY := filtering.Shape.Y
	for _, ly := range []*gogui.Layout{lists, cache} {
		if absF(ly.Shape.Y-bottomY) > 1 {
			t.Errorf("%s y=%.0f is not on the bottom row y=%.0f", ly.Shape.ID, ly.Shape.Y, bottomY)
		}
		if absF(ly.Shape.Width-filtering.Shape.Width) > 1 {
			t.Errorf("%s width %.0f != filtering width %.0f",
				ly.Shape.ID, ly.Shape.Width, filtering.Shape.Width)
		}
		if absF(ly.Shape.Height-filtering.Shape.Height) > 1 {
			t.Errorf("%s height %.0f != filtering height %.0f",
				ly.Shape.ID, ly.Shape.Height, filtering.Shape.Height)
		}
	}
	if filtering.Shape.Y <= hero.Shape.Y+hero.Shape.Height-0.5 {
		t.Errorf("bottom row y=%.0f does not sit below the top row", filtering.Shape.Y)
	}

	// One grid: the connection card is the bottom row's third column, so its
	// left edge and width match the cache card's, and the hero's right edge
	// lands on the gutter before that column.
	if absF(conn.Shape.X-cache.Shape.X) > 0.5 {
		t.Errorf("connection x=%.0f does not land on the cache column x=%.0f",
			conn.Shape.X, cache.Shape.X)
	}
	if absF(conn.Shape.Width-cache.Shape.Width) > 0.5 {
		t.Errorf("connection width %.0f != cache column width %.0f",
			conn.Shape.Width, cache.Shape.Width)
	}
	if absF((hero.Shape.X+hero.Shape.Width)-(lists.Shape.X+lists.Shape.Width)) > 0.5 {
		t.Errorf("hero right %.0f does not land on the lists edge %.0f",
			hero.Shape.X+hero.Shape.Width, lists.Shape.X+lists.Shape.Width)
	}

	// One gutter across the whole dashboard.
	gap1 := lists.Shape.X - (filtering.Shape.X + filtering.Shape.Width)
	gap2 := cache.Shape.X - (lists.Shape.X + lists.Shape.Width)
	if gap1 <= 0.5 || gap2 <= 0.5 {
		t.Errorf("bottom-row gutter is missing: %.1f/%.1f", gap1, gap2)
	}
	if absF(gap1-gap2) > 0.5 {
		t.Errorf("bottom-row gutters differ: %.1f/%.1f", gap1, gap2)
	}
	if absF(topGap-gap1) > 0.5 {
		t.Errorf("top-row gutter %.1f != bottom-row gutter %.1f", topGap, gap1)
	}

	assertNoFindings(t, w)
}

// assertCardHeaderRhythm proves the four titled Status cards share one card
// anatomy: the title sits at the same inset, and the first row starts at the
// same distance below it.
func assertCardHeaderRhythm(t *testing.T, w *gogui.Window, root *gogui.Layout) {
	t.Helper()

	titles := map[string]string{
		"gui:card:connection":  "Connection Info",
		"gui:card:filtering":   "Filtering",
		"gui:card:filterlists": "Filter lists",
		"gui:card:cache":       "Cache & bootstrap",
	}
	var titleOffset, rowOffset float32 = -1, -1
	for id, title := range titles {
		card := mustFindLayout(t, w, root, id)
		if len(card.Children) < 2 {
			t.Fatalf("%s has %d children, want a title and at least one row", id, len(card.Children))
		}
		head := card.Children[0]
		if head.Shape.TC == nil || head.Shape.TC.Text != title {
			t.Fatalf("%s first child is not the %q title", id, title)
		}
		tOff := head.Shape.Y - card.Shape.Y
		rOff := card.Children[1].Shape.Y - card.Shape.Y
		if titleOffset < 0 {
			titleOffset, rowOffset = tOff, rOff

			continue
		}
		if absF(tOff-titleOffset) > 0.5 {
			t.Errorf("%s title inset %.1f != %.1f", id, tOff, titleOffset)
		}
		if absF(rOff-rowOffset) > 0.5 {
			t.Errorf("%s first row starts %.1f below the card, want %.1f", id, rOff, rowOffset)
		}
	}

	// The hero is deliberately not a titled card: its content group is centred
	// in the card's inner height rather than sitting on the title inset.
	hero := mustFindLayout(t, w, root, "gui:banner")
	row := mustFindLayout(t, w, root, "gui:banner:row")
	innerTop := hero.Shape.Y + hero.Shape.SizeBorder + hero.Shape.Padding.Top
	innerBottom := hero.Shape.Y + hero.Shape.Height - hero.Shape.SizeBorder - hero.Shape.Padding.Bottom
	if got, want := row.Shape.Y+row.Shape.Height/2, (innerTop+innerBottom)/2; absF(got-want) > 1 {
		t.Errorf("hero content centre %.1f != card centre %.1f", got, want)
	}
}

// TestStatusGridEqualColumnsWithStats proves the three-column grid holds when
// the Filtering card grows its pause row: the cards stay equal thirds instead
// of letting their content set the widths, and the pause control stays on the
// card's content edge down to the stack breakpoint.
func TestStatusGridEqualColumnsWithStats(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int
	}{
		{"default", defaultWidth, defaultHeight},
		{"screenshot", 1320, 700},
		{"stack-breakpoint", statusStackWidth, 700},
	} {
		t.Run(size.name, func(t *testing.T) {
			app := statusApp()
			app.snap.status.Stats = &ipc.RuntimeStats{Queries: 1234567, Blocked: 890}

			w, root := layoutFor(t, app, size.w, size.h)
			filtering := mustFindLayout(t, w, root, "gui:card:filtering")
			lists := mustFindLayout(t, w, root, "gui:card:filterlists")
			cache := mustFindLayout(t, w, root, "gui:card:cache")

			for _, ly := range []*gogui.Layout{lists, cache} {
				if absF(ly.Shape.Width-filtering.Shape.Width) > 0.5 {
					t.Errorf("%s width %.1f != filtering width %.1f",
						ly.Shape.ID, ly.Shape.Width, filtering.Shape.Width)
				}
			}

			pause := mustFindLayout(t, w, root, "gui:filtering:toggle")
			contentRight := filtering.Shape.X + filtering.Shape.Width -
				filtering.Shape.Padding.Right - filtering.Shape.SizeBorder
			if absF(pause.Shape.X+pause.Shape.Width-contentRight) > 0.5 {
				t.Errorf("pause control right %.1f != card content right %.1f",
					pause.Shape.X+pause.Shape.Width, contentRight)
			}
			if !layoutContains(filtering, pause) {
				t.Error("pause control is outside the Filtering card")
			}

			page := mustFindLayout(t, w, root, "gui:content")
			if ov, ok := w.ScrollOverflowX(page.Shape.ID); !ok || ov > 0.5 {
				t.Errorf("Status page has %.1f px of horizontal overflow (ok=%v); want none", ov, ok)
			}

			assertNoFindings(t, w)
		})
	}
}

// TestStatusFilterListsEmptyState proves an empty Filter lists card says so
// once, in a centred body, instead of rendering a bare em-dash row that reads
// as data.
func TestStatusFilterListsEmptyState(t *testing.T) {
	app := statusApp()
	app.snap.summary.Lists = nil
	app.snap.summary.ListCount = 0

	for _, size := range []struct {
		name string
		w, h int
	}{
		{"default", defaultWidth, defaultHeight},
		{"minimum", minWidth, minHeight},
	} {
		t.Run(size.name, func(t *testing.T) {
			w, root := layoutFor(t, app, size.w, size.h)
			card := mustFindLayout(t, w, root, "gui:card:filterlists")
			empty := mustFindLayout(t, w, root, "gui:status:filterlists:empty")

			if !layoutContains(card, empty) {
				t.Fatal("empty state is outside the Filter lists card")
			}
			if !textSet(empty)["No filter lists configured."] {
				t.Error(`empty state does not read "No filter lists configured."`)
			}
			if n := countText(card, "—"); n != 0 {
				t.Errorf("empty card still renders %d em-dash rows", n)
			}
			if empty.Shape.Height <= 0 {
				t.Error("empty state collapsed")
			}
		})
	}
}

// TestSettingsLayout proves the Settings page lays out the same way.
func TestSettingsLayout(t *testing.T) {
	ids := []string{"gui:card:service", "gui:card:tray", "gui:card:profiles", "gui:card:editor"}

	for _, size := range []struct {
		name string
		w, h int
	}{{"default", defaultWidth, defaultHeight}, {"large", wideWidth, wideHeight}} {
		t.Run(size.name, func(t *testing.T) {
			assertPageLayout(t, settingsApp(), size.w, size.h, ids)
		})
	}
}

// TestSettingsLayoutWhileStopped proves the editor still lays out when the
// service is stopped and the form is populated from the cache.
func TestSettingsLayoutWhileStopped(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.cacheSummary(sampleSnapshot())
	app.snap = snapshot{trust: trustVerified, installed: true, scmState: "stopped"}
	app.page = pageSettings

	assertPageLayout(t, app, defaultWidth, defaultHeight,
		[]string{"gui:card:service", "gui:card:tray", "gui:card:profiles", "gui:card:editor"})
}

// TestRulesLayout proves the Rules page lays out: both editor cards are
// present, content-sized, on one column, and the page body is one scrollable
// column.
func TestRulesLayout(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int
	}{{"default", defaultWidth, defaultHeight}, {"large", wideWidth, wideHeight}} {
		t.Run(size.name, func(t *testing.T) {
			assertPageLayout(t, rulesApp(), size.w, size.h,
				[]string{"gui:card:lists", "gui:card:rules"})
		})
	}
}

// TestRulesTwoCards proves the Rules page splits into two headed cards — the
// filter-list card and the per-domain routing card — with exactly one Save row,
// outside both cards.
func TestRulesTwoCards(t *testing.T) {
	w := newSettingsWindow(t, rulesApp())
	root := w.TestRender(nil)

	for _, id := range []string{"gui:card:lists", "gui:card:rules"} {
		if n := len(w.ResolveID(id)); n != 1 {
			t.Errorf("%s rendered %d times, want 1", id, n)
		}
	}

	// Each body lives under its own card, and only its own card.
	listsCard := mustFindLayout(t, w, root, "gui:card:lists")
	rulesCard := mustFindLayout(t, w, root, "gui:card:rules")
	listEditor := mustFindLayout(t, w, root, "gui:edit:lists")
	ruleEditor := mustFindLayout(t, w, root, "gui:edit:rules")

	if !layoutContains(listsCard, listEditor) {
		t.Error("Filter lists card does not contain the list editor")
	}
	if !layoutContains(rulesCard, ruleEditor) {
		t.Error("Per-domain routing card does not contain the rule editor")
	}
	if layoutContains(rulesCard, listEditor) {
		t.Error("Per-domain routing card contains the list editor")
	}
	if layoutContains(listsCard, ruleEditor) {
		t.Error("Filter lists card contains the rule editor")
	}

	// Exactly one Save, and it is outside both cards.
	if n := len(w.ResolveID("gui:profile:save")); n != 1 {
		t.Fatalf("Save rendered %d times, want exactly 1", n)
	}
	save := mustFindLayout(t, w, root, "gui:profile:save")
	if layoutContains(listsCard, save) || layoutContains(rulesCard, save) {
		t.Error("Save must sit outside both cards")
	}

	assertNoFindings(t, w)
}

// layoutContains reports whether target is parent or one of its descendants.
func layoutContains(parent, target *gogui.Layout) bool {
	if parent == target {
		return true
	}
	for i := range parent.Children {
		if layoutContains(&parent.Children[i], target) {
			return true
		}
	}

	return false
}

// TestRulesPageScrolls proves the Rules page body is a scrollable column:
// with a long rule set at the default window height the content overflows and
// is reachable by scrolling, rather than growing the window.
func TestRulesPageScrolls(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	for i := 0; i < 30; i++ {
		app.draft.perDomain = append(app.draft.perDomain, ruleDraft{
			domains: fmt.Sprintf("domain-%d.example", i), enabled: true,
		})
	}
	app.draft.rulesKnown = true
	app.seeded = true

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	root := w.TestRender(nil)

	page := mustFindLayout(t, w, root, "gui:rules")
	if !page.Shape.Scrollable {
		t.Fatal("Rules page body is not scrollable")
	}

	ids := w.ResolveID("gui:rules")
	if ov, ok := w.ScrollOverflowY(ids[0]); !ok || ov <= 0 {
		t.Errorf("Rules page hidden height = %.1f (ok=%v), want > 0", ov, ok)
	}
	assertNoFindings(t, w)
}

// TestRulesListSection proves the filter-list editor renders on the Rules page
// (one row per seeded list showing the redacted host, the add-by-URL field, and
// the "Update filter lists" action) — and is absent from Settings — with no
// widget findings.
func TestRulesListSection(t *testing.T) {
	app := rulesApp()
	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	for _, id := range []string{
		"gui:edit:lists",
		"gui:edit:filtering", "gui:edit:block",
		"gui:list:0", "gui:list:1",
		"gui:list:0:enable", "gui:list:0:remove",
		"gui:list:1:enable", "gui:list:1:remove",
		"gui:list:add:url", "gui:list:add",
		"gui:lists:refresh",
	} {
		if n := len(w.ResolveID(id)); n != 1 {
			t.Errorf("%s rendered %d times, want 1", id, n)
		}
	}

	// Filtering is part of the list card, directly above the rows.
	listsCard := mustFindLayout(t, w, root, "gui:card:lists")
	for _, id := range []string{"gui:edit:filtering", "gui:edit:block"} {
		if ly := mustFindLayout(t, w, root, id); !layoutContains(listsCard, ly) {
			t.Errorf("%s is not inside the Filter lists card", id)
		}
	}

	// The rows show the seeded redacted hosts, never a URL.
	texts := textSet(mustFindLayout(t, w, root, "gui:edit:lists"))
	for _, want := range []string{"ads.example.net", "trackers.example.org"} {
		if !texts[want] {
			t.Errorf("list section does not show %q", want)
		}
	}

	if got := len(w.ResolveID("gui:list:2")); got != 0 {
		t.Errorf("a third list row rendered without being added (%d)", got)
	}

	// Running service: the refresh action is offered.
	assertDisabled(t, root, w, "gui:lists:refresh", false)
	assertNoFindings(t, w)

	// The moved controls follow the Rules page's own gates.
	off := rulesApp()
	off.ensureDraft()
	off.draft.filtering = false
	off.seeded = true
	ow := newSettingsWindow(t, off)
	oroot := ow.TestRender(nil)
	assertDisabled(t, oroot, ow, "gui:edit:block", true)

	busy := rulesApp()
	busy.ensureDraft()
	busy.draft.name = "default"
	busy.busy = true
	busy.seeded = true
	bw := newSettingsWindow(t, busy)
	broot := bw.TestRender(nil)
	assertDisabled(t, broot, bw, "gui:edit:filtering", true)
	assertDisabled(t, broot, bw, "gui:edit:block", true)
}

// TestSettingsDropsMovedSections proves the filter-list, filtering, and
// per-domain editors no longer render on the Settings page (they moved to the
// Rules page).
func TestSettingsDropsMovedSections(t *testing.T) {
	w := newSettingsWindow(t, settingsApp())
	w.TestRender(nil)

	for _, id := range []string{
		"gui:edit:lists", "gui:edit:rules", "gui:edit:filtering", "gui:edit:block",
		"gui:rule:add", "gui:list:add",
	} {
		if n := len(w.ResolveID(id)); n != 0 {
			t.Errorf("%s still renders on Settings (%d), want moved to Rules", id, n)
		}
	}
}

// TestListAddRemoveRows drives the add-by-URL field and a remove control on the
// Rules page and checks the draft rows track them.
func TestListAddRemoveRows(t *testing.T) {
	app := rulesApp()
	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if err := w.TestType("gui:list:add:url", "https://new.example/hosts.txt"); err != nil {
		t.Fatalf("type list url: %v", err)
	}
	if err := w.TestClick("gui:list:add"); err != nil {
		t.Fatalf("click add list: %v", err)
	}
	if len(app.draft.lists) != 3 {
		t.Fatalf("lists = %d, want 3", len(app.draft.lists))
	}
	if app.listInput != "" {
		t.Fatalf("add field not cleared: %q", app.listInput)
	}
	w.TestRender(nil)
	if n := len(w.ResolveID("gui:list:2:host")); n != 1 {
		t.Fatalf("new list row = %d, want 1", n)
	}

	if err := w.TestClick("gui:list:0:remove"); err != nil {
		t.Fatalf("click remove list: %v", err)
	}
	// Remove asks first; confirm it.
	w.TestRender(nil)
	if err := w.TestClick("gui:list:0:remove:confirm:1"); err != nil {
		t.Fatalf("confirm remove list: %v", err)
	}
	if len(app.draft.lists) != 2 {
		t.Fatalf("lists after remove = %d, want 2", len(app.draft.lists))
	}
	assertNoFindings(t, w)
}

// TestRefreshDisabledWhenStopped proves "Update filter lists" is inert while the
// service is not running (the action would have nothing to drive).
func TestRefreshDisabledWhenStopped(t *testing.T) {
	app := rulesApp()
	app.snap.running = false
	app.snap.status = nil

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)
	assertDisabled(t, root, w, "gui:lists:refresh", true)
}

// TestRulesRuleSection proves the per-domain editor renders on the Rules page
// a row per rule (domains + upstream, upstream blank, an enable toggle) plus
// the add control and the wildcard hint, with no findings.
func TestRulesRuleSection(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{
		{domains: "example.com, www.example.com", enabled: true},
		{domains: "ads.example", upstream: "1.1.1.1", enabled: false},
	}
	app.draft.rulesKnown = true
	app.seeded = true // keep the hand-built draft; do not reseed from the summary

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	for _, id := range []string{
		"gui:edit:rules",
		"gui:rule:0:domains", "gui:rule:0:kind", "gui:rule:0:upstream", "gui:rule:0:enable", "gui:rule:0:remove",
		"gui:rule:1:domains", "gui:rule:1:kind", "gui:rule:1:upstream", "gui:rule:1:enable", "gui:rule:1:remove",
		"gui:rule:add",
	} {
		if n := len(w.ResolveID(id)); n != 1 {
			t.Errorf("%s rendered %d times, want 1", id, n)
		}
	}

	// A seeded rule's upstream starts blank: the editor never reads a stored
	// target.
	if app.draft.perDomain[0].upstream != "" {
		t.Errorf("seeded rule upstream = %q, want blank", app.draft.perDomain[0].upstream)
	}

	// The seeded enable state travels into the rows: rule 0 on, rule 1 off.
	if !app.draft.perDomain[0].enabled || app.draft.perDomain[1].enabled {
		t.Errorf("seeded enable states = %v/%v, want true/false",
			app.draft.perDomain[0].enabled, app.draft.perDomain[1].enabled)
	}
	assertNoFindings(t, w)
}

// TestRulesRuleKind proves the per-domain editor offers both rule kinds: an
// Upstream / Bypass selector per row, a live upstream input only for an
// upstream rule, and a grounded caption in its place for a bypass rule — with
// the enable and remove controls still present for both.
func TestRulesRuleKind(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{
		{domains: "up.example", enabled: true},
		{domains: "bypass.example", bypass: true, enabled: true},
	}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	// Both rows offer the kind selector, and both keep the row controls.
	for _, id := range []string{
		"gui:rule:0:kind", "gui:rule:1:kind",
		"gui:rule:0:enable", "gui:rule:1:enable",
		"gui:rule:0:remove", "gui:rule:1:remove",
	} {
		if n := len(w.ResolveID(id)); n != 1 {
			t.Errorf("%s rendered %d times, want 1", id, n)
		}
	}

	// Only the upstream row has a live target input.
	if n := len(w.ResolveID("gui:rule:0:upstream")); n != 1 {
		t.Errorf("upstream row target input = %d, want 1", n)
	}
	if n := len(w.ResolveID("gui:rule:1:upstream")); n != 0 {
		t.Errorf("bypass row has an upstream input (%d), want none", n)
	}

	// The bypass row states where its domains resolve; the upstream row does not.
	const want = "Resolved by the system's normal DNS, not by Sigil DNS."
	if !textSet(mustFindLayout(t, w, root, "gui:rule:1"))[want] {
		t.Errorf("bypass row does not show %q", want)
	}
	if textSet(mustFindLayout(t, w, root, "gui:rule:0"))[want] {
		t.Error("upstream row shows the bypass caption")
	}

	assertNoFindings(t, w)
}

// TestRulesKindFitsMinWindow proves the kind selector and its row keep the
// Rules page inside the minimum window width (no horizontal scrollbar).
func TestRulesKindFitsMinWindow(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{
		{domains: "up.example", enabled: true},
		{domains: "bypass.example", bypass: true, enabled: true},
	}
	app.draft.rulesKnown = true
	app.seeded = true

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: minWidth, Height: minHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	ids := w.ResolveID("gui:rules")
	if len(ids) != 1 {
		t.Fatalf("rules page widgets = %d, want 1", len(ids))
	}
	if ov, ok := w.ScrollOverflowX(ids[0]); !ok || ov > 0.5 {
		t.Errorf("rules page has %.1f px of horizontal overflow (ok=%v); want none", ov, ok)
	}
	assertNoFindings(t, w)
}

// TestRuleKindSwitch proves switching a row to Bypass drops its live upstream
// input for the caption while keeping the row's other state, and switching back
// restores the input.
func TestRuleKindSwitch(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{{domains: "a.example", upstream: "1.1.1.1", enabled: false}}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if err := w.TestClick("gui:rule:0:kind:opt:1"); err != nil {
		t.Fatalf("click Bypass segment: %v", err)
	}
	w.TestRender(nil)
	if !app.draft.perDomain[0].bypass {
		t.Fatal("Bypass segment did not set the rule kind")
	}
	if n := len(w.ResolveID("gui:rule:0:upstream")); n != 0 {
		t.Errorf("bypass row still shows an upstream input (%d)", n)
	}
	// Switching the kind leaves the rest of the row alone.
	if app.draft.perDomain[0].domains != "a.example" || app.draft.perDomain[0].enabled {
		t.Errorf("row state changed while switching kind: %+v", app.draft.perDomain[0])
	}

	if err := w.TestClick("gui:rule:0:kind:opt:0"); err != nil {
		t.Fatalf("click Upstream segment: %v", err)
	}
	w.TestRender(nil)
	if app.draft.perDomain[0].bypass {
		t.Fatal("Upstream segment did not clear the rule kind")
	}
	if n := len(w.ResolveID("gui:rule:0:upstream")); n != 1 {
		t.Errorf("upstream row input = %d, want 1", n)
	}
	assertNoFindings(t, w)
}

// TestRuleToggleRow proves clicking a rule's enable toggle flips only that
// row's flag in the draft.
func TestRuleToggleRow(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{
		{domains: "a.example", enabled: true},
		{domains: "b.example", enabled: true},
	}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if err := w.TestClick("gui:rule:1:enable"); err != nil {
		t.Fatalf("click rule toggle: %v", err)
	}
	if app.draft.perDomain[0].enabled != true || app.draft.perDomain[1].enabled != false {
		t.Fatalf("enable states = %v/%v, want true/false",
			app.draft.perDomain[0].enabled, app.draft.perDomain[1].enabled)
	}
	assertNoFindings(t, w)
}

// TestRulesWildcardHint proves the per-domain section explains how the `*.`
// pattern matches, and that the domains input keeps it as typed (no
// rewriting).
func TestRulesWildcardHint(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{{domains: "*.example.com", enabled: true}}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	const want = "*.example.com matches subdomains only; example.com matches the domain and its subdomains."
	if !textSet(root)[want] {
		t.Errorf("wildcard hint not found; want %q", want)
	}

	// The value passes through untouched.
	if app.draft.perDomain[0].domains != "*.example.com" {
		t.Errorf("wildcard input rewritten: %q", app.draft.perDomain[0].domains)
	}
}

// TestRuleAddRemoveRows drives the Add rule and per-row Remove controls on the
// Rules page, and checks a new rule starts enabled.
func TestRuleAddRemoveRows(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{{domains: "a.example", enabled: true}}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if err := w.TestClick("gui:rule:add"); err != nil {
		t.Fatalf("click add rule: %v", err)
	}
	if len(app.draft.perDomain) != 2 {
		t.Fatalf("rules = %d, want 2", len(app.draft.perDomain))
	}
	if !app.draft.perDomain[1].enabled {
		t.Error("a newly added rule must start enabled")
	}
	w.TestRender(nil)
	if n := len(w.ResolveID("gui:rule:1:domains")); n != 1 {
		t.Fatalf("added rule row = %d, want 1", n)
	}

	if err := w.TestClick("gui:rule:0:remove"); err != nil {
		t.Fatalf("click remove rule: %v", err)
	}
	// Remove asks first; confirm it.
	w.TestRender(nil)
	if err := w.TestClick("gui:rule:0:remove:confirm:1"); err != nil {
		t.Fatalf("confirm remove rule: %v", err)
	}
	if len(app.draft.perDomain) != 1 {
		t.Fatalf("rules after remove = %d, want 1", len(app.draft.perDomain))
	}
	assertNoFindings(t, w)
}

// TestRemoveConfirm proves both Remove controls ask before deleting: the confirm
// dialog appears, the row survives until Yes, and No leaves it untouched.
func TestRemoveConfirm(t *testing.T) {
	ruleApp := func() *App {
		app := rulesApp()
		app.draft.name = "default"
		app.draft.perDomain = []ruleDraft{
			{domains: "a.example", enabled: true},
			{domains: "b.example", enabled: true},
		}
		app.draft.rulesKnown = true
		app.seeded = true

		return app
	}

	t.Run("rule yes removes", func(t *testing.T) {
		app := ruleApp()
		w := newSettingsWindow(t, app)
		w.TestRender(nil)

		if err := w.TestClick("gui:rule:0:remove"); err != nil {
			t.Fatalf("click remove: %v", err)
		}
		w.TestRender(nil)

		// The dialog is up and nothing is removed yet.
		if n := len(w.ResolveID("gui:rule:0:remove:confirm")); n != 1 {
			t.Fatalf("confirm dialog missing (%d)", n)
		}
		if len(app.draft.perDomain) != 2 {
			t.Fatalf("row removed before confirming: %d", len(app.draft.perDomain))
		}

		if err := w.TestClick("gui:rule:0:remove:confirm:1"); err != nil {
			t.Fatalf("click confirm yes: %v", err)
		}
		w.TestRender(nil)
		if len(app.draft.perDomain) != 1 || app.draft.perDomain[0].domains != "b.example" {
			t.Fatalf("rules after confirm = %+v, want just b.example", app.draft.perDomain)
		}
		assertNoFindings(t, w)
	})

	t.Run("rule no keeps", func(t *testing.T) {
		app := ruleApp()
		w := newSettingsWindow(t, app)
		w.TestRender(nil)

		if err := w.TestClick("gui:rule:0:remove"); err != nil {
			t.Fatalf("click remove: %v", err)
		}
		w.TestRender(nil)
		if err := w.TestClick("gui:rule:0:remove:confirm"); err != nil {
			t.Fatalf("click confirm no: %v", err)
		}
		w.TestRender(nil)
		if len(app.draft.perDomain) != 2 {
			t.Fatalf("row removed on cancel: %d", len(app.draft.perDomain))
		}
		assertNoFindings(t, w)
	})

	t.Run("list yes removes", func(t *testing.T) {
		app := rulesApp()
		w := newSettingsWindow(t, app)
		w.TestRender(nil)
		before := len(app.draft.lists)

		if err := w.TestClick("gui:list:0:remove"); err != nil {
			t.Fatalf("click remove: %v", err)
		}
		w.TestRender(nil)
		if n := len(w.ResolveID("gui:list:0:remove:confirm")); n != 1 {
			t.Fatalf("confirm dialog missing (%d)", n)
		}
		if len(app.draft.lists) != before {
			t.Fatalf("list removed before confirming: %d", len(app.draft.lists))
		}

		if err := w.TestClick("gui:list:0:remove:confirm:1"); err != nil {
			t.Fatalf("click confirm yes: %v", err)
		}
		w.TestRender(nil)
		if len(app.draft.lists) != before-1 {
			t.Fatalf("lists after confirm = %d, want %d", len(app.draft.lists), before-1)
		}
		assertNoFindings(t, w)
	})

	t.Run("list no keeps", func(t *testing.T) {
		app := rulesApp()
		w := newSettingsWindow(t, app)
		w.TestRender(nil)
		before := len(app.draft.lists)

		if err := w.TestClick("gui:list:0:remove"); err != nil {
			t.Fatalf("click remove: %v", err)
		}
		w.TestRender(nil)
		if err := w.TestClick("gui:list:0:remove:confirm"); err != nil {
			t.Fatalf("click confirm no: %v", err)
		}
		w.TestRender(nil)
		if len(app.draft.lists) != before {
			t.Fatalf("list removed on cancel: %d", len(app.draft.lists))
		}
		assertNoFindings(t, w)
	})
}

// TestRuleRowItemsCentred proves the Enabled toggle and the Remove button are
// vertically centred on the upstream input's centreline: the labelled input's
// label stack used to leave them pinned high.
func TestRuleRowItemsCentred(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{{domains: "a.example", enabled: true}}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	field := mustFindLayout(t, w, root, "gui:rule:0:upstream")
	fieldCY := field.Shape.Y + field.Shape.Height/2
	for _, id := range []string{"gui:rule:0:enable", "gui:rule:0:remove"} {
		ly := mustFindLayout(t, w, root, id)
		cy := ly.Shape.Y + ly.Shape.Height/2
		if absF(cy-fieldCY) > 1 {
			t.Errorf("%s centre %.1f is not on the upstream field centre %.1f", id, cy, fieldCY)
		}
	}
}

// TestRemoveButtonsDanger proves the repeated per-row destructive controls wear
// the danger *outline* — danger-styled (error border) but subordinate (neutral
// fill), not the filled danger block — matching the profile Delete; and that a
// disabled one reads muted rather than full-red.
func TestRemoveButtonsDanger(t *testing.T) {
	newApp := func(busy bool) *App {
		app := rulesApp()
		app.busy = busy
		app.draft.name = "default"
		app.draft.perDomain = []ruleDraft{{domains: "a.example", enabled: true}}
		app.draft.lists = []listDraft{{host: "ads.example", enabled: true, idx: 0}}
		app.draft.rulesKnown = true
		app.seeded = true

		return app
	}

	t.Run("enabled is a danger outline, not a filled block", func(t *testing.T) {
		w := newSettingsWindow(t, newApp(false))
		root := w.TestRender(nil)
		th := gogui.CurrentTheme()

		for _, id := range []string{"gui:rule:0:remove", "gui:list:0:remove"} {
			ly := mustFindLayout(t, w, root, id)
			if ly.Shape.Color == th.Cfg.ColorError {
				t.Errorf("%s fill = danger %v: a row remove must be an outline, not a filled block",
					id, ly.Shape.Color)
			}
			if ly.Shape.ColorBorder != th.Cfg.ColorError {
				t.Errorf("%s border = %v, want danger outline %v", id, ly.Shape.ColorBorder, th.Cfg.ColorError)
			}
		}
	})

	t.Run("disabled reads muted", func(t *testing.T) {
		w := newSettingsWindow(t, newApp(true))
		root := w.TestRender(nil)
		th := gogui.CurrentTheme()

		ly := mustFindLayout(t, w, root, "gui:rule:0:remove")
		if !ly.Shape.Disabled {
			t.Fatal("remove should be disabled while busy")
		}
		if ly.Shape.Color != th.Cfg.ColorActive {
			t.Errorf("disabled remove fill = %v, want muted %v", ly.Shape.Color, th.Cfg.ColorActive)
		}
		// The silhouette stays a button: a visible neutral border, not a red
		// one blended away behind bare error text.
		if ly.Shape.ColorBorder != th.Cfg.ColorBorder {
			t.Errorf("disabled remove border = %v, want neutral %v", ly.Shape.ColorBorder, th.Cfg.ColorBorder)
		}
	})
}

// TestSettingsCardTitleStrongerThanGroupHeading proves the editor's two heading
// levels are distinct: the card title is the strong level and the in-card group
// heading is a clearly subordinate, quieter level.
func TestSettingsCardTitleStrongerThanGroupHeading(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, 2400)

	card := mustFindLayout(t, w, root, "gui:card:editor")
	title := childLayout(card, 0)
	if title == nil || title.Shape == nil || title.Shape.TC == nil {
		t.Fatal("editor card title text not found")
	}
	group := mustFindLayout(t, w, root, "gui:edit:group:provider")
	if group.Shape == nil || group.Shape.TC == nil {
		t.Fatal("group heading text not found")
	}

	titleStyle := title.Shape.TC.TextStyle
	groupStyle := group.Shape.TC.TextStyle
	if titleStyle.Size <= groupStyle.Size {
		t.Errorf("card title size %.1f is not larger than group heading size %.1f: levels are flat",
			titleStyle.Size, groupStyle.Size)
	}
	if titleStyle.Color == groupStyle.Color {
		t.Errorf("card title and group heading share color %v: the heading must be quieter", titleStyle.Color)
	}
}

// TestRulesControlColumnsAligned proves the repeated row controls share fixed
// columns within each section: the list rows' toggles/removes line up, and the
// rule rows' do too.  A rule is a contained panel with its own inner padding,
// so its control column is inset from the list rows' by that padding; each
// section still holds one deliberate enable column and one remove column rather
// than drifting row to row.
func TestRulesControlColumnsAligned(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{
		{domains: "a.example", enabled: true},
		{domains: "b.example", enabled: true},
	}
	app.draft.lists = []listDraft{
		{host: "ads.example.net", enabled: true, idx: 0},
		{host: "trackers.example.org", enabled: false, idx: 1},
	}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	for _, pair := range [][2]string{
		{"gui:list:0:enable", "gui:list:1:enable"},
		{"gui:rule:0:enable", "gui:rule:1:enable"},
	} {
		a := mustFindLayout(t, w, root, pair[0])
		b := mustFindLayout(t, w, root, pair[1])
		if absF(a.Shape.X-b.Shape.X) > 1 {
			t.Errorf("%s x=%.1f != %s x=%.1f: enable column drifts",
				pair[0], a.Shape.X, pair[1], b.Shape.X)
		}
	}

	for _, pair := range [][2]string{
		{"gui:list:0:remove", "gui:list:1:remove"},
		{"gui:rule:0:remove", "gui:rule:1:remove"},
	} {
		a := mustFindLayout(t, w, root, pair[0])
		b := mustFindLayout(t, w, root, pair[1])
		if ar, br := a.Shape.X+a.Shape.Width, b.Shape.X+b.Shape.Width; absF(ar-br) > 1 {
			t.Errorf("%s remove right=%.1f != %s right=%.1f: remove column drifts",
				pair[0], ar, pair[1], br)
		}
	}
}

// TestRuleRowsArePaneled proves each rule is its own contained panel — a
// hairline border and a numbered header above its fields — so a stack of rules
// reads as distinct units rather than one undifferentiated column.
func TestRuleRowsArePaneled(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{
		{domains: "a.example", enabled: true},
		{domains: "b.example", enabled: true},
	}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)
	th := gogui.CurrentTheme()

	for i, want := range []string{"Rule 1", "Rule 2"} {
		panel := mustFindLayout(t, w, root, fmt.Sprintf("gui:rule:%d", i))
		if panel.Shape.ColorBorder != th.ColorBorder || panel.Shape.SizeBorder == 0 {
			t.Errorf("rule %d panel has no hairline border (color %v, size %.0f)",
				i, panel.Shape.ColorBorder, panel.Shape.SizeBorder)
		}
		if !textSet(panel)[want] {
			t.Errorf("rule %d panel does not carry its %q header", i, want)
		}

		title := mustFindLayout(t, w, root, fmt.Sprintf("gui:rule:%d:title", i))
		domains := mustFindLayout(t, w, root, fmt.Sprintf("gui:rule:%d:domains", i))
		if title.Shape.Y >= domains.Shape.Y {
			t.Errorf("rule %d header y=%.0f is not above its fields y=%.0f",
				i, title.Shape.Y, domains.Shape.Y)
		}
	}

	// Each panel carries its own separator so the header reads as a header, not
	// as the first field.
	if n := len(w.ResolveID("gui:rule:1:sep")); n != 1 {
		t.Errorf("gui:rule:1:sep rendered %d times, want 1", n)
	}
	assertNoFindings(t, w)
}

// TestAddRuleGuard proves "Add rule" is inert while any rule row has blank
// domains — so empty rules cannot be stacked — and live once every rule is
// filled or when there are none.
func TestAddRuleGuard(t *testing.T) {
	cases := []struct {
		name  string
		rules []ruleDraft
		want  bool
	}{
		{"no rules", nil, false},
		{"all filled", []ruleDraft{{domains: "a.example", enabled: true}}, false},
		{"one blank", []ruleDraft{{enabled: true}}, true},
		{"blank among filled", []ruleDraft{
			{domains: "a.example", enabled: true},
			{domains: "  ", enabled: true},
		}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := rulesApp()
			app.draft.name = "default"
			app.draft.perDomain = tc.rules
			app.draft.rulesKnown = true
			app.seeded = true

			w := newSettingsWindow(t, app)
			root := w.TestRender(nil)
			assertDisabled(t, root, w, "gui:rule:add", tc.want)
		})
	}
}

// TestAddRuleEnablesWhenFilled proves the Add rule guard clears as the user
// fills the last blank row (the row handler already repaints).
func TestAddRuleEnablesWhenFilled(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{{enabled: true}}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)
	assertDisabled(t, root, w, "gui:rule:add", true)

	if err := w.TestType("gui:rule:0:domains", "a.example"); err != nil {
		t.Fatalf("type domains: %v", err)
	}
	root = w.TestRender(nil)
	assertDisabled(t, root, w, "gui:rule:add", false)
}

// TestNoticeCalmAndTitled proves the inline notice is a calm neutral panel with
// a heading title (not a full-bleed warning wash in body text), and that the two
// severities stay distinguishable by their accent stripe.
func TestNoticeCalmAndTitled(t *testing.T) {
	th := gogui.CurrentTheme()

	unverified := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	unverified.snap = snapshot{trust: trustUnverified, running: true, updated: time.Now()}
	unverified.page = pageSettings

	w, root := layoutFor(t, unverified, defaultWidth, 2400)
	n := mustFindLayout(t, w, root, "gui:notice")
	if n.Shape.Color != th.ColorPanel {
		t.Errorf("notice fill = %v, want calm panel %v", n.Shape.Color, th.ColorPanel)
	}
	stripe := childLayout(n, 0)
	body := childLayout(n, 1)
	title := childLayout(body, 0)
	detail := childLayout(body, 1)
	if stripe == nil || title == nil || detail == nil ||
		stripe.Shape == nil || title.Shape == nil || detail.Shape == nil ||
		title.Shape.TC == nil || detail.Shape.TC == nil {
		t.Fatal("notice stripe/title/detail missing")
	}
	if *title.Shape.TC.TextStyle != th.TextStyleTitleSmall {
		t.Errorf("notice title is not the heading role (size %.1f, color %v)",
			title.Shape.TC.TextStyle.Size, title.Shape.TC.TextStyle.Color)
	}
	if *detail.Shape.TC.TextStyle != th.TextStyleSecondary {
		t.Errorf("notice detail is not the secondary role (size %.1f)",
			detail.Shape.TC.TextStyle.Size)
	}
	warnAccent := stripe.Shape.Color

	offline := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	offline.cached = sampleSnapshot().summary
	offline.snap = snapshot{trust: trustVerified, installed: true, scmState: "stopped"}
	offline.page = pageSettings
	w2, root2 := layoutFor(t, offline, defaultWidth, 2400)
	n2 := mustFindLayout(t, w2, root2, "gui:notice")
	stripe2 := childLayout(n2, 0)
	if stripe2.Shape.Color == warnAccent {
		t.Errorf("both notice severities share accent %v; keep them distinguishable", warnAccent)
	}
}

// TestSettingsOfflineNoticeWraps proves the long "Offline editing" notice// wraps instead of forcing the page into a horizontal scrollbar. The notice is
// a fill-width column: a fit-width one takes the length of its unwrapped detail
// sentence and pushes the scrollable page wider than the window.
func TestSettingsOfflineNoticeWraps(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.cached = sampleSnapshot().summary
	app.snap = snapshot{trust: trustVerified, installed: true, scmState: "stopped"}
	app.page = pageSettings

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: minWidth, Height: minHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	ids := w.ResolveID("gui:settings")
	if len(ids) != 1 {
		t.Fatalf("settings page widgets = %d, want 1", len(ids))
	}
	if ov, ok := w.ScrollOverflowX(ids[0]); !ok || ov > 0.5 {
		t.Errorf("settings page has %.1f px of horizontal overflow (ok=%v); want none", ov, ok)
	}
}

// TestStatusHasNoLifecycleControls proves the Status page carries no lifecycle
// controls — Start/Stop and Install/Uninstall live in Settings, and the header
// has no power button — and that the header shows the service state badge.
func TestStatusHasNoLifecycleControls(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	for _, id := range []string{
		"gui:power",
		"gui:action:enable", "gui:action:disable", "gui:action:restore",
		"gui:service:install", "gui:service:uninstall",
		"gui:service:start", "gui:service:stop",
	} {
		if len(w.ResolveID(id)) != 0 {
			t.Errorf("Status page must not render %q", id)
		}
	}

	// The SCM state badge sits in the header, beside the app name.
	if !textSet(mustFindLayout(t, w, root, "gui:brand:title"))["Running"] {
		t.Error("header title row does not carry the service state badge")
	}

	assertNoFindings(t, w)
}

// TestStatusRestoreOnlyForDirtyLeftover proves Restore is offered only for the
// crash-leftover state (!Enabled && Dirty), never beside a healthy session.
func TestStatusRestoreOnlyForDirtyLeftover(t *testing.T) {
	healthy := statusApp()
	if n := len(renderIDs(t, healthy, "gui:action:restore")); n != 0 {
		t.Fatalf("healthy running showed %d Restore buttons", n)
	}

	dirty := statusApp()
	dirty.snap.status.Enabled = false
	dirty.snap.status.Mode = "off"

	if n := len(renderIDs(t, dirty, "gui:action:restore")); n != 1 {
		t.Fatalf("dirty-leftover showed %d Restore buttons, want 1", n)
	}
}

// renderIDs builds a Status window for app and returns the resolved IDs.
func renderIDs(t *testing.T, app *App, id string) []string {
	t.Helper()

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	return w.ResolveID(id)
}

// layoutFor builds a window for app and returns it with the arranged layout.
func layoutFor(t *testing.T, app *App, width, height int) (*gogui.Window, *gogui.Layout) {
	t.Helper()

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: width, Height: height, State: app,
	})
	w.SetView(view)

	return w, w.TestRender(nil)
}

// TestNavRailCompact proves the rail hugs its NavigationView items (no dead
// space) and every icon+label item still fits inside the fixed item width.
func TestNavRailCompact(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	rail := mustFindLayout(t, w, root, "gui:nav")
	btn := mustFindLayout(t, w, root, "gui:nav:status")

	// The rail is one item wide plus its own inset.
	wantRail := navRailWidth + 2*navRailPad
	if absF(rail.Shape.Width-wantRail) > 2 {
		t.Errorf("rail width %.0f, want %.0f (item %.0f + rail inset)",
			rail.Shape.Width, wantRail, navRailWidth)
	}
	if rail.Shape.Width-btn.Shape.Width > 2*navRailPad+2 {
		t.Errorf("rail width %.0f vs button width %.0f: dead space beside the items",
			rail.Shape.Width, btn.Shape.Width)
	}

	// Every item is the same width and has room for the accent bar, the icon
	// column and the longest label ("Settings").
	inset := gogui.CurrentTheme().PaddingButton
	// bar (3) + icon column + two inter-part gaps (SpacingMedium each).
	navItemLeading := 3 + navIconWidth + 2*gogui.CurrentTheme().SpacingMedium
	for _, tc := range []struct{ id, label string }{
		{"gui:nav:status", "Status"},
		{"gui:nav:settings", "Settings"},
		{"gui:nav:rules", "Rules"},
		{"gui:nav:about", "About"},
	} {
		button := mustFindLayout(t, w, root, tc.id)
		if absF(button.Shape.Width-navRailWidth) > 1 {
			t.Errorf("%s width %.0f != navRailWidth %.0f", tc.id, button.Shape.Width, navRailWidth)
		}
		labelW := findTextWidth(button, tc.label)
		if labelW == 0 {
			t.Errorf("%s does not render its %q label", tc.id, tc.label)

			continue
		}
		// Measure the label's intrinsic width (font-free fallback included)
		// and check the leading parts plus the button inset leave room for it.
		intrinsic := w.TextWidth(tc.label, gogui.CurrentTheme().TextStyleBody)
		if intrinsic+navItemLeading+inset.Left+inset.Right > navRailWidth {
			t.Errorf("%s label %.0f + leading %.0f + inset %.0f exceeds item width %.0f: label would clip",
				tc.id, intrinsic, navItemLeading, inset.Left+inset.Right, navRailWidth)
		}
	}
}

// TestNavLabelsShareLeftEdge proves the icon column fixes the label X: every
// nav label starts at the same point, right after the fixed-width icon slot and
// one gap, and each glyph is centred in that slot.  Glyph widths differ between
// fonts, so only a fixed slot can keep the labels on one edge.
func TestNavLabelsShareLeftEdge(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)
	gap := gogui.CurrentTheme().SpacingMedium

	var labelX float32
	for i, tc := range []struct{ button, icon, label string }{
		{"gui:nav:status", "gui:nav:icon:status", "Status"},
		{"gui:nav:settings", "gui:nav:icon:settings", "Settings"},
		{"gui:nav:rules", "gui:nav:icon:rules", "Rules"},
		{"gui:nav:logging", "gui:nav:icon:logging", "Logging"},
		{"gui:nav:about", "gui:nav:icon:about", "About"},
	} {
		btn := mustFindLayout(t, w, root, tc.button)
		slot := mustFindLayout(t, w, root, tc.icon)
		label := findTextLayout(btn, tc.label)
		if label == nil || label.Shape == nil {
			t.Fatalf("%s does not render its %q label", tc.button, tc.label)
		}

		if i == 0 {
			labelX = label.Shape.X
		} else if absF(label.Shape.X-labelX) > 1 {
			t.Errorf("%s label x=%.0f, want %.0f: labels are ragged", tc.button, label.Shape.X, labelX)
		}

		if want := slot.Shape.X + slot.Shape.Width + gap; absF(label.Shape.X-want) > 1 {
			t.Errorf("%s label x=%.0f, want icon slot right edge + gap = %.0f", tc.button, label.Shape.X, want)
		}

		glyph := childLayout(slot, 0)
		if glyph == nil || glyph.Shape == nil {
			t.Fatalf("%s icon slot is empty", tc.button)
		}
		if absF(glyph.Shape.X+glyph.Shape.Width/2-(slot.Shape.X+slot.Shape.Width/2)) > 1 {
			t.Errorf("%s glyph centre is not the icon slot centre", tc.button)
		}
	}
}

// findTextWidth returns the width of the first text shape in the subtree whose
// content is label, or 0 when none renders it.
func findTextWidth(ly *gogui.Layout, label string) (width float32) {
	if got := findTextLayout(ly, label); got != nil {
		return got.Shape.Width
	}

	return 0
}

// findTextLayout returns the first text layout in the subtree whose content is
// label.
func findTextLayout(ly *gogui.Layout, label string) *gogui.Layout {
	if ly == nil || ly.Shape == nil {
		return nil
	}
	if ly.Shape.TC != nil && ly.Shape.TC.Text == label {
		return ly
	}
	for i := range ly.Children {
		if got := findTextLayout(&ly.Children[i], label); got != nil {
			return got
		}
	}

	return nil
}

// TestBannerIndicatorCentred proves the status indicator is vertically centred
// on the headline line inside the state pill.
func TestBannerIndicatorCentred(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	dot := mustFindLayout(t, w, root, "gui:banner:dot")
	headline := mustFindLayout(t, w, root, "gui:banner:headline")

	dotCY := dot.Shape.Y + dot.Shape.Height/2
	headlineCY := headline.Shape.Y + headline.Shape.Height/2
	if absF(dotCY-headlineCY) > 2 {
		t.Errorf("indicator centre %.1f is not on the headline centre %.1f", dotCY, headlineCY)
	}
}

// TestHeroAligned proves the hero's caption, headline pill, and detail body
// share one left edge inside the card.
func TestHeroAligned(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	body := mustFindLayout(t, w, root, "gui:banner:body")
	pill := mustFindLayout(t, w, root, "gui:banner:title")
	detail := mustFindLayout(t, w, root, "gui:banner:detail")

	left := body.Shape.X + body.Shape.Padding.Left + body.Shape.SizeBorder
	if absF(pill.Shape.X-left) > 1 {
		t.Errorf("pill x=%.0f, want hero content x=%.0f", pill.Shape.X, left)
	}
	if absF(detail.Shape.X-left) > 1 {
		t.Errorf("detail x=%.0f, want hero content x=%.0f", detail.Shape.X, left)
	}
}

// TestToastAligned proves the toast's title and body share the same left inset.
func TestToastAligned(t *testing.T) {
	app := statusApp()
	app.toast = &noticeMessage{kind: noticeError, title: "Could not enable protection", body: "Access denied while binding."}

	w, root := layoutFor(t, app, defaultWidth, defaultHeight)

	toast := mustFindLayout(t, w, root, "gui:toast")
	titleRow := childLayout(toast, 0)
	body := mustFindLayout(t, w, root, "gui:toast:body")
	if titleRow == nil || body.Shape == nil {
		t.Fatal("toast title/body missing")
	}
	titleText := childLayout(titleRow, 0)
	if titleText == nil || titleText.Shape == nil {
		t.Fatal("toast title text missing")
	}
	if absF(titleText.Shape.X-body.Shape.X) > 1 {
		t.Errorf("toast title x=%.0f vs body x=%.0f: not on one left inset", titleText.Shape.X, body.Shape.X)
	}
}

// TestToastLongTitleWraps proves a long title wraps inside the fixed toast
// width instead of running past the close button and the card edge.
func TestToastLongTitleWraps(t *testing.T) {
	app := statusApp()
	app.toast = &noticeMessage{kind: noticeSuccess,
		title: "Filtering paused; resumes automatically in 15 minutes",
		body:  "Resumes automatically in 15 minutes."}

	w, root := layoutFor(t, app, defaultWidth, defaultHeight)
	toast := mustFindLayout(t, w, root, "gui:toast")
	titleRow := childLayout(toast, 0)
	title := childLayout(titleRow, 0)
	dismiss := childLayout(titleRow, 1)
	if title == nil || dismiss == nil || title.Shape == nil || dismiss.Shape == nil {
		t.Fatal("toast title row is missing the title or dismiss control")
	}
	if title.Shape.X+title.Shape.Width > dismiss.Shape.X+0.5 {
		t.Errorf("title right %.0f runs under the dismiss button (x=%.0f)",
			title.Shape.X+title.Shape.Width, dismiss.Shape.X)
	}
}

// TestToastTitleOnly proves a body-less toast renders no body node and no
// stranded gap: it is just the title row, shorter than a toast with a body.
func TestToastTitleOnly(t *testing.T) {
	withBody := statusApp()
	withBody.toast = &noticeMessage{kind: noticeSuccess, title: "Enabled", body: "Protection is on for every adapter."}
	wb, rootb := layoutFor(t, withBody, defaultWidth, defaultHeight)
	bodyToast := mustFindLayout(t, wb, rootb, "gui:toast")
	if n := len(wb.ResolveID("gui:toast:body")); n != 1 {
		t.Fatalf("setup toast body widgets = %d, want 1", n)
	}

	titleOnly := statusApp()
	titleOnly.toast = &noticeMessage{kind: noticeSuccess, title: "Enabled"}
	w, root := layoutFor(t, titleOnly, defaultWidth, defaultHeight)
	toast := mustFindLayout(t, w, root, "gui:toast")

	if n := len(w.ResolveID("gui:toast:body")); n != 0 {
		t.Fatalf("title-only toast renders %d body widgets", n)
	}
	if len(toast.Children) != 1 {
		t.Fatalf("title-only toast children = %d, want 1", len(toast.Children))
	}
	if toast.Shape.Height >= bodyToast.Shape.Height {
		t.Errorf("title-only toast height %.1f >= with-body height %.1f: stranded gap",
			toast.Shape.Height, bodyToast.Shape.Height)
	}
}

// childLayout returns the i-th child layout, or nil when out of range.
func childLayout(ly *gogui.Layout, i int) *gogui.Layout {
	if ly == nil || i < 0 || i >= len(ly.Children) {
		return nil
	}

	return &ly.Children[i]
}

// TestServiceStartStopButton proves the single Start/Stop control lives in the
// Settings service card, reads Start when stopped / Stop when running, is only
// offered for an installed service, and is disabled when a write is not allowed.
func TestServiceStartStopButton(t *testing.T) {
	tests := []struct {
		name     string
		snap     snapshot
		id       string
		label    string
		disabled bool
	}{
		{"running shows Stop", sampleSnapshot(), "gui:service:stop", "Stop", false},
		{"stopped-installed shows Start", stoppedInstalledSnapshot(), "gui:service:start", "Start", false},
		{"not-installed shows neither", snapshot{trust: trustVerified, updated: time.Now()}, "", "", false},
		{"unverified is disabled", snapshot{trust: trustUnverified, running: true, installed: true, updated: time.Now()}, "gui:service:start", "Start", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
			app.snap = tt.snap
			app.page = pageSettings

			w, root := layoutFor(t, app, defaultWidth, 2400)

			if tt.id == "" {
				for _, id := range []string{"gui:service:start", "gui:service:stop"} {
					if n := len(w.ResolveID(id)); n != 0 {
						t.Errorf("not-installed Settings rendered %q", id)
					}
				}

				return
			}

			btn := mustFindLayout(t, w, root, tt.id)
			if got := buttonLabel(btn); got != tt.label {
				t.Errorf("label = %q, want %q", got, tt.label)
			}
			if btn.Shape.Disabled != tt.disabled {
				t.Errorf("disabled = %v, want %v", btn.Shape.Disabled, tt.disabled)
			}

			// The header carries no power control any more; the state badge is
			// not a button.
			if n := len(w.ResolveID("gui:power")); n != 0 {
				t.Errorf("header still renders a power control (%d)", n)
			}
		})
	}
}

// TestTrayIconIsPrescaled proves the tray host receives an embedded frame at
// the requested notification-area size, so the shell does not scale a larger
// icon itself.  Sizes without a frame take the next one up.
func TestTrayIconIsPrescaled(t *testing.T) {
	for _, tt := range []struct {
		px   int
		want int
	}{
		{16, 16},
		{20, 20},
		{24, 24},
		{32, 32},
		{40, 40},
		{48, 48},
		{96, 96},
		{100, 128},
		{200, 256},
	} {
		img, err := png.Decode(bytes.NewReader(appIconPNGAt(tt.px)))
		if err != nil {
			t.Fatalf("appIconPNGAt(%d): %v", tt.px, err)
		}
		if got := img.Bounds().Dx(); got != tt.want {
			t.Errorf("appIconPNGAt(%d) is %dpx wide, want %dpx", tt.px, got, tt.want)
		}
	}
}

// TestBrandMarksUseAppIcon proves the header and About marks draw the embedded
// window icon, not a second hand-drawn logo that can drift from it.
func TestBrandMarksUseAppIcon(t *testing.T) {
	w, root := layoutFor(t, aboutArtifactApp(), defaultWidth, defaultHeight)

	header := mustFindLayout(t, w, root, "gui:brand")
	about := mustFindLayout(t, w, root, "gui:about:mark")

	if !strings.HasPrefix(header.Shape.Resource, "mem:") {
		t.Fatal("header mark is not the embedded app icon")
	}
	if header.Shape.Resource != about.Shape.Resource {
		t.Error("header and About marks draw different icon art")
	}
	if header.Shape.Width != brandSize || header.Shape.Height != brandSize {
		t.Errorf("header mark is %.0fx%.0f, want %.0fx%.0f",
			header.Shape.Width, header.Shape.Height, brandSize, brandSize)
	}
	if header.Shape.Width != about.Shape.Width || header.Shape.Height != about.Shape.Height {
		t.Errorf("header mark %.0fx%.0f != About mark %.0fx%.0f",
			header.Shape.Width, header.Shape.Height, about.Shape.Width, about.Shape.Height)
	}
}

// TestHeaderControlsRightOfTitle proves the header has no theme control any
// more, and that Restore (when offered for a crash leftover) is the only and
// therefore right-most action.
func TestHeaderControlsRightOfTitle(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	title := mustFindLayout(t, w, root, "gui:brand:title")
	if n := len(w.ResolveID("gui:theme")); n != 0 {
		t.Errorf("header still renders the theme control (%d)", n)
	}

	dirty := statusApp()
	dirty.snap.status.Enabled = false
	dirty.snap.status.Mode = "off"
	dw, droot := layoutFor(t, dirty, defaultWidth, defaultHeight)

	restore := mustFindLayout(t, dw, droot, "gui:action:restore")
	if restore.Shape.X <= title.Shape.X {
		t.Errorf("Restore x=%.1f is not right of the title x=%.1f", restore.Shape.X, title.Shape.X)
	}
	if n := len(dw.ResolveID("gui:theme")); n != 0 {
		t.Errorf("dirty header renders the theme control (%d)", n)
	}
}

// TestServiceStartStopStyled proves the Start/Stop palette: the actionable Start
// wears the success green, and Stop does NOT wear danger-red — a healthy
// "protected" session must not look like an error.  A disabled Start reads
// inert/neutral rather than a washed success block.
func TestServiceStartStopStyled(t *testing.T) {
	th := gogui.CurrentTheme()

	off := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	off.snap = stoppedInstalledSnapshot()
	off.page = pageSettings
	w, root := layoutFor(t, off, defaultWidth, 2400)
	start := mustFindLayout(t, w, root, "gui:service:start")
	if start.Shape.Color != th.Cfg.ColorSuccess {
		t.Errorf("Start fill = %v, want success %v", start.Shape.Color, th.Cfg.ColorSuccess)
	}

	on := settingsApp()
	w, root = layoutFor(t, on, defaultWidth, 2400)
	stop := mustFindLayout(t, w, root, "gui:service:stop")
	if stop.Shape.Color == th.Cfg.ColorError {
		t.Errorf("Stop fill = %v: the protected state must not be danger-red", stop.Shape.Color)
	}

	// Disabled (unverified identity): Start must read inert/neutral, never a
	// washed success-green block.
	unverified := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	unverified.snap = snapshot{trust: trustUnverified, installed: true, running: false, updated: time.Now()}
	unverified.page = pageSettings
	w, root = layoutFor(t, unverified, defaultWidth, 2400)
	idle := mustFindLayout(t, w, root, "gui:service:start")
	if !idle.Shape.Disabled {
		t.Fatal("Start should be disabled when the service identity is unverified")
	}
	if idle.Shape.Color == th.Cfg.ColorSuccess {
		t.Errorf("disabled Start fill = success %v: must be neutral/inert", idle.Shape.Color)
	}
	if idle.Shape.Color != th.Cfg.ColorActive {
		t.Errorf("disabled Start fill = %v, want the neutral inert surface %v", idle.Shape.Color, th.Cfg.ColorActive)
	}
}

// TestCardRowsCompact proves the label/value rows carry no container padding:
// each body row is only as tall as its content, so a two-row card reads as a
// list rather than a stack of padded blocks.
func TestCardRowsCompact(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, defaultHeight)
	card := mustFindLayout(t, w, root, "gui:card:service")

	// Children after the heading are the Fluent settings rows.
	for i := 1; i < len(card.Children); i++ {
		row := childLayout(card, i)
		if row == nil || row.Shape == nil {
			t.Fatalf("service card row %d is missing", i)
		}
		var tallest float32
		for j := range row.Children {
			if c := row.Children[j].Shape; c != nil && c.Height > tallest {
				tallest = c.Height
			}
		}
		if tallest == 0 {
			t.Fatalf("service card row %d has no children", i)
		}
		if extra := row.Shape.Height - tallest; extra > 2 {
			t.Errorf("row %d is %.1f tall but its content is %.1f: %.1f px of padding",
				i, row.Shape.Height, tallest, extra)
		}
	}
}

// TestProfileButtonsAlign proves the neutral profile verbs share one width and
// span the field column, and that the destructive Delete is subordinate
// (narrower) and still reaches the column's right edge.
func TestProfileButtonsAlign(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, defaultHeight)

	sel := mustFindLayout(t, w, root, "gui:profile:select")
	column := mustFindLayout(t, w, root, "gui:edit:provider") // same card column width

	neutral := []string{"gui:profile:add", "gui:profile:rename", "gui:profile:active"}
	widths := make([]float32, 0, len(neutral))
	var first *gogui.Layout
	for i, id := range neutral {
		btn := mustFindLayout(t, w, root, id)
		if i == 0 {
			first = btn
		}
		widths = append(widths, btn.Shape.Width)
	}
	del := mustFindLayout(t, w, root, "gui:profile:delete")

	if absF(first.Shape.X-sel.Shape.X) > 1 {
		t.Errorf("profile row x=%.1f does not start at the field column x=%.1f", first.Shape.X, sel.Shape.X)
	}
	colRight := column.Shape.X + column.Shape.Width
	if selRight := sel.Shape.X + sel.Shape.Width; absF(selRight-colRight) > 1 {
		t.Errorf("profile selector right %.1f does not reach the field column right %.1f", selRight, colRight)
	}
	if lastRight := del.Shape.X + del.Shape.Width; absF(lastRight-colRight) > 1 {
		t.Errorf("profile row right %.1f does not reach the field column right %.1f", lastRight, colRight)
	}
	for i := 1; i < len(widths); i++ {
		if absF(widths[i]-widths[0]) > 1 {
			t.Errorf("neutral button width %.1f differs from %.1f", widths[i], widths[0])
		}
	}
	if del.Shape.Width >= widths[0] {
		t.Errorf("Delete width %.1f is not narrower than the neutral verbs %.1f: not subordinate",
			del.Shape.Width, widths[0])
	}
}

// TestDropdownPopupsWide proves the Settings dropdown popups share one width
// at the default window — a short list (Profile) no longer collapses to a tiny
// box — and that the shared floor does not push the closed field past its card
// at the minimum window.  The Block response select moved to the Rules page, so
// it is measured there against the same floor.
func TestDropdownPopupsWide(t *testing.T) {
	ids := []string{"gui:profile:select", "gui:edit:provider", "gui:edit:variant"}

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: settingsApp(),
	})
	w.SetView(view)
	w.TestRender(nil)
	for _, id := range ids {
		if err := w.TestKey(id, gogui.KeyEnter, 0); err != nil {
			t.Fatalf("open %s: %v", id, err)
		}
	}
	root := w.TestRender(nil)

	var first float32
	for i, id := range ids {
		ly := mustFindLayout(t, w, root, id+":dropdown")
		if ly.Shape.Width <= 200 {
			t.Errorf("%s popup width %.0f is still a tiny box", id, ly.Shape.Width)
		}
		if i == 0 {
			first = ly.Shape.Width
			continue
		}
		if absF(ly.Shape.Width-first) > 0.5 {
			t.Errorf("%s popup width %.0f differs from %s popup %.0f",
				id, ly.Shape.Width, ids[0], first)
		}
	}

	// The Block response select lives on the Rules page now, but it uses the
	// same floor: its popup is the same wide menu.
	rw := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: 2400, State: rulesApp(),
	})
	rw.SetView(view)
	rw.TestRender(nil)
	if err := rw.TestKey("gui:edit:block", gogui.KeyEnter, 0); err != nil {
		t.Fatalf("open gui:edit:block: %v", err)
	}
	rroot := rw.TestRender(nil)
	block := mustFindLayout(t, rw, rroot, "gui:edit:block:dropdown")
	if block.Shape.Width <= 200 {
		t.Errorf("gui:edit:block popup width %.0f is still a tiny box", block.Shape.Width)
	}
	if absF(block.Shape.Width-first) > 0.5 {
		t.Errorf("gui:edit:block popup width %.0f differs from the Settings popup %.0f",
			block.Shape.Width, first)
	}

	// The floor is below the field width, so at the smallest window the field
	// still fits inside the window rather than overflowing its card.
	narrow := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: minWidth, Height: minHeight, State: settingsApp(),
	})
	narrow.SetView(view)
	nroot := narrow.TestRender(nil)
	field := mustFindLayout(t, narrow, nroot, "gui:edit:provider")
	if right := field.Shape.X + field.Shape.Width; right > float32(minWidth) {
		t.Errorf("field right %.0f overflows the min window width %d", right, minWidth)
	}
}

// TestEditorSaveAligned proves the Save button is right-aligned to the editor's
// field column and narrower than it, so the primary action reads as a button
// rather than a full-column bar.
func TestEditorSaveAligned(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, defaultHeight)
	save := mustFindLayout(t, w, root, "gui:profile:save")

	for _, id := range []string{"gui:edit:provider", "gui:edit:id", "gui:edit:bootstrap"} {
		field := mustFindLayout(t, w, root, id)
		if sr, fr := save.Shape.X+save.Shape.Width, field.Shape.X+field.Shape.Width; absF(sr-fr) > 1 {
			t.Errorf("Save right %.1f vs %s right %.1f", sr, id, fr)
		}
		if save.Shape.Width >= field.Shape.Width {
			t.Errorf("Save width %.1f is not narrower than %s width %.1f: a full-column bar",
				save.Shape.Width, id, field.Shape.Width)
		}
		if save.Shape.X <= field.Shape.X {
			t.Errorf("Save x=%.1f is not inset from %s x=%.1f", save.Shape.X, id, field.Shape.X)
		}
	}
}

// TestEditorGroupsSeparated proves the editor card marks its field groups with
// dividers, so the grouping is legible rather than reading as accidental gaps.
func TestEditorGroupsSeparated(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, defaultHeight)
	card := mustFindLayout(t, w, root, "gui:card:editor")

	for _, id := range []string{"gui:edit:divider:1", "gui:edit:divider:2"} {
		ly := mustFindLayout(t, w, root, id)
		if !layoutContains(card, ly) {
			t.Errorf("%s is not inside the editor card", id)
		}
	}
}

// TestRulesRightEdgesAlign proves every right-side control in the Rules list
// card lands on the card's inner right edge, and the add field is attached to
// its button (no detached gap).  Applies at both widths.
func TestRulesRightEdgesAlign(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int
	}{{"default", defaultWidth, defaultHeight}, {"large", wideWidth, wideHeight}} {
		t.Run(size.name, func(t *testing.T) {
			w := gogui.NewTestWindow(t, gogui.WindowCfg{
				Width: size.w, Height: 2400, State: rulesApp(),
			})
			w.SetView(view)
			root := w.TestRender(nil)

			card := mustFindLayout(t, w, root, "gui:card:lists")
			innerRight := card.Shape.X + card.Shape.Width - card.Shape.Padding.Right - card.Shape.SizeBorder

			for _, id := range []string{
				"gui:edit:filtering",
				"gui:list:0:remove", "gui:list:1:remove",
				"gui:list:add:row", "gui:lists:refresh",
			} {
				ly := mustFindLayout(t, w, root, id)
				if r := ly.Shape.X + ly.Shape.Width; absF(r-innerRight) > 0.5 {
					t.Errorf("%s right %.1f != card inner right %.1f", id, r, innerRight)
				}
			}

			if !textSet(card)["Blocks domains on the enabled filter lists below."] {
				t.Error("filtering switch is missing its descriptor")
			}

			field := mustFindLayout(t, w, root, "gui:list:add:url")
			btn := mustFindLayout(t, w, root, "gui:list:add")
			if gap := btn.Shape.X - (field.Shape.X + field.Shape.Width); gap > 2 {
				t.Errorf("add field and its button have a %.1f px gap; want attached", gap)
			}

			assertNoFindings(t, w)
		})
	}
}

// TestCacheSwitchUsesSettingRow proves the cache switch uses the same settings
// row as Window & tray: its descriptor sits on the left and the switch lands on
// the editor card's inner right edge.
func TestCacheSwitchUsesSettingRow(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, 2400)

	card := mustFindLayout(t, w, root, "gui:card:editor")
	innerRight := card.Shape.X + card.Shape.Width - card.Shape.Padding.Right - card.Shape.SizeBorder

	sw := mustFindLayout(t, w, root, "gui:edit:cache")
	if r := sw.Shape.X + sw.Shape.Width; absF(r-innerRight) > 0.5 {
		t.Errorf("cache switch right %.1f != card inner right %.1f", r, innerRight)
	}
	if !textSet(card)["Keeps recent answers so repeat lookups are faster."] {
		t.Error("cache switch is missing its descriptor")
	}
	assertNoFindings(t, w)
}

// TestHeaderAligned proves the header action is vertically centred in the
// header row.
func TestHeaderAligned(t *testing.T) {
	app := statusApp()
	app.snap.status.Enabled = false
	app.snap.status.Mode = "off"

	w, root := layoutFor(t, app, defaultWidth, defaultHeight)

	hdr := mustFindLayout(t, w, root, "gui:header")
	restore := mustFindLayout(t, w, root, "gui:action:restore")

	restoreCY := restore.Shape.Y + restore.Shape.Height/2
	hdrCY := hdr.Shape.Y + hdr.Shape.Height/2
	if absF(restoreCY-hdrCY) > 2 {
		t.Errorf("Restore centre %.1f is not on the header centre %.1f", restoreCY, hdrCY)
	}
}

// TestNoFooterBand proves there is no footer element and the body reaches the
// window bottom (no reserved blank band under the page).
func TestNoFooterBand(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	if n := len(w.ResolveID("gui:footer")); n != 0 {
		t.Fatalf("footer widget present (%d)", n)
	}
	body := mustFindLayout(t, w, root, "gui:body")
	if bottom, wh := body.Shape.Y+body.Shape.Height, float32(defaultHeight); absF(bottom-wh) > 1 {
		t.Errorf("body bottom %.1f != window height %.1f: reserved band below the content", bottom, wh)
	}
}

// TestToastLayout proves the toast floats bottom-right and its close button sits
// inside it, padded and vertically centred on the title row (the toolkit-toast
// defect).
func TestToastLayout(t *testing.T) {
	app := statusApp()
	app.toast = &noticeMessage{kind: noticeError, title: "Could not enable", body: "Access denied while binding."}

	w, root := layoutFor(t, app, defaultWidth, defaultHeight)

	toast := mustFindLayout(t, w, root, "gui:toast")
	title := mustFindLayout(t, w, root, "gui:toast:title")
	close := mustFindLayout(t, w, root, "gui:toast:dismiss")

	// Floats bottom-right, not in the content flow.
	if toast.Shape.X < float32(defaultWidth)/2 || toast.Shape.Y < float32(defaultHeight)/2 {
		t.Errorf("toast not bottom-right: x=%.0f y=%.0f", toast.Shape.X, toast.Shape.Y)
	}
	if close.Shape.X < toast.Shape.X ||
		close.Shape.X+close.Shape.Width > toast.Shape.X+toast.Shape.Width+0.5 {
		t.Errorf("close button outside the toast horizontally: %.0f..%.0f vs %.0f..%.0f",
			close.Shape.X, close.Shape.X+close.Shape.Width,
			toast.Shape.X, toast.Shape.X+toast.Shape.Width)
	}
	if close.Shape.Y < toast.Shape.Y ||
		close.Shape.Y+close.Shape.Height > toast.Shape.Y+toast.Shape.Height+0.5 {
		t.Error("close button outside the toast vertically (clipped)")
	}

	closeCY := close.Shape.Y + close.Shape.Height/2
	titleCY := title.Shape.Y + title.Shape.Height/2
	if absF(closeCY-titleCY) > 2 {
		t.Errorf("close centre %.1f is not on the title centre %.1f", closeCY, titleCY)
	}
}

// TestToastDismissClears proves the close button clears the toast.
func TestToastDismissClears(t *testing.T) {
	app := statusApp()
	app.toast = &noticeMessage{kind: noticeSuccess, title: "Enabled"}

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	if n := len(w.ResolveID("gui:toast")); n != 1 {
		t.Fatalf("toast widgets = %d, want 1", n)
	}
	if err := w.TestClick("gui:toast:dismiss"); err != nil {
		t.Fatalf("click dismiss: %v", err)
	}
	if app.toast != nil {
		t.Fatal("dismiss did not clear the toast")
	}
	w.TestRender(nil)
	if n := len(w.ResolveID("gui:toast")); n != 0 {
		t.Fatalf("toast still rendered after dismiss (%d)", n)
	}
}

// TestToastAutoDismiss proves the toast clears itself after its TTL.
func TestToastAutoDismiss(t *testing.T) {
	app := statusApp()
	app.toastTTL = 5 * time.Millisecond

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	app.showToast(w, noticeMessage{kind: noticeSuccess, title: "Enabled"})
	w.TestRender(nil)
	if n := len(w.ResolveID("gui:toast")); n == 0 {
		t.Fatal("toast not shown")
	}

	deadline := time.Now().Add(2 * time.Second)
	for app.toast != nil && time.Now().Before(deadline) {
		w.FrameFn()
		time.Sleep(2 * time.Millisecond)
	}
	if app.toast != nil {
		t.Fatal("toast did not auto-dismiss")
	}
}

// stoppedInstalledSnapshot is an installed-but-stopped read.
func stoppedInstalledSnapshot() snapshot {
	s := sampleSnapshot()
	s.running = false
	s.status = nil

	return s
}

// buttonLabel returns the label of a Label-built Button (its text child).
func buttonLabel(ly *gogui.Layout) (label string) {
	for i := range ly.Children {
		if c := ly.Children[i].Shape; c != nil && c.TC != nil {
			return c.TC.Text
		}
	}

	return ""
}

// assertPageLayout renders app at the given size and asserts the layout is
// sound: non-blank pixels, every card present and content-sized, no vertical
// overlap between cards, no collapsed widget, and no widget findings.
func assertPageLayout(t *testing.T, app *App, width, height int, cardIDs []string) {
	t.Helper()

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: width, Height: height, State: app,
	})
	w.SetView(view)
	root := w.TestRender(nil)

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

	rects := make([]cardRect, 0, len(cardIDs))
	for _, id := range cardIDs {
		ly := mustFindLayout(t, w, root, id)
		sh := ly.Shape
		if sh.Width <= 1 || sh.Height <= 1 {
			t.Errorf("%s collapsed to %.0fx%.0f", id, sh.Width, sh.Height)
		}
		assertCardContent(t, id, ly)
		rects = append(rects, cardRect{id: id, x: sh.X, y: sh.Y, w: sh.Width, h: sh.Height})
	}

	// Every listed block must share one column: same left edge and same width.
	// This is what keeps a page's blocks on one column.
	for i := 1; i < len(rects); i++ {
		if absF(rects[i].x-rects[0].x) > 0.5 || absF(rects[i].w-rects[0].w) > 0.5 {
			t.Errorf("blocks not on one column: %s (x=%.0f w=%.0f) vs %s (x=%.0f w=%.0f)",
				rects[0].id, rects[0].x, rects[0].w, rects[i].id, rects[i].x, rects[i].w)
		}
	}

	sort.Slice(rects, func(i, j int) bool { return rects[i].y < rects[j].y })
	for i := 1; i < len(rects); i++ {
		prev, cur := rects[i-1], rects[i]
		if cur.y < prev.y+prev.h-0.5 {
			t.Errorf("cards overlap: %s (y=%.0f h=%.0f) and %s (y=%.0f)",
				prev.id, prev.y, prev.h, cur.id, cur.y)
		}
	}

	assertNoFindings(t, w)
}

// TestContentColumnCapped proves the Status column is capped at
// contentMaxWidth, that the dashboard fills it (minus the page inset), and that
// the page keeps the scrollbar gutter (right = left + scrollbarGutter).
func TestContentColumnCapped(t *testing.T) {
	navDivider := float32(1)

	for _, size := range []struct {
		name string
		w, h int
	}{{"default", defaultWidth, defaultHeight}, {"large", wideWidth, wideHeight}} {
		t.Run(size.name, func(t *testing.T) {
			w, root := layoutFor(t, statusApp(), size.w, size.h)

			page := mustFindLayout(t, w, root, "gui:content")
			avail := float32(size.w) - (navRailWidth + 2*navRailPad) - navDivider - 2*contentPad
			wantWidth := min(avail, contentMaxWidth)
			if absF(page.Shape.Width-wantWidth) > 0.5 {
				t.Errorf("page width %.1f != min(available, cap) %.1f", page.Shape.Width, wantWidth)
			}
			if got := page.Shape.Padding.Right - page.Shape.Padding.Left; absF(got-scrollbarGutter) > 0.5 {
				t.Errorf("page right-left padding = %.1f, want scrollbar gutter %.0f", got, scrollbarGutter)
			}

			// The dashboard fills the column's content box.
			grid := mustFindLayout(t, w, root, "gui:status:grid")
			if want := page.Shape.Width - page.Shape.Padding.Width(); absF(grid.Shape.Width-want) > 0.5 {
				t.Errorf("dashboard width %.1f != page content width %.1f", grid.Shape.Width, want)
			}

			// Centered within the content area.
			wrap := mustFindLayout(t, w, root, "gui:contentwrap")
			leftGap := page.Shape.X - (wrap.Shape.X + wrap.Shape.Padding.Left)
			rightGap := (wrap.Shape.X + wrap.Shape.Width - wrap.Shape.Padding.Right) - (page.Shape.X + page.Shape.Width)
			if absF(leftGap-rightGap) > 1 {
				t.Errorf("column not centered: left gap %.1f, right gap %.1f", leftGap, rightGap)
			}
		})
	}
}

// TestPagesSharePageColumn proves every page sits in the same capped, centred
// column, so switching pages never shifts the content edges.
func TestPagesSharePageColumn(t *testing.T) {
	sw, statusRoot := layoutFor(t, statusApp(), wideWidth, wideHeight)
	status := mustFindLayout(t, sw, statusRoot, "gui:content")

	pages := []struct {
		name string
		id   string
		app  *App
	}{
		{"settings", "gui:settings", settingsApp()},
		{"rules", "gui:rules", rulesApp()},
		{"logging", "gui:logging", loggingApp()},
		{"about", "gui:about", aboutArtifactApp()},
	}

	for _, p := range pages {
		w, root := layoutFor(t, p.app, wideWidth, wideHeight)
		col := mustFindLayout(t, w, root, p.id)
		if absF(col.Shape.Width-status.Shape.Width) > 0.5 {
			t.Errorf("%s width %.0f != Status width %.0f", p.name, col.Shape.Width, status.Shape.Width)
		}
		if absF(col.Shape.X-status.Shape.X) > 0.5 {
			t.Errorf("%s x=%.0f != Status x=%.0f", p.name, col.Shape.X, status.Shape.X)
		}
	}
}

// absF is a float32 absolute value.
func absF(f float32) float32 {
	if f < 0 {
		return -f
	}

	return f
}

// assertCardContent checks one card's height matches its content and that its
// heading does not overlap the first body row.
func assertCardContent(t *testing.T, id string, ly *gogui.Layout) {
	t.Helper()

	kids := ly.Children
	if len(kids) == 0 {
		t.Fatalf("%s has no children", id)
	}

	var contentH float32
	for i, k := range kids {
		if k.Shape == nil {
			t.Errorf("%s child %d has no shape", id, i)

			continue
		}
		if k.Shape.Height <= 0 {
			t.Errorf("%s child %d collapsed (height %.0f)", id, i, k.Shape.Height)
		}
		contentH += k.Shape.Height
		if i > 0 {
			contentH += ly.Shape.Spacing
		}
	}

	pad := ly.Shape.Padding.Height()
	if ly.Shape.Height+0.5 < contentH {
		t.Errorf("%s height %.0f is smaller than its content %.0f", id, ly.Shape.Height, contentH)
	}
	// Height should be content + padding + the border on both edges, within a
	// pixel of rounding.
	if limit := contentH + pad + ly.Shape.SizeBorder*2 + 1; ly.Shape.Height > limit {
		t.Errorf("%s is stretched: height %.0f vs content %.0f + padding %.0f (limit %.0f)",
			id, ly.Shape.Height, contentH, pad, limit)
	}

	if len(kids) >= 2 {
		head, body := kids[0].Shape, kids[1].Shape
		if head.Y+head.Height > body.Y+0.5 {
			t.Errorf("%s heading overlaps its body (heading bottom %.0f > body top %.0f)",
				id, head.Y+head.Height, body.Y)
		}
	}
}

// mustFindLayout resolves a widget ID and returns its layout, or fails.
func mustFindLayout(t *testing.T, w *gogui.Window, root *gogui.Layout, id string) *gogui.Layout {
	t.Helper()

	ids := w.ResolveID(id)
	if len(ids) == 0 {
		t.Fatalf("widget %q is not rendered", id)
	}
	ly, ok := root.FindByID(ids[0])
	if !ok || ly.Shape == nil {
		t.Fatalf("widget %q is not in the layout", id)
	}

	return ly
}

// rulesArtifactApp is rulesApp with a couple of rule rows, so the artifact PNGs
// show the rule-panel design rather than only the empty-state card.
func rulesArtifactApp() *App {
	app := rulesApp()
	app.ensureDraft()
	app.draft.perDomain = []ruleDraft{
		{domains: "example.com, www.example.com", upstream: "1.1.1.1", enabled: true},
		{domains: "ads.example", bypass: true, enabled: false},
	}
	app.draft.rulesKnown = true
	app.seeded = true

	return app
}

// loggingArtifactApp shows a full tail so the artifact PNGs exercise the
// viewer's monospace role and the Verbose level that produces DBG lines.
func loggingArtifactApp() *App {
	app := loggingApp()
	app.snap.summary.LogLevel = config.LogLevelDebug
	app.logText = "2026-10-12T14:03:22 INF resolver started\n" +
		"2026-10-12T14:03:22 INF profile default (controld/p2)\n" +
		"2026-10-12T14:03:23 INF filtering enabled (2 lists)\n" +
		"2026-10-12T14:03:24 INF listening on 127.0.0.1:53 and [::1]:53\n" +
		"2026-10-12T14:03:24 DBG query example.com A\n" +
		"2026-10-12T14:03:24 DBG cached example.com A 0.0.0.0\n" +
		"2026-10-12T14:03:25 DBG query ads.example.net A\n" +
		"2026-10-12T14:03:25 DBG blocked ads.example.net (list 0)\n" +
		"2026-10-12T14:03:26 DBG query github.com AAAA\n" +
		"2026-10-12T14:03:26 INF upstream response github.com AAAA 59s\n"

	return app
}

// aboutArtifactApp is the About page with the sample service info resolved, so
// the version card renders real values in the artifact PNGs.
func aboutArtifactApp() *App {
	app := settingsApp()
	app.page = pageAbout

	return app
}

// TestRenderArtifacts writes PNGs of every page at three sizes in light and dark,
// gated so it stays out of the normal test path.  It runs when
// GUI_RENDER_ARTIFACTS=1 is set — or when the -run selector names it, which is
// how the documented `./dev/go test -run Render` invocation reaches it (the
// Docker wrapper forwards test flags but not arbitrary host env vars).
// Output defaults to <repo>/.opencode/renders; override with GUI_RENDER_DIR.
func TestRenderArtifacts(t *testing.T) {
	if os.Getenv("GUI_RENDER_ARTIFACTS") != "1" && !runSelectorMentions("Render") {
		t.Skip("set GUI_RENDER_ARTIFACTS=1 (or run -run Render) to write render artifacts")
	}

	dir := os.Getenv("GUI_RENDER_DIR")
	if dir == "" {
		dir = filepath.Join("..", "..", ".opencode", "renders")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	pages := []struct {
		name string
		mk   func() *App
	}{
		{"status", statusApp},
		{"settings", settingsApp},
		{"rules", rulesArtifactApp},
		{"logging", loggingArtifactApp},
		{"about", aboutArtifactApp},
	}
	sizes := []struct {
		name string
		w, h int
	}{
		{"default", defaultWidth, defaultHeight},
		{"large", wideWidth, wideHeight},
		{"minimum", minWidth, minHeight},
	}
	themes := []struct {
		name string
		th   gogui.Theme
	}{
		{"light", windowsTheme("windows", gogui.ThemeLight)},
		{"dark", windowsTheme("windows-dark", gogui.ThemeDark)},
	}

	for _, p := range pages {
		for _, size := range sizes {
			for _, th := range themes {
				w := gogui.NewTestWindow(t, gogui.WindowCfg{
					Width: size.w, Height: size.h, State: p.mk(),
				})
				w.SetView(view)
				w.SetTheme(th.th)

				name := p.name + "-" + size.name + "-" + th.name + ".png"
				path := filepath.Join(dir, name)
				if err := soft.RenderToPNG(w, 1, path); err != nil {
					t.Fatalf("render %s: %v", name, err)
				}
				t.Logf("wrote %s", path)
			}
		}
	}
}

// runSelectorMentions reports whether the -test.run selector mentions needle,
// so the artifact dump can be requested through the test selector when a host
// env var cannot reach the container.
func runSelectorMentions(needle string) (ok bool) {
	f := flag.Lookup("test.run")
	if f == nil {
		return false
	}

	return strings.Contains(f.Value.String(), needle)
}
