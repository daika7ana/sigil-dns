package redact

import (
	"strings"
	"testing"
)

func TestUpstream(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"doh path id", "https://dns.nextdns.io/SECRET123", "https://dns.nextdns.io"},
		{"doh path + device", "https://dns.nextdns.io/SECRET123/device", "https://dns.nextdns.io"},
		{"doh query and fragment", "https://dns.quad9.net/dns-query?x=1#frag", "https://dns.quad9.net"},
		{"userinfo stripped", "https://user:pass@dns.quad9.net/dns-query", "https://dns.quad9.net"},
		{"dot", "tls://dns.quad9.net:853", "tls://dns.quad9.net:853"},
		{"doq", "quic://dns.quad9.net:853", "quic://dns.quad9.net:853"},
		{"h3", "h3://dns.google/dns-query", "h3://dns.google"},
		{"bare hostport", "1.1.1.1:53", "1.1.1.1:53"},
		{"bare hostpath", "127.0.0.1:5353/whatever", "127.0.0.1:5353"},
		{"sdns stamp fully redacted", "sdns://AQcAAAAAAAAADS4xMS4xMS4xMS4xMQ", "sdns://<redacted>"},
		{"trimmed", "  https://dns.nextdns.io/SECRET123  ", "https://dns.nextdns.io"},
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Upstream(tc.in)
			if got != tc.want {
				t.Fatalf("Upstream(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// The planted id must never survive redaction.
			if strings.Contains(got, "SECRET123") || strings.Contains(got, "AQcAAAAAAAAADS4x") {
				t.Fatalf("redacted output leaked a secret: %q", got)
			}
		})
	}
}

func TestList(t *testing.T) {
	got := List([]string{
		"https://dns.nextdns.io/SECRET123",
		"tls://dns.quad9.net:853",
		"",
	})
	want := "https://dns.nextdns.io,tls://dns.quad9.net:853"
	if got != want {
		t.Fatalf("List = %q, want %q", got, want)
	}
	if strings.Contains(got, "SECRET123") {
		t.Fatal("List leaked the resolver id")
	}
}

// TestUpstreamDoesNotLeakID is the explicit regression guard from the review:
// a planted NextDNS/ControlD id must never appear in redacted output.
func TestUpstreamDoesNotLeakID(t *testing.T) {
	const id = "SECRET123"

	inputs := []string{
		"https://dns.nextdns.io/" + id,
		"https://dns.controld.com/" + id,
		"https://user:" + id + "@dns.nextdns.io/" + id,
		id + ".dns.nextdns.io:853", // documented limitation (see note below)
	}

	// Only the DoH forms are guaranteed to hide the id (scheme+host). The DoT/DoQ
	// form carries the id as a hostname label and is included here only to show
	// the current, documented behaviour.
	for _, in := range inputs {
		out := Upstream(in)
		if !strings.Contains(in, "://") {
			// host-only form: redaction keeps the host; the id is intentionally
			// still present.  Assert nothing here.
			continue
		}
		if strings.Contains(out, id) {
			t.Fatalf("Upstream(%q) = %q still contains the id", in, out)
		}
	}
}
