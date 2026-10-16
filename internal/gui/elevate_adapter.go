//go:build gui

package gui

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/elevate"
	"github.com/daika7ana/dns-client/internal/providers"
)

// elevateApply is the production applyFunc.  It is the only place that imports
// internal/elevate, so the rest of the GUI stays testable without it and
// without a Windows host.
//
// One call is one explicit user action, gated behind the elevate one-shot
// helper: a single UAC prompt, no persistent broker (SPEC §15.1/§15.3).
func elevateApply(ctx context.Context, req applyRequest) (applyResult, error) {
	er, ok := buildElevateRequest(req)
	if !ok {
		return applyResult{}, errors.New("unknown action")
	}

	res, err := elevate.Apply(ctx, er)
	if err != nil {
		switch {
		case errors.Is(err, elevate.ErrUnsupported):
			return applyResult{}, errUnsupported
		case errors.Is(err, elevate.ErrRevision):
			return applyResult{}, errRevision
		}

		return applyResult{}, err
	}

	return applyResult{ok: res.OK, message: res.Message, errText: res.Err}, nil
}

// buildElevateRequest maps a GUI write onto the staged elevate request.  It is
// pure (no I/O) and is the single place that knows how a draft becomes a Patch,
// so the mapping — id nil when blank, URL only for custom, one structural op —
// is unit-testable.
func buildElevateRequest(req applyRequest) (er elevate.Request, ok bool) {
	action, ok := elevateActionFor(req.action)
	if !ok {
		return elevate.Request{}, false
	}

	er = elevate.Request{
		Action:           action,
		ExpectedRevision: req.expectedRevision,
		UUID:             rand.Text(),
	}

	switch req.action {
	case actionSaveProfile:
		er.Patch.Profiles = patchProfiles(draftName(req.draft), req.draft)

	case actionAddProfile:
		if req.op != nil {
			er.Patch.AddProfile = new(req.op.name)
		}

	case actionRenameProfile:
		if req.op != nil {
			er.Patch.RenameProfile = &elevate.RenameProfile{
				From: req.op.name,
				To:   req.op.to,
			}
			// The editor's pending edits belong to the renamed profile, so
			// they are keyed to its new name (the helper renames first).
			er.Patch.Profiles = patchProfiles(req.op.to, req.draft)
		}

	case actionDeleteProfile:
		if req.op != nil {
			er.Patch.DeleteProfile = new(req.op.name)
		}

	case actionSetActive:
		// Covered by the shared activeSwitch handling below.

	case actionSaveLog:
		// The level is always sent as chosen (an empty one falls back to
		// off).  The path is always sent too, including an explicit empty
		// string: a blank field means "use the default location", which the
		// helper's apply expresses by clearing cfg.Log.Path (validate accepts an
		// empty value).  A log-only patch never triggers a service reload
		// (LOGGING_PLAN §1.6).
		if req.log != nil {
			level := strings.TrimSpace(req.log.level)
			if level == "" {
				level = config.LogLevelOff
			}
			er.Patch.LogLevel = new(level)
			er.Patch.LogPath = new(strings.TrimSpace(req.log.path))
		}
	}

	if req.activeSwitch != nil {
		er.Patch.ActiveProfile = new(*req.activeSwitch)
	}

	return er, true
}

// elevateActionFor maps a GUI action onto an elevate.Action.
func elevateActionFor(a guiAction) (elevate.Action, bool) {
	switch a {
	case actionEnable:
		return elevate.ActionEnable, true
	case actionRestore:
		return elevate.ActionRestore, true
	case actionInstall:
		return elevate.ActionServiceInstall, true
	case actionUninstall:
		return elevate.ActionServiceUninstall, true
	case actionServiceStop:
		return elevate.ActionServiceStop, true
	case actionRefreshLists:
		return elevate.ActionRefreshLists, true
	case actionSaveProfile, actionAddProfile, actionRenameProfile, actionDeleteProfile, actionSetActive, actionSaveLog:
		return elevate.ActionApply, true
	default:
		return "", false
	}
}

// patchProfiles wraps a single-profile patch under name, or nil when there is
// no draft or no name (an empty map is omitted from the request).
func patchProfiles(name string, d *profileDraft) map[string]elevate.ProfilePatch {
	if d == nil || strings.TrimSpace(name) == "" {
		return nil
	}

	return map[string]elevate.ProfilePatch{name: patchFromDraft(*d)}
}

// draftName is the profile name a draft targets, or "".
func draftName(d *profileDraft) (name string) {
	if d == nil {
		return ""
	}

	return strings.TrimSpace(d.name)
}

// patchFromDraft builds the allowlisted per-profile patch from the editor
// buffer.  Provider/Variant/Protocol are always set (the helper re-expands the
// endpoints); ID is set only when typed (nil = keep, SPEC §15.2); URL only for
// the custom provider and only when filled; Bootstrap and the filtering/cache
// blocks carry the draft's values.
func patchFromDraft(d profileDraft) elevate.ProfilePatch {
	provider := strings.TrimSpace(d.provider)
	pp := elevate.ProfilePatch{
		Provider: new(provider),
		Variant:  new(strings.TrimSpace(d.variant)),
		Protocol: new(strings.TrimSpace(d.protocol)),
	}

	// The id is sent only for a provider/variant that uses one, so a value
	// typed before switching to a no-id provider can never travel.  A blank
	// field still means "keep" (SPEC §15.2).
	if needsResolverID(provider, d.variant) {
		if id := strings.TrimSpace(d.id); id != "" {
			pp.ID = new(id)
		}
	}

	// URL applies to the custom provider only, and only when the user filled
	// it in (blank = keep the existing upstream).
	if provider == providers.Custom {
		if u := strings.TrimSpace(d.url); u != "" {
			pp.URL = new(u)
		}
	}

	if bs := parseBootstrap(d.bootstrap); len(bs) > 0 {
		pp.Bootstrap = &bs
	}

	pp.Filtering = &elevate.FilteringPatch{Enabled: new(d.filtering)}
	if d.filtering {
		if br := strings.TrimSpace(d.blockResp); br != "" {
			pp.Filtering.BlockResponse = new(br)
		}
	}
	if lp := listPatch(d); lp != nil {
		pp.Filtering.Lists = lp
	}

	// The full per-domain rule set replaces the profile's rules, so a deleted
	// rule actually disappears.  Only sent when the seed carried the rule set:
	// a legacy summary without it must never be saved as "no rules".
	if d.rulesKnown {
		pp.PerDomain = perDomainPatch(d.perDomain)
	}

	pp.Cache = &elevate.CachePatch{Enabled: new(d.cache)}
	if mb := parseMiB(d.cacheSizeMB); mb > 0 {
		size := mb * (1 << 20)
		pp.Cache.SizeBytes = &size
	}

	return pp
}

// listPatch builds the filter-list diff between the seeded lists and the editor
// rows.  Existing lists are addressed by their original index — SetEnabled for
// a changed toggle, Remove for a deleted row — so their (possibly
// credential-bearing) URLs never travel; only a newly typed row carries a URL
// (Add).  A nil result means no list changed.
func listPatch(d profileDraft) *elevate.ListPatch {
	lp := elevate.ListPatch{}
	for i, seeded := range d.seededLists {
		cur, ok := listAt(d.lists, i)
		if !ok {
			lp.Remove = append(lp.Remove, i)

			continue
		}
		if cur.enabled != seeded.Enabled {
			if lp.SetEnabled == nil {
				lp.SetEnabled = map[int]bool{}
			}
			lp.SetEnabled[i] = cur.enabled
		}
	}

	for _, l := range d.lists {
		if l.idx < 0 {
			if u := strings.TrimSpace(l.url); u != "" {
				lp.Add = append(lp.Add, elevate.ListEntry{URL: u, Enabled: l.enabled})
			}
		}
	}

	if len(lp.SetEnabled) == 0 && len(lp.Remove) == 0 && len(lp.Add) == 0 {
		return nil
	}

	return &lp
}

// listAt finds the current editor row for an existing list index.
func listAt(lists []listDraft, idx int) (l listDraft, ok bool) {
	for _, l := range lists {
		if l.idx == idx {
			return l, true
		}
	}

	return listDraft{}, false
}

// perDomainPatch builds the full per-domain rule array from the editor rows.
// Domains are always sent; a blank upstream names the existing rule to inherit
// from by its original index (Keep), so deleting another row cannot shift the
// source (replace-only, SPEC §15.2).  A row with no domains is dropped.  The
// result is always non-nil so a removed rule is actually cleared.
func perDomainPatch(rules []ruleDraft) *[]elevate.PerDomainPatch {
	out := make([]elevate.PerDomainPatch, 0, len(rules))
	for _, r := range rules {
		domains := parseBootstrap(r.domains)
		if len(domains) == 0 {
			continue
		}

		p := elevate.PerDomainPatch{
			Domains:  domains,
			Upstream: strings.TrimSpace(r.upstream),
			Enabled:  new(r.enabled),
		}
		if p.Upstream == "" && r.idx >= 0 {
			p.Keep = new(r.idx)
		}

		out = append(out, p)
	}

	return &out
}
