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
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

	// ActionRefreshLists re-downloads the active profile's filter lists and
	// rebuilds the filter engine.  It does not touch the config; it drives the
	// running service over the verified control pipe.
	ActionRefreshLists Action = "refresh_lists"
)

// valid reports whether a is a known action.
func (a Action) valid() (ok bool) {
	switch a {
	case ActionEnable, ActionRestore, ActionApply,
		ActionServiceInstall, ActionServiceUninstall, ActionServiceStart, ActionServiceStop,
		ActionRefreshLists:
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

// noPatch reports whether a takes no config patch (service actions and the
// service-only refresh_lists action).
func (a Action) noPatch() (ok bool) {
	return a.serviceAction() || a == ActionRefreshLists
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

	// Logging (LOGGING_PLAN).  A log-only change is saved but never triggers a
	// service reload (see reloadRelevant).
	LogLevel     *string `json:"log_level,omitempty"`
	LogPath      *string `json:"log_path,omitempty"`
	LogMaxSizeMB *int    `json:"log_max_size_mb,omitempty"`
	LogKeepMB    *int    `json:"log_keep_mb,omitempty"`
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

	// FallbackProfile names the profile to use when this one's upstreams fail.
	// An explicit empty value clears the fallback.
	FallbackProfile *string `json:"fallback_profile,omitempty"`

	// PerDomain replaces the profile's per-domain routing rules.  A blank
	// Upstream on an entry keeps the existing rule's upstream at the same index
	// (replace-only), so a configured per-domain upstream never has to travel.
	PerDomain *[]PerDomainPatch `json:"per_domain,omitempty"`
}

// FilteringPatch is the allowlisted filtering patch.
type FilteringPatch struct {
	Enabled       *bool      `json:"enabled,omitempty"`
	BlockResponse *string    `json:"block_response,omitempty"`
	BlockTTL      *uint32    `json:"block_ttl,omitempty"`
	Lists         *ListPatch `json:"lists,omitempty"`
}

// ListPatch is the allowlisted set of filter-list operations.  Existing lists
// are addressed by their index in config order so their (possibly
// credential-bearing) URLs never round-trip; only a newly added list carries a
// URL.  It is applied after the scalar filtering fields.
type ListPatch struct {
	// SetEnabled maps an existing list index to a new enabled state.
	SetEnabled map[int]bool `json:"set_enabled,omitempty"`

	// Add appends new lists.
	Add []ListEntry `json:"add,omitempty"`

	// Remove deletes existing lists by index.
	Remove []int `json:"remove,omitempty"`
}

// ListEntry is one list to append.
type ListEntry struct {
	// URL is the list source (http(s) URL or local path).
	URL string `json:"url"`

	// Enabled reports whether the added list starts enabled.
	Enabled bool `json:"enabled"`
}

// PerDomainPatch is one per-domain routing rule.  Domains are always
// re-specified; a blank Upstream keeps the existing rule's upstream, named by
// Keep (the source rule's original index), so a configured target never has to
// travel and a deletion elsewhere in the list cannot shift the source.  A
// bypass patch (Bypass) carries no Upstream and no Keep: its domains resolve
// via the system resolver.
type PerDomainPatch struct {
	// Domains is the rule's domain list (dnsmasq conventions).
	Domains []string `json:"domains"`

	// Bypass routes the domains via the system resolver instead of Upstream.
	Bypass bool `json:"bypass,omitempty"`

	// Upstream is the routing target; blank keeps the existing rule's target
	// named by Keep.
	Upstream string `json:"upstream,omitempty"`

	// Keep is the original index of an existing rule whose upstream is
	// inherited when Upstream is blank.  It is required for a blank upstream
	// and ignored when Upstream is set.
	Keep *int `json:"keep,omitempty"`

	// Enabled toggles the rule; nil (absent) leaves it enabled.
	Enabled *bool `json:"enabled,omitempty"`
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
		p.DeleteProfile == nil &&
		p.LogLevel == nil &&
		p.LogPath == nil &&
		p.LogMaxSizeMB == nil &&
		p.LogKeepMB == nil
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

	if r.Action.noPatch() {
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

	if err = p.validateLog(); err != nil {
		return err
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

// validateLog enforces the logging patch constraints.  A blank path means "use
// the default"; errors never echo the path value.
func (p Patch) validateLog() (err error) {
	if p.LogLevel != nil {
		if err = config.ValidateLogLevel(strings.TrimSpace(*p.LogLevel)); err != nil {
			return err
		}
	}
	if p.LogPath != nil {
		if path := strings.TrimSpace(*p.LogPath); path != "" {
			if err = config.ValidateLogPath(path); err != nil {
				return err
			}
		}
	}
	if p.LogMaxSizeMB != nil && *p.LogMaxSizeMB <= 0 {
		return errors.New("log max size must be greater than zero")
	}
	if p.LogKeepMB != nil && *p.LogKeepMB <= 0 {
		return errors.New("log keep size must be greater than zero")
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

	if pp.PerDomain != nil {
		if err = validatePerDomainPatch(*pp.PerDomain); err != nil {
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
	if f.Lists != nil {
		if err = f.Lists.validateShape(); err != nil {
			return err
		}
	}

	return nil
}

// validateShape enforces the list-operation constraints.  Indices are checked
// against the profile's actual list count when the patch is applied (only then
// is the config in hand); here only the intrinsic shape is checked.
func (l ListPatch) validateShape() (err error) {
	for i := range l.SetEnabled {
		if i < 0 {
			return errors.New("list index must not be negative")
		}
	}
	for _, i := range l.Remove {
		if i < 0 {
			return errors.New("list index must not be negative")
		}
	}
	for _, e := range l.Add {
		if strings.TrimSpace(e.URL) == "" {
			return errors.New("an added list needs a url")
		}
	}

	return nil
}

// validatePerDomainPatch enforces the per-domain rule constraints.
func validatePerDomainPatch(rules []PerDomainPatch) (err error) {
	for _, r := range rules {
		if len(r.Domains) == 0 {
			return errors.New("a per-domain rule needs at least one domain")
		}
		if r.Bypass {
			if r.Upstream != "" || r.Keep != nil {
				return errors.New("a bypass per-domain rule must not set an upstream or keep index")
			}

			continue
		}
		if r.Keep != nil && *r.Keep < 0 {
			return errors.New("a per-domain keep index must not be negative")
		}
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

	// 5. Logging.
	applyLogPatch(&cfg.Log, req.Patch)

	// A non-blank log path must have an existing parent directory: otherwise
	// the service silently fails open to stderr and the user gets no file.
	// Blank means "use the default", which the logger resolves itself.
	if path := cfg.Log.Path; path != "" {
		if info, statErr := os.Stat(filepath.Dir(path)); statErr != nil || !info.IsDir() {
			return errors.New("the log directory does not exist")
		}
	}

	return cfg.Validate()
}

// applyLogPatch merges the allowlisted logging fields.
func applyLogPatch(l *config.LogConfig, p Patch) {
	if p.LogLevel != nil {
		l.Level = strings.TrimSpace(*p.LogLevel)
	}
	if p.LogPath != nil {
		l.Path = strings.TrimSpace(*p.LogPath)
	}
	if p.LogMaxSizeMB != nil {
		l.MaxSizeMB = *p.LogMaxSizeMB
	}
	if p.LogKeepMB != nil {
		l.KeepMB = *p.LogKeepMB
	}
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

	if pp.FallbackProfile != nil {
		p.FallbackProfile = strings.TrimSpace(*pp.FallbackProfile)
	}

	if pp.Filtering != nil {
		if err = applyFilteringPatch(&p.Filtering, pp.Filtering); err != nil {
			return err
		}
	}

	if pp.Cache != nil {
		applyCachePatch(p, pp.Cache)
	}

	if pp.PerDomain != nil {
		if err = applyPerDomainPatch(p, *pp.PerDomain); err != nil {
			return err
		}
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

	return slices.Contains(providers.VariantNames(pr), variant)
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
func applyFilteringPatch(f *config.Filtering, fp *FilteringPatch) (err error) {
	if fp.Enabled != nil {
		f.Enabled = *fp.Enabled
	}
	if fp.BlockResponse != nil {
		f.BlockResponse = strings.TrimSpace(*fp.BlockResponse)
	}
	if fp.BlockTTL != nil {
		f.BlockTTL = *fp.BlockTTL
	}
	if fp.Lists != nil {
		if err = applyListPatch(&f.Lists, *fp.Lists); err != nil {
			return err
		}
	}

	return nil
}

// applyListPatch applies the allowlisted filter-list operations: in-place
// enable toggles, then removals by descending index, then appends.  Existing
// lists are addressed only by index; a new list carries its URL.  Every
// addressed index must be within [0, len(*lists)); an out-of-range index is a
// static error (the value is never echoed) so a stale UI cannot corrupt an
// unrelated list.
func applyListPatch(lists *[]config.FilterList, lp ListPatch) (err error) {
	cur := *lists

	for i := range lp.SetEnabled {
		if i < 0 || i >= len(cur) {
			return errors.New("list index is out of range")
		}
	}
	for _, i := range lp.Remove {
		if i < 0 || i >= len(cur) {
			return errors.New("list index is out of range")
		}
	}

	for i, enabled := range lp.SetEnabled {
		cur[i].Enabled = enabled
	}

	// Remove in descending index order so an earlier removal never shifts a
	// later index.
	if len(lp.Remove) > 0 {
		idx := append([]int(nil), lp.Remove...)
		sort.Sort(sort.Reverse(sort.IntSlice(idx)))

		for _, i := range idx {
			cur = append(cur[:i], cur[i+1:]...)
		}
	}

	for _, e := range lp.Add {
		cur = append(cur, config.FilterList{URL: strings.TrimSpace(e.URL), Enabled: e.Enabled})
	}

	*lists = cur

	return nil
}

// applyPerDomainPatch replaces p's per-domain routing rules.  A non-blank
// Upstream sets the rule's target; a blank Upstream keeps the target of the
// existing rule named by Keep (replace-only), so a configured target never has
// to travel and the source is explicit rather than positional.  A blank
// upstream with no in-range Keep (a new rule), or whose inherited upstream is
// blank, is a static error.
func applyPerDomainPatch(p *config.Profile, patches []PerDomainPatch) (err error) {
	old := p.PerDomain
	out := make([]config.PerDomain, 0, len(patches))

	for _, rp := range patches {
		if rp.Bypass {
			out = append(out, config.PerDomain{
				Domains: append([]string(nil), rp.Domains...),
				Bypass:  true,
				Enabled: rp.Enabled,
			})

			continue
		}

		up := strings.TrimSpace(rp.Upstream)
		if up == "" {
			if rp.Keep == nil || *rp.Keep < 0 || *rp.Keep >= len(old) ||
				strings.TrimSpace(old[*rp.Keep].Upstream) == "" {
				return errors.New("a new per-domain rule needs an upstream")
			}

			up = old[*rp.Keep].Upstream
		}

		out = append(out, config.PerDomain{
			Domains:  append([]string(nil), rp.Domains...),
			Upstream: up,
			Enabled:  rp.Enabled,
		})
	}

	p.PerDomain = out

	return nil
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
		bin, after, found := strings.Cut(s[1:], `"`)
		if !found {
			return false
		}

		return strings.EqualFold(bin, exe) && strings.TrimSpace(after) == "--service"
	}

	bin, args, found := strings.Cut(s, " ")
	if !found {
		return false
	}

	return strings.EqualFold(bin, exe) && strings.TrimSpace(args) == "--service"
}

// applyShouldReload decides whether a config-only apply should push the new
// config to the service.  It reloads only when the service is already running
// and the persisted bytes actually changed; a stopped or absent service is
// never started, and the config takes effect on the next enable.  Pure so the
// policy is unit-testable.
func applyShouldReload(running, changed bool) bool { return running && changed }

// reloadRelevant reports whether before->after changed in a way that requires a
// service reload.  A log-only edit is persisted but takes effect on the next
// service start (LOGGING_PLAN §1.6), so the Log block is excluded; every other
// field still reloads.  Pure so the policy is unit-testable.
func reloadRelevant(before, after *config.Config) (changed bool) {
	b, a := *before, *after
	b.Log, a.Log = config.LogConfig{}, config.LogConfig{}

	return !reflect.DeepEqual(b, a)
}

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
	running bool,
	load func() (enabled, dirty, ok bool),
	probeActive func() bool,
) (enabled, dirty, active bool) {
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

// stagingRequestExt is the extension of the request file Apply creates.
const stagingRequestExt = ".req"

// validateStagingRequest rejects a --apply path that is not a request file the
// helper itself creates: it must live directly in dir and carry the .req
// extension.  The comparison is lexical (the staging dir is user-only ACL'd), and
// runs before any read or delete so a rejected path leaves the filesystem alone.
func validateStagingRequest(reqPath, dir string) (err error) {
	if !strings.EqualFold(filepath.Clean(filepath.Dir(reqPath)), filepath.Clean(dir)) {
		return errors.New("request path is outside the staging directory")
	}

	base := filepath.Base(reqPath)
	if filepath.Ext(base) != stagingRequestExt || strings.TrimSuffix(base, stagingRequestExt) == "" {
		return errors.New("request path is not a staging request")
	}

	return nil
}

// readStagingRequest validates reqPath against dir, then reads and removes it.
// The containment check runs first, so a rejected path is never read or deleted.
func readStagingRequest(reqPath, dir string) (data []byte, err error) {
	if err = validateStagingRequest(reqPath, dir); err != nil {
		return nil, err
	}

	data, err = os.ReadFile(reqPath)
	if err != nil {
		return nil, fmt.Errorf("reading request: %w", err)
	}
	// Delete immediately: it may carry settings, and the elevated admin token
	// must not leave it behind.
	_ = os.Remove(reqPath)

	return data, nil
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
