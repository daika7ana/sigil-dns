//go:build gui

package gui

import (
	"flag"
	"fmt"
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
// the "Update blocklists" action) — and is absent from Settings — with no
// widget findings.
func TestRulesListSection(t *testing.T) {
	app := rulesApp()
	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	for _, id := range []string{
		"gui:edit:lists",
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
}

// TestSettingsDropsMovedSections proves the filter-list and per-domain editors
// no longer render on the Settings page (they moved to the Rules page).
func TestSettingsDropsMovedSections(t *testing.T) {
	w := newSettingsWindow(t, settingsApp())
	w.TestRender(nil)

	for _, id := range []string{"gui:edit:lists", "gui:edit:rules", "gui:rule:add", "gui:list:add"} {
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

// TestRefreshDisabledWhenStopped proves "Update blocklists" is inert while the
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
		"gui:rule:0:domains", "gui:rule:0:upstream", "gui:rule:0:enable", "gui:rule:0:remove",
		"gui:rule:1:domains", "gui:rule:1:upstream", "gui:rule:1:enable", "gui:rule:1:remove",
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

	const want = "*.example.com matches subdomains only; example.com matches the domain and its subdomains"
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

// TestRulesControlColumnsAligned proves the per-row controls share two fixed
// columns — one enable toggle column and one remove column — identical between
// list rows and rule rows, so the two-column layout reads as deliberate.
func TestRulesControlColumnsAligned(t *testing.T) {
	app := rulesApp()
	app.draft.name = "default"
	app.draft.perDomain = []ruleDraft{{domains: "a.example", enabled: true}}
	app.draft.lists = []listDraft{
		{host: "ads.example.net", enabled: true, idx: 0},
		{host: "trackers.example.org", enabled: false, idx: 1},
	}
	app.draft.rulesKnown = true
	app.seeded = true

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	var wantToggleX float32
	for i, id := range []string{"gui:list:0:enable", "gui:list:1:enable", "gui:rule:0:enable"} {
		ly := mustFindLayout(t, w, root, id)
		if i == 0 {
			wantToggleX = ly.Shape.X

			continue
		}
		if absF(ly.Shape.X-wantToggleX) > 1 {
			t.Errorf("%s toggle x=%.1f != %.1f: enable column drifts", id, ly.Shape.X, wantToggleX)
		}
	}

	var wantRemoveRight float32
	for i, id := range []string{"gui:list:0:remove", "gui:list:1:remove", "gui:rule:0:remove"} {
		ly := mustFindLayout(t, w, root, id)
		r := ly.Shape.X + ly.Shape.Width
		if i == 0 {
			wantRemoveRight = r

			continue
		}
		if absF(r-wantRemoveRight) > 1 {
			t.Errorf("%s remove right=%.1f != %.1f: remove column drifts", id, r, wantRemoveRight)
		}
	}
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
	// and the longest label ("Settings").
	inset := gogui.CurrentTheme().PaddingButton
	// bar (3) + icon (~17, IconLarge) + two inter-part gaps (SpacingMedium each).
	const navItemLeading float32 = 48
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

// findTextWidth returns the width of the first text shape in the subtree whose
// content is label, or 0 when none renders it.
func findTextWidth(ly *gogui.Layout, label string) (width float32) {
	if ly == nil || ly.Shape == nil {
		return 0
	}
	if ly.Shape.TC != nil && ly.Shape.TC.Text == label {
		return ly.Shape.Width
	}
	for i := range ly.Children {
		if w := findTextWidth(&ly.Children[i], label); w > 0 {
			return w
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

// TestHeaderPowerStyled proves the power button's palette: the affirmative
// Enable wears the success green, and Disable does NOT wear danger-red — a
// healthy "protected" session must not look like an error.
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
	if disable.Shape.Color == th.Cfg.ColorError {
		t.Errorf("Disable fill = %v: the protected state must not be danger-red", disable.Shape.Color)
	}

	// Disabled (service not installed): the power button must read inert/neutral,
	// never a washed success-green block with white-on-pale-green text.
	notInstalled := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	notInstalled.snap = snapshot{trust: trustVerified, updated: time.Now()}
	w, root = layoutFor(t, notInstalled, defaultWidth, defaultHeight)
	idle := mustFindLayout(t, w, root, "gui:power")
	if !idle.Shape.Disabled {
		t.Fatal("power should be disabled when the service is not installed")
	}
	if idle.Shape.Color == th.Cfg.ColorSuccess {
		t.Errorf("disabled power fill = success %v: must be neutral/inert, not a success block", idle.Shape.Color)
	}
	if idle.Shape.Color != th.Cfg.ColorActive {
		t.Errorf("disabled power fill = %v, want the neutral inert surface %v", idle.Shape.Color, th.Cfg.ColorActive)
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
				"gui:list:0:remove", "gui:list:1:remove",
				"gui:list:add:row", "gui:lists:refresh",
			} {
				ly := mustFindLayout(t, w, root, id)
				if r := ly.Shape.X + ly.Shape.Width; absF(r-innerRight) > 0.5 {
					t.Errorf("%s right %.1f != card inner right %.1f", id, r, innerRight)
				}
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

// TestContentColumnCapped proves the reading column is capped at
// contentMaxWidth and centered, that the cards fill it (minus the page inset),
// and that the page keeps the scrollbar gutter (right = left + scrollbarGutter).
func TestContentColumnCapped(t *testing.T) {
	for _, size := range []struct {
		name string
		w, h int
	}{{"default", defaultWidth, defaultHeight}, {"large", wideWidth, wideHeight}} {
		t.Run(size.name, func(t *testing.T) {
			w, root := layoutFor(t, statusApp(), size.w, size.h)

			page := mustFindLayout(t, w, root, "gui:content")
			if absF(page.Shape.Width-contentMaxWidth) > 0.5 {
				t.Errorf("page width %.1f != contentMaxWidth %.0f", page.Shape.Width, contentMaxWidth)
			}
			if got := page.Shape.Padding.Right - page.Shape.Padding.Left; absF(got-scrollbarGutter) > 0.5 {
				t.Errorf("page right-left padding = %.1f, want scrollbar gutter %.0f", got, scrollbarGutter)
			}

			// Cards fill the column's content box.
			card := mustFindLayout(t, w, root, "gui:card:profile")
			if want := page.Shape.Width - page.Shape.Padding.Width(); absF(card.Shape.Width-want) > 0.5 {
				t.Errorf("card width %.1f != page content width %.1f", card.Shape.Width, want)
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
		{"rules", rulesApp},
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
