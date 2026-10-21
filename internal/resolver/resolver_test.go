package resolver

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// startUpstream starts a plain UDP DNS server on an ephemeral port that answers
// names under .invalid. with NXDOMAIN (so a forwarded synthetic probe would
// fail) and every other name with an A record equal to ip.  hits counts every
// query that reached it.
func startUpstream(t *testing.T, ip string) (addr string, hits *atomic.Int32) {
	t.Helper()

	hits = &atomic.Int32{}

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen test upstream: %v", err)
	}

	srv := &dns.Server{
		PacketConn: pc,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
			hits.Add(1)

			resp := &dns.Msg{}
			if strings.HasSuffix(strings.ToLower(req.Question[0].Name), ".invalid.") {
				resp.SetRcode(req, dns.RcodeNameError)
				_ = w.WriteMsg(resp)

				return
			}

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

	return pc.LocalAddr().String(), hits
}

func query(t *testing.T, addr, name string, qtype uint16) (resp *dns.Msg) {
	t.Helper()

	c := &dns.Client{Net: "udp", Timeout: 5 * time.Second}
	m := new(dns.Msg)
	m.SetQuestion(name, qtype)

	resp, _, err := c.Exchange(m, addr)
	if err != nil {
		t.Fatalf("exchange %s: %v", name, err)
	}

	return resp
}

func TestResolverBindsEphemeralAndAnswers(t *testing.T) {
	upstreamAddr, _ := startUpstream(t, "192.0.2.1")

	r, err := New(Config{
		Logger:    slog.Default(),
		Upstreams: []string{upstreamAddr},
		Listen:    []string{"127.0.0.1:0"},
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
		t.Fatalf("UDPAddrs = %v, want 1", addrs)
	}

	resp := query(t, addrs[0], "example.com.", dns.TypeA)
	if resp.Rcode != dns.RcodeSuccess {
		t.Fatalf("rcode = %s, want NOERROR", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) == 0 {
		t.Fatal("no answer from upstream")
	}

	// The synthetic probe name is answered by our own handler, not forwarded
	// upstream (which would answer .invalid. with NXDOMAIN).
	resp = query(t, addrs[0], r.SyntheticName(), dns.TypeA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) == 0 {
		t.Fatalf("synthetic probe: rcode=%s answers=%d", dns.RcodeToString[resp.Rcode], len(resp.Answer))
	}
	a, ok := resp.Answer[0].(*dns.A)
	if !ok || a.A.String() != probeIPv4 {
		t.Fatalf("synthetic probe was not answered by the local handler: %v", resp.Answer)
	}

	// Shutdown must be clean and idempotent-safe (no double close).
	if err = r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestResolverFallsBackWhenPrimaryFails points the primary at a dead TCP port
// (instant refusal) and proves the query is answered by the fallback profile.
func TestResolverFallsBackWhenPrimaryFails(t *testing.T) {
	fallbackAddr, fallbackHits := startUpstream(t, "192.0.2.2")

	r, err := New(Config{
		Logger:            slog.Default(),
		Upstreams:         []string{"tcp://127.0.0.1:1"},
		FallbackUpstreams: []string{fallbackAddr},
		Listen:            []string{"127.0.0.1:0"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	if err = r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	resp := query(t, r.UDPAddrs()[0], "example.com.", dns.TypeA)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) == 0 {
		t.Fatalf("rcode = %s, answers = %d (want a fallback answer)",
			dns.RcodeToString[resp.Rcode], len(resp.Answer))
	}
	if fallbackHits.Load() == 0 {
		t.Fatal("fallback upstream was never queried")
	}
}

func TestIPv4Addrs(t *testing.T) {
	got := ipv4Addrs([]string{"127.0.0.1:53", "[::1]:53", "0.0.0.0:53"})
	want := []string{"127.0.0.1:53", "0.0.0.0:53"}
	if len(got) != len(want) {
		t.Fatalf("ipv4Addrs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ipv4Addrs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSyntheticNameUnique(t *testing.T) {
	a, b := SyntheticName(), SyntheticName()
	if a == b {
		t.Fatalf("SyntheticName not unique: %q", a)
	}
	if len(a) < 20 {
		t.Fatalf("SyntheticName too short: %q", a)
	}
}
