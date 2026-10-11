package providers

import (
	"errors"
	"slices"
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
