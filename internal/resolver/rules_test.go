package resolver

import (
	"context"
	"log/slog"
	"net"
	"testing"

	"github.com/AdguardTeam/dnsproxy/proxy"
	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/miekg/dns"
)

func TestRulesToLines(t *testing.T) {
	lines, err := rulesToLines([]Rule{
		{Domains: []string{"a.test", "b.test"}, Upstream: "1.1.1.1"},
		{Domains: []string{"excl.test"}, Upstream: "#"},
		{Domains: []string{""}, Upstream: "2.2.2.2"},
		{Domains: []string{"*.sub.test"}, Upstream: "3.3.3.3"},
		{Domains: []string{"trailing.test."}, Upstream: "4.4.4.4"},
	})
	if err != nil {
		t.Fatalf("rulesToLines: %v", err)
	}

	want := []string{
		"[/a.test/b.test/]1.1.1.1",
		"[/excl.test/]#",
		"[//]2.2.2.2",
		"[/*.sub.test/]3.3.3.3",
		"[/trailing.test/]4.4.4.4",
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %v, want %v", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}

	bad := []struct {
		name string
		rule Rule
	}{
		{"empty upstream", Rule{Domains: []string{"a.test"}}},
		{"no domains", Rule{Upstream: "1.1.1.1"}},
		{"domain with slash", Rule{Domains: []string{"a/b.test"}, Upstream: "1.1.1.1"}},
	}
	for _, tc := range bad {
		if _, err = rulesToLines([]Rule{tc.rule}); err == nil {
			t.Errorf("%s: want error", tc.name)
		}
	}
}

// TestParseRulesShape checks the generated proxy.UpstreamConfig maps.  Semantics
// are dnsproxy's; we only assert we build the maps the way dnsproxy does.
func TestParseRulesShape(t *testing.T) {
	conf, err := parseRules([]Rule{
		{Domains: []string{"a.test"}, Upstream: "127.0.0.1:5353"},
		{Domains: []string{"*.sub.test"}, Upstream: "127.0.0.1:5353"},
		{Domains: []string{"excl.test"}, Upstream: "#"},
		{Domains: []string{""}, Upstream: "127.0.0.1:5354"},
	}, &upstream.Options{})
	if err != nil {
		t.Fatalf("parseRules: %v", err)
	}
	t.Cleanup(func() { _ = conf.Close() })

	if len(conf.Upstreams) != 0 {
		t.Errorf("default Upstreams should be empty, got %d", len(conf.Upstreams))
	}

	// Exact domain is both reserved and specified.
	if _, ok := conf.DomainReservedUpstreams["a.test."]; !ok {
		t.Error("a.test. missing from DomainReservedUpstreams")
	}
	if _, ok := conf.SpecifiedDomainUpstreams["a.test."]; !ok {
		t.Error("a.test. missing from SpecifiedDomainUpstreams")
	}

	// Wildcard maps to the stripped base and marks a subdomain exclusion, and
	// does not populate SpecifiedDomainUpstreams.
	if !conf.SubdomainExclusions.Has("sub.test.") {
		t.Error("sub.test. missing from SubdomainExclusions")
	}
	if _, ok := conf.DomainReservedUpstreams["sub.test."]; !ok {
		t.Error("sub.test. missing from DomainReservedUpstreams")
	}
	if _, ok := conf.SpecifiedDomainUpstreams["sub.test."]; ok {
		t.Error("wildcard should not add a specified-domain entry")
	}

	// "#" is an exclusion: present but with no upstreams.
	ups, ok := conf.DomainReservedUpstreams["excl.test."]
	if !ok {
		t.Fatal("excl.test. not reserved")
	}
	if len(ups) != 0 {
		t.Errorf("excluded domain should have no upstreams, got %d", len(ups))
	}

	// Unqualified names use dnsproxy's sentinel key.
	if _, ok := conf.DomainReservedUpstreams[proxy.UnqualifiedNames]; !ok {
		t.Errorf("%q missing from DomainReservedUpstreams", proxy.UnqualifiedNames)
	}
}

// startFakeUpstream starts a UDP DNS server that answers every query with an A
// record equal to ip (the question is echoed, so this also identifies which
// upstream answered a DS query).
func startFakeUpstream(t *testing.T, ip string) (addr string) {
	t.Helper()

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake upstream: %v", err)
	}

	srv := &dns.Server{
		PacketConn: pc,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
			resp := &dns.Msg{}
			resp.SetReply(req)
			resp.Answer = append(resp.Answer, &dns.A{
				Hdr: dns.RR_Header{
					Name:   req.Question[0].Name,
					Rrtype: dns.TypeA,
					Class:  dns.ClassINET,
					Ttl:    0,
				},
				A: net.ParseIP(ip).To4(),
			})
			_ = w.WriteMsg(resp)
		}),
	}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	return pc.LocalAddr().String()
}

// firstA returns the address of the first A record in resp.
func firstA(t *testing.T, resp *dns.Msg) (ip string) {
	t.Helper()

	if len(resp.Answer) == 0 {
		t.Fatalf("no answer: %s", resp)
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok {
		t.Fatalf("first answer is %T, want *dns.A", resp.Answer[0])
	}

	return a.A.String()
}

func TestRulesRoutingEndToEnd(t *testing.T) {
	const (
		ipDefault = "192.0.2.1"
		ipRule    = "192.0.2.2"
	)

	addrDefault := startFakeUpstream(t, ipDefault)
	addrRule := startFakeUpstream(t, ipRule)

	r, err := New(Config{
		Logger:    slog.Default(),
		Upstreams: []string{addrDefault},
		Rules: []Rule{
			{Domains: []string{"tm.test"}, Upstream: addrRule},
			{Domains: []string{"excluded.tm.test"}, Upstream: "#"},
			{Domains: []string{"*.sub.test"}, Upstream: addrRule},
			{Domains: []string{""}, Upstream: addrRule},
			{Domains: []string{"secure.ds.test"}, Upstream: addrRule},
		},
		Listen: []string{"127.0.0.1:0"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	if err = r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	addrs := r.UDPAddrs()
	if len(addrs) != 1 {
		t.Fatalf("UDPAddrs = %v", addrs)
	}
	addr := addrs[0]

	cases := []struct {
		name  string
		qname string
		qtype uint16
		want  string
	}{
		{"non-matching uses default", "nomatch.test.", dns.TypeA, ipDefault},
		{"matching domain uses rule", "other.tm.test.", dns.TypeA, ipRule},
		{"exact rule domain uses rule", "tm.test.", dns.TypeA, ipRule},
		{"# exclusion falls back to default", "excluded.tm.test.", dns.TypeA, ipDefault},
		{"subdomain of wildcard uses rule", "x.sub.test.", dns.TypeA, ipRule},
		{"base of wildcard is not matched", "sub.test.", dns.TypeA, ipDefault},
		{"unqualified name uses rule", "unqualified.", dns.TypeA, ipRule},
		{"exact ds zone A uses rule", "secure.ds.test.", dns.TypeA, ipRule},
		{"DS query uses parent (default)", "secure.ds.test.", dns.TypeDS, ipDefault},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := query(t, addr, tc.qname, tc.qtype)
			if got := firstA(t, resp); got != tc.want {
				t.Errorf("%s %s: answered by %s, want %s", tc.qname, dns.TypeToString[tc.qtype], got, tc.want)
			}
		})
	}
}
