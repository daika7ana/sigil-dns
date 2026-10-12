//go:build gui

package gui

import (
	"testing"

	"github.com/daika7ana/dns-client/internal/elevate"
)

// TestElevateActionFor covers the full GUI-action -> elevate.Action mapping.
func TestElevateActionFor(t *testing.T) {
	cases := map[guiAction]elevate.Action{
		actionEnable:        elevate.ActionEnable,
		actionDisable:       elevate.ActionDisable,
		actionRestore:       elevate.ActionRestore,
		actionInstall:       elevate.ActionServiceInstall,
		actionUninstall:     elevate.ActionServiceUninstall,
		actionStart:         elevate.ActionServiceStart,
		actionStop:          elevate.ActionServiceStop,
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

// TestServiceRequestHasEmptyPatch checks the four service actions map to the
// four elevate actions with no config patch and a valid request.
func TestServiceRequestHasEmptyPatch(t *testing.T) {
	cases := map[guiAction]elevate.Action{
		actionInstall:   elevate.ActionServiceInstall,
		actionUninstall: elevate.ActionServiceUninstall,
		actionStart:     elevate.ActionServiceStart,
		actionStop:      elevate.ActionServiceStop,
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

	req, ok := buildElevateRequest(applyRequest{action: actionSetActive, activeSwitch: strptr("work")})
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

	if p.Mode != nil || p.ActiveProfile != nil || len(p.Profiles) != 0 ||
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
