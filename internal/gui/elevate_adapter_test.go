//go:build gui

package gui

import (
	"testing"

	"github.com/daika7ana/sigil-dns/internal/elevate"
	"github.com/daika7ana/sigil-dns/internal/ipc"
)

// TestElevateActionFor covers the full GUI-action -> elevate.Action mapping.
func TestElevateActionFor(t *testing.T) {
	cases := map[guiAction]elevate.Action{
		actionEnable:        elevate.ActionEnable,
		actionRestore:       elevate.ActionRestore,
		actionInstall:       elevate.ActionServiceInstall,
		actionUninstall:     elevate.ActionServiceUninstall,
		actionServiceStop:   elevate.ActionServiceStop,
		actionRefreshLists:  elevate.ActionRefreshLists,
		actionClearCache:    elevate.ActionClearCache,
		actionSaveProfile:   elevate.ActionApply,
		actionAddProfile:    elevate.ActionApply,
		actionRenameProfile: elevate.ActionApply,
		actionDeleteProfile: elevate.ActionApply,
		actionSetActive:     elevate.ActionApply,
	}

	for a, want := range cases {
		got, ok := elevateActionFor(a)
		if !ok || got != want {
			t.Errorf("elevateActionFor(%q) = (%q, %v), want (%q, true)", a, got, ok, want)
		}
	}

	if _, ok := elevateActionFor("nonsense"); ok {
		t.Error("unknown action should not map")
	}
}

// TestServiceRequestHasEmptyPatch checks the no-patch actions (the service
// lifecycle and the refresh_lists action) map to their elevate action with no
// config patch and a valid request.
func TestServiceRequestHasEmptyPatch(t *testing.T) {
	cases := map[guiAction]elevate.Action{
		actionInstall:      elevate.ActionServiceInstall,
		actionUninstall:    elevate.ActionServiceUninstall,
		actionServiceStop:  elevate.ActionServiceStop,
		actionRefreshLists: elevate.ActionRefreshLists,
		actionClearCache:   elevate.ActionClearCache,
	}

	for a, want := range cases {
		req, ok := buildElevateRequest(applyRequest{action: a, expectedRevision: "rev-1"})
		if !ok {
			t.Fatalf("%s: buildElevateRequest returned !ok", a)
		}
		if req.Action != want {
			t.Errorf("%s: action = %q, want %q", a, req.Action, want)
		}
		if req.ExpectedRevision != "rev-1" {
			t.Errorf("%s: revision = %q", a, req.ExpectedRevision)
		}
		if req.UUID == "" {
			t.Errorf("%s: empty UUID", a)
		}
		assertNoPatch(t, req.Patch)
		if err := req.Validate(); err != nil {
			t.Errorf("%s: Validate: %v", a, err)
		}
	}
}

// TestSavePatchMapping checks the draft -> ProfilePatch mapping: id nil when
// blank, URL only for custom, bootstrap parsed, filtering + cache carried.
func TestSavePatchMapping(t *testing.T) {
	t.Run("blank id is nil, non-custom URL omitted", func(t *testing.T) {
		d := profileDraft{
			name: "work", provider: "controld", variant: "p2", protocol: "doh",
			bootstrap: "1.1.1.1, , 2.2.2.2", filtering: true, blockResp: "nxdomain",
			cache: true, cacheSizeMB: "32",
			url: "https://ignored.example/dns-query", // non-custom: must be dropped
			id:  "   ",                               // blank: must be nil
		}

		req := saveRequest(t, d)
		pp, ok := req.Patch.Profiles["work"]
		if !ok {
			t.Fatal("no patch for work")
		}

		if pp.ID != nil {
			t.Fatalf("blank id must be nil, got %q", *pp.ID)
		}
		if pp.URL != nil {
			t.Fatalf("URL must be nil for a non-custom provider, got %q", *pp.URL)
		}
		if ptrOr(pp.Provider) != "controld" || ptrOr(pp.Variant) != "p2" || ptrOr(pp.Protocol) != "doh" {
			t.Errorf("provider/variant/protocol = %q/%q/%q", ptrOr(pp.Provider), ptrOr(pp.Variant), ptrOr(pp.Protocol))
		}
		if pp.Bootstrap == nil || len(*pp.Bootstrap) != 2 || (*pp.Bootstrap)[0] != "1.1.1.1" {
			t.Errorf("bootstrap = %v", ptrSlice(pp.Bootstrap))
		}
		if pp.Filtering == nil || pp.Filtering.Enabled == nil || !*pp.Filtering.Enabled {
			t.Errorf("filtering enabled not carried")
		}
		if ptrOr(pp.Filtering.BlockResponse) != "nxdomain" {
			t.Errorf("block response = %q", ptrOr(pp.Filtering.BlockResponse))
		}
		if pp.Cache == nil || pp.Cache.Enabled == nil || !*pp.Cache.Enabled {
			t.Errorf("cache enabled not carried")
		}
		if pp.Cache.SizeBytes == nil || *pp.Cache.SizeBytes != 32<<20 {
			t.Errorf("cache size = %v", pp.Cache.SizeBytes)
		}
	})

	t.Run("typed id is carried", func(t *testing.T) {
		req := saveRequest(t, profileDraft{name: "work", provider: "nextdns", variant: "ultralow", protocol: "doh", id: " abc_123 "})
		pp := req.Patch.Profiles["work"]
		if pp.ID == nil || *pp.ID != "abc_123" {
			t.Fatalf("id = %v, want abc_123", pp.ID)
		}
	})

	t.Run("URL carried only for custom", func(t *testing.T) {
		req := saveRequest(t, profileDraft{
			name: "c", provider: "custom", variant: "custom", protocol: "doh",
			url: " https://x.example/dns-query ",
		})
		pp := req.Patch.Profiles["c"]
		if pp.URL == nil || *pp.URL != "https://x.example/dns-query" {
			t.Fatalf("url = %v", pp.URL)
		}
	})

	t.Run("filtering off drops block response, bad size omitted", func(t *testing.T) {
		req := saveRequest(t, profileDraft{name: "w", provider: "quad9", variant: "secure", protocol: "doh", filtering: false, blockResp: "0.0.0.0", cacheSizeMB: "oops"})
		pp := req.Patch.Profiles["w"]
		if pp.Filtering.BlockResponse != nil {
			t.Errorf("block response should be omitted when filtering is off")
		}
		if pp.Cache.SizeBytes != nil {
			t.Errorf("invalid size should be omitted")
		}
	})
}

// TestFallbackProfileMapping checks the draft fallback reaches the patch and
// always travels: a name is trimmed, and a blank draft explicitly clears it.
func TestFallbackProfileMapping(t *testing.T) {
	t.Run("seeded from the summary", func(t *testing.T) {
		d := draftForProfile(ipc.ProfileSummary{Name: "w", FallbackProfile: "backup"})
		if d.fallback != "backup" {
			t.Fatalf("fallback = %q, want backup", d.fallback)
		}
	})

	t.Run("set", func(t *testing.T) {
		pp := saveRequest(t, profileDraft{name: "w", provider: "quad9", variant: "secure", protocol: "doh", fallback: " backup "}).Patch.Profiles["w"]
		if pp.FallbackProfile == nil || *pp.FallbackProfile != "backup" {
			t.Fatalf("fallback = %v, want backup", pp.FallbackProfile)
		}
	})

	t.Run("cleared", func(t *testing.T) {
		pp := saveRequest(t, profileDraft{name: "w", provider: "quad9", variant: "secure", protocol: "doh", fallback: "   "}).Patch.Profiles["w"]
		if pp.FallbackProfile == nil || *pp.FallbackProfile != "" {
			t.Fatalf("fallback = %v, want an explicit empty string", pp.FallbackProfile)
		}
	})
}

// TestListDiffMapping checks the seeded-list vs editor-list diff: a changed
// toggle is SetEnabled by original index, a deleted row is Remove by index, a
// new row travels as Add with its URL, and an unchanged seed sends no list
// patch at all.
func TestListDiffMapping(t *testing.T) {
	seeded := []ipc.ListSummary{
		{Host: "ads.example", Enabled: true},
		{Host: "track.example", Enabled: false},
		{Host: "gone.example", Enabled: true},
	}

	t.Run("toggle, remove and add", func(t *testing.T) {
		d := profileDraft{
			name: "w", provider: "quad9", variant: "secure", protocol: "doh",
			seededLists: seeded,
			lists: []listDraft{
				{host: "ads.example", enabled: false, idx: 0}, // toggled off
				{host: "track.example", enabled: false, idx: 1},
				// idx 2 removed
				{url: " https://new.example/hosts.txt ", enabled: true, idx: -1},
			},
		}

		pp := saveRequest(t, d).Patch.Profiles["w"]
		if pp.Filtering == nil || pp.Filtering.Lists == nil {
			t.Fatal("no list patch built")
		}
		lp := pp.Filtering.Lists

		if v, ok := lp.SetEnabled[0]; !ok || v {
			t.Errorf("SetEnabled[0] = (%v, %v), want (false, true)", v, ok)
		}
		if _, ok := lp.SetEnabled[1]; ok {
			t.Errorf("unchanged list 1 must not be in SetEnabled")
		}
		if len(lp.Remove) != 1 || lp.Remove[0] != 2 {
			t.Errorf("Remove = %v, want [2]", lp.Remove)
		}
		if len(lp.Add) != 1 || lp.Add[0].URL != "https://new.example/hosts.txt" || !lp.Add[0].Enabled {
			t.Errorf("Add = %+v", lp.Add)
		}
	})

	t.Run("unchanged seed sends no list patch", func(t *testing.T) {
		d := profileDraft{
			name: "w", provider: "quad9", variant: "secure", protocol: "doh",
			seededLists: seeded,
			lists: []listDraft{
				{host: "ads.example", enabled: true, idx: 0},
				{host: "track.example", enabled: false, idx: 1},
				{host: "gone.example", enabled: true, idx: 2},
			},
		}

		pp := saveRequest(t, d).Patch.Profiles["w"]
		if pp.Filtering.Lists != nil {
			t.Errorf("unchanged lists built a patch: %+v", pp.Filtering.Lists)
		}
	})
}

// TestPerDomainMapping checks the editor rows become the full rule array:
// domains are always sent, a typed upstream is trimmed, a kept rule's blank
// upstream names its source by original index (Keep), a new rule has no Keep,
// every entry carries its enable state, and a row with no domains is dropped.
func TestPerDomainMapping(t *testing.T) {
	d := profileDraft{
		name: "w", provider: "quad9", variant: "secure", protocol: "doh",
		rulesKnown: true,
		perDomain: []ruleDraft{
			{domains: "example.com, www.example.com", idx: 2, enabled: true},          // blank upstream -> Keep 2
			{domains: "ads.example", upstream: "  1.1.1.1  ", idx: 0, enabled: false}, // toggled off
			{domains: "new.example", idx: -1, enabled: true},                          // new rule: no Keep
			{domains: "   ", idx: 5, enabled: true},                                   // no domains: dropped
		},
	}

	pp := saveRequest(t, d).Patch.Profiles["w"]
	if pp.PerDomain == nil {
		t.Fatal("no per-domain array built")
	}
	rules := *pp.PerDomain
	if len(rules) != 3 {
		t.Fatalf("rules = %d, want 3", len(rules))
	}
	if len(rules[0].Domains) != 2 || rules[0].Domains[0] != "example.com" ||
		rules[0].Upstream != "" || rules[0].Keep == nil || *rules[0].Keep != 2 {
		t.Errorf("rule 0 = %+v, want a blank upstream kept by index 2", rules[0])
	}
	if rules[1].Upstream != "1.1.1.1" || rules[1].Keep != nil {
		t.Errorf("rule 1 = %+v, want an explicit upstream and no Keep", rules[1])
	}
	if rules[2].Keep != nil {
		t.Errorf("rule 2 Keep = %v, want nil for a new rule", *rules[2].Keep)
	}

	// Every emitted entry carries its enable state, so a toggled-off rule is
	// actually disabled by the full-replace array.
	if rules[0].Enabled == nil || !*rules[0].Enabled {
		t.Errorf("rule 0 Enabled = %v, want &true", rules[0].Enabled)
	}
	if rules[1].Enabled == nil || *rules[1].Enabled {
		t.Errorf("rule 1 Enabled = %v, want &false", rules[1].Enabled)
	}
	if rules[2].Enabled == nil || !*rules[2].Enabled {
		t.Errorf("rule 2 Enabled = %v, want &true", rules[2].Enabled)
	}
}

// TestPerDomainBypassMapping proves a bypass row carries Bypass and never a
// typed or kept upstream, so a stale value (or a seeded index) cannot leak into
// a bypass patch — which the helper rejects outright.
func TestPerDomainBypassMapping(t *testing.T) {
	d := profileDraft{
		name: "w", provider: "quad9", variant: "secure", protocol: "doh",
		rulesKnown: true,
		perDomain: []ruleDraft{
			// A seeded rule switched to bypass: no Keep, even with a blank upstream.
			{domains: "local.example", bypass: true, idx: 3, enabled: true},
			// A bypass row carrying a leftover typed upstream: it must not travel.
			{domains: "lan.example", bypass: true, upstream: "  tls://10.0.0.1:853  ", enabled: false},
		},
	}

	rules := *saveRequest(t, d).Patch.Profiles["w"].PerDomain
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(rules))
	}
	for i, r := range rules {
		if !r.Bypass || r.Upstream != "" || r.Keep != nil {
			t.Errorf("rule %d = %+v, want Bypass with no upstream and no Keep", i, r)
		}
	}
}

// TestLegacySummaryDoesNotSendRules proves a draft seeded from a legacy
// top-level summary (which carries no per-domain rules) does not send an empty
// rule array — that would silently clear the profile's rules on an unrelated
// save.
func TestLegacySummaryDoesNotSendRules(t *testing.T) {
	d := draftFromActiveProfile(&ipc.ConfigSummary{Profile: "w", Provider: "quad9"})
	if d.rulesKnown {
		t.Fatal("legacy summary must leave the rule set unknown")
	}

	pp := saveRequest(t, d).Patch.Profiles["w"]
	if pp.PerDomain != nil {
		t.Errorf("legacy summary sent a rule array: %+v", *pp.PerDomain)
	}
}

// TestNoIDProviderDropsTypedID proves a value typed into the Resolver ID field
// is never transmitted for a provider/variant that does not use one, so a value
// typed before switching provider cannot leak into the request.
func TestNoIDProviderDropsTypedID(t *testing.T) {
	for _, d := range []profileDraft{
		{name: "q", provider: "quad9", variant: "secure", protocol: "doh", id: "leak-me"},
		{name: "c", provider: "cloudflare", variant: "standard", protocol: "doh", id: "leak-me"},
		{name: "p", provider: "controld", variant: "p2", protocol: "doh", id: "leak-me"},
		{name: "u", provider: "custom", variant: "custom", protocol: "doh", id: "leak-me"},
	} {
		pp := saveRequest(t, d).Patch.Profiles[d.name]
		if pp.ID != nil {
			t.Errorf("%s/%s: id = %q, want nil", d.provider, d.variant, *pp.ID)
		}
	}
}

// TestNeedsIDProviderCarriesTypedID is the anti-regression twin: a needs-id
// selection still transmits the trimmed typed value.
func TestNeedsIDProviderCarriesTypedID(t *testing.T) {
	for _, d := range []profileDraft{
		{name: "n", provider: "nextdns", variant: "ultralow", protocol: "doh", id: " abc "},
		{name: "n2", provider: "nextdns", variant: "anycast", protocol: "doh", id: "abc"},
		{name: "c", provider: "controld", variant: "custom", protocol: "doh", id: "abc"},
		{name: "h", provider: "controld", variant: "x-hagezi", protocol: "doh", id: "abc"},
		{name: "f", provider: "controld", variant: "free", protocol: "doh", id: "abc"},
	} {
		pp := saveRequest(t, d).Patch.Profiles[d.name]
		if pp.ID == nil || *pp.ID != "abc" {
			t.Errorf("%s/%s: id = %v, want abc", d.provider, d.variant, pp.ID)
		}
	}
}

// TestStructuralOps checks add/rename/delete each set exactly one structural
// op, and that a rename carries the editor's pending edits keyed to the new
// name.
func TestStructuralOps(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		req, ok := buildElevateRequest(applyRequest{action: actionAddProfile, op: &profileOp{kind: opAdd, name: "new"}})
		if !ok {
			t.Fatal("build returned !ok")
		}
		if ptrOr(req.Patch.AddProfile) != "new" {
			t.Errorf("AddProfile = %v", req.Patch.AddProfile)
		}
		assertNoOtherStructural(t, req.Patch, "add")
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("rename without pending edits", func(t *testing.T) {
		req, _ := buildElevateRequest(applyRequest{action: actionRenameProfile, op: &profileOp{kind: opRename, name: "a", to: "b"}})
		if req.Patch.RenameProfile == nil || req.Patch.RenameProfile.From != "a" || req.Patch.RenameProfile.To != "b" {
			t.Fatalf("RenameProfile = %v", req.Patch.RenameProfile)
		}
		assertNoOtherStructural(t, req.Patch, "rename")
		if len(req.Patch.Profiles) != 0 {
			t.Errorf("expected no profile patch without edits, got %v", req.Patch.Profiles)
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("rename with pending edits keys the patch to the new name", func(t *testing.T) {
		d := profileDraft{name: "a", provider: "quad9", variant: "noblock", protocol: "doh"}
		req, _ := buildElevateRequest(applyRequest{action: actionRenameProfile, op: &profileOp{kind: opRename, name: "a", to: "b"}, draft: &d})
		if req.Patch.RenameProfile == nil || req.Patch.RenameProfile.To != "b" {
			t.Fatalf("RenameProfile = %v", req.Patch.RenameProfile)
		}
		if _, ok := req.Patch.Profiles["b"]; !ok {
			t.Fatalf("expected patch keyed to b, got %v", req.Patch.Profiles)
		}
		assertNoOtherStructural(t, req.Patch, "rename")
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("delete", func(t *testing.T) {
		req, _ := buildElevateRequest(applyRequest{action: actionDeleteProfile, op: &profileOp{kind: opDelete, name: "gone"}})
		if ptrOr(req.Patch.DeleteProfile) != "gone" {
			t.Errorf("DeleteProfile = %v", req.Patch.DeleteProfile)
		}
		assertNoOtherStructural(t, req.Patch, "delete")
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
}

// TestActiveProfileOnlyOnSwitch checks ActiveProfile is set only for the
// set-active action, never on a plain save.
func TestActiveProfileOnlyOnSwitch(t *testing.T) {
	save := saveRequest(t, profileDraft{name: "work", provider: "quad9", variant: "secure", protocol: "doh"})
	if save.Patch.ActiveProfile != nil {
		t.Fatalf("save must not set ActiveProfile, got %q", *save.Patch.ActiveProfile)
	}

	req, ok := buildElevateRequest(applyRequest{action: actionSetActive, activeSwitch: new("work")})
	if !ok {
		t.Fatal("build returned !ok")
	}
	if req.Patch.ActiveProfile == nil || *req.Patch.ActiveProfile != "work" {
		t.Fatalf("ActiveProfile = %v", req.Patch.ActiveProfile)
	}
	if len(req.Patch.Profiles) != 0 {
		t.Errorf("set-active must not carry a profile patch")
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// saveRequest builds a save request for d and asserts it validates.
func saveRequest(t *testing.T, d profileDraft) elevate.Request {
	t.Helper()

	req, ok := buildElevateRequest(applyRequest{action: actionSaveProfile, draft: &d})
	if !ok {
		t.Fatal("buildElevateRequest returned !ok")
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	return req
}

// assertNoPatch checks a patch carries nothing at all.
func assertNoPatch(t *testing.T, p elevate.Patch) {
	t.Helper()

	if p.ActiveProfile != nil || len(p.Profiles) != 0 ||
		p.AddProfile != nil || p.RenameProfile != nil || p.DeleteProfile != nil {
		t.Fatalf("expected empty patch, got %+v", p)
	}
}

// assertNoOtherStructural checks only keep string is set among the structural
// fields.
func assertNoOtherStructural(t *testing.T, p elevate.Patch, keep string) {
	t.Helper()

	if keep != "add" && p.AddProfile != nil {
		t.Errorf("AddProfile unexpectedly set")
	}
	if keep != "rename" && p.RenameProfile != nil {
		t.Errorf("RenameProfile unexpectedly set")
	}
	if keep != "delete" && p.DeleteProfile != nil {
		t.Errorf("DeleteProfile unexpectedly set")
	}
}

// ptrOr dereferences an optional string, or "" when nil.
func ptrOr(s *string) (v string) {
	if s == nil {
		return ""
	}

	return *s
}

// ptrSlice dereferences an optional string slice, or nil when nil.
func ptrSlice(s *[]string) (v []string) {
	if s == nil {
		return nil
	}

	return *s
}
