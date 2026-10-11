// Package providers implements the Phase-2 preset registry: it expands a
// (provider, variant, protocol, id) selection into the profile's upstream
// URLs and bootstrap addresses exactly as specified in SPEC §7.
//
// Endpoints are hardcoded from SPEC §7; do not re-research them online.
package providers

import (
	"errors"
	"fmt"
	"strings"
)

// Protocol is an upstream transport.
type Protocol string

// Upstream protocols.
const (
	DoH Protocol = "doh"
	DoT Protocol = "dot"
	DoQ Protocol = "doq"
)

// Protocols returns all supported protocols in display order.
func Protocols() []Protocol { return []Protocol{DoH, DoT, DoQ} }

// Valid reports whether p is a known protocol.
func (p Protocol) Valid() bool {
	switch p {
	case DoH, DoT, DoQ:
		return true
	default:
		return false
	}
}

// Provider names.
const (
	Quad9    = "quad9"
	NextDNS  = "nextdns"
	ControlD = "controld"
	Custom   = "custom"
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

	return []Provider{quad9, nextdns, controld, custom}
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

	ep.Upstreams = []string{dohURL(m.host)}
	if proto != DoH {
		ep.Upstreams = []string{hostURL(proto, m.host)}
	}
	ep.Bootstrap = append(append([]string{}, m.v4...), m.v6...)

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

// hostURL returns a DoT or DoQ URL for a hostname (DoH falls back to the DoH
// form).  proto must be DoT or DoQ.
func hostURL(proto Protocol, host string) (u string) {
	switch proto {
	case DoT:
		return "tls://" + host + ":853"
	case DoQ:
		return "quic://" + host + ":853"
	default:
		return dohURL(host)
	}
}

// controlDURL builds a ControlD DoH/DoT/DoQ URL for a base host and id/preset.
func controlDURL(proto Protocol, base, id string) (u string) {
	switch proto {
	case DoH:
		return "https://" + base + "/" + id
	case DoT:
		return "tls://" + id + "." + base + ":853"
	case DoQ:
		return "quic://" + id + "." + base + ":853"
	default:
		return "https://" + base + "/" + id
	}
}
