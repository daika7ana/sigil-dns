//go:build gui

package gui

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend/soft"

	"github.com/daika7ana/sigil-dns/internal/ipc"
	"github.com/daika7ana/sigil-dns/internal/providers"
)

// TestDraftFromSnapshot checks the seeded editor buffer.  The resolver id and
// upstream URL are never read (SPEC §15.2), so both start blank.
func TestDraftFromSnapshot(t *testing.T) {
	d := draftFromSummary(sampleSnapshot().summary, "default")

	if d.name != "default" {
		t.Errorf("name = %q, want default", d.name)
	}
	if d.provider != "controld" {
		t.Errorf("provider = %q, want controld", d.provider)
	}
	if d.variant != "p2" || d.protocol != "doh" {
		t.Errorf("variant/protocol = %q/%q, want p2/doh", d.variant, d.protocol)
	}
	if d.id != "" {
		t.Errorf("draft read the resolver id: %q", d.id)
	}
	if d.url != "" {
		t.Errorf("draft read the upstream URL: %q", d.url)
	}
	if d.bootstrap != "76.76.2.22, 76.76.10.22" {
		t.Errorf("bootstrap = %q", d.bootstrap)
	}
	if !d.filtering || d.blockResp != "0.0.0.0" {
		t.Errorf("filtering = %v blockResp = %q", d.filtering, d.blockResp)
	}
	if !d.cache || d.cacheSizeMB != "4" {
		t.Errorf("cache = %v sizeMiB = %q, want true/4", d.cache, d.cacheSizeMB)
	}
}

// TestRuleDraftSeedsEnabled proves the editor's rule rows take their enable
// state from the sanitized per-domain summary.
func TestRuleDraftSeedsEnabled(t *testing.T) {
	d := draftForProfile(ipc.ProfileSummary{
		Name: "default",
		PerDomain: []ipc.PerDomainSummary{
			{Domains: []string{"a.example"}, Enabled: true},
			{Domains: []string{"b.example"}, Enabled: false},
		},
	})

	if len(d.perDomain) != 2 {
		t.Fatalf("rules = %d, want 2", len(d.perDomain))
	}
	if !d.perDomain[0].enabled || d.perDomain[1].enabled {
		t.Errorf("seeded enable states = %v/%v, want true/false",
			d.perDomain[0].enabled, d.perDomain[1].enabled)
	}
}

// TestRuleDraftSeedsBypass proves the editor's rule rows take their kind from
// the sanitized per-domain summary, so draftDirty can see a kind change.
func TestRuleDraftSeedsBypass(t *testing.T) {
	d := draftForProfile(ipc.ProfileSummary{
		Name: "default",
		PerDomain: []ipc.PerDomainSummary{
			{Domains: []string{"a.example"}, Bypass: true, Enabled: true},
			{Domains: []string{"b.example"}},
		},
	})

	if len(d.perDomain) != 2 {
		t.Fatalf("rules = %d, want 2", len(d.perDomain))
	}
	if !d.perDomain[0].bypass || d.perDomain[1].bypass {
		t.Errorf("seeded bypass = %v/%v, want true/false",
			d.perDomain[0].bypass, d.perDomain[1].bypass)
	}
}

// TestDraftFromProfiles seeds from the per-profile list, picking the named
// profile and never the active one unless it was asked for.
func TestDraftFromProfiles(t *testing.T) {
	snap := profilesSnapshot()

	if d := draftFromSummary(snap.summary, "home"); d.name != "home" || d.provider != "nextdns" || d.cache {
		t.Errorf("home draft = %+v", d)
	}
	if d := draftFromSummary(snap.summary, "work"); d.name != "work" || d.provider != "quad9" || !d.cache {
		t.Errorf("work draft = %+v", d)
	}
	if d := draftFromSummary(snap.summary, "work"); d.id != "" || d.url != "" {
		t.Errorf("draft leaked a secret: id=%q url=%q", d.id, d.url)
	}
	// Unknown name with a populated list stays blank but named.
	if d := draftFromSummary(snap.summary, "ghost"); d.name != "ghost" || d.provider != "" {
		t.Errorf("ghost draft = %+v", d)
	}
}

// TestSeedPopulatesProfiles checks the poller seed fills the selector from the
// sanitized list and defaults the selection to the active profile.
func TestSeedPopulatesProfiles(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.seed(profilesSnapshot())

	want := []string{"home", "work"} // name-sorted, as the summary is
	if len(app.profiles) != 2 || app.profiles[0] != want[0] || app.profiles[1] != want[1] {
		t.Fatalf("profiles = %v, want %v", app.profiles, want)
	}
	if app.active != "work" || app.selected != "work" {
		t.Fatalf("active/selected = %q/%q, want work/work", app.active, app.selected)
	}
	if app.draft.name != "work" {
		t.Fatalf("draft seeded for %q, want work", app.draft.name)
	}
}

// TestSettingsRendersFullProfileList renders Settings from a summary carrying
// the full profile list and checks the selector and editor reflect the active
// profile.
func TestSettingsRendersFullProfileList(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.seed(profilesSnapshot())
	app.page = pageSettings

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if len(w.ResolveID("gui:profile:select")) == 0 {
		t.Fatal("profile selector not rendered")
	}
	if app.selected != "work" || app.draft.provider != "quad9" {
		t.Fatalf("editor seeded for %q provider %q, want work/quad9", app.selected, app.draft.provider)
	}
	assertNoFindings(t, w)
}

// TestDeleteDisabledRules covers the profile deletion rules as data.
func TestDeleteDisabledRules(t *testing.T) {
	tests := []struct {
		name     string
		names    []string
		active   string
		target   string
		disabled bool
	}{
		{"active profile", []string{"a", "b"}, "a", "a", true},
		{"last profile", []string{"a"}, "a", "a", true},
		{"other profile", []string{"a", "b"}, "a", "b", false},
		{"no selection", []string{"a", "b"}, "a", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deleteDisabled(tt.names, tt.active, tt.target); got != tt.disabled {
				t.Fatalf("deleteDisabled(%v, %q, %q) = %v, want %v",
					tt.names, tt.active, tt.target, got, tt.disabled)
			}
		})
	}
}

// TestSettingsPageRenders renders the Settings screen headlessly and asserts
// the service section, editor, and profile controls are present with no widget
// findings.
func TestSettingsPageRenders(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()
	app.page = pageSettings

	w := newSettingsWindow(t, app)

	img, err := soft.RenderToImage(w, 1)
	if err != nil {
		t.Fatalf("RenderToImage: %v", err)
	}
	if img == nil || img.Bounds().Empty() {
		t.Fatal("render produced an empty image")
	}

	for _, id := range []string{
		"gui:nav:status", "gui:nav:settings", "gui:nav:rules", "gui:nav:about",
		"gui:service:stop", "gui:service:uninstall", "gui:tray:enabled",
		"gui:profile:select", "gui:profile:add", "gui:profile:rename", "gui:profile:delete",
		"gui:edit:provider", "gui:edit:variant", "gui:edit:protocol", "gui:edit:id",
		"gui:edit:bootstrap", "gui:edit:cache",
		"gui:edit:cachesize", "gui:profile:save",
	} {
		if len(w.ResolveID(id)) == 0 {
			t.Errorf("expected %q to be rendered", id)
		}
	}

	// Running service: the Stop control and Uninstall show; Install and Start
	// do not.
	if len(w.ResolveID("gui:service:install")) != 0 {
		t.Error("Install must not render when the service is already installed")
	}
	if len(w.ResolveID("gui:service:start")) != 0 {
		t.Error("Start must not render while the service is running")
	}

	assertNoFindings(t, w)
}

// TestServiceSectionEnablement checks the Settings lifecycle controls: Install
// shows only when absent, Uninstall only when present, Start only when installed
// and stopped, Stop only when installed and running — all gated by the
// unverified-trust write rule.
func TestServiceSectionEnablement(t *testing.T) {
	tests := []struct {
		name         string
		snap         snapshot
		wantInstall  bool
		wantUninst   bool
		wantStart    bool
		wantStop     bool
		wantDisabled bool // for whichever controls are shown
	}{
		{"absent-trusted", snapshot{trust: trustVerified, updated: time.Now()}, true, false, false, false, false},
		{"present-running", sampleSnapshot(), false, true, false, true, false},
		{"present-stopped", stoppedInstalledSnapshot(), false, true, true, false, false},
		{"absent-unverified", snapshot{trust: trustUnverified, running: true, updated: time.Now()}, true, false, false, false, true},
		{"present-unverified", snapshot{trust: trustUnverified, installed: true, running: true, updated: time.Now()}, false, true, true, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
			app.snap = tt.snap
			app.page = pageSettings

			w := newSettingsWindow(t, app)
			root := w.TestRender(nil)

			checks := map[string]bool{
				"gui:service:install":   tt.wantInstall,
				"gui:service:uninstall": tt.wantUninst,
				"gui:service:start":     tt.wantStart,
				"gui:service:stop":      tt.wantStop,
			}
			for id, want := range checks {
				present := len(w.ResolveID(id)) > 0
				if present != want {
					t.Errorf("%s present = %v, want %v", id, present, want)
				}
				if want {
					assertDisabled(t, root, w, id, tt.wantDisabled)
				}
			}
		})
	}
}

// TestUninstallConfirm proves the Uninstall service button asks first: the
// confirm dialog appears and the action only runs from the confirm.
func TestUninstallConfirm(t *testing.T) {
	t.Run("yes runs the uninstall action", func(t *testing.T) {
		got := make(chan guiAction, 1)
		app := settingsApp()
		app.apply = func(_ context.Context, req applyRequest) (applyResult, error) {
			got <- req.action

			return applyResult{ok: true, message: "Service removed."}, nil
		}

		w := newSettingsWindow(t, app)
		w.TestRender(nil)

		if err := w.TestClick("gui:service:uninstall"); err != nil {
			t.Fatalf("click uninstall: %v", err)
		}
		w.TestRender(nil)
		if n := len(w.ResolveID("gui:service:uninstall:confirm")); n != 1 {
			t.Fatalf("confirm dialog missing (%d)", n)
		}

		if err := w.TestClick("gui:service:uninstall:confirm:1"); err != nil {
			t.Fatalf("click confirm yes: %v", err)
		}

		select {
		case action := <-got:
			if action != actionUninstall {
				t.Fatalf("action = %q, want %q", action, actionUninstall)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("apply was not invoked")
		}
		waitIdle(t, w, app)
	})

	t.Run("no runs nothing", func(t *testing.T) {
		got := make(chan guiAction, 1)
		app := settingsApp()
		app.apply = func(_ context.Context, req applyRequest) (applyResult, error) {
			got <- req.action

			return applyResult{ok: true}, nil
		}

		w := newSettingsWindow(t, app)
		w.TestRender(nil)

		if err := w.TestClick("gui:service:uninstall"); err != nil {
			t.Fatalf("click uninstall: %v", err)
		}
		w.TestRender(nil)
		if err := w.TestClick("gui:service:uninstall:confirm"); err != nil {
			t.Fatalf("click confirm no: %v", err)
		}
		w.TestRender(nil)

		select {
		case action := <-got:
			t.Fatalf("apply ran on cancel: %q", action)
		case <-time.After(50 * time.Millisecond):
		}
	})
}

// TestProfileOpEnablement renders Settings and checks Delete is inert for the
// active profile and for the last profile, but live for another profile.
func TestProfileOpEnablement(t *testing.T) {
	t.Run("other profile is deletable", func(t *testing.T) {
		app := settingsApp()
		app.profiles = []string{"work", "home"}
		app.active = "work"
		app.selected = "home"

		w := newSettingsWindow(t, app)
		root := w.TestRender(nil)

		assertDisabled(t, root, w, "gui:profile:delete", false)
	})

	t.Run("active profile is not deletable", func(t *testing.T) {
		app := settingsApp()
		app.profiles = []string{"work", "home"}
		app.active = "work"
		app.selected = "work"

		w := newSettingsWindow(t, app)
		root := w.TestRender(nil)

		assertDisabled(t, root, w, "gui:profile:delete", true)
	})

	t.Run("last profile is not deletable", func(t *testing.T) {
		app := settingsApp()
		app.profiles = []string{"work"}
		app.active = "work"
		app.selected = "work"

		w := newSettingsWindow(t, app)
		root := w.TestRender(nil)

		assertDisabled(t, root, w, "gui:profile:delete", true)
	})
}

// TestSaveCarriesDraft drives the Save button and asserts the apply seam
// receives the save action with the draft, and that a blank id field never
// puts an id on the draft.
func TestSaveCarriesDraft(t *testing.T) {
	t.Run("typed id travels", func(t *testing.T) {
		got := runSave(t, "new-id-123")
		if got.action != actionSaveProfile {
			t.Fatalf("action = %q, want %q", got.action, actionSaveProfile)
		}
		if got.draft == nil || got.draft.id != "new-id-123" {
			t.Fatalf("draft = %+v, want id new-id-123", got.draft)
		}
	})

	t.Run("blank id stays blank", func(t *testing.T) {
		got := runSave(t, "")
		if got.draft == nil {
			t.Fatal("save must carry a draft")
		}
		if got.draft.id != "" {
			t.Fatalf("blank id field produced an id: %q", got.draft.id)
		}
	})
}

// TestNavSwitchesPage checks the left nav toggles between Status, Settings,
// and the Rules page.
func TestNavSwitchesPage(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if len(w.ResolveID("gui:banner")) == 0 {
		t.Fatal("Status page did not render the state hero")
	}
	if len(w.ResolveID("gui:profile:save")) != 0 {
		t.Fatal("Settings content must not render on the Status page")
	}

	if err := w.TestClick("gui:nav:settings"); err != nil {
		t.Fatalf("click settings nav: %v", err)
	}
	w.TestRender(nil)

	if app.page != pageSettings {
		t.Fatalf("page = %v, want settings", app.page)
	}
	if len(w.ResolveID("gui:profile:save")) == 0 {
		t.Fatal("Settings page did not render the editor")
	}

	if err := w.TestClick("gui:nav:rules"); err != nil {
		t.Fatalf("click rules nav: %v", err)
	}
	w.TestRender(nil)

	if app.page != pageRules {
		t.Fatalf("page = %v, want rules", app.page)
	}
	if len(w.ResolveID("gui:edit:rules")) == 0 || len(w.ResolveID("gui:edit:lists")) == 0 {
		t.Fatal("Rules page did not render the list/rule editors")
	}
	if len(w.ResolveID("gui:profile:save")) == 0 {
		t.Fatal("Rules page did not render a Save control")
	}
	if len(w.ResolveID("gui:edit:provider")) != 0 {
		t.Fatal("Settings editor must not render on the Rules page")
	}
}

// TestThemePickerInSettings proves the Auto/Light/Dark picker now lives in the
// Settings Appearance card and still persists the choice.
func TestThemePickerInSettings(t *testing.T) {
	app := settingsApp()
	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	card := mustFindLayout(t, w, root, "gui:card:appearance")
	theme := mustFindLayout(t, w, root, "gui:theme")
	if !layoutContains(card, theme) {
		t.Fatal("theme picker is not inside the Appearance card")
	}

	if err := w.TestClick("gui:theme:opt:2"); err != nil {
		t.Fatalf("click Dark: %v", err)
	}
	if app.themePref != themeDark {
		t.Errorf("themePref = %q, want %q", app.themePref, themeDark)
	}
	if app.settings.Theme != themeDark {
		t.Errorf("saved theme = %q, want %q", app.settings.Theme, themeDark)
	}
}

// runSave renders Settings with a fake apply, types id (if any), clicks Save,
// and returns the captured request.  The draft is seeded onto NextDNS, a
// provider that uses a resolver id, so the id field is live and typable.
func runSave(t *testing.T, id string) (got applyRequest) {
	t.Helper()

	ch := make(chan applyRequest, 1)
	app := settingsApp()
	app.draft = profileDraft{name: "work", provider: providers.NextDNS, variant: "ultralow", protocol: "doh"}
	app.seeded = true // keep the hand-built draft; do not reseed from the summary
	app.apply = func(_ context.Context, req applyRequest) (applyResult, error) {
		ch <- req

		return applyResult{ok: true, message: "Saved."}, nil
	}

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if id != "" {
		if err := w.TestType("gui:edit:id", id); err != nil {
			t.Fatalf("type id: %v", err)
		}
		w.TestRender(nil)
	}

	if err := w.TestClick("gui:profile:save"); err != nil {
		t.Fatalf("click save: %v", err)
	}

	select {
	case got = <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("apply was not invoked")
	}

	return got
}

// settingsApp is a verified App on the Settings page with a healthy snapshot.
func settingsApp() *App {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()
	app.page = pageSettings

	return app
}

// rulesApp is a verified App on the Rules page with a healthy snapshot.
func rulesApp() *App {
	app := settingsApp()
	app.page = pageRules

	return app
}

// newSettingsWindow builds a test window for app.  It is tall enough that the
// whole page — Settings or the Rules page, including the list and per-domain
// sections — fits without scrolling, so clicks land on the editor.
func newSettingsWindow(t *testing.T, app *App) *gogui.Window {
	t.Helper()

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width:  defaultWidth,
		Height: 2400,
		State:  app,
	})
	w.SetView(view)

	return w
}

// assertDisabled resolves id to its layout and checks the Disabled flag.
func assertDisabled(t *testing.T, root *gogui.Layout, w *gogui.Window, id string, want bool) {
	t.Helper()

	ly := mustFindLayout(t, w, root, id)
	if ly.Shape.Disabled != want {
		t.Errorf("%q Disabled = %v, want %v", id, ly.Shape.Disabled, want)
	}
}

// assertNoFindings fails on duplicate IDs, missing IDs, or event/focus drift.
func assertNoFindings(t *testing.T, w *gogui.Window) {
	t.Helper()

	findings := w.TestFindings(gogui.DebugDuplicates | gogui.DebugMissingIDs |
		gogui.DebugUnconsumed | gogui.DebugUnknownFocus |
		gogui.DebugStampDrift | gogui.DebugAutoIDs)
	if len(findings) > 0 {
		t.Fatalf("widget findings: %v", findings)
	}
}

// profilesSnapshot is a healthy snapshot whose summary carries the full,
// name-sorted per-profile list with "work" active.
func profilesSnapshot() snapshot {
	s := sampleSnapshot()
	s.summary.Profiles = []ipc.ProfileSummary{
		{
			Name: "home", Provider: "nextdns", Variant: "ultralow", Protocol: "doh",
			IDSet: true, FilteringEnabled: true, BlockResponse: "0.0.0.0",
		},
		{
			Name: "work", Active: true, Provider: "quad9", Variant: "secure", Protocol: "doh",
			FilteringEnabled: true, BlockResponse: "nxdomain", CacheEnabled: true,
			CacheSizeBytes: 8 << 20,
		},
	}

	return s
}

// TestDefaultProfileOnFreshInstall proves that with no live summary and no
// cache (a brand-new install) the editor is seeded with a usable "default"
// profile, keeps a blank id, and enables Save.
func TestDefaultProfileOnFreshInstall(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = snapshot{trust: trustVerified, installed: false, updated: time.Now()}
	app.page = pageSettings

	if app.summary() != nil {
		t.Fatal("test needs no live and no cached summary")
	}

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	if app.selected != "default" || app.draft.name != "default" {
		t.Fatalf("selected/draft name = %q/%q, want default/default", app.selected, app.draft.name)
	}
	if len(app.profiles) != 1 || app.profiles[0] != "default" {
		t.Fatalf("profiles = %v, want [default]", app.profiles)
	}
	if app.draft.provider == "" {
		t.Fatal("default profile has no provider seeded")
	}
	if app.draft.id != "" {
		t.Fatalf("default profile invented a resolver id: %q", app.draft.id)
	}

	// The editor and Save are usable immediately.
	assertDisabled(t, root, w, "gui:edit:provider", false)
	assertDisabled(t, root, w, "gui:profile:save", false)
}

// TestCacheSummaryStripsRevision checks the cached summary loses its revision
// token and is only rewritten when the revision changes.
func TestCacheSummaryStripsRevision(t *testing.T) {
	app := &App{}
	s := sampleSnapshot()
	s.summary.Revision = "123-4"

	app.cacheSummary(s)
	if app.cached == nil {
		t.Fatal("summary was not cached")
	}
	if app.cached.Revision != "" {
		t.Fatalf("revision was not stripped: %q", app.cached.Revision)
	}
	if app.cached.Profile != "default" {
		t.Fatalf("cached summary = %+v", app.cached)
	}

	// Same revision: the cache is left alone.
	s.summary.Profile = "other"
	app.cacheSummary(s)
	if app.cached.Profile != "default" {
		t.Fatal("cache was rewritten without a revision change")
	}
}

// TestEditWhileStoppedUsesCache proves the editor works while the service is
// stopped, seeded from the cached summary.
func TestEditWhileStoppedUsesCache(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}

	live := sampleSnapshot()
	live.summary.Revision = "rev-live"
	app.cacheSummary(live)
	app.snap = snapshot{trust: trustVerified, installed: true, scmState: "stopped"}
	app.page = pageSettings

	w := newSettingsWindow(t, app)
	root := w.TestRender(nil)

	if app.summary() == nil {
		t.Fatal("editor did not fall back to the cache")
	}
	if app.draft.provider != "controld" {
		t.Fatalf("draft not seeded from cache: %+v", app.draft)
	}
	if hint := editingHint(app); hint == "" {
		t.Fatal("expected an offline-editing hint")
	}

	// The editor is usable (trust verified + cache present), not disabled.
	assertDisabled(t, root, w, "gui:edit:provider", false)
}

// TestSaveWhileStoppedSendsEmptyRevision proves a save staged from the cache
// carries no expected revision.
func TestSaveWhileStoppedSendsEmptyRevision(t *testing.T) {
	ch := make(chan applyRequest, 1)

	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	live := sampleSnapshot()
	live.summary.Revision = "rev-live"
	app.cacheSummary(live)
	app.snap = snapshot{trust: trustVerified, installed: true, scmState: "stopped"}
	app.page = pageSettings
	app.apply = func(_ context.Context, req applyRequest) (applyResult, error) {
		ch <- req

		return applyResult{ok: true, message: "Saved."}, nil
	}

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if err := w.TestClick("gui:profile:save"); err != nil {
		t.Fatalf("click save: %v", err)
	}

	select {
	case req := <-ch:
		if req.expectedRevision != "" {
			t.Fatalf("expected an empty revision while cached, got %q", req.expectedRevision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("apply was not invoked")
	}

	waitIdle(t, w, app)
}

// TestProtocolSelectCarriesProtocol drives the Protocol select for a normal
// provider and checks the choice reaches the draft and the elevate patch, so a
// dropped protocol cannot regress.
func TestProtocolSelectCarriesProtocol(t *testing.T) {
	app := settingsApp()
	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if len(w.ResolveID("gui:edit:protocol")) == 0 {
		t.Fatal("protocol select not rendered for a normal provider")
	}

	// Open the drop-down (highlight = current protocol), move to the next
	// option, and commit it.
	if err := w.TestKey("gui:edit:protocol", gogui.KeyEnter, 0); err != nil {
		t.Fatalf("open protocol select: %v", err)
	}
	if err := w.TestKey("gui:edit:protocol", gogui.KeyDown, 0); err != nil {
		t.Fatalf("move protocol select: %v", err)
	}
	if err := w.TestKey("gui:edit:protocol", gogui.KeyEnter, 0); err != nil {
		t.Fatalf("commit protocol select: %v", err)
	}

	// sampleSnapshot's protocol is "doh"; the next option is "doh3".
	if app.draft.protocol != "doh3" {
		t.Fatalf("draft.protocol = %q, want doh3", app.draft.protocol)
	}

	req, ok := buildElevateRequest(applyRequest{action: actionSaveProfile, draft: &app.draft})
	if !ok {
		t.Fatal("buildElevateRequest returned !ok")
	}
	pp, ok := req.Patch.Profiles[app.draft.name]
	if !ok || pp.Protocol == nil || *pp.Protocol != "doh3" {
		t.Fatalf("patch protocol = %v, want doh3", pp.Protocol)
	}
}

// TestBootstrapSync proves the editor recomputes the bootstrap list when the
// provider changes, and leaves it alone when the selection cannot be expanded.
// This is the reported bug: the ControlD default 76.76.2.11:53 stuck in the box
// whatever provider was selected, and was then saved onto the new profile.
func TestBootstrapSync(t *testing.T) {
	t.Run("provider change refreshes", func(t *testing.T) {
		app := settingsApp()
		app.draft = profileDraft{
			name: "default", provider: providers.ControlD, variant: "p0",
			protocol: "doh", bootstrap: "76.76.2.11:53",
		}
		app.seeded = true // keep the hand-built draft; do not reseed from the summary

		w := newSettingsWindow(t, app)
		w.TestRender(nil)

		setDraftProvider([]string{providers.Cloudflare}, gogui.EventCtx{Window: w})

		if app.draft.variant != "standard" {
			t.Fatalf("variant = %q, want standard", app.draft.variant)
		}
		if want := "1.1.1.1, 1.0.0.1, 2606:4700:4700::1111, 2606:4700:4700::1001"; app.draft.bootstrap != want {
			t.Fatalf("bootstrap = %q, want %q", app.draft.bootstrap, want)
		}
	})

	t.Run("unexpandable selection is left alone", func(t *testing.T) {
		d := profileDraft{provider: providers.NextDNS, variant: "anycast", bootstrap: "9.9.9.9"}
		syncBootstrap(&d)
		if d.bootstrap != "9.9.9.9" {
			t.Fatalf("nextdns (no id) bootstrap = %q, want untouched", d.bootstrap)
		}

		c := profileDraft{provider: providers.Custom, variant: "custom", bootstrap: "9.9.9.9"}
		syncBootstrap(&c)
		if c.bootstrap != "9.9.9.9" {
			t.Fatalf("custom bootstrap = %q, want untouched", c.bootstrap)
		}
	})
}

// TestProtocolHiddenForCustom proves the Protocol control is not rendered for
// the custom provider (its transport comes from the URL), while the custom URL
// input is.
func TestProtocolHiddenForCustom(t *testing.T) {
	app := settingsApp()
	app.draft.provider = providers.Custom
	app.draft.variant = "custom"
	app.seeded = true // keep the hand-built draft; do not reseed from the summary

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if n := len(w.ResolveID("gui:edit:protocol")); n != 0 {
		t.Fatalf("protocol select present for custom (%d), want absent", n)
	}
	if n := len(w.ResolveID("gui:edit:url")); n != 1 {
		t.Fatalf("custom URL input = %d, want 1", n)
	}
}

// TestNeedsResolverID covers the provider/variant id predicate: the no-id
// selections, the ones that require an id, and the fail-open default.
func TestNeedsResolverID(t *testing.T) {
	tests := []struct {
		provider, variant string
		want              bool
	}{
		// No id: the metadata says so.
		{"quad9", "secure", false},
		{"quad9", "noblock-ecs", false},
		{"cloudflare", "standard", false},
		{"cloudflare", "family", false},
		{"controld", "p2", false},
		{"controld", "x-oisd", false},
		{"controld", "family", false},
		// No id: custom takes its endpoint from the URL.
		{providers.Custom, "custom", false},
		// Requires an id.
		{"nextdns", "ultralow", true},
		{"nextdns", "anycast", true},
		{"controld", "custom", true},
		{"controld", "x-hagezi", true},
		{"controld", "free", true},
		// Fail open: an unclassifiable selection must not disable the field.
		{"mystery", "whatever", true},
		{"quad9", "not-a-variant", true},
		{"", "", true},
		// Inputs are trimmed before lookup.
		{"  quad9  ", "  secure  ", false},
	}

	for _, tt := range tests {
		if got := needsResolverID(tt.provider, tt.variant); got != tt.want {
			t.Errorf("needsResolverID(%q, %q) = %v, want %v", tt.provider, tt.variant, got, tt.want)
		}
	}
}

// TestResolverIDFieldEnablement proves the Resolver ID input is disabled for a
// no-id provider and enabled for one that needs an id, derived per frame from
// the current provider+variant.  Its hint line is always present and sits under
// the field.
func TestResolverIDFieldEnablement(t *testing.T) {
	tests := []struct {
		name              string
		provider, variant string
		wantDisabled      bool
	}{
		{"quad9", "quad9", "secure", true},
		{"cloudflare", "cloudflare", "standard", true},
		{"controld free preset", "controld", "p1", true},
		{"custom", providers.Custom, "custom", true},
		{"nextdns", "nextdns", "ultralow", false},
		{"controld custom", "controld", "custom", false},
		{"unknown provider fails open", "mystery", "whatever", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := settingsApp()
			app.draft.name = "work"
			app.draft.provider = tt.provider
			app.draft.variant = tt.variant
			app.seeded = true // keep the hand-built draft; do not reseed

			w := newSettingsWindow(t, app)
			root := w.TestRender(nil)

			assertDisabled(t, root, w, "gui:edit:id", tt.wantDisabled)

			hint := mustFindLayout(t, w, root, "gui:edit:id:hint")
			field := mustFindLayout(t, w, root, "gui:edit:id")
			if hint.Shape.Y < field.Shape.Y {
				t.Errorf("hint y=%.0f is above the id field y=%.0f", hint.Shape.Y, field.Shape.Y)
			}
		})
	}
}

// TestResolverIDHint proves the line under the Resolver ID field names the
// value to enter for the selected provider and variant, in plain language, and
// never carries the CLI's --id syntax.
func TestResolverIDHint(t *testing.T) {
	tests := []struct {
		name              string
		provider, variant string
		want              string
	}{
		{"quad9", "quad9", "secure", "This provider does not use a Resolver ID."},
		{"nextdns", "nextdns", "ultralow", "Enter your NextDNS configuration ID."},
		{"controld custom", "controld", "custom", "Enter the profile ID from your ControlD dashboard."},
		{"controld hagezi", "controld", "x-hagezi", "Enter the Hagezi list name, for example x-hagezi-pro or x-hagezi-pro-plus."},
		{"controld free", "controld", "free", "Enter the ControlD free preset name."},
		{"unknown fails neutral", "mystery", "whatever", "Leave blank to keep the current Resolver ID."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolverIDHint(tt.provider, tt.variant)
			if got != tt.want {
				t.Errorf("resolverIDHint(%q, %q) = %q, want %q", tt.provider, tt.variant, got, tt.want)
			}
			if strings.Contains(got, "--") {
				t.Errorf("hint still carries CLI syntax: %q", got)
			}
		})
	}
}

// TestEditorProviderLabelNotDuplicated proves "Provider" is printed once in the
// profile editor — the field-group heading only — now that the provider select
// carries no visible label (it keeps a name for assistive tech instead).
func TestEditorProviderLabelNotDuplicated(t *testing.T) {
	w, root := layoutFor(t, settingsApp(), defaultWidth, defaultHeight)
	card := mustFindLayout(t, w, root, "gui:card:editor")
	if n := countText(card, "Provider"); n != 1 {
		t.Fatalf(`"Provider" rendered %d times, want 1`, n)
	}
	assertNoFindings(t, w)
}

// TestProviderOptionsUseHumanNames proves the provider menu carries only the
// display name: no raw backend id, no em-dashed description, no parenthetical.
func TestProviderOptionsUseHumanNames(t *testing.T) {
	want := map[string]string{
		providers.Quad9:      "Quad9",
		providers.Cloudflare: "Cloudflare",
		providers.NextDNS:    "NextDNS",
		providers.ControlD:   "ControlD",
		providers.Custom:     "Custom",
	}

	for _, opt := range providerOptions() {
		label, ok := want[opt.Value]
		if !ok {
			t.Errorf("unexpected provider option %q", opt.Value)

			continue
		}
		if opt.Label != label {
			t.Errorf("%s label = %q, want %q", opt.Value, opt.Label, label)
		}
		if strings.Contains(opt.Label, "—") || strings.Contains(opt.Label, "(") {
			t.Errorf("%s label %q still carries detail", opt.Value, opt.Label)
		}
	}
}

// TestVariantOptionsUseHumanLabels proves every variant menu label is human
// copy: no raw variant name prefix, no parenthetical, and none of the CLI
// instructions the catalog carries for --id.
func TestVariantOptionsUseHumanLabels(t *testing.T) {
	for _, p := range providers.Registry() {
		for _, opt := range variantOptions(p.Name) {
			if opt.Label == "" {
				t.Errorf("%s/%s has an empty label", p.Name, opt.Value)
			}
			for _, banned := range []string{"—", "(", "--id", "pass the", "requires --id"} {
				if strings.Contains(opt.Label, banned) {
					t.Errorf("%s/%s label %q contains %q", p.Name, opt.Value, opt.Label, banned)
				}
			}
			if strings.HasPrefix(opt.Label, opt.Value) {
				t.Errorf("%s/%s label %q repeats the backend name", p.Name, opt.Value, opt.Label)
			}
		}
	}
}

// TestProviderDetailStatesTheRequirement proves the small line under the
// provider select states only what the current choice needs: a Resolver ID when
// one is required, and nothing otherwise.
func TestProviderDetailStatesTheRequirement(t *testing.T) {
	app := settingsApp()
	app.ensureDraft()
	app.draft.provider = providers.ControlD
	app.draft.variant = "custom"
	app.seeded = true

	w := newSettingsWindow(t, app)
	detail := func() (texts map[string]bool) {
		t.Helper()
		root := w.TestRender(nil)

		if len(w.ResolveID("gui:edit:provider:detail")) == 0 {
			return nil
		}

		return textSet(mustFindLayout(t, w, root, "gui:edit:provider:detail"))
	}

	if texts := detail(); !texts["Requires a Resolver ID to be set."] {
		t.Errorf("ControlD detail = %v", texts)
	}

	setDraftProvider([]string{providers.NextDNS}, gogui.EventCtx{Window: w})
	if texts := detail(); !texts["Requires a Resolver ID to be set."] {
		t.Errorf("NextDNS detail = %v", texts)
	}

	setDraftProvider([]string{providers.Quad9}, gogui.EventCtx{Window: w})
	if texts := detail(); texts != nil {
		t.Errorf("Quad9 should not state a requirement, got %v", texts)
	}

	setDraftProvider([]string{providers.Custom}, gogui.EventCtx{Window: w})
	if texts := detail(); texts != nil {
		t.Errorf("Custom should not state a requirement, got %v", texts)
	}
}

// TestSettingsPositionRoundTrip proves the remembered window position survives
// a save/load through gui.json, including a negative X (a monitor left of the
// primary).
func TestSettingsPositionRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gui.json")
	want := settings{Width: 1280, Height: 720, Theme: themeDark, X: -1200, Y: 40}

	if err := saveSettings(path, want); err != nil {
		t.Fatalf("saveSettings: %v", err)
	}

	got := loadSettings(path)
	if got.X != want.X || got.Y != want.Y {
		t.Fatalf("position = (%d,%d), want (%d,%d)", got.X, got.Y, want.X, want.Y)
	}
	if got.Width != want.Width || got.Height != want.Height || got.Theme != want.Theme {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
}

// TestTraySettingsRoundTrip proves the opt-in tray preferences survive gui.json,
// and that an absent file leaves the tray off.
func TestTraySettingsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gui.json")

	if got := loadSettings(path); got.Tray || got.MinimizeToTray || got.CloseToTray {
		t.Fatalf("missing gui.json enabled the tray: %+v", got)
	}

	want := settings{Tray: true, MinimizeToTray: true, CloseToTray: true}
	if err := saveSettings(path, want); err != nil {
		t.Fatalf("saveSettings: %v", err)
	}

	got := loadSettings(path)
	if !got.Tray || !got.MinimizeToTray || !got.CloseToTray {
		t.Fatalf("tray settings = %+v, want %+v", got, want)
	}
}

// TestTrayMenuHasExpectedActions proves the right-click menu carries the
// status row, the three actions, and exit, in the order the user sees.
func TestTrayMenuHasExpectedActions(t *testing.T) {
	items := trayMenu("Service: Running")

	if len(items) != 7 {
		t.Fatalf("tray menu has %d items, want 7", len(items))
	}
	if !items[0].Disabled || items[0].Text != "Service: Running" {
		t.Errorf("item 0 = %+v, want the disabled current status", items[0])
	}
	if !items[1].Separator || items[1].ID != trayActionOpen {
		t.Errorf("item 1 = %+v, want the Open separator", items[1])
	}

	want := []string{"Open Sigil DNS", "Open log file", "Clear DNS cache"}
	for i, text := range want {
		if got := items[i+2]; got.Text != text || got.Disabled {
			t.Errorf("item %d = %+v, want enabled %q", i+2, got, text)
		}
	}
	if !items[5].Separator || items[5].ID != trayActionExit {
		t.Errorf("item 5 = %+v, want the Exit separator", items[5])
	}
	if items[6].Text != "Exit" || items[6].Separator {
		t.Errorf("item 6 = %+v, want Exit", items[6])
	}
}

// TestTrayMenuIDsCompensateGoGUIRoot pins the workaround for go-gui v0.89.1's
// Win32 tray menu: a click on item i resolves through menuNodes[i], whose
// element 0 is the root, so it fires item i-1's ID.  If a go-gui upgrade fixes
// that, this test still passes but the real menu will be wrong: the shortcut
// comment on trayMenu is the reminder to re-check it.
func TestTrayMenuIDsCompensateGoGUIRoot(t *testing.T) {
	items := trayMenu("Service: Running")

	nodes := []string{""} // go-gui's root menuNode
	for _, it := range items {
		nodes = append(nodes, it.ID)
	}

	want := map[int]string{
		2: trayActionOpen,
		3: trayActionLog,
		4: trayActionFlush,
		6: trayActionExit,
	}
	for click, action := range want {
		if click >= len(nodes) {
			t.Fatalf("click %d has no menu node", click)
		}
		if got := nodes[click]; got != action {
			t.Errorf("click on item %d fires %q, want %q", click, got, action)
		}
	}
}

// TestCloseWindowSavesSize proves Tray Exit captures the live window size
// before destroying the window; the final save after RunApp cannot read it.
func TestCloseWindowSavesSize(t *testing.T) {
	app := settingsApp()
	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	ww, wh := w.WindowSize()
	app.closeWindow(w)

	if app.settings.Width != ww || app.settings.Height != wh {
		t.Errorf("saved size = %dx%d, want %dx%d",
			app.settings.Width, app.settings.Height, ww, wh)
	}
	if !w.CloseRequested() {
		t.Error("closeWindow did not request the close")
	}
}

// TestSaveSizeIgnoresNonNormalWindow proves a maximized or minimized window's
// transient size never replaces the size the user chose in the normal state.
func TestSaveSizeIgnoresNonNormalWindow(t *testing.T) {
	orig := windowNormal
	windowNormal = func(string) bool { return false }
	defer func() { windowNormal = orig }()

	app := &App{settings: settings{Width: 1145, Height: 858}}
	w := gogui.NewTestWindow(t, gogui.WindowCfg{Width: 1278, Height: 1031})

	app.saveSize(w)

	if app.settings.Width != 1145 || app.settings.Height != 858 {
		t.Errorf("size = %dx%d after a non-normal save, want 1145x858",
			app.settings.Width, app.settings.Height)
	}
}

// TestSaveSizeUsesNormalWindow proves a normal window's live size is kept.
func TestSaveSizeUsesNormalWindow(t *testing.T) {
	orig := windowNormal
	windowNormal = func(string) bool { return true }
	defer func() { windowNormal = orig }()

	app := &App{}
	w := gogui.NewTestWindow(t, gogui.WindowCfg{Width: 1145, Height: 858})

	app.saveSize(w)

	if app.settings.Width != 1145 || app.settings.Height != 858 {
		t.Errorf("size = %dx%d, want 1145x858",
			app.settings.Width, app.settings.Height)
	}
}

// TestCloseToTrayPersistsSize proves the close path writes the size before the
// process quits: close-to-tray now exits the window and leaves the separate
// tray host running, so the size must survive that exit.
func TestCloseToTrayPersistsSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gui.json")
	app := settingsApp()
	app.guiPath = path
	app.settings.Tray = true
	app.settings.CloseToTray = true

	w := newSettingsWindow(t, app)
	w.TestRender(nil)
	ww, wh := w.WindowSize()

	app.closeWindow(w)

	if !w.CloseRequested() {
		t.Fatal("close-to-tray did not quit the window")
	}
	got := loadSettings(path)
	if got.Width != ww || got.Height != wh {
		t.Errorf("persisted size = %dx%d, want %dx%d", got.Width, got.Height, ww, wh)
	}
}

// TestCloseWindowTrayHost proves close-to-tray relies on the already-running
// host (starting one here would race the tray Exit action, which closes this
// window while the host is exiting), while a normal close stops the icon.
func TestCloseWindowTrayHost(t *testing.T) {
	tests := []struct {
		name        string
		closeToTray bool
		wantStop    int
	}{
		{"close to tray leaves host", true, 0},
		{"normal close stops host", false, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := settingsApp()
			app.settings.Tray = true
			app.settings.CloseToTray = tt.closeToTray

			var started, stopped int
			origStart, origStop := hostStart, hostStop
			hostStart = func() error { started++; return nil }
			hostStop = func() { stopped++ }
			t.Cleanup(func() { hostStart, hostStop = origStart, origStop })

			w := newSettingsWindow(t, app)
			w.TestRender(nil)
			app.closeWindow(w)

			if started != 0 || stopped != tt.wantStop {
				t.Fatalf("close: hostStart=%d hostStop=%d, want hostStart=0 hostStop=%d",
					started, stopped, tt.wantStop)
			}
		})
	}
}

// TestTrayConfigCarriesTooltip proves the tray config carries the app name;
// a Windows-only shim turns it into the standard tooltip (sni omits
// NIF_SHOWTIP, which would otherwise suppress it).
func TestTrayConfigCarriesTooltip(t *testing.T) {
	cfg := trayConfig("Service: Running")
	if cfg.Tooltip != windowTitle {
		t.Errorf("tray tooltip = %q, want %q", cfg.Tooltip, windowTitle)
	}
	if len(cfg.Menu) != 7 || cfg.Menu[0].Text != "Service: Running" {
		t.Errorf("tray menu = %+v, want the status row first", cfg.Menu)
	}
}

// TestTrayStatusText proves the status row never claims protection for a
// service that is not installed, stopped, or unverified.
func TestTrayStatusText(t *testing.T) {
	tests := []struct {
		name string
		snap snapshot
		want string
	}{
		{"unknown", snapshot{}, "Service: Unknown"},
		{"not installed", snapshot{scmState: "not installed"}, "Service: Not installed"},
		{"running", snapshot{installed: true, scmState: "running"}, "Service: Running"},
		{"stopped", snapshot{installed: true, scmState: "stopped"}, "Service: Stopped"},
		{"unverified", snapshot{installed: true, scmState: "running", trust: trustUnverified}, "Service: Unverified"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := trayStatusText(tt.snap); got != tt.want {
				t.Errorf("trayStatusText = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestTrayBehaviourSwitchesOnlyWithTray proves the close/minimize options stay
// hidden until the tray icon is enabled.
func TestTrayBehaviourSwitchesOnlyWithTray(t *testing.T) {
	app := settingsApp()
	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	for _, id := range []string{"gui:tray:autostart", "gui:tray:minimize", "gui:tray:close"} {
		if len(w.ResolveID(id)) != 0 {
			t.Errorf("%q rendered with the tray off", id)
		}
	}

	app.settings.Tray = true
	w.InvalidateLayout()
	w.TestRender(nil)

	for _, id := range []string{"gui:tray:autostart", "gui:tray:minimize", "gui:tray:close"} {
		if len(w.ResolveID(id)) == 0 {
			t.Errorf("%q missing with the tray on", id)
		}
	}
}

// TestTrayEnableFailureReverts proves a platform that cannot create the icon
// leaves the setting off instead of persisting a dead toggle.
func TestTrayEnableFailureReverts(t *testing.T) {
	app := settingsApp()
	w := newSettingsWindow(t, app)

	setTrayEnabled(gogui.EventCtx{Window: w}, true)

	// The start runs off the frame thread; drive frames until its result lands.
	for i := 0; i < 400 && app.settings.Tray; i++ {
		w.FrameFn()
		time.Sleep(5 * time.Millisecond)
	}

	if app.settings.Tray {
		t.Error("settings.Tray = true after the tray host could not start")
	}
}

// TestTrayToggleSyncsMinimize proves the poller's atomic copy follows the
// setting change, since the poller cannot read the UI-thread settings.
func TestTrayToggleSyncsMinimize(t *testing.T) {
	app := settingsApp()
	app.settings.Tray = true
	w := newSettingsWindow(t, app)

	toggleTrayBehaviour(gogui.EventCtx{Window: w}, func(s *settings) {
		s.MinimizeToTray = true
	})

	if !app.tray.minimize.Load() {
		t.Error("tray.minimize = false after enabling Minimize to tray")
	}
}

// TestDisableTrayClearsStartWithWindows proves turning the tray off drops the
// startup preference (and the logon task), since there is no tray to start into.
func TestDisableTrayClearsStartWithWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("would delete the machine's real SigilDNS logon task")
	}

	app := settingsApp()
	app.settings.Tray = true
	app.settings.StartWithWindows = true
	w := newSettingsWindow(t, app)

	setTrayEnabled(gogui.EventCtx{Window: w}, false)

	if app.settings.StartWithWindows {
		t.Error("settings.StartWithWindows = true after disabling the tray")
	}
}

// TestServiceControlFollowsInterception proves the Start/Stop control follows the
// INTERCEPTION state, not the service process: a running service with
// interception off offers Start (so Enable can turn protection back on), and an
// on state offers Stop.
func TestServiceControlFollowsInterception(t *testing.T) {
	tests := []struct {
		name     string
		mk       func() snapshot
		wantStop bool
	}{
		{"interception on", sampleSnapshot, true},
		{"running but off", func() snapshot {
			s := sampleSnapshot()
			s.status.Enabled = false
			s.status.Mode = "off"

			return s
		}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
			app.snap = tt.mk()
			app.page = pageSettings

			w := newSettingsWindow(t, app)
			w.TestRender(nil)

			want := "gui:service:start"
			unwanted := "gui:service:stop"
			if tt.wantStop {
				want, unwanted = unwanted, want
			}
			if n := len(w.ResolveID(want)); n != 1 {
				t.Errorf("%s rendered %d times, want 1", want, n)
			}
			if n := len(w.ResolveID(unwanted)); n != 0 {
				t.Errorf("%s rendered %d times, want 0", unwanted, n)
			}
			assertNoFindings(t, w)
		})
	}
}
