package providers

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestExpandQuad9(t *testing.T) {
	cases := []struct {
		name      string
		variant   string
		proto     Protocol
		upstreams []string
		bootstrap []string
	}{
		{
			name:      "secure_doh",
			variant:   "secure",
			proto:     DoH,
			upstreams: []string{"https://dns.quad9.net/dns-query"},
			bootstrap: []string{"9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"},
		},
		{
			name:      "secure_dot",
			variant:   "secure",
			proto:     DoT,
			upstreams: []string{"tls://dns.quad9.net:853"},
			bootstrap: []string{"9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"},
		},
		{
			name:      "secure_doq",
			variant:   "secure",
			proto:     DoQ,
			upstreams: []string{"quic://dns.quad9.net:853"},
			bootstrap: []string{"9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"},
		},
		{
			name:      "secure_doh3",
			variant:   "secure",
			proto:     DoH3,
			upstreams: []string{"h3://dns.quad9.net/dns-query"},
			bootstrap: []string{"9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"},
		},
		{
			name:      "noblock",
			variant:   "noblock",
			proto:     DoH,
			upstreams: []string{"https://dns10.quad9.net/dns-query"},
			bootstrap: []string{"9.9.9.10", "149.112.112.10", "2620:fe::10", "2620:fe::fe:10"},
		},
		{
			name:      "secure_ecs",
			variant:   "secure-ecs",
			proto:     DoH,
			upstreams: []string{"https://dns11.quad9.net/dns-query"},
			bootstrap: []string{"9.9.9.11", "149.112.112.11", "2620:fe::11", "2620:fe::fe:11"},
		},
		{
			name:      "noblock_ecs",
			variant:   "noblock-ecs",
			proto:     DoH,
			upstreams: []string{"https://dns12.quad9.net/dns-query"},
			bootstrap: []string{"9.9.9.12", "149.112.112.12", "2620:fe::12", "2620:fe::fe:12"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, err := Expand(Selection{Provider: Quad9, Variant: tc.variant, Protocol: tc.proto})
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			if !slices.Equal(ep.Upstreams, tc.upstreams) {
				t.Errorf("upstreams = %v, want %v", ep.Upstreams, tc.upstreams)
			}
			if !slices.Equal(ep.Bootstrap, tc.bootstrap) {
				t.Errorf("bootstrap = %v, want %v", ep.Bootstrap, tc.bootstrap)
			}
		})
	}

	if _, err := Expand(Selection{Provider: Quad9, Variant: "bogus"}); !errors.Is(err, ErrUnknownVariant) {
		t.Errorf("unknown quad9 variant: got %v, want ErrUnknownVariant", err)
	}
}

func TestExpandCloudflare(t *testing.T) {
	standardBoot := []string{"1.1.1.1", "1.0.0.1", "2606:4700:4700::1111", "2606:4700:4700::1001"}
	securityBoot := []string{"1.1.1.2", "1.0.0.2", "2606:4700:4700::1112", "2606:4700:4700::1002"}
	familyBoot := []string{"1.1.1.3", "1.0.0.3", "2606:4700:4700::1113", "2606:4700:4700::1003"}

	cases := []struct {
		name      string
		variant   string
		proto     Protocol
		upstreams []string
		bootstrap []string
	}{
		{"standard_doh", "standard", DoH, []string{"https://cloudflare-dns.com/dns-query"}, standardBoot},
		{"standard_dot", "standard", DoT, []string{"tls://1dot1dot1dot1.cloudflare-dns.com:853"}, standardBoot},
		{"standard_doq", "standard", DoQ, []string{"quic://cloudflare-dns.com:853"}, standardBoot},
		{"standard_doh3", "standard", DoH3, []string{"h3://cloudflare-dns.com/dns-query"}, standardBoot},
		{"security_doh", "security", DoH, []string{"https://security.cloudflare-dns.com/dns-query"}, securityBoot},
		{"security_dot", "security", DoT, []string{"tls://security.cloudflare-dns.com:853"}, securityBoot},
		{"security_doq", "security", DoQ, []string{"quic://security.cloudflare-dns.com:853"}, securityBoot},
		{"family_doh", "family", DoH, []string{"https://family.cloudflare-dns.com/dns-query"}, familyBoot},
		{"family_dot", "family", DoT, []string{"tls://family.cloudflare-dns.com:853"}, familyBoot},
		{"family_doq", "family", DoQ, []string{"quic://family.cloudflare-dns.com:853"}, familyBoot},
		{"empty_variant_defaults_standard", "", DoH, []string{"https://cloudflare-dns.com/dns-query"}, standardBoot},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// No id is required or accepted for any Cloudflare selection.
			ep, err := Expand(Selection{Provider: Cloudflare, Variant: tc.variant, Protocol: tc.proto})
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			if !slices.Equal(ep.Upstreams, tc.upstreams) {
				t.Errorf("upstreams = %v, want %v", ep.Upstreams, tc.upstreams)
			}
			if !slices.Equal(ep.Bootstrap, tc.bootstrap) {
				t.Errorf("bootstrap = %v, want %v", ep.Bootstrap, tc.bootstrap)
			}
		})
	}

	if _, err := Expand(Selection{Provider: Cloudflare, Variant: "bogus"}); !errors.Is(err, ErrUnknownVariant) {
		t.Errorf("unknown cloudflare variant: got %v, want ErrUnknownVariant", err)
	}

	// Registry metadata: standard default and no variant needs an id.
	p, ok := Lookup(Cloudflare)
	if !ok {
		t.Fatal("cloudflare missing from registry")
	}
	if p.DefaultVariant != "standard" {
		t.Errorf("DefaultVariant = %q, want standard", p.DefaultVariant)
	}
	for _, v := range p.Variants {
		if v.NeedsID {
			t.Errorf("variant %q requires an id, want none", v.Name)
		}
	}
}

func TestExpandNextDNS(t *testing.T) {
	cases := []struct {
		name      string
		variant   string
		proto     Protocol
		upstreams []string
		bootstrap []string
	}{
		{
			name:      "ultralow_doh_no_bootstrap",
			variant:   "ultralow",
			proto:     DoH,
			upstreams: []string{"https://dns.nextdns.io/abc123"},
			bootstrap: nil,
		},
		{
			name:      "ultralow_dot_no_bootstrap",
			variant:   "ultralow",
			proto:     DoT,
			upstreams: []string{"tls://abc123.dns.nextdns.io:853"},
			bootstrap: nil,
		},
		{
			name:      "ultralow_doq_no_bootstrap",
			variant:   "ultralow",
			proto:     DoQ,
			upstreams: []string{"quic://abc123.dns.nextdns.io:853"},
			bootstrap: nil,
		},
		{
			name:      "ultralow_doh3_no_bootstrap",
			variant:   "ultralow",
			proto:     DoH3,
			upstreams: []string{"h3://dns.nextdns.io/abc123"},
			bootstrap: nil,
		},
		{
			name:      "anycast_has_bootstrap",
			variant:   "anycast",
			proto:     DoH,
			upstreams: []string{"https://dns.nextdns.io/abc123"},
			bootstrap: []string{"45.90.28.0", "45.90.30.0", "2a07:a8c0::", "2a07:a8c1::"},
		},
		{
			name:      "empty_variant_defaults_ultralow",
			variant:   "",
			proto:     DoH,
			upstreams: []string{"https://dns.nextdns.io/abc123"},
			bootstrap: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, err := Expand(Selection{Provider: NextDNS, Variant: tc.variant, Protocol: tc.proto, ID: "abc123"})
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			if !slices.Equal(ep.Upstreams, tc.upstreams) {
				t.Errorf("upstreams = %v, want %v", ep.Upstreams, tc.upstreams)
			}
			if !slices.Equal(ep.Bootstrap, tc.bootstrap) {
				t.Errorf("bootstrap = %v, want %v", ep.Bootstrap, tc.bootstrap)
			}
		})
	}

	if _, err := Expand(Selection{Provider: NextDNS, Variant: "ultralow"}); !errors.Is(err, ErrIDRequired) {
		t.Errorf("missing id: got %v, want ErrIDRequired", err)
	}
}

func TestExpandControlD(t *testing.T) {
	customBoot := []string{"76.76.2.22", "76.76.10.22", "2606:1a40::22", "2606:1a40:1::22"}
	freeBoot := []string{"76.76.2.11", "76.76.10.11", "2606:1a40::11", "2606:1a40:1::11"}

	cases := []struct {
		name      string
		variant   string
		proto     Protocol
		id        string
		upstreams []string
		bootstrap []string
	}{
		{
			name:      "custom_doh",
			variant:   "custom",
			proto:     DoH,
			id:        "xyz",
			upstreams: []string{"https://dns.controld.com/xyz"},
			bootstrap: customBoot,
		},
		{
			name:      "custom_dot",
			variant:   "custom",
			proto:     DoT,
			id:        "xyz",
			upstreams: []string{"tls://xyz.dns.controld.com:853"},
			bootstrap: customBoot,
		},
		{
			name:      "custom_doq",
			variant:   "custom",
			proto:     DoQ,
			id:        "xyz",
			upstreams: []string{"quic://xyz.dns.controld.com:853"},
			bootstrap: customBoot,
		},
		{
			name:      "custom_doh3",
			variant:   "custom",
			proto:     DoH3,
			id:        "xyz",
			upstreams: []string{"h3://dns.controld.com/xyz"},
			bootstrap: customBoot,
		},
		{
			name:      "free_p0_doh",
			variant:   "p0",
			proto:     DoH,
			upstreams: []string{"https://freedns.controld.com/p0"},
			bootstrap: freeBoot,
		},
		{
			name:      "free_p1_dot",
			variant:   "p1",
			proto:     DoT,
			upstreams: []string{"tls://p1.freedns.controld.com:853"},
			bootstrap: freeBoot,
		},
		{
			name:      "free_x_oisd",
			variant:   "x-oisd",
			proto:     DoH,
			upstreams: []string{"https://freedns.controld.com/x-oisd"},
			bootstrap: freeBoot,
		},
		{
			name:      "free_x_hagezi_via_id",
			variant:   "x-hagezi",
			proto:     DoH,
			id:        "x-hagezi-pro",
			upstreams: []string{"https://freedns.controld.com/x-hagezi-pro"},
			bootstrap: freeBoot,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ep, err := Expand(Selection{Provider: ControlD, Variant: tc.variant, Protocol: tc.proto, ID: tc.id})
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			if !slices.Equal(ep.Upstreams, tc.upstreams) {
				t.Errorf("upstreams = %v, want %v", ep.Upstreams, tc.upstreams)
			}
			if !slices.Equal(ep.Bootstrap, tc.bootstrap) {
				t.Errorf("bootstrap = %v, want %v", ep.Bootstrap, tc.bootstrap)
			}
		})
	}

	if _, err := Expand(Selection{Provider: ControlD, Variant: "custom"}); !errors.Is(err, ErrIDRequired) {
		t.Errorf("custom without id: got %v, want ErrIDRequired", err)
	}
	if _, err := Expand(Selection{Provider: ControlD, Variant: "x-hagezi"}); !errors.Is(err, ErrIDRequired) {
		t.Errorf("x-hagezi without id: got %v, want ErrIDRequired", err)
	}
}

func TestExpandCustomAndErrors(t *testing.T) {
	ep, err := Expand(Selection{
		Provider:  Custom,
		Protocol:  DoT,
		URL:       "tls://1.1.1.1:853",
		Bootstrap: []string{"1.1.1.1"},
	})
	if err != nil {
		t.Fatalf("Expand custom: %v", err)
	}
	if !slices.Equal(ep.Upstreams, []string{"tls://1.1.1.1:853"}) || !slices.Equal(ep.Bootstrap, []string{"1.1.1.1"}) {
		t.Errorf("custom endpoints = %+v", ep)
	}

	if _, err = Expand(Selection{Provider: Custom}); !errors.Is(err, ErrURLRequired) {
		t.Errorf("custom without URL: got %v, want ErrURLRequired", err)
	}
	if _, err = Expand(Selection{Provider: "nope"}); !errors.Is(err, ErrUnknownProvider) {
		t.Errorf("unknown provider: got %v, want ErrUnknownProvider", err)
	}
	if _, err = Expand(Selection{Provider: Quad9, Protocol: "bogus"}); !errors.Is(err, ErrUnknownProtocol) {
		t.Errorf("unknown protocol: got %v, want ErrUnknownProtocol", err)
	}
}

// TestExtractID covers NextDNS/ControlD across all protocols plus the error
// cases, and asserts no error string ever carries the id or the URL.
func TestExtractID(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		proto     Protocol
		upstreams []string
		want      string
		wantErr   error
	}{
		{"nextdns_doh", NextDNS, DoH, []string{"https://dns.nextdns.io/abc123"}, "abc123", nil},
		{"nextdns_dot", NextDNS, DoT, []string{"tls://abc123.dns.nextdns.io:853"}, "abc123", nil},
		{"nextdns_doq", NextDNS, DoQ, []string{"quic://abc123.dns.nextdns.io:853"}, "abc123", nil},
		{"nextdns_doh3", NextDNS, DoH3, []string{"h3://dns.nextdns.io/abc123"}, "abc123", nil},
		{"nextdns_doh_urlencoded", NextDNS, DoH, []string{"https://dns.nextdns.io/ab%63"}, "abc", nil},
		{"controld_doh", ControlD, DoH, []string{"https://dns.controld.com/xyz789"}, "xyz789", nil},
		{"controld_doh3", ControlD, DoH3, []string{"h3://dns.controld.com/xyz789"}, "xyz789", nil},
		{"controld_dot", ControlD, DoT, []string{"tls://xyz789.dns.controld.com:853"}, "xyz789", nil},
		{"controld_doq", ControlD, DoQ, []string{"quic://xyz789.dns.controld.com:853"}, "xyz789", nil},
		{"nextdns_ambiguous", NextDNS, DoH, []string{
			"https://dns.nextdns.io/aaa",
			"https://dns.nextdns.io/bbb",
		}, "", ErrAmbiguousID},
		{"controld_ambiguous", ControlD, DoT, []string{
			"tls://aaa.dns.controld.com:853",
			"tls://bbb.dns.controld.com:853",
		}, "", ErrAmbiguousID},
		{"controld_freedns_doh", ControlD, DoH, []string{"https://freedns.controld.com/p0"}, "", ErrNoID},
		{"controld_freedns_dot", ControlD, DoT, []string{"tls://p1.freedns.controld.com:853"}, "", ErrNoID},
		{"quad9", Quad9, DoH, []string{"https://dns.quad9.net/dns-query"}, "", ErrNoID},
		{"cloudflare_doh", Cloudflare, DoH, []string{"https://cloudflare-dns.com/dns-query"}, "", ErrNoID},
		{"cloudflare_dot", Cloudflare, DoT, []string{"tls://1dot1dot1dot1.cloudflare-dns.com:853"}, "", ErrNoID},
		{"cloudflare_doq", Cloudflare, DoQ, []string{"quic://cloudflare-dns.com:853"}, "", ErrNoID},
		{"cloudflare_security", Cloudflare, DoH, []string{"https://security.cloudflare-dns.com/dns-query"}, "", ErrNoID},
		{"empty", NextDNS, DoH, nil, "", ErrNoID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExtractID(tc.provider, tc.proto, tc.upstreams)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ExtractID error = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("ExtractID = %q, want %q", got, tc.want)
			}

			// No error may echo the id or an upstream URL.
			if err != nil {
				for _, secret := range append([]string{tc.want}, tc.upstreams...) {
					if secret != "" && strings.Contains(err.Error(), secret) {
						t.Fatalf("error leaks %q: %v", secret, err)
					}
				}
			}
		})
	}
}

// TestExtractIDs covers the never-abort collector: all candidates are kept
// (H2), metadata may be absent, the protocol comes from the scheme, and
// public/id-less tokens are never seeded.
func TestExtractIDs(t *testing.T) {
	cases := []struct {
		name      string
		provider  string
		proto     Protocol
		upstreams []string
		want      []string
	}{
		{"ambiguous kept", NextDNS, DoH, []string{
			"https://dns.nextdns.io/aaa111",
			"https://dns.nextdns.io/bbb222",
		}, []string{"aaa111", "bbb222"}},
		{"metadata absent doh", "", "", []string{"https://dns.nextdns.io/abc123"}, []string{"abc123"}},
		{"metadata absent h3", "", "", []string{"h3://dns.nextdns.io/abc123"}, []string{"abc123"}},
		{"metadata absent dot", "", "", []string{"tls://abc123.dns.nextdns.io:853"}, []string{"abc123"}},
		{"scheme overrides wrong proto", NextDNS, DoH, []string{"tls://abc123.dns.nextdns.io:853"}, []string{"abc123"}},
		{"custom provider known dot host", Custom, DoT, []string{"tls://abc123.dns.nextdns.io:853"}, []string{"abc123"}},
		{"mismatched provider cross dot host", NextDNS, DoT, []string{"tls://xyz789.dns.controld.com:853"}, []string{"xyz789"}},
		{"custom host doh path", NextDNS, DoH, []string{"https://custom.example.com/secret42"}, []string{"secret42"}},
		{"custom host h3 path", "", "", []string{"h3://custom.example.com/secret42"}, []string{"secret42"}},
		{"freedns not seeded", ControlD, DoH, []string{"https://freedns.controld.com/p0"}, nil},
		{"public endpoint not seeded", "", "", []string{"https://dns.quad9.net/dns-query"}, nil},
		{"deduped", NextDNS, DoH, []string{
			"https://dns.nextdns.io/dup123",
			"tls://dup123.dns.nextdns.io:853",
		}, []string{"dup123"}},
		{"short provider id not seeded", NextDNS, DoH, []string{"https://dns.nextdns.io/a"}, nil},
		{"three char provider id not seeded", NextDNS, DoT, []string{"tls://abc.dns.nextdns.io:853"}, nil},
		{"six char provider id seeded", NextDNS, DoH, []string{"https://dns.nextdns.io/abcdef"}, []string{"abcdef"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractIDs(tc.provider, tc.proto, tc.upstreams)
			if len(got) != len(tc.want) {
				t.Fatalf("ExtractIDs = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("ExtractIDs = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// TestProtocolForURL pins the scheme->transport map shared by the custom-URL
// validation in the elevated helper and the GUI's draft-to-patch adapter.
func TestProtocolForURL(t *testing.T) {
	cases := []struct {
		url  string
		want Protocol
		ok   bool
	}{
		{"https://dns.quad9.net/dns-query", DoH, true},
		{" h3://dns.quad9.net/dns-query ", DoH3, true},
		{"tls://dns.quad9.net:853", DoT, true},
		{"quic://dns.quad9.net:853", DoQ, true},
		{"sdns://AQcAAAAA", "", false},
		{"dot://dns.quad9.net:853", "", false}, // metadata alias, not fetchable
		{"", "", false},
	}

	for _, tc := range cases {
		got, ok := ProtocolForURL(tc.url)
		if got != tc.want || ok != tc.ok {
			t.Errorf("ProtocolForURL(%q) = (%q, %v), want (%q, %v)", tc.url, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRegistryECSMarked(t *testing.T) {
	p, ok := Lookup(Quad9)
	if !ok {
		t.Fatal("quad9 missing from registry")
	}

	ecs := map[string]bool{}
	for _, v := range p.Variants {
		ecs[v.Name] = v.ECSLeak
	}
	if !ecs["secure-ecs"] || !ecs["noblock-ecs"] {
		t.Error("ECS variants not marked as leaking")
	}
	if ecs["secure"] || ecs["noblock"] {
		t.Error("non-ECS variants incorrectly marked as leaking")
	}
}
