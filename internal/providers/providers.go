// Package providers implements the Phase-2 preset registry: it expands a
// (provider, variant, protocol, id) selection into the profile's upstream
// URLs and bootstrap addresses exactly as specified in SPEC §7.
//
// Endpoints are hardcoded from SPEC §7; do not re-research them online.
package providers

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Protocol is an upstream transport.
type Protocol string

// Upstream protocols.
const (
	DoH  Protocol = "doh"
	DoH3 Protocol = "doh3"
	DoT  Protocol = "dot"
	DoQ  Protocol = "doq"
)

// Protocols returns all supported protocols in display order.
func Protocols() []Protocol { return []Protocol{DoH, DoH3, DoT, DoQ} }

// Valid reports whether p is a known protocol.
func (p Protocol) Valid() bool {
	return slices.Contains(Protocols(), p)
}

// Provider names.
const (
	Quad9      = "quad9"
	Cloudflare = "cloudflare"
	NextDNS    = "nextdns"
	ControlD   = "controld"
	Custom     = "custom"
)

// Variant describes one provider variant.
type Variant struct {
	Name        string
	Description string
	NeedsID     bool
	ECSLeak     bool
}

// Provider describes a provider and its variants.
type Provider struct {
	Name           string
	Description    string
	DefaultVariant string
	Variants       []Variant
	Protocols      []Protocol
}

// Selection identifies the endpoints to expand.
type Selection struct {
	Provider  string
	Variant   string
	Protocol  Protocol
	ID        string
	URL       string
	Bootstrap []string
}

// Endpoints is the generated profile fragment.
type Endpoints struct {
	Upstreams []string
	Bootstrap []string
}

// Errors returned by Expand.
var (
	ErrUnknownProvider = errors.New("unknown provider")
	ErrUnknownVariant  = errors.New("unknown variant")
	ErrUnknownProtocol = errors.New("unknown protocol")
	ErrIDRequired      = errors.New("provider variant requires an id")
	ErrURLRequired     = errors.New("custom provider requires a URL")
)

// Errors returned by ExtractID.  Neither error string ever includes the id or
// the upstream URL.
var (
	ErrNoID        = errors.New("no resolver id in the configured upstream")
	ErrAmbiguousID = errors.New("ambiguous resolver id")
)

// controlDFreePresets are the documented ControlD free preset names.  The
// x-hagezi-* family is open-ended and handled separately.
var controlDFreePresets = []Variant{
	{Name: "p0", Description: "unfiltered"},
	{Name: "p1", Description: "malware blocked"},
	{Name: "p2", Description: "ads and tracking blocked"},
	{Name: "p3", Description: "social blocked"},
	{Name: "x-oisd", Description: "OISD blocklist"},
	{Name: "x-stevenblack", Description: "StevenBlack blocklist"},
	{Name: "x-adguard", Description: "AdGuard blocklist"},
	{Name: "family", Description: "family-friendly"},
	{Name: "uncensored", Description: "uncensored"},
}

// Registry returns the provider catalog for listing.
func Registry() (ps []Provider) {
	quad9 := Provider{
		Name:           Quad9,
		Description:    "Quad9 (DNSSEC-validating, dual-stack)",
		DefaultVariant: "secure",
		Protocols:      Protocols(),
	}
	for _, v := range []struct {
		name string
		desc string
		ecs  bool
	}{
		{"secure", "malware blocking (recommended)", false},
		{"noblock", "no threat blocking", false},
		{"secure-ecs", "malware blocking + ECS (leaks a network prefix)", true},
		{"noblock-ecs", "no blocking + ECS (leaks a network prefix)", true},
	} {
		quad9.Variants = append(quad9.Variants, Variant{
			Name:        v.name,
			Description: v.desc,
			ECSLeak:     v.ecs,
		})
	}

	cloudflare := Provider{
		Name:           Cloudflare,
		Description:    "Cloudflare 1.1.1.1 (unfiltered, security, family)",
		DefaultVariant: "standard",
		Protocols:      Protocols(),
		Variants: []Variant{
			{Name: "standard", Description: "unfiltered 1.1.1.1"},
			{Name: "security", Description: "malware blocking (1.1.1.2)"},
			{Name: "family", Description: "malware + adult blocking (1.1.1.3)"},
		},
	}

	nextdns := Provider{
		Name:           NextDNS,
		Description:    "NextDNS (requires --id)",
		DefaultVariant: "ultralow",
		Protocols:      Protocols(),
		Variants: []Variant{
			{Name: "ultralow", Description: "hostname-only, NO bootstrap (best routing; needs the system resolver once)", NeedsID: true},
			{Name: "anycast", Description: "bootstrap-pinned anycast IPs (avoids the system resolver; worse routing)", NeedsID: true},
		},
	}

	controld := Provider{
		Name:           ControlD,
		Description:    "ControlD (requires --id for custom/x-hagezi/free)",
		DefaultVariant: "custom",
		Protocols:      Protocols(),
		Variants: []Variant{
			{Name: "custom", Description: "custom profile (requires --id)", NeedsID: true},
		},
	}
	controld.Variants = append(controld.Variants, controlDFreePresets...)
	controld.Variants = append(controld.Variants,
		Variant{Name: "x-hagezi", Description: "Hagezi list, pass the full name as --id (e.g. x-hagezi-pro)", NeedsID: true},
		Variant{Name: "free", Description: "arbitrary free preset name (pass as --id)", NeedsID: true},
	)

	custom := Provider{
		Name:           Custom,
		Description:    "raw user-entered upstream URL + optional bootstrap",
		DefaultVariant: "custom",
		Protocols:      Protocols(),
		Variants:       []Variant{{Name: "custom", Description: "raw DoH/DoT/DoQ URL"}},
	}

	return []Provider{quad9, cloudflare, nextdns, controld, custom}
}

// Lookup finds a provider by name.
func Lookup(name string) (p Provider, ok bool) {
	for _, p := range Registry() {
		if p.Name == name {
			return p, true
		}
	}

	return Provider{}, false
}

// VariantNames returns the variant names of a provider.
func VariantNames(p Provider) (names []string) {
	for _, v := range p.Variants {
		names = append(names, v.Name)
	}

	return names
}

// Expand generates the upstreams and bootstrap addresses for a selection.
func Expand(sel Selection) (ep Endpoints, err error) {
	proto := sel.Protocol
	if proto == "" {
		proto = DoH
	}
	if !proto.Valid() {
		return Endpoints{}, fmt.Errorf("%w: %q", ErrUnknownProtocol, sel.Protocol)
	}

	switch sel.Provider {
	case Quad9:
		return expandQuad9(sel.Variant, proto)
	case Cloudflare:
		return expandCloudflare(sel.Variant, proto)
	case NextDNS:
		return expandNextDNS(sel.Variant, proto, sel.ID)
	case ControlD:
		return expandControlD(sel.Variant, proto, sel.ID)
	case Custom:
		if strings.TrimSpace(sel.URL) == "" {
			return Endpoints{}, ErrURLRequired
		}

		return Endpoints{Upstreams: []string{sel.URL}, Bootstrap: sel.Bootstrap}, nil
	default:
		return Endpoints{}, fmt.Errorf("%w: %q", ErrUnknownProvider, sel.Provider)
	}
}

// expandQuad9 implements the §7.1 matrix.
func expandQuad9(variant string, proto Protocol) (ep Endpoints, err error) {
	type quad9Variant struct {
		host string
		v4   []string
		v6   []string
	}

	matrix := map[string]quad9Variant{
		"secure": {
			host: "dns.quad9.net",
			v4:   []string{"9.9.9.9", "149.112.112.112"},
			v6:   []string{"2620:fe::fe", "2620:fe::9"},
		},
		"noblock": {
			host: "dns10.quad9.net",
			v4:   []string{"9.9.9.10", "149.112.112.10"},
			v6:   []string{"2620:fe::10", "2620:fe::fe:10"},
		},
		"secure-ecs": {
			host: "dns11.quad9.net",
			v4:   []string{"9.9.9.11", "149.112.112.11"},
			v6:   []string{"2620:fe::11", "2620:fe::fe:11"},
		},
		"noblock-ecs": {
			host: "dns12.quad9.net",
			v4:   []string{"9.9.9.12", "149.112.112.12"},
			v6:   []string{"2620:fe::12", "2620:fe::fe:12"},
		},
	}
	if variant == "" {
		variant = "secure"
	}

	m, ok := matrix[variant]
	if !ok {
		return Endpoints{}, fmt.Errorf("%w: quad9/%s", ErrUnknownVariant, variant)
	}

	ep.Upstreams = []string{hostURL(proto, m.host)}
	ep.Bootstrap = append(append([]string{}, m.v4...), m.v6...)

	return ep, nil
}

// expandCloudflare implements the Cloudflare 1.1.1.1 matrix.  Every variant is
// id-less; standard uses a dedicated DoT host while its DoH/DoQ host is shared.
func expandCloudflare(variant string, proto Protocol) (ep Endpoints, err error) {
	type cloudflareVariant struct {
		dohHost   string // also the DoQ host
		dotHost   string
		bootstrap []string
	}

	matrix := map[string]cloudflareVariant{
		"standard": {
			dohHost:   "cloudflare-dns.com",
			dotHost:   "1dot1dot1dot1.cloudflare-dns.com",
			bootstrap: []string{"1.1.1.1", "1.0.0.1", "2606:4700:4700::1111", "2606:4700:4700::1001"},
		},
		"security": {
			dohHost:   "security.cloudflare-dns.com",
			dotHost:   "security.cloudflare-dns.com",
			bootstrap: []string{"1.1.1.2", "1.0.0.2", "2606:4700:4700::1112", "2606:4700:4700::1002"},
		},
		"family": {
			dohHost:   "family.cloudflare-dns.com",
			dotHost:   "family.cloudflare-dns.com",
			bootstrap: []string{"1.1.1.3", "1.0.0.3", "2606:4700:4700::1113", "2606:4700:4700::1003"},
		},
	}
	if variant == "" {
		variant = "standard"
	}

	m, ok := matrix[variant]
	if !ok {
		return Endpoints{}, fmt.Errorf("%w: cloudflare/%s", ErrUnknownVariant, variant)
	}

	switch proto {
	case DoT:
		ep.Upstreams = []string{hostURL(DoT, m.dotHost)}
	case DoQ:
		ep.Upstreams = []string{hostURL(DoQ, m.dohHost)}
	case DoH3:
		ep.Upstreams = []string{hostURL(DoH3, m.dohHost)}
	default:
		ep.Upstreams = []string{dohURL(m.dohHost)}
	}
	ep.Bootstrap = append([]string{}, m.bootstrap...)

	return ep, nil
}

// expandNextDNS implements §7.2.  Ultralow is hostname-only with no bootstrap;
// anycast pins the documented anycast IPs.
func expandNextDNS(variant string, proto Protocol, id string) (ep Endpoints, err error) {
	switch variant {
	case "", "ultralow", "anycast":
	default:
		return Endpoints{}, fmt.Errorf("%w: nextdns/%s", ErrUnknownVariant, variant)
	}
	if strings.TrimSpace(id) == "" {
		return Endpoints{}, fmt.Errorf("%w: nextdns requires a profile id", ErrIDRequired)
	}

	switch proto {
	case DoH:
		ep.Upstreams = []string{"https://dns.nextdns.io/" + id}
	case DoH3:
		ep.Upstreams = []string{"h3://dns.nextdns.io/" + id}
	default:
		ep.Upstreams = []string{hostURL(proto, id+".dns.nextdns.io")}
	}

	if variant == "anycast" {
		ep.Bootstrap = []string{
			"45.90.28.0", "45.90.30.0",
			"2a07:a8c0::", "2a07:a8c1::",
		}
	}

	return ep, nil
}

// controlDCustomBootstrap is the bootstrap for ControlD custom profiles.
var controlDCustomBootstrap = []string{
	"76.76.2.22", "76.76.10.22",
	"2606:1a40::22", "2606:1a40:1::22",
}

// controlDFreeBootstrap is the bootstrap for ControlD free presets.
var controlDFreeBootstrap = []string{
	"76.76.2.11", "76.76.10.11",
	"2606:1a40::11", "2606:1a40:1::11",
}

// expandControlD implements §7.3.  custom uses dns.controld.com/<id>; free
// presets use freedns.controld.com/<name>.
func expandControlD(variant string, proto Protocol, id string) (ep Endpoints, err error) {
	switch variant {
	case "", "custom":
		if strings.TrimSpace(id) == "" {
			return Endpoints{}, fmt.Errorf("%w: controld/custom requires an id", ErrIDRequired)
		}

		ep.Upstreams = []string{controlDURL(proto, "dns.controld.com", id)}
		ep.Bootstrap = append([]string{}, controlDCustomBootstrap...)

		return ep, nil
	case "x-hagezi", "free":
		if strings.TrimSpace(id) == "" {
			return Endpoints{}, fmt.Errorf("%w: controld/%s requires the preset name as id", ErrIDRequired, variant)
		}

		ep.Upstreams = []string{controlDURL(proto, "freedns.controld.com", id)}
		ep.Bootstrap = append([]string{}, controlDFreeBootstrap...)

		return ep, nil
	}

	// Named free preset (p0–p3, x-oisd, ...).  Never treat these shared names
	// as a user's own custom id.
	for _, v := range controlDFreePresets {
		if v.Name == variant {
			ep.Upstreams = []string{controlDURL(proto, "freedns.controld.com", variant)}
			ep.Bootstrap = append([]string{}, controlDFreeBootstrap...)

			return ep, nil
		}
	}

	return Endpoints{}, fmt.Errorf("%w: controld/%s", ErrUnknownVariant, variant)
}

// dohURL returns a DoH URL for host.
func dohURL(host string) string { return "https://" + host + "/dns-query" }

// hostURL returns a DoT, DoQ, or DoH3 URL for a hostname (DoH falls back to
// the DoH form).  proto must be DoT, DoQ, or DoH3.
func hostURL(proto Protocol, host string) (u string) {
	switch proto {
	case DoT:
		return "tls://" + host + ":853"
	case DoQ:
		return "quic://" + host + ":853"
	case DoH3:
		return "h3://" + host + "/dns-query"
	default:
		return dohURL(host)
	}
}

// controlDURL builds a ControlD DoH-family/DoT/DoQ URL for a base host and
// id/preset.
func controlDURL(proto Protocol, base, id string) (u string) {
	switch proto {
	case DoH:
		return "https://" + base + "/" + id
	case DoH3:
		return "h3://" + base + "/" + id
	case DoT:
		return "tls://" + id + "." + base + ":853"
	case DoQ:
		return "quic://" + id + "." + base + ":853"
	default:
		return "https://" + base + "/" + id
	}
}

// ExtractID recovers the resolver id embedded in a provider's generated
// upstream URLs for the given protocol.  It returns ErrNoID when none is
// recoverable (including ControlD's shared free presets) and ErrAmbiguousID
// when the upstreams disagree.  The returned error never contains the id or the
// URL; callers must not add them either.
func ExtractID(provider string, proto Protocol, upstreams []string) (id string, err error) {
	if proto == "" {
		proto = DoH
	}

	ids := map[string]struct{}{}
	misses := 0

	for _, u := range upstreams {
		got, ok := extractOne(provider, proto, u)
		if !ok {
			misses++

			continue
		}

		ids[got] = struct{}{}
	}

	switch {
	case len(ids) == 0:
		return "", ErrNoID
	case len(ids) > 1 || misses > 0:
		return "", ErrAmbiguousID
	default:
		for got := range ids {
			return got, nil
		}
	}

	return "", ErrNoID
}

// ExtractIDs returns every resolver id recoverable from upstreams, in order,
// with duplicates removed.  Unlike ExtractID it never aborts on a miss or a
// disagreement, so a mixed profile still contributes each id as a redaction
// seed.  When provider is empty it is inferred from the URL host, and the
// protocol is always taken from the URL scheme, so absent or mismatched
// metadata still yields the id.  When no provider applies, a DoH path segment
// is used as a last-resort candidate.  The ids are seeds only and must never be
// logged.
func ExtractIDs(provider string, proto Protocol, upstreams []string) (ids []string) {
	seen := map[string]struct{}{}

	// A very short id (even a provider-extracted one) would rewrite nearly
	// every occurrence of those characters in the log, so it is never seeded;
	// real NextDNS/ControlD ids are >= 6 chars.
	add := func(id string) {
		if len(id) < minGenericIDLen || isPublicEndpoint(id) {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}

		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	for _, raw := range upstreams {
		u := strings.TrimSpace(raw)
		if u == "" {
			continue
		}

		p := provider
		pr := proto
		host, scheme := upstreamParts(u)
		if p == "" {
			p = providerForHost(host)
		}
		if s := protoForScheme(scheme); s != "" {
			pr = s
		}

		if id, ok := extractOne(p, pr, u); ok {
			add(id)

			continue
		}

		// The caller's provider metadata can disagree with the URL (e.g. a
		// custom or per-domain DoT/DoQ upstream on a known provider's host).
		// Retry with the host-derived provider, but only for a host some
		// provider actually owns, so nothing new is over-scrubbed.
		if hp := providerForHost(host); p != "" && hp != "" && hp != p {
			if id, ok := extractOne(hp, pr, u); ok {
				add(id)

				continue
			}
		}

		// No provider matched: a custom DoH endpoint may still hide its id in
		// the path.  Shared/public hosts (e.g. ControlD freedns presets) carry
		// no private id.
		if schemeIsDoH(scheme) && !isSharedDoHHost(host) {
			add(doHPathID(u))
		}
	}

	return ids
}

// upstreamParts returns the lowercased host and scheme of u.
func upstreamParts(u string) (host, scheme string) {
	p, err := url.Parse(u)
	if err != nil {
		return "", ""
	}

	return strings.ToLower(p.Hostname()), strings.ToLower(p.Scheme)
}

// providerForHost infers a provider from a generated upstream hostname.
func providerForHost(host string) (provider string) {
	switch {
	case host == "nextdns.io" || strings.HasSuffix(host, ".nextdns.io"):
		return NextDNS
	case host == "controld.com" || strings.HasSuffix(host, ".controld.com"):
		return ControlD
	default:
		return ""
	}
}

// protoForScheme maps a URL scheme to a transport, or "" when unknown.  It
// accepts metadata-only aliases (dot/doq) that dnsproxy itself cannot fetch.
func protoForScheme(scheme string) (proto Protocol) {
	switch scheme {
	case "tls", "dot":
		return DoT
	case "quic", "doq":
		return DoQ
	case "http", "https":
		return DoH
	case "h3":
		return DoH3
	default:
		return ""
	}
}

// ProtocolForURL maps a custom upstream URL to the transport dnsproxy will use
// for it.  ok is false for schemes dnsproxy cannot fetch (e.g. sdns://) and for
// metadata-only aliases.
func ProtocolForURL(rawURL string) (proto Protocol, ok bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "", false
	}

	switch strings.ToLower(u.Scheme) {
	case "https":
		return DoH, true
	case "h3":
		return DoH3, true
	case "tls":
		return DoT, true
	case "quic":
		return DoQ, true
	default:
		return "", false
	}
}

// isDoH reports whether proto is a DoH transport, over HTTP/2/1.1 or HTTP/3.
func isDoH(proto Protocol) bool { return proto == DoH || proto == DoH3 }

// schemeIsDoH reports whether scheme is a DoH scheme (HTTP(S) or HTTP/3).
func schemeIsDoH(scheme string) (ok bool) {
	return scheme == "http" || scheme == "https" || scheme == "h3"
}

// isSharedDoHHost reports hosts whose DoH path is a shared public preset
// rather than a user's private id (ControlD freedns).
func isSharedDoHHost(host string) (ok bool) {
	return host == "freedns.controld.com" || strings.HasSuffix(host, ".freedns.controld.com")
}

// minGenericIDLen is the shortest id accepted as a seed, provider-extracted or
// not.  A 1-3 character seed would scrub ordinary log text (and query names)
// far beyond the credential it represents.
const minGenericIDLen = 4

// doHPathID returns the last path segment of a DoH URL, the location a private
// id occupies for e.g. NextDNS/ControlD.
func doHPathID(u string) (id string) {
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}

	path := strings.Trim(p.Path, "/")
	if path == "" {
		return ""
	}
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}

	return path
}

// publicEndpoints are shared, id-less resolver tokens that must never become
// scrub seeds (scrubbing them would corrupt ordinary log lines).
func isPublicEndpoint(s string) (ok bool) {
	switch strings.ToLower(s) {
	case "dns-query", "resolve", "query", "dns":
		return true
	default:
		return false
	}
}

// extractOne recovers the id from a single upstream URL, or ok=false when the
// provider/protocol/host combination carries none.
func extractOne(provider string, proto Protocol, upstream string) (id string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(upstream))
	if err != nil {
		return "", false
	}

	host := strings.ToLower(u.Hostname())

	switch provider {
	case NextDNS:
		if isDoH(proto) {
			if host != "dns.nextdns.io" {
				return "", false
			}

			return decodePathID(u.Path)
		}

		return firstHostLabel(host, "dns.nextdns.io")
	case ControlD:
		// freedns.controld.com/<preset> is a shared public name, never a
		// user's private id.
		if host == "freedns.controld.com" || strings.HasSuffix(host, ".freedns.controld.com") {
			return "", false
		}
		if isDoH(proto) {
			if host != "dns.controld.com" {
				return "", false
			}

			return decodePathID(u.Path)
		}

		return firstHostLabel(host, "dns.controld.com")
	default:
		// Cloudflare and Quad9 are public, id-less providers: an id is neither
		// required nor extractable.
		return "", false
	}
}

// decodePathID URL-decodes the leading DoH path segment into an id, the
// position NextDNS/ControlD put it in.  Later segments (a device or client
// name) are not part of the id.  An empty path means no id.
func decodePathID(path string) (id string, ok bool) {
	path = strings.Trim(path, "/")
	if path == "" {
		return "", false
	}
	if i := strings.IndexByte(path, '/'); i >= 0 {
		path = path[:i]
	}

	decoded, err := url.PathUnescape(path)
	if err != nil || decoded == "" {
		return "", false
	}

	return decoded, true
}

// firstHostLabel returns the single label preceding suffix in host (e.g.
// "abc.dns.nextdns.io" with suffix "dns.nextdns.io" yields "abc").  It rejects
// a bare suffix and multi-label prefixes.
func firstHostLabel(host, suffix string) (label string, ok bool) {
	if !strings.HasSuffix(host, "."+suffix) {
		return "", false
	}

	label = strings.TrimSuffix(host, "."+suffix)
	if label == "" || strings.Contains(label, ".") {
		return "", false
	}

	return label, true
}
