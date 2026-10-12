//go:build gui

package gui

import (
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
}

// draftForProfile maps one sanitized ProfileSummary into the editor buffer.
// The resolver id and upstream URL are never read: both start blank, which
// means "keep" (SPEC §15.2).
func draftForProfile(p ipc.ProfileSummary) profileDraft {
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
	}
}

// draftFromActiveProfile maps the legacy top-level (active-profile) fields when
// a summary predates the per-profile list.
func draftFromActiveProfile(sum *ipc.ConfigSummary) profileDraft {
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
	}
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
// never invent one.
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
// alongside a rename.
func (a *App) draftDirty() (dirty bool) {
	p, ok := a.selectedSummary()
	if !ok {
		return false
	}

	return a.draft != draftForProfile(p)
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
