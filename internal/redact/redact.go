// Package redact reduces DNS upstream URLs to their non-secret parts so they
// can be logged safely.  Resolver ids (NextDNS/ControlD profile ids) live in the
// URL path (DoH) or as a hostname label (DoT/DoQ), so anything beyond scheme +
// host is treated as sensitive.
package redact

import (
	"net/url"
	"strings"
)

// Upstream returns u reduced to scheme + host, with path, query, fragment, and
// userinfo removed (e.g. "https://dns.nextdns.io/<id>" -> "https://dns.nextdns.io").
//
// A bare "host:port" is returned with any trailing path removed.  sdns:// stamps
// are opaque secrets and are fully redacted.
func Upstream(u string) (s string) {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}

	if !strings.Contains(u, "://") {
		// Bare host:port (or IP:port); drop any defensive trailing path.
		host, _, _ := strings.Cut(u, "/")

		return host
	}

	parsed, err := url.Parse(u)
	if err != nil {
		// Fall back to a conservative manual strip.
		scheme, rest, _ := strings.Cut(u, "://")
		host, _, _ := strings.Cut(rest, "/")

		return scheme + "://" + host
	}

	if parsed.Scheme == "sdns" {
		// DNS stamps embed keys/addresses; never emit their contents.
		return "sdns://<redacted>"
	}

	if parsed.Host == "" {
		return parsed.Scheme + "://"
	}

	return parsed.Scheme + "://" + parsed.Host
}

// List redacts each upstream and comma-joins the results.
func List(upstreams []string) (s string) {
	out := make([]string, 0, len(upstreams))
	for _, u := range upstreams {
		if r := Upstream(u); r != "" {
			out = append(out, r)
		}
	}

	return strings.Join(out, ",")
}
