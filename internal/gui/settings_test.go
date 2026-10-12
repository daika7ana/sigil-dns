//go:build gui

package gui

import (
	"context"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
	"github.com/go-gui-org/go-gui/gui/backend/soft"

	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/providers"
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
		"gui:nav:status", "gui:nav:settings",
		"gui:service:uninstall",
		"gui:profile:select", "gui:profile:add", "gui:profile:rename", "gui:profile:delete",
		"gui:edit:provider", "gui:edit:variant", "gui:edit:protocol", "gui:edit:id",
		"gui:edit:bootstrap", "gui:edit:filtering", "gui:edit:block", "gui:edit:cache",
		"gui:edit:cachesize", "gui:profile:save",
	} {
		if len(w.ResolveID(id)) == 0 {
			t.Errorf("expected %q to be rendered", id)
		}
	}

	// The lifecycle controls are Install OR Uninstall, never both, and there
	// is no Start/Stop anywhere.
	if len(w.ResolveID("gui:service:install")) != 0 {
		t.Error("Install must not render when the service is already installed")
	}
	for _, id := range []string{"gui:service:start", "gui:service:stop"} {
		if len(w.ResolveID(id)) != 0 {
			t.Errorf("Settings must not render %q", id)
		}
	}

	assertNoFindings(t, w)
}

// TestServiceSectionEnablement moves the lifecycle buttons to Settings and
// checks Install shows only when absent and Uninstall only when present, both
// gated by the unverified-trust write rule.
func TestServiceSectionEnablement(t *testing.T) {
	tests := []struct {
		name         string
		snap         snapshot
		wantInstall  bool
		wantUninst   bool
		wantDisabled bool // for whichever button is shown
	}{
		{"absent-trusted", snapshot{trust: trustVerified, updated: time.Now()}, true, false, false},
		{"present-trusted", runningService(), false, true, false},
		{"absent-unverified", snapshot{trust: trustUnverified, running: true, updated: time.Now()}, true, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
			app.snap = tt.snap
			app.page = pageSettings

			w := newSettingsWindow(t, app)
			root := w.TestRender(nil)

			if got := len(w.ResolveID("gui:service:install")) > 0; got != tt.wantInstall {
				t.Errorf("install present = %v, want %v", got, tt.wantInstall)
			}
			if got := len(w.ResolveID("gui:service:uninstall")) > 0; got != tt.wantUninst {
				t.Errorf("uninstall present = %v, want %v", got, tt.wantUninst)
			}

			shown := "gui:service:install"
			if tt.wantUninst {
				shown = "gui:service:uninstall"
			}
			assertDisabled(t, root, w, shown, tt.wantDisabled)
		})
	}
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

// TestNavSwitchesPage checks the left nav toggles between Status and Settings.
func TestNavSwitchesPage(t *testing.T) {
	app := &App{themePref: themeSystem, refresh: make(chan struct{}, 1)}
	app.snap = sampleSnapshot()

	w := newSettingsWindow(t, app)
	w.TestRender(nil)

	if len(w.ResolveID("gui:power")) == 0 {
		t.Fatal("Status page did not render the power control")
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

// newSettingsWindow builds a test window for app.  It is tall enough that the
// whole Settings page fits without scrolling, so clicks land on the editor.
func newSettingsWindow(t *testing.T, app *App) *gogui.Window {
	t.Helper()

	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width:  defaultWidth,
		Height: 1400,
		State:  app,
	})
	w.SetView(view)

	return w
}

// assertDisabled resolves id to its layout and checks the Disabled flag.
func assertDisabled(t *testing.T, root *gogui.Layout, w *gogui.Window, id string, want bool) {
	t.Helper()

	ids := w.ResolveID(id)
	if len(ids) == 0 {
		t.Fatalf("widget %q is not rendered", id)
	}
	ly, ok := root.FindByID(ids[0])
	if !ok || ly.Shape == nil {
		t.Fatalf("widget %q is not in the layout", id)
	}
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

// runningService is a healthy snapshot with the service installed and running.
func runningService() snapshot {
	s := sampleSnapshot()
	s.installed = true
	s.scmState = "running"

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

	// sampleSnapshot's protocol is "doh"; the next option is "dot".
	if app.draft.protocol != "dot" {
		t.Fatalf("draft.protocol = %q, want dot", app.draft.protocol)
	}

	req, ok := buildElevateRequest(applyRequest{action: actionSaveProfile, draft: &app.draft})
	if !ok {
		t.Fatal("buildElevateRequest returned !ok")
	}
	pp, ok := req.Patch.Profiles[app.draft.name]
	if !ok || pp.Protocol == nil || *pp.Protocol != "dot" {
		t.Fatalf("patch protocol = %v, want dot", pp.Protocol)
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
// no-id provider (with an explaining hint) and enabled for one that needs an
// id, derived per frame from the current provider+variant.
func TestResolverIDFieldEnablement(t *testing.T) {
	tests := []struct {
		name              string
		provider, variant string
		wantDisabled      bool
		wantHint          bool
	}{
		{"quad9", "quad9", "secure", true, true},
		{"cloudflare", "cloudflare", "standard", true, true},
		{"controld free preset", "controld", "p1", true, true},
		{"custom", providers.Custom, "custom", true, true},
		{"nextdns", "nextdns", "ultralow", false, false},
		{"controld custom", "controld", "custom", false, false},
		{"unknown provider fails open", "mystery", "whatever", false, false},
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

			if got := len(w.ResolveID("gui:edit:id:hint")) > 0; got != tt.wantHint {
				t.Errorf("id hint present = %v, want %v", got, tt.wantHint)
			}
		})
	}
}
