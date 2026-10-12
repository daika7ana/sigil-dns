package filter

import (
	"net/netip"
	"testing"

	"github.com/AdguardTeam/urlfilter"
	"github.com/miekg/dns"
)

func newTestEngine(t *testing.T, text string) (e *Engine) {
	t.Helper()

	e, err := New([]RawList{{Text: []byte(text)}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })

	return e
}

func TestHostsRuleBlocks(t *testing.T) {
	e := newTestEngine(t, "0.0.0.0 blocked.test\n")

	res := e.Match("blocked.test.", dns.TypeA)
	if res.Action != ActionHosts {
		t.Fatalf("Action = %v, want ActionHosts", res.Action)
	}
	if len(res.Hosts) != 1 || res.Hosts[0] != netip.MustParseAddr("0.0.0.0") {
		t.Fatalf("Hosts = %v, want [0.0.0.0]", res.Hosts)
	}

	if got := e.Match("other.test.", dns.TypeA); got.Action != ActionNone {
		t.Fatalf("non-matching hosts: Action = %v, want ActionNone", got.Action)
	}
}

func TestAdblockRuleBlocks(t *testing.T) {
	e := newTestEngine(t, "||ads.test^\n")

	if got := e.Match("ads.test.", dns.TypeA); got.Action != ActionBlock {
		t.Fatalf("Action = %v, want ActionBlock", got.Action)
	}
	if got := e.Match("sub.ads.test.", dns.TypeA); got.Action != ActionBlock {
		t.Fatalf("subdomain: Action = %v, want ActionBlock", got.Action)
	}
	if got := e.Match("notads.test.", dns.TypeA); got.Action != ActionNone {
		t.Fatalf("non-matching: Action = %v, want ActionNone", got.Action)
	}
}

func TestExceptionRuleAllows(t *testing.T) {
	e := newTestEngine(t, "@@||ads.test^\n||ads.test^\n")

	if got := e.Match("ads.test.", dns.TypeA); got.Action != ActionAllow {
		t.Fatalf("Action = %v, want ActionAllow", got.Action)
	}
}

// TestDNSRewriteAppliedEvenThoughNotMatched checks the urlfilter subtlety: the
// matched return value is false for rewrite-only matches, so DNSRewrites must be
// inspected regardless.
func TestDNSRewriteAppliedEvenThoughNotMatched(t *testing.T) {
	e := newTestEngine(t, "||rw.test^$dnsrewrite=1.2.3.4\n")

	// Sanity: the raw engine reports not-matched for a rewrite-only rule.
	if _, matched := e.eng.MatchRequest(&urlfilter.DNSRequest{Hostname: "rw.test", DNSType: dns.TypeA}); matched {
		t.Fatal("expected matched=false for rewrite-only rule")
	}

	res := e.Match("rw.test.", dns.TypeA)
	if res.Action != ActionRewrite {
		t.Fatalf("Action = %v, want ActionRewrite", res.Action)
	}
	if res.Rewrite == nil || res.Rewrite.RCode != dns.RcodeSuccess {
		t.Fatalf("Rewrite = %+v", res.Rewrite)
	}
	if res.Rewrite.RRType != dns.TypeA {
		t.Fatalf("RRType = %d, want A", res.Rewrite.RRType)
	}
	ip, ok := res.Rewrite.Value.(netip.Addr)
	if !ok || ip != netip.MustParseAddr("1.2.3.4") {
		t.Fatalf("Value = %v, want 1.2.3.4", res.Rewrite.Value)
	}
}

func TestNoMatch(t *testing.T) {
	e := newTestEngine(t, "||ads.test^\n0.0.0.0 hosts.test\n")

	if got := e.Match("clean.test.", dns.TypeA); got.Action != ActionNone {
		t.Fatalf("Action = %v, want ActionNone", got.Action)
	}
}
