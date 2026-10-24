package elevate

import (
	"reflect"
	"testing"

	"github.com/daika7ana/sigil-dns/internal/config"
)

// TestApplyListPatch covers the allowlisted list operations and index bounds.
func TestApplyListPatch(t *testing.T) {
	base := func() []config.FilterList {
		return []config.FilterList{
			{URL: "https://a.example/list", Enabled: true},
			{URL: "https://b.example/list", Enabled: true},
			{URL: "https://c.example/list", Enabled: false},
		}
	}

	cases := []struct {
		name    string
		patch   ListPatch
		want    []config.FilterList
		wantErr bool
	}{
		{
			name:  "toggle",
			patch: ListPatch{SetEnabled: map[int]bool{0: false, 2: true}},
			want: []config.FilterList{
				{URL: "https://a.example/list", Enabled: false},
				{URL: "https://b.example/list", Enabled: true},
				{URL: "https://c.example/list", Enabled: true},
			},
		},
		{
			name:  "add trims and appends",
			patch: ListPatch{Add: []ListEntry{{URL: "  https://d.example/list  ", Enabled: true}}},
			want:  append(base(), config.FilterList{URL: "https://d.example/list", Enabled: true}),
		},
		{
			name:  "remove by descending index",
			patch: ListPatch{Remove: []int{0, 2}},
			want:  []config.FilterList{{URL: "https://b.example/list", Enabled: true}},
		},
		{
			name:    "out-of-range set rejected",
			patch:   ListPatch{SetEnabled: map[int]bool{5: true}},
			wantErr: true,
		},
		{
			name:    "out-of-range remove rejected",
			patch:   ListPatch{Remove: []int{3}},
			wantErr: true,
		},
		{
			name:    "duplicate remove rejected before mutation",
			patch:   ListPatch{SetEnabled: map[int]bool{1: false}, Remove: []int{0, 0}},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lists := base()
			err := applyListPatch(&lists, tc.patch)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected a validation error")
				}
				if !reflect.DeepEqual(lists, base()) {
					t.Fatalf("validation error mutated lists: %v", lists)
				}

				return
			}
			if err != nil {
				t.Fatalf("applyListPatch: %v", err)
			}
			if !reflect.DeepEqual(lists, tc.want) {
				t.Fatalf("lists = %v, want %v", lists, tc.want)
			}
		})
	}
}

// TestApplyPerDomainPatch covers replace-only target inheritance by explicit
// source index (Keep).
func TestApplyPerDomainPatch(t *testing.T) {
	old := []config.PerDomain{
		{Domains: []string{"a.example"}, Upstream: "tls://keep.example:853"},
	}

	cases := []struct {
		name    string
		pd      []config.PerDomain
		patches []PerDomainPatch
		want    []config.PerDomain
		wantErr bool
	}{
		{
			name:    "full replace",
			pd:      old,
			patches: []PerDomainPatch{{Domains: []string{"x.example"}, Upstream: "tls://new.example:853"}},
			want:    []config.PerDomain{{Domains: []string{"x.example"}, Upstream: "tls://new.example:853"}},
		},
		{
			name:    "blank upstream keeps existing target by keep index",
			pd:      old,
			patches: []PerDomainPatch{{Domains: []string{"a2.example"}, Keep: new(0)}},
			want:    []config.PerDomain{{Domains: []string{"a2.example"}, Upstream: "tls://keep.example:853"}},
		},
		{
			name: "keep index is not positional",
			pd: []config.PerDomain{
				{Domains: []string{"a.example"}, Upstream: "tls://first.example:853"},
				{Domains: []string{"b.example"}, Upstream: "tls://second.example:853"},
			},
			patches: []PerDomainPatch{{Domains: []string{"b2.example"}, Keep: new(1)}},
			want:    []config.PerDomain{{Domains: []string{"b2.example"}, Upstream: "tls://second.example:853"}},
		},
		{
			name:    "new rule needs an upstream",
			pd:      nil,
			patches: []PerDomainPatch{{Domains: []string{"new.example"}}},
			wantErr: true,
		},
		{
			name:    "cannot inherit a blank existing target",
			pd:      []config.PerDomain{{Domains: []string{"a.example"}}},
			patches: []PerDomainPatch{{Domains: []string{"a.example"}, Keep: new(0)}},
			wantErr: true,
		},
		{
			name:    "keep index out of range",
			pd:      old,
			patches: []PerDomainPatch{{Domains: []string{"a.example"}, Keep: new(3)}},
			wantErr: true,
		},
		{
			name:    "bypass rule needs no upstream or keep",
			pd:      old,
			patches: []PerDomainPatch{{Domains: []string{"local.example"}, Bypass: true}},
			want:    []config.PerDomain{{Domains: []string{"local.example"}, Bypass: true}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &config.Profile{PerDomain: tc.pd}
			err := applyPerDomainPatch(p, tc.patches)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected a static error")
				}

				return
			}
			if err != nil {
				t.Fatalf("applyPerDomainPatch: %v", err)
			}
			if !reflect.DeepEqual(p.PerDomain, tc.want) {
				t.Fatalf("per-domain = %v, want %v", p.PerDomain, tc.want)
			}
		})
	}
}

// TestApplyPerDomainPatchEnabled proves a rule's enabled state travels through
// the patch, and an absent flag (nil) leaves the rule enabled.
func TestApplyPerDomainPatchEnabled(t *testing.T) {
	p := &config.Profile{}
	err := applyPerDomainPatch(p, []PerDomainPatch{
		{Domains: []string{"off.example"}, Upstream: "tls://a.example:853", Enabled: new(false)},
		{Domains: []string{"on.example"}, Upstream: "tls://b.example:853"},
	})
	if err != nil {
		t.Fatalf("applyPerDomainPatch: %v", err)
	}
	if len(p.PerDomain) != 2 {
		t.Fatalf("rules = %d, want 2", len(p.PerDomain))
	}
	if p.PerDomain[0].IsEnabled() {
		t.Errorf("rule 0 IsEnabled = true, want false")
	}
	if !p.PerDomain[1].IsEnabled() {
		t.Errorf("rule 1 IsEnabled = false, want true (nil enabled)")
	}
}

// TestValidatePerDomainBypass proves a bypass patch must not also set an
// upstream or a keep index.
func TestValidatePerDomainBypass(t *testing.T) {
	cases := []struct {
		name    string
		patch   PerDomainPatch
		wantErr bool
	}{
		{"bypass ok", PerDomainPatch{Domains: []string{"local.example"}, Bypass: true}, false},
		{"bypass with upstream rejected", PerDomainPatch{
			Domains: []string{"local.example"}, Bypass: true, Upstream: "tls://10.0.0.1:853",
		}, true},
		{"bypass with keep rejected", PerDomainPatch{Domains: []string{"local.example"}, Bypass: true, Keep: new(0)}, true},
		{"bypass without domains rejected", PerDomainPatch{Bypass: true}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePerDomainPatch([]PerDomainPatch{tc.patch})
			if tc.wantErr && err == nil {
				t.Fatal("expected a validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validatePerDomainPatch: %v", err)
			}
		})
	}
}
