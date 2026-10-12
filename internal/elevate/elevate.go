// Package elevate implements the one-shot elevated helper (SPEC §15.1/§15.2).
//
// The unelevated UI cannot change the config or the service mode directly: the
// config file is ACL'd to SYSTEM/Administrators.  Instead it drops a
// DPAPI-encrypted request into a per-user staging directory, launches this same
// binary elevated (UAC) with --apply <path>, and reads the verdict back from a
// sibling .res file.  The helper re-reads and re-validates everything itself and
// applies only an allowlisted patch: the request body is never trusted.
//
// The frozen API is:
//
//	Apply(ctx, req) (Result, error)  // unelevated: stage, elevate, collect
//	HelperMain(path) error           // elevated: verify, apply, write result
package elevate

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/providers"
)

// Action is the requested one-shot operation.
type Action string

// The allowlisted actions.  Anything else is rejected.
const (
	// ActionEnable persists mode=on, reloads, then enables interception.
	ActionEnable Action = "enable"

	// ActionDisable persists mode=off, then disables interception.
	ActionDisable Action = "disable"

	// ActionRestore restores interception from persisted state.  It does not
	// touch the config and works even when the config is broken.
	ActionRestore Action = "restore"

	// ActionApply persists a config patch, reloading only if the config changed.
	ActionApply Action = "apply"

	// ActionServiceInstall registers the Windows service.
	ActionServiceInstall Action = "service_install"

	// ActionServiceUninstall restores interception, stops, then removes the
	// Windows service.
	ActionServiceUninstall Action = "service_uninstall"

	// ActionServiceStart starts the Windows service.
	ActionServiceStart Action = "service_start"

	// ActionServiceStop stops the Windows service.
	ActionServiceStop Action = "service_stop"
)

// valid reports whether a is a known action.
func (a Action) valid() (ok bool) {
	switch a {
	case ActionEnable, ActionDisable, ActionRestore, ActionApply,
		ActionServiceInstall, ActionServiceUninstall, ActionServiceStart, ActionServiceStop:
		return true
	default:
		return false
	}
}

// serviceAction reports whether a is one of the service lifecycle actions,
// which take no config patch.
func (a Action) serviceAction() (ok bool) {
	switch a {
	case ActionServiceInstall, ActionServiceUninstall, ActionServiceStart, ActionServiceStop:
		return true
	default:
		return false
	}
}

// Patch is the allowlisted set of config fields a request may change.  Only the
// fields below are writable; every other field is ignored even if present in the
// decoded request, so the helper can never be walked into writing arbitrary
// config.
type Patch struct {
	Mode          *string                 `json:"mode,omitempty"`
	ActiveProfile *string                 `json:"active_profile,omitempty"`
	Profiles      map[string]ProfilePatch `json:"profiles,omitempty"`
	AddProfile    *string                 `json:"add_profile,omitempty"`
	RenameProfile *RenameProfile          `json:"rename_profile,omitempty"`
	DeleteProfile *string                 `json:"delete_profile,omitempty"`
}

// RenameProfile names the profile rename pair.
type RenameProfile struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ProfilePatch is the allowlisted per-profile patch.  A nil field is left
// unchanged.  ID is replace-only: an explicitly empty id preserves the existing
// one (see applyRequest).
type ProfilePatch struct {
	Provider  *string         `json:"provider,omitempty"`
	Variant   *string         `json:"variant,omitempty"`
	Protocol  *string         `json:"protocol,omitempty"`
	ID        *string         `json:"id,omitempty"`
	URL       *string         `json:"url,omitempty"`
	Bootstrap *[]string       `json:"bootstrap,omitempty"`
	Filtering *FilteringPatch `json:"filtering,omitempty"`
	Cache     *CachePatch     `json:"cache,omitempty"`
}

// FilteringPatch is the allowlisted filtering patch.
type FilteringPatch struct {
	Enabled       *bool   `json:"enabled,omitempty"`
	BlockResponse *string `json:"block_response,omitempty"`
	BlockTTL      *uint32 `json:"block_ttl,omitempty"`
}

// CachePatch is the allowlisted cache patch.
type CachePatch struct {
	Enabled   *bool `json:"enabled,omitempty"`
	SizeBytes *int  `json:"size_bytes,omitempty"`
}

// empty reports whether the patch carries no allowlisted field.
func (p Patch) empty() (ok bool) {
	return p.Mode == nil &&
		p.ActiveProfile == nil &&
		len(p.Profiles) == 0 &&
		p.AddProfile == nil &&
		p.RenameProfile == nil &&
		p.DeleteProfile == nil
}

// Request is the staged request body.  It is untrusted input: Validate and the
// per-field allowlist are the boundary.
type Request struct {
	Action Action `json:"action"`

	Patch Patch `json:"patch,omitempty"`

	// ExpectedRevision is the config revision (mtime+size) the UI last read.
	// When set, the helper refuses to write if the file changed underneath it.
	ExpectedRevision string `json:"expected_revision,omitempty"`

	// UUID names the staging files (<uuid>.req / <uuid>.res).
	UUID string `json:"uuid"`
}

// Result is the helper's verdict, written to <uuid>.res.
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Err     string `json:"err,omitempty"`
}

// ErrUnsupported is returned by the non-Windows facade.
var ErrUnsupported = errors.New("the elevated helper is only supported on Windows")

// ErrRevision is returned when the on-disk config changed since the UI read it.
var ErrRevision = errors.New("config changed, refresh")

// errCustomProtocolFixed is returned when an explicit protocol disagrees with
// the scheme of a custom upstream URL (the URL is authoritative).  It is static
// and contains no id or URL.
var errCustomProtocolFixed = errors.New("the protocol is fixed by the custom upstream url; re-enter the url to change it")

// Validate checks a request without touching the filesystem.  It is the single
// gate every action passes through, on both sides of the elevation boundary.
func (r Request) Validate() (err error) {
	if !r.Action.valid() {
		return fmt.Errorf("unknown action %q", r.Action)
	}

	if r.UUID == "" {
		return errors.New("missing request id")
	}

	if r.Action.serviceAction() {
		if !r.Patch.empty() {
			return errors.New("service actions do not take a config patch")
		}

		return nil
	}

	if r.Patch.Mode != nil {
		switch *r.Patch.Mode {
		case config.ModeOn, config.ModeOff:
		default:
			return fmt.Errorf("invalid mode %q", *r.Patch.Mode)
		}
	}

	return r.Patch.validateShape()
}

// validateShape enforces the allowlisted patch's field-level constraints.  It
// never compares against an id or URL value in an error, so nothing secret can
// leak into Result.Err.
func (p Patch) validateShape() (err error) {
	structural := 0
	if p.AddProfile != nil {
		structural++
	}
	if p.RenameProfile != nil {
		structural++
	}
	if p.DeleteProfile != nil {
		structural++
	}
	if structural > 1 {
		return errors.New("only one of add_profile, rename_profile, delete_profile may be set")
	}

	names := make([]string, 0, len(p.Profiles))
	for name := range p.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if err = p.Profiles[name].validateShape(); err != nil {
			return err
		}
	}

	return nil
}

// validateShape enforces a single profile patch's field-level constraints.
func (pp ProfilePatch) validateShape() (err error) {
	// An id is only meaningful for a provider selection, and the custom
	// provider ignores it entirely.  Reject either so a security-relevant field
	// can never silently no-op.
	if pp.Provider == nil {
		if pp.ID != nil {
			return errors.New("resolver id requires an explicit provider")
		}
		if pp.Variant != nil || pp.Protocol != nil {
			return errors.New("select a provider explicitly to change its variant or protocol")
		}
	} else if strings.EqualFold(strings.TrimSpace(*pp.Provider), providers.Custom) &&
		pp.ID != nil && strings.TrimSpace(*pp.ID) != "" {
		return errors.New("the custom provider does not use a resolver id")
	}

	if pp.ID != nil {
		if err = validateID(*pp.ID); err != nil {
			return err
		}
	}

	if pp.URL != nil {
		raw := strings.TrimSpace(*pp.URL)
		if raw == "" {
			return errors.New("upstream url must not be empty")
		}

		parsed, perr := url.Parse(raw)
		if perr != nil || parsed.Scheme == "" {
			return errors.New("upstream url must include a scheme")
		}
	}

	if pp.Bootstrap != nil {
		for _, b := range *pp.Bootstrap {
			if !config.ValidBootstrap(b) {
				return errors.New("bootstrap must be an IP or IP:port literal")
			}
		}
	}

	if pp.Filtering != nil {
		if err = pp.Filtering.validateShape(); err != nil {
			return err
		}
	}

	if pp.Cache != nil {
		if err = pp.Cache.validateShape(); err != nil {
			return err
		}
	}

	return nil
}

// validateID enforces the resolver id charset (ASCII letters/digits/-/_) and
// length.  The id value is never included in the error.
func validateID(id string) (err error) {
	if len(id) > 64 {
		return errors.New("resolver id is too long")
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}

		return errors.New("resolver id contains an unsupported character")
	}

	return nil
}

// validateShape enforces the filtering patch constraints.
func (f FilteringPatch) validateShape() (err error) {
	if f.BlockTTL != nil && *f.BlockTTL == 0 {
		return errors.New("block_ttl must be greater than zero")
	}
	if f.BlockResponse != nil && strings.TrimSpace(*f.BlockResponse) == "" {
		return errors.New("block_response must not be empty")
	}

	return nil
}

// validateShape enforces the cache patch constraints.
func (c CachePatch) validateShape() (err error) {
	if c.SizeBytes != nil && *c.SizeBytes <= 0 && (c.Enabled == nil || *c.Enabled) {
		return errors.New("cache size must be positive while the cache is enabled")
	}

	return nil
}

// checkRevision returns ErrRevision when expected is non-empty and differs from
// the actual on-disk revision.  An empty expected means "no expectation" (the
// caller did not read a revision, e.g. a first-time enable).
func checkRevision(expected, actual string) (err error) {
	if expected == "" || expected == actual {
		return nil
	}

	return fmt.Errorf("%w (expected %s, found %s)", ErrRevision, expected, actual)
}

// applyRequest is the pure config-mutation path used by the helper: validate,
// enforce the expected revision, apply the allowlisted patch in a deterministic
// order, then validate the result.  It is factored out so it is unit-testable
// off Windows.  No error it returns ever contains a resolver id or an upstream
// URL (Result.Err is shown to an unelevated caller).
func applyRequest(cfg *config.Config, req Request, actualRevision string) (err error) {
	if err = req.Validate(); err != nil {
		return err
	}
	if err = checkRevision(req.ExpectedRevision, actualRevision); err != nil {
		return err
	}

	// 1. Structural operation.
	if err = applyStructural(cfg, req.Patch); err != nil {
		return err
	}

	// 2. Per-profile patches, deterministic order.
	names := make([]string, 0, len(req.Patch.Profiles))
	for name := range req.Patch.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		p, ok := cfg.Profiles[name]
		if !ok {
			return errors.New("cannot patch an unknown profile")
		}

		if err = applyProfilePatch(&p, req.Patch.Profiles[name]); err != nil {
			return err
		}

		cfg.Profiles[name] = p
	}

	// 3. Active profile.
	if req.Patch.ActiveProfile != nil {
		if err = cfg.SetActiveProfile(*req.Patch.ActiveProfile); err != nil {
			return err
		}
	}

	// 4. Mode.
	if req.Patch.Mode != nil {
		cfg.Mode = *req.Patch.Mode
	}

	return cfg.Validate()
}

// applyStructural performs the single structural profile operation, if any.
func applyStructural(cfg *config.Config, p Patch) (err error) {
	switch {
	case p.AddProfile != nil:
		return cfg.AddProfile(*p.AddProfile)
	case p.RenameProfile != nil:
		return cfg.RenameProfile(p.RenameProfile.From, p.RenameProfile.To)
	case p.DeleteProfile != nil:
		return cfg.DeleteProfile(*p.DeleteProfile)
	default:
		return nil
	}
}

// applyProfilePatch mutates p in place with the allowlisted fields only.
func applyProfilePatch(p *config.Profile, pp ProfilePatch) (err error) {
	if pp.Provider != nil {
		if err = applyProviderPatch(p, pp); err != nil {
			return err
		}
	} else if pp.Variant != nil || pp.Protocol != nil {
		// Metadata must never be set to values that disagree with the kept
		// upstreams; the caller has to pick the provider explicitly.
		return errors.New("select a provider explicitly to change its variant or protocol")
	}

	if pp.Bootstrap != nil {
		p.Bootstrap = append([]string(nil), (*pp.Bootstrap)...)
	}

	if pp.Filtering != nil {
		applyFilteringPatch(&p.Filtering, pp.Filtering)
	}

	if pp.Cache != nil {
		applyCachePatch(p, pp.Cache)
	}

	return nil
}

// applyProviderPatch re-expands the profile for the patched provider selection,
// recovering the existing id when the id field is left blank (replace-only).
func applyProviderPatch(p *config.Profile, pp ProfilePatch) (err error) {
	provider := strings.TrimSpace(*pp.Provider)

	if provider == providers.Custom {
		// Replace-only for custom: the stored URL is a secret the GUI never
		// shows, so a blank URL means "keep the existing upstream".  The
		// transport is dictated by the URL scheme, so an explicit protocol that
		// disagrees is a static error rather than a silent no-op.
		if pp.URL == nil || strings.TrimSpace(*pp.URL) == "" {
			return applyCustomKeep(p, pp)
		}
		if pp.Protocol != nil && !customProtocolMatches(*pp.URL, providers.Protocol(strings.TrimSpace(*pp.Protocol))) {
			return errCustomProtocolFixed
		}
	}

	proto := providers.DoH
	if pp.Protocol != nil {
		proto = providers.Protocol(strings.TrimSpace(*pp.Protocol))
	} else if strings.TrimSpace(p.Protocol) != "" {
		proto = providers.Protocol(strings.TrimSpace(p.Protocol))
	}

	id := ""
	if pp.ID != nil {
		id = strings.TrimSpace(*pp.ID)
	}

	sel := providers.Selection{Provider: provider, Protocol: proto, ID: id}

	if pp.Variant != nil {
		sel.Variant = strings.TrimSpace(*pp.Variant)
	}
	if pp.URL != nil {
		sel.URL = strings.TrimSpace(*pp.URL)
	}
	if pp.Bootstrap != nil {
		sel.Bootstrap = append([]string(nil), (*pp.Bootstrap)...)
	}

	// Empty id = keep: recover the existing id from the profile's EXISTING
	// upstreams, using the profile's EXISTING protocol (not the requested one),
	// then re-expand with the requested protocol.  Parsing old upstreams with
	// the new protocol would lose the id when the transport changes.  The
	// recovered value is re-validated because ExtractID URL-decodes path
	// content.
	if id == "" && selectionNeedsID(sel) {
		existing, xErr := recoverExistingID(provider, p.Protocol, p.Upstreams)
		if xErr != nil {
			return errors.New("cannot preserve the existing resolver id for this provider")
		}
		if err = validateID(existing); err != nil {
			return errors.New("the existing resolver id is not valid for this provider")
		}

		sel.ID = existing
	}

	return p.ApplySelection(sel)
}

// recoverExistingID recovers a profile's resolver id from its existing
// upstreams.  It tries the profile's recorded protocol first, then every
// supported protocol, so a protocol change (or a legacy profile without
// protocol metadata) does not lose the id.  An ambiguity reported by the
// recorded protocol is propagated rather than masked.
func recoverExistingID(provider, recorded string, upstreams []string) (id string, err error) {
	if proto := providers.Protocol(strings.TrimSpace(recorded)); proto.Valid() {
		got, xErr := providers.ExtractID(provider, proto, upstreams)
		if xErr == nil {
			return got, nil
		}
		if errors.Is(xErr, providers.ErrAmbiguousID) {
			return "", xErr
		}
	}

	for _, proto := range providers.Protocols() {
		got, xErr := providers.ExtractID(provider, proto, upstreams)
		if xErr == nil {
			return got, nil
		}
	}

	return "", providers.ErrNoID
}

// applyCustomKeep keeps a custom profile's existing upstreams (the URL is a
// secret the GUI never displays) and only refreshes the display metadata.  The
// error text never contains a URL or id.
func applyCustomKeep(p *config.Profile, pp ProfilePatch) (err error) {
	if len(p.Upstreams) == 0 {
		return errors.New("a custom upstream requires a url")
	}

	variant := ""
	if pp.Variant != nil {
		variant = strings.TrimSpace(*pp.Variant)
	}
	switch {
	case variant == "":
		if pr, ok := providers.Lookup(providers.Custom); ok {
			variant = pr.DefaultVariant
		}
	case !validVariant(providers.Custom, variant):
		return errors.New("unknown variant for the custom provider")
	}

	p.Provider = providers.Custom
	p.Variant = variant

	if pp.Protocol != nil {
		requested := providers.Protocol(strings.TrimSpace(*pp.Protocol))
		if !customProtocolMatches(p.Upstreams[0], requested) {
			return errCustomProtocolFixed
		}

		p.Protocol = string(requested)
	}
	// Without an explicit protocol the metadata is left untouched: it
	// describes the kept upstream.  Bootstrap is applied by applyProfilePatch.

	return nil
}

// protocolForScheme maps a custom upstream URL scheme to the transport it
// implies.  ok is false for an unrecognized scheme (e.g. sdns://).
func protocolForScheme(rawURL string) (proto providers.Protocol, ok bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}

	switch strings.ToLower(u.Scheme) {
	case "https":
		return providers.DoH, true
	case "tls":
		return providers.DoT, true
	case "quic":
		return providers.DoQ, true
	default:
		return "", false
	}
}

// customProtocolMatches reports whether an explicit protocol agrees with the
// scheme of a custom upstream URL.  It is false when they disagree or the
// scheme is unrecognized: both are static errors, never a silent drop.
func customProtocolMatches(rawURL string, requested providers.Protocol) (ok bool) {
	implied, known := protocolForScheme(rawURL)

	return known && implied == requested
}

// validVariant reports whether variant is one of provider's registered
// variants.
func validVariant(provider, variant string) (ok bool) {
	pr, ok := providers.Lookup(provider)
	if !ok {
		return false
	}

	for _, v := range pr.Variants {
		if v.Name == variant {
			return true
		}
	}

	return false
}

// selectionNeedsID reports whether sel's provider/variant requires an id.
func selectionNeedsID(sel providers.Selection) (needs bool) {
	pr, ok := providers.Lookup(sel.Provider)
	if !ok {
		return false
	}

	variant := sel.Variant
	if variant == "" {
		variant = pr.DefaultVariant
	}
	for _, v := range pr.Variants {
		if v.Name == variant {
			return v.NeedsID
		}
	}

	return false
}

// applyFilteringPatch merges the allowlisted filtering fields.
func applyFilteringPatch(f *config.Filtering, fp *FilteringPatch) {
	if fp.Enabled != nil {
		f.Enabled = *fp.Enabled
	}
	if fp.BlockResponse != nil {
		f.BlockResponse = strings.TrimSpace(*fp.BlockResponse)
	}
	if fp.BlockTTL != nil {
		f.BlockTTL = *fp.BlockTTL
	}
}

// applyCachePatch merges the allowlisted cache fields.
func applyCachePatch(p *config.Profile, cp *CachePatch) {
	if p.Cache == nil {
		p.Cache = &config.Cache{}
	}
	if cp.Enabled != nil {
		p.Cache.Enabled = *cp.Enabled
	}
	if cp.SizeBytes != nil {
		p.Cache.SizeBytes = *cp.SizeBytes
	}
}

// serviceBinaryMatches reports whether a registered service BinaryPathName
// still points at exe followed by the fixed "--service" argument.  It is pure
// so the anti-hijack check is unit-testable off Windows.
func serviceBinaryMatches(binPath, exe string) (ok bool) {
	s := strings.TrimSpace(binPath)
	if s == "" {
		return false
	}

	if strings.HasPrefix(s, `"`) {
		end := strings.Index(s[1:], `"`)
		if end < 0 {
			return false
		}

		bin := s[1 : 1+end]
		rest := strings.TrimSpace(s[end+2:])

		return strings.EqualFold(bin, exe) && rest == "--service"
	}

	fields := strings.Fields(s)
	if len(fields) != 2 {
		return false
	}

	return strings.EqualFold(fields[0], exe) && fields[1] == "--service"
}

// applyShouldReload decides whether a config-only apply should push the new
// config to the service.  It reloads only when the service is already running
// and the persisted bytes actually changed; a stopped or absent service is
// never started, and the config takes effect on the next enable.  Pure so the
// policy is unit-testable.
func applyShouldReload(running, changed bool) bool { return running && changed }

// uninstallPlan decides whether uninstall must first clear interception.  It is
// pure so the policy is unit-testable off Windows.
//
//	restore - interception must be cleared before the registration is deleted.
//	reason  - a static, secret-free explanation for the decision.
func uninstallPlan(running, enabled, dirty, active bool) (restore bool, reason string) {
	switch {
	case running:
		return true, "running service; interception is restored first"
	case active:
		return true, "active interception is restored first"
	case enabled || dirty:
		return true, "recorded interception is restored first"
	default:
		return false, "no active interception"
	}
}

// uninstallStateFrom combines the SCM state with the persisted state and a
// positive NRPT probe.  load reports (enabled, dirty, ok); a missing or corrupt
// state.json (ok=false) is NOT a negative witness.  probeActive reports whether
// dns-client NRPT interception is currently present.  When the service is
// running it is always restored, so the probe is skipped.  Pure so the
// fail-open default is unit-testable.
func uninstallStateFrom(
	running bool, pid uint32,
	load func() (enabled, dirty, ok bool),
	probeActive func() bool,
) (enabled, dirty, active bool) {
	_ = pid

	enabled, dirty, ok := load()
	if !ok {
		enabled, dirty = false, false
	}

	if running {
		return enabled, dirty, false
	}

	return enabled, dirty, probeActive()
}

// uninstallSequence runs the ordered uninstall steps, invoking del only after
// every preceding step has succeeded.  The platform operations are injected so
// the abort-before-delete guarantee is unit-testable off Windows.
//
//	restore -> stop (confirmed) -> verify (interception proven down) -> delete
func uninstallSequence(
	restore bool,
	doRestore, stop, verify, del func() error,
) (err error) {
	if restore {
		if err = doRestore(); err != nil {
			return err
		}
	}

	if err = stop(); err != nil {
		return err
	}

	if err = verify(); err != nil {
		return err
	}

	return del()
}

// restoreInterceptionWith tries the verified control-pipe restore when a
// service pid is known, then falls back to the local config-independent
// restore.  It is injected so the fallback contract (including a lock-held
// ErrLocked from the local restore) is unit-testable off Windows.
func restoreInterceptionWith(pid uint32, pipe, local func() error) (err error) {
	if pid != 0 {
		if err = pipe(); err == nil {
			return nil
		}
	}

	return local()
}

// canonicalServiceExe returns the executable the service is registered as and
// compared against: the non-GUI sibling dns-client.exe when exe is the GUI
// executable and that sibling exists, else exe.  stat is injected so the
// two-binary resolution is unit-testable.
func canonicalServiceExe(exe string, stat func(string) bool) (canonical string) {
	if strings.EqualFold(filepath.Base(exe), "dns-client-gui.exe") {
		sibling := filepath.Join(filepath.Dir(exe), "dns-client.exe")
		if stat(sibling) {
			return sibling
		}
	}

	return exe
}
