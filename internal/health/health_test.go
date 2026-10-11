package health

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestRandomNameUnique(t *testing.T) {
	a, b := RandomName(), RandomName()
	if a == b {
		t.Fatalf("RandomName not unique: %q", a)
	}
	if a[len(a)-1] != '.' {
		t.Fatalf("RandomName not FQDN: %q", a)
	}
}

func TestIsIPv4(t *testing.T) {
	if !isIPv4("127.0.0.1:53") {
		t.Error("127.0.0.1 should be IPv4")
	}
	if isIPv4("[::1]:53") {
		t.Error("::1 should not be IPv4")
	}
}

// TestCheckSyntheticNotOurs verifies that stage A fails when the endpoint does
// not answer the per-process synthetic name (i.e. it is some other resolver).
func TestCheckSyntheticNotOurs(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &dns.Server{
		PacketConn: pc,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
			resp := &dns.Msg{}
			resp.SetRcode(req, dns.RcodeNameError)
			_ = w.WriteMsg(resp)
		}),
	}
	go func() { _ = srv.ActivateAndServe() }()
	t.Cleanup(func() { _ = srv.Shutdown() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	r := Check(ctx, Target{
		Synthetic: "deadbeef.dns-client-probe.invalid.",
		UDPAddrs:  []string{pc.LocalAddr().String()},
	})
	if r.Ours {
		t.Fatalf("Ours = true, want false: %+v", r)
	}
	if r.OK {
		t.Fatalf("OK = true, want false: %+v", r)
	}
}
