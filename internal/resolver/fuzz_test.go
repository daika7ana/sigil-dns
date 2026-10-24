package resolver

import (
	"testing"

	"github.com/AdguardTeam/dnsproxy/upstream"
)

// FuzzPerDomainRules feeds arbitrary domains and upstreams through the same
// renderer and dnsproxy parser the service uses.  Only a panic fails; every
// malformed rule is allowed to be rejected.  Seeds run as ordinary tests,
// `go test -fuzz=FuzzPerDomainRules ./internal/resolver/` explores more.
func FuzzPerDomainRules(f *testing.F) {
	for _, seed := range [][2]string{
		{"example.com", "tls://1.1.1.1"},
		{"", "#"},
		{"*.example.com", "https://dns.example/dns-query"},
		{"bad/domain", "1.1.1.1"},
	} {
		f.Add(seed[0], seed[1])
	}

	f.Fuzz(func(t *testing.T, domain, target string) {
		rule := Rule{Domains: []string{domain}, Upstream: target}
		if _, err := rulesToLines([]Rule{rule}); err != nil {
			return
		}

		conf, err := parseRules([]Rule{rule}, &upstream.Options{})
		if err != nil {
			return
		}
		_ = conf.Close()
	})
}
