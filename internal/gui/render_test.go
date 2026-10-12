//go:build gui

package gui

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend/soft"
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

// TestStatusLayout proves the Status page lays out: banner and cards present,
// each sized to its content, all on one column, and no two overlapping.
func TestStatusLayout(t *testing.T) {
	ids := []string{"gui:banner", "gui:card:profile", "gui:card:filtering", "gui:card:cache"}

	for _, size := range []struct {
		name string
		w, h int
	}{{"default", defaultWidth, defaultHeight}, {"large", wideWidth, wideHeight}} {
		t.Run(size.name, func(t *testing.T) {
			assertPageLayout(t, statusApp(), size.w, size.h, ids)
		})
	}
}

// TestSettingsLayout proves the Settings page lays out the same way.
func TestSettingsLayout(t *testing.T) {
	ids := []string{"gui:card:service", "gui:card:profiles", "gui:card:editor"}

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
		[]string{"gui:card:service", "gui:card:profiles", "gui:card:editor"})
}

// TestSettingsOfflineNoticeWraps proves the long "Offline editing" notice
// wraps instead of forcing the page into a horizontal scrollbar. The notice is
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

// statusPowerControlID is the one Status power control.
const statusPowerControlID = "gui:power"

// TestStatusSinglePowerControl proves the Status page has exactly one power
// control and no Enable/Disable or Start/Stop/Install/Uninstall buttons.
func TestStatusSinglePowerControl(t *testing.T) {
	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: statusApp(),
	})
	w.SetView(view)
	w.TestRender(nil)

	if n := len(w.ResolveID(statusPowerControlID)); n != 1 {
		t.Fatalf("power controls = %d, want exactly 1", n)
	}
	for _, id := range []string{
		"gui:action:enable", "gui:action:disable", "gui:action:restore",
		"gui:service:install", "gui:service:uninstall",
		"gui:service:start", "gui:service:stop",
	} {
		if len(w.ResolveID(id)) != 0 {
			t.Errorf("Status page must not render %q", id)
		}
	}

	assertNoFindings(t, w)
}

// TestStatusRestoreOnlyForDirtyLeftover proves Restore is offered only for the
// crash-leftover state (!Enabled && Dirty), never beside a healthy control.
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
	if n := len(renderIDs(t, dirty, statusPowerControlID)); n != 1 {
		t.Fatalf("dirty-leftover showed %d power controls, want 1", n)
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

// TestNavRailCompact proves the rail hugs its buttons (no dead space) and every
// label still fits inside the fixed button width after the rail was narrowed.
func TestNavRailCompact(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	rail := mustFindLayout(t, w, root, "gui:nav")
	btn := mustFindLayout(t, w, root, "gui:nav:status")
	if absF(rail.Shape.Width-btn.Shape.Width) > 2 {
		t.Errorf("rail width %.0f vs button width %.0f: dead space beside the buttons",
			rail.Shape.Width, btn.Shape.Width)
	}
	if rail.Shape.Width > navRailWidth+2 {
		t.Errorf("rail width %.0f exceeds the button width %d", rail.Shape.Width, navRailWidth)
	}

	// The longest label ("Settings") must fit inside the fixed width with the
	// theme's button inset, so the narrower rail does not clip it.
	inset := gogui.CurrentTheme().PaddingButton
	for _, id := range []string{"gui:nav:status", "gui:nav:settings", "gui:nav:about"} {
		button := mustFindLayout(t, w, root, id)
		if absF(button.Shape.Width-navRailWidth) > 1 {
			t.Errorf("%s width %.0f != navRailWidth %d", id, button.Shape.Width, navRailWidth)
		}
		labelW := buttonLabelWidth(button)
		if labelW+inset.Left+inset.Right > navRailWidth {
			t.Errorf("%s label %.0f + button inset %.0f exceeds rail width %d: label would clip",
				id, labelW, inset.Left+inset.Right, navRailWidth)
		}
	}
}

// buttonLabelWidth returns the width of a Label-built button's text child.
func buttonLabelWidth(ly *gogui.Layout) (width float32) {
	for i := range ly.Children {
		if c := ly.Children[i].Shape; c != nil && c.TC != nil {
			return c.Width
		}
	}

	return 0
}

// TestBannerIndicatorCentred proves the status indicator is vertically centred
// on the headline line, not parked at the banner's top corner.
func TestBannerIndicatorCentred(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	dot := mustFindLayout(t, w, root, "gui:banner:dot")
	title := mustFindLayout(t, w, root, "gui:banner:title")

	dotCY := dot.Shape.Y + dot.Shape.Height/2
	titleCY := title.Shape.Y + title.Shape.Height/2
	if absF(dotCY-titleCY) > 2 {
		t.Errorf("indicator centre %.1f is not on the title centre %.1f", dotCY, titleCY)
	}
}

// TestBannerAligned proves the banner's dot and the detail body share one left
// edge (the dot was indented by the title row's default padding).
func TestBannerAligned(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	dot := mustFindLayout(t, w, root, "gui:banner:dot")
	banner := mustFindLayout(t, w, root, "gui:banner")
	detail := childLayout(banner, 1)
	if detail == nil || detail.Shape == nil {
		t.Fatal("banner detail row missing")
	}
	if absF(dot.Shape.X-detail.Shape.X) > 1 {
		t.Errorf("banner dot x=%.0f vs detail x=%.0f: not on one left edge", dot.Shape.X, detail.Shape.X)
	}
}

// TestToastAligned proves the toast's title and body share the same left inset.
func TestToastAligned(t *testing.T) {
	app := statusApp()
	app.toast = &noticeMessage{kind: noticeSuccess, title: "Enabled", body: "Done."}

	w, root := layoutFor(t, app, defaultWidth, defaultHeight)

	toast := mustFindLayout(t, w, root, "gui:toast")
	titleRow := childLayout(toast, 0)
	body := childLayout(toast, 1)
	if titleRow == nil || body == nil || body.Shape == nil {
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

// childLayout returns the i-th child layout, or nil when out of range.
func childLayout(ly *gogui.Layout, i int) *gogui.Layout {
	if ly == nil || i < 0 || i >= len(ly.Children) {
		return nil
	}

	return &ly.Children[i]
}

// TestHeaderPowerButton proves the single Protection button lives in the header
// (on both pages), reads Enable when off / Disable when on, and is disabled when
// a write is not allowed.
func TestHeaderPowerButton(t *testing.T) {
	tests := []struct {
		name     string
		snap     snapshot
		label    string
		disabled bool
	}{
		{"running", sampleSnapshot(), "Disable", false},
		{"stopped-installed", stoppedInstalledSnapshot(), "Enable", false},
		{"not-installed", snapshot{trust: trustVerified, updated: time.Now()}, "Enable", true},
		{"unverified", snapshot{trust: trustUnverified, running: true, updated: time.Now()}, "Enable", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
			app.snap = tt.snap

			w, root := layoutFor(t, app, defaultWidth, defaultHeight)
			btn := mustFindLayout(t, w, root, "gui:power")
			if got := buttonLabel(btn); got != tt.label {
				t.Errorf("power label = %q, want %q", got, tt.label)
			}
			if btn.Shape.Disabled != tt.disabled {
				t.Errorf("power disabled = %v, want %v", btn.Shape.Disabled, tt.disabled)
			}

			// The header is global: the same button renders on Settings.
			app.page = pageSettings
			if n := len(renderIDs(t, app, "gui:power")); n != 1 {
				t.Errorf("power button on Settings = %d, want 1", n)
			}
		})
	}
}

// TestHeaderPowerRightMost proves the power control is the last (right-most)
// header action, sitting past the theme control.
func TestHeaderPowerRightMost(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	power := mustFindLayout(t, w, root, "gui:power")
	for _, id := range []string{"gui:theme"} {
		other := mustFindLayout(t, w, root, id)
		if power.Shape.X <= other.Shape.X {
			t.Errorf("power x=%.1f is not right of %s x=%.1f", power.Shape.X, id, other.Shape.X)
		}
	}
}

// TestHeaderPowerStyled proves the power button wears the theme's state colour:
// the success green to enable, the error red to disable.
func TestHeaderPowerStyled(t *testing.T) {
	th := gogui.CurrentTheme()

	off := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	off.snap = stoppedInstalledSnapshot()
	w, root := layoutFor(t, off, defaultWidth, defaultHeight)
	enable := mustFindLayout(t, w, root, "gui:power")
	if enable.Shape.Color != th.Cfg.ColorSuccess {
		t.Errorf("Enable fill = %v, want success %v", enable.Shape.Color, th.Cfg.ColorSuccess)
	}

	w, root = layoutFor(t, statusApp(), defaultWidth, defaultHeight)
	disable := mustFindLayout(t, w, root, "gui:power")
	if disable.Shape.Color != th.Cfg.ColorError {
		t.Errorf("Disable fill = %v, want error %v", disable.Shape.Color, th.Cfg.ColorError)
	}
}

// TestCardRowsCompact proves the label/value rows carry no container padding:
// each row is only as tall as its content, so a two-row card reads as a list.
func TestCardRowsCompact(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, defaultHeight)
	card := mustFindLayout(t, w, root, "gui:card:service")

	for i := 1; i <= 2; i++ {
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

// TestProfileButtonsAlign proves the four profile verbs share one width and
// span the field column: the row starts with the selector and ends with the
// editor fields.
func TestProfileButtonsAlign(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, defaultHeight)

	sel := mustFindLayout(t, w, root, "gui:profile:select")
	column := mustFindLayout(t, w, root, "gui:edit:provider") // same card column width

	ids := []string{"gui:profile:add", "gui:profile:rename", "gui:profile:delete", "gui:profile:active"}
	widths := make([]float32, 0, len(ids))
	var first, last *gogui.Layout
	for i, id := range ids {
		btn := mustFindLayout(t, w, root, id)
		if i == 0 {
			first = btn
		}
		last = btn
		widths = append(widths, btn.Shape.Width)
	}

	if absF(first.Shape.X-sel.Shape.X) > 1 {
		t.Errorf("profile row x=%.1f does not start at the field column x=%.1f", first.Shape.X, sel.Shape.X)
	}
	selRight := sel.Shape.X + sel.Shape.Width
	lastRight := last.Shape.X + last.Shape.Width
	colRight := column.Shape.X + column.Shape.Width
	if absF(selRight-colRight) > 1 {
		t.Errorf("profile selector right %.1f does not reach the field column right %.1f", selRight, colRight)
	}
	if absF(lastRight-colRight) > 1 {
		t.Errorf("profile row right %.1f does not reach the field column right %.1f", lastRight, colRight)
	}
	for i := 1; i < len(widths); i++ {
		if absF(widths[i]-widths[0]) > 1 {
			t.Errorf("button width %.1f differs from %.1f", widths[i], widths[0])
		}
	}
}

// TestDropdownPopupsWide proves the Settings dropdown popups share one width
// at the default window — a short list (Profile, Block response) no longer
// collapses to a tiny box — and that the shared floor does not push the closed
// field past its card at the minimum window.
func TestDropdownPopupsWide(t *testing.T) {
	ids := []string{"gui:profile:select", "gui:edit:provider", "gui:edit:variant", "gui:edit:block"}

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

// TestEditorSaveAligned proves the Save button shares both edges of the editor's
// field column, rather than floating at an indented offset.
func TestEditorSaveAligned(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, defaultHeight)
	save := mustFindLayout(t, w, root, "gui:profile:save")

	for _, id := range []string{"gui:edit:provider", "gui:edit:id", "gui:edit:bootstrap"} {
		field := mustFindLayout(t, w, root, id)
		if absF(save.Shape.X-field.Shape.X) > 1 {
			t.Errorf("Save x=%.1f vs %s x=%.1f", save.Shape.X, id, field.Shape.X)
		}
		if sv, fv := save.Shape.X+save.Shape.Width, field.Shape.X+field.Shape.Width; absF(sv-fv) > 1 {
			t.Errorf("Save right %.1f vs %s right %.1f", sv, id, fv)
		}
	}
}

// TestHeaderAligned proves the header controls are vertically centred in the
// header row.
func TestHeaderAligned(t *testing.T) {
	w, root := layoutFor(t, statusApp(), defaultWidth, defaultHeight)

	hdr := mustFindLayout(t, w, root, "gui:header")
	power := mustFindLayout(t, w, root, "gui:power")

	powerCY := power.Shape.Y + power.Shape.Height/2
	hdrCY := hdr.Shape.Y + hdr.Shape.Height/2
	if absF(powerCY-hdrCY) > 2 {
		t.Errorf("power button centre %.1f is not on the header centre %.1f", powerCY, hdrCY)
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
	app.toast = &noticeMessage{kind: noticeSuccess, title: "Enabled", body: "Done."}

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

	app.showToast(w, noticeMessage{kind: noticeSuccess, title: "Enabled", body: "Done."})
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
	// This is what keeps the banner exactly as wide as the cards below it.
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

// TestRenderArtifacts writes PNGs of both pages at two sizes in light and dark,
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
	}
	sizes := []struct {
		name string
		w, h int
	}{
		{"default", defaultWidth, defaultHeight},
		{"large", wideWidth, wideHeight},
	}
	themes := []struct {
		name string
		th   gogui.Theme
	}{
		{"light", gogui.ThemeLight},
		{"dark", gogui.ThemeDark},
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
