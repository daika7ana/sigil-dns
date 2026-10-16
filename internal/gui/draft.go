//go:build gui

package gui

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/providers"
)

// profileDraft is the GUI-local editing buffer for one profile.  It is
// deliberately decoupled from internal/config so the editor, the request
// builder, and their tests compile and run without the backend.
//
// A blank name/id/url means "keep" — the editor never reads the existing
// resolver id, and only a typed value travels onward (SPEC §15.2).
type profileDraft struct {
	name        string
	provider    string
	variant     string
	protocol    string
	id          string // typed new resolver id; blank = keep existing
	url         string // custom provider only; blank = keep
	bootstrap   string // comma-separated IP literals
	filtering   bool
	blockResp   string // "0.0.0.0" or "nxdomain"
	cache       bool
	cacheSizeMB string // MiB, as typed

	// lists are the editable filter-list rows in config order.  A seeded row
	// carries an existing list's redacted host and its original config index;
	// a row with idx < 0 is one the user typed and carries the full URL that
	// travels for it.  seededLists is the immutable seed the list diff is
	// computed against (redacted hosts only — never a URL).
	lists       []listDraft
	seededLists []ipc.ListSummary

	// perDomain holds the editable per-domain routing rules.  domains is the
	// comma-separated input; upstream starts blank = keep (a stored upstream is
	// never available, so it is never seeded).  rulesKnown reports whether the
	// seed carried the full rule set: a legacy summary without it must not be
	// saved as if it had no rules, or an unrelated save would clear them.
	perDomain  []ruleDraft
	rulesKnown bool
}

// listDraft is one editable filter-list row.
type listDraft struct {
	host    string // redacted host of an existing list (display only)
	url     string // full URL of a newly added list (idx < 0)
	enabled bool
	idx     int // original config index of an existing list; -1 for a new one
}

// ruleDraft is one editable per-domain routing rule.  A rule is one of two
// kinds: upstream (routed to the typed target) or bypass (resolved by the
// system resolver, no upstream).
type ruleDraft struct {
	domains  string // comma-separated domains, as typed
	bypass   bool   // bypass kind: resolved by the system resolver, no upstream
	upstream string // upstream kind only; blank = keep the existing target
	enabled  bool   // the rule's active state (a disabled rule is skipped)
	idx      int    // original config index of an existing rule; -1 for a new one
}

// draftForProfile maps one sanitized ProfileSummary into the editor buffer.
// The resolver id and upstream URL are never read: both start blank, which
// means "keep" (SPEC §15.2).
func draftForProfile(p ipc.ProfileSummary) profileDraft {
	seeded := append([]ipc.ListSummary(nil), p.Lists...)

	return profileDraft{
		name:        p.Name,
		provider:    p.Provider,
		variant:     p.Variant,
		protocol:    p.Protocol,
		id:          "",
		url:         "",
		bootstrap:   strings.Join(p.Bootstrap, ", "),
		filtering:   p.FilteringEnabled,
		blockResp:   p.BlockResponse,
		cache:       p.CacheEnabled,
		cacheSizeMB: miBString(p.CacheSizeBytes),
		lists:       listDrafts(seeded),
		seededLists: seeded,
		perDomain:   ruleDrafts(p.PerDomain),
		rulesKnown:  true,
	}
}

// draftFromActiveProfile maps the legacy top-level (active-profile) fields when
// a summary predates the per-profile list.  That summary carries list hosts but
// no per-domain details, so the rules stay unknown (never saved as "none").
func draftFromActiveProfile(sum *ipc.ConfigSummary) profileDraft {
	seeded := append([]ipc.ListSummary(nil), sum.Lists...)

	return profileDraft{
		name:        sum.Profile,
		provider:    sum.Provider,
		variant:     sum.Variant,
		protocol:    sum.Protocol,
		id:          "",
		url:         "",
		bootstrap:   strings.Join(sum.Bootstrap, ", "),
		filtering:   sum.FilteringEnabled,
		blockResp:   sum.BlockResponse,
		cache:       sum.CacheEnabled,
		cacheSizeMB: miBString(sum.CacheSizeBytes),
		lists:       listDrafts(seeded),
		seededLists: seeded,
		rulesKnown:  false,
	}
}

// listDrafts maps seeded list summaries into editable rows, preserving config
// order so an existing list is addressed by its original index.
func listDrafts(seeded []ipc.ListSummary) (out []listDraft) {
	for i, l := range seeded {
		out = append(out, listDraft{host: l.Host, enabled: l.Enabled, idx: i})
	}

	return out
}

// ruleDrafts maps per-domain summaries into editable rows.  The upstream is
// never seeded: the editor never reads a stored target, and a blank input means
// "keep" (SPEC §15.2).  Only the bypass flag is seeded, so a rule's kind is
// carried into the editor (and into draftDirty's comparison).  Each row keeps
// its original index so a kept rule's target is inherited by that index, not by
// its position in the edited list.
func ruleDrafts(rules []ipc.PerDomainSummary) (out []ruleDraft) {
	for i, r := range rules {
		out = append(out, ruleDraft{
			domains: strings.Join(r.Domains, ", "),
			bypass:  r.Bypass,
			enabled: r.Enabled,
			idx:     i,
		})
	}

	return out
}

// draftFromSummary seeds the editor for the named profile from a sanitized
// summary (the live one or the cached last-good one).  It prefers the
// per-profile list, falling back to the top-level active fields when the list
// is absent (backwards compatibility).  The resolver id is never read.
func draftFromSummary(sum *ipc.ConfigSummary, name string) (d profileDraft) {
	if sum == nil {
		return profileDraft{name: name}
	}

	if p, ok := findProfile(sum.Profiles, name); ok {
		return draftForProfile(p)
	}
	if len(sum.Profiles) == 0 {
		return draftFromActiveProfile(sum)
	}

	return profileDraft{name: name}
}

// defaultDraft seeds the editor on a brand-new install, where neither the live
// service nor a cached summary exists, so the editor is usable immediately and
// Save can create the config.  It mirrors config.Default(): the "default"
// profile on ControlD's free p0 endpoint (resolver.DefaultUpstream), cache on
// at the default size.  The resolver id is blank — blank means "keep", and we
// never invent one.  The rule set is empty and known (a new profile has none).
func defaultDraft() profileDraft {
	return profileDraft{
		name:        config.DefaultProfileName,
		provider:    providers.ControlD,
		variant:     "p0",
		protocol:    string(providers.DoH),
		id:          "",
		url:         "",
		bootstrap:   "",
		filtering:   false,
		blockResp:   "",
		cache:       true,
		cacheSizeMB: strconv.Itoa(config.DefaultCacheSizeBytes >> 20),
		rulesKnown:  true,
	}
}

// findProfile finds a profile by name in the sanitized list.
func findProfile(profiles []ipc.ProfileSummary, name string) (p ipc.ProfileSummary, ok bool) {
	for _, ps := range profiles {
		if ps.Name == name {
			return ps, true
		}
	}

	return ipc.ProfileSummary{}, false
}

// activeProfileName is the active profile per the summary: the marked profile
// when the list is present, else the top-level name.
func activeProfileName(sum *ipc.ConfigSummary) (name string) {
	for _, p := range sum.Profiles {
		if p.Active {
			return p.Name
		}
	}

	return sum.Profile
}

// profileNames lists the sanitized profile names in summary order (sorted).
func profileNames(profiles []ipc.ProfileSummary) (names []string) {
	for _, p := range profiles {
		names = append(names, p.Name)
	}

	return names
}

// selectedSummary is the summary of the profile the editor is focused on.
func (a *App) selectedSummary() (p ipc.ProfileSummary, ok bool) {
	return findProfile(a.summaries, selectedProfile(a))
}

// draftDirty reports whether the editor buffer differs from the selected
// profile's sanitized values — i.e. whether there are unsaved edits to carry
// alongside a rename.  The draft carries slices, so a plain == cannot be used.
func (a *App) draftDirty() (dirty bool) {
	p, ok := a.selectedSummary()
	if !ok {
		return false
	}

	return !reflect.DeepEqual(a.draft, draftForProfile(p))
}

// parseBootstrap splits a comma-separated list, trimming whitespace and
// dropping empty entries.  A blank input yields nil, never [""].
func parseBootstrap(s string) (out []string) {
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}

	return out
}

// syncBootstrap refreshes the editor's bootstrap list from the expansion of the
// current provider/variant/protocol selection.  Switching provider, variant,
// protocol, or typing a resolver id recomputes it, so the box never keeps the
// previous profile's addresses (the ControlD default used to stick there for
// every selection).  A selection that cannot be expanded — an id-requiring
// provider with no id yet, or the custom provider, whose bootstrap the user
// enters by hand — leaves the current value untouched.
func syncBootstrap(d *profileDraft) {
	provider := strings.TrimSpace(d.provider)
	if provider == "" || provider == providers.Custom {
		return
	}

	ep, err := providers.Expand(providers.Selection{
		Provider: provider,
		Variant:  strings.TrimSpace(d.variant),
		Protocol: providers.Protocol(strings.TrimSpace(d.protocol)),
		ID:       strings.TrimSpace(d.id),
	})
	if err != nil {
		return
	}

	d.bootstrap = strings.Join(ep.Bootstrap, ", ")
}

// parseMiB reads a cache size in MiB.  A blank or invalid value is 0, which
// the appliers read as "leave the size alone".
func parseMiB(s string) (n int) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0
	}

	return n
}

// miBString renders a byte count as whole MiB, or "" when unset.
func miBString(bytes int) (s string) {
	if bytes <= 0 {
		return ""
	}

	return strconv.Itoa(bytes / (1 << 20))
}

// deleteDisabled reports whether the delete button must be inert for name:
// the active profile is never deletable, and the last profile is never
// deletable.
func deleteDisabled(names []string, active, name string) (disabled bool) {
	return name == "" || name == active || len(names) <= 1
}

// canEdit reports whether profile edits are allowed in the current trust
// state.  Writes are refused while the service identity is unverified
// (SPEC §15.3.12).
func canEdit(s snapshot) bool {
	return s.trust != trustUnverified
}

// needsResolverID reports whether the provider/variant pair takes a resolver id
// (the value typed into the Resolver ID field).  It is the single predicate the
// editor (disable the field) and the request builder (omit the id) share, so
// the two can never disagree.
//
// It fails open: an unknown provider or variant returns true, so a selection we
// cannot classify never blocks the user from typing an id.  The custom provider
// is the documented exception — its endpoint comes from the URL, so it never
// needs an id even when its variant name is unknown.
func needsResolverID(provider, variant string) (needs bool) {
	provider = strings.TrimSpace(provider)
	if provider == providers.Custom {
		return false
	}

	p, ok := providers.Lookup(provider)
	if !ok {
		return true
	}

	variant = strings.TrimSpace(variant)
	for _, v := range p.Variants {
		if v.Name == variant {
			return v.NeedsID
		}
	}

	return true
}
