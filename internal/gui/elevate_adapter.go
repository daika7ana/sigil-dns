//go:build gui

package gui

import (
	"context"
	"errors"
	"strings"

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
		UUID:             newUUID(),
	}

	switch req.action {
	case actionSaveProfile:
		er.Patch.Profiles = patchProfiles(draftName(req.draft), req.draft)

	case actionAddProfile:
		if req.op != nil {
			er.Patch.AddProfile = strptr(req.op.name)
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
			er.Patch.DeleteProfile = strptr(req.op.name)
		}

	case actionSetActive:
		// Covered by the shared activeSwitch handling below.
	}

	if req.activeSwitch != nil {
		er.Patch.ActiveProfile = strptr(*req.activeSwitch)
	}

	return er, true
}

// elevateActionFor maps a GUI action onto an elevate.Action.
func elevateActionFor(a guiAction) (elevate.Action, bool) {
	switch a {
	case actionEnable:
		return elevate.ActionEnable, true
	case actionDisable:
		return elevate.ActionDisable, true
	case actionRestore:
		return elevate.ActionRestore, true
	case actionInstall:
		return elevate.ActionServiceInstall, true
	case actionUninstall:
		return elevate.ActionServiceUninstall, true
	case actionStart:
		return elevate.ActionServiceStart, true
	case actionStop:
		return elevate.ActionServiceStop, true
	case actionSaveProfile, actionAddProfile, actionRenameProfile, actionDeleteProfile, actionSetActive:
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
		Provider: strptr(provider),
		Variant:  strptr(strings.TrimSpace(d.variant)),
		Protocol: strptr(strings.TrimSpace(d.protocol)),
	}

	// The id is sent only for a provider/variant that uses one, so a value
	// typed before switching to a no-id provider can never travel.  A blank
	// field still means "keep" (SPEC §15.2).
	if needsResolverID(provider, d.variant) {
		if id := strings.TrimSpace(d.id); id != "" {
			pp.ID = strptr(id)
		}
	}

	// URL applies to the custom provider only, and only when the user filled
	// it in (blank = keep the existing upstream).
	if provider == providers.Custom {
		if u := strings.TrimSpace(d.url); u != "" {
			pp.URL = strptr(u)
		}
	}

	if bs := parseBootstrap(d.bootstrap); len(bs) > 0 {
		pp.Bootstrap = &bs
	}

	pp.Filtering = &elevate.FilteringPatch{Enabled: boolptr(d.filtering)}
	if d.filtering {
		if br := strings.TrimSpace(d.blockResp); br != "" {
			pp.Filtering.BlockResponse = strptr(br)
		}
	}

	pp.Cache = &elevate.CachePatch{Enabled: boolptr(d.cache)}
	if mb := parseMiB(d.cacheSizeMB); mb > 0 {
		size := mb * (1 << 20)
		pp.Cache.SizeBytes = &size
	}

	return pp
}

// strptr returns a pointer to a copy of s.
func strptr(s string) *string { return &s }

// boolptr returns a pointer to a copy of b.
func boolptr(b bool) *bool { return &b }
