package intercept

import (
	"context"
	"net"
	"testing"

	"github.com/miekg/dns"
)

// TestProbePlainDNSReachable: any response, even SERVFAIL, proves the plain-DNS
// path is reachable.
func TestProbePlainDNSReachable(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := pc.LocalAddr().String()

	srv := &dns.Server{
		PacketConn: pc,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
			m := new(dns.Msg)
			m.SetRcode(r, dns.RcodeServerFailure)
			_ = w.WriteMsg(m)
		}),
	}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	got := probePlainDNS(context.Background(), []string{addr})
	if len(got) != 1 || got[0] != addr {
		t.Fatalf("probePlainDNS(%s) = %v, want [%s]", addr, got, addr)
	}
}

// TestProbePlainDNSUnreachable: a closed port must not be reported as answering.
func TestProbePlainDNSUnreachable(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := pc.LocalAddr().String()
	if err = pc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if got := probePlainDNS(context.Background(), []string{addr}); len(got) != 0 {
		t.Fatalf("probePlainDNS(%s) = %v, want none", addr, got)
	}
}
