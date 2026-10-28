package resolver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AdguardTeam/dnsproxy/upstream"
	"github.com/daika7ana/sigil-dns/internal/filter"
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

	r, err := New(context.Background(), Config{
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

	r, err := New(context.Background(), Config{
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

// TestStatsAndFilteringPause proves the counters track every non-probe query
// (blocked included) and that a pause skips only the filter stage: the same
// blocked name resolves through the upstream while the pause is active.
func TestStatsAndFilteringPause(t *testing.T) {
	upstreamAddr, _ := startUpstream(t, "192.0.2.1")

	listPath := filepath.Join(t.TempDir(), "block.txt")
	if err := os.WriteFile(listPath, []byte("0.0.0.0 blocked.example\n"), 0o600); err != nil {
		t.Fatalf("write list: %v", err)
	}

	r, err := New(context.Background(), Config{
		Logger:    slog.Default(),
		Upstreams: []string{upstreamAddr},
		Listen:    []string{"127.0.0.1:0"},
		Filter: FilterConfig{
			Enabled:       true,
			Lists:         []filter.List{{Source: listPath}},
			BlockResponse: BlockResponseZero,
			TTL:           60,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx := context.Background()
	if err = r.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	addr := r.UDPAddrs()[0]

	// The health probe is not user traffic and must not move the counter.
	query(t, addr, r.SyntheticName(), dns.TypeA)
	if s := r.Stats(); s.Queries != 0 || s.Blocked != 0 {
		t.Fatalf("probe counted: %+v", s)
	}

	resp := query(t, addr, "blocked.example.", dns.TypeA)
	if len(resp.Answer) != 1 {
		t.Fatalf("blocked query answers = %v", resp.Answer)
	}
	if s := r.Stats(); s.Queries != 1 || s.Blocked != 1 || !s.PausedUntil.IsZero() {
		t.Fatalf("stats after blocked query = %+v", s)
	}

	r.PauseFiltering(time.Minute)
	if s := r.Stats(); s.PausedUntil.IsZero() || !s.PausedUntil.After(time.Now()) {
		t.Fatalf("pause not recorded: %+v", s)
	}

	// The same name now reaches the upstream: only filtering was skipped.
	resp = query(t, addr, "blocked.example.", dns.TypeA)
	a, ok := resp.Answer[0].(*dns.A)
	if !ok || !a.A.Equal(net.ParseIP("192.0.2.1")) {
		t.Fatalf("paused query was still filtered: %v", resp.Answer)
	}
	if s := r.Stats(); s.Queries != 2 || s.Blocked != 1 {
		t.Fatalf("stats after paused query = %+v", s)
	}

	// Resume early: the same name is filtered again before the interval ends.
	r.ResumeFiltering()
	if s := r.Stats(); !s.PausedUntil.IsZero() {
		t.Fatalf("resume did not clear the pause: %+v", s)
	}
	resp = query(t, addr, "blocked.example.", dns.TypeA)
	a, ok = resp.Answer[0].(*dns.A)
	if !ok || !a.A.IsUnspecified() {
		t.Fatalf("query after resume was not filtered: %v", resp.Answer)
	}
	if s := r.Stats(); s.Queries != 3 || s.Blocked != 2 {
		t.Fatalf("stats after resumed query = %+v", s)
	}
}

// trackingUpstream records whether a failed construction closed it.
type trackingUpstream struct {
	addr   string
	closed atomic.Bool
}

func (u *trackingUpstream) Exchange(context.Context, *dns.Msg) (*dns.Msg, error) {
	return nil, fmt.Errorf("unexpected exchange on %s", u.addr)
}

func (u *trackingUpstream) Address() string { return u.addr }

func (u *trackingUpstream) Close() error {
	u.closed.Store(true)

	return nil
}

// stubUpstreams replaces the upstream constructor for the duration of a test.
func stubUpstreams(t *testing.T, ups ...*trackingUpstream) {
	t.Helper()

	byAddr := make(map[string]*trackingUpstream, len(ups))
	for _, u := range ups {
		byAddr[u.addr] = u
	}

	prev := newUpstream
	newUpstream = func(addr string, _ *upstream.Options) (upstream.Upstream, error) {
		u, ok := byAddr[addr]
		if !ok {
			return nil, fmt.Errorf("unexpected upstream %q", addr)
		}

		return u, nil
	}
	t.Cleanup(func() { newUpstream = prev })
}

// stubUpstreamError makes the constructor return u together with err, the
// partially-built-upstream case that must be closed.
func stubUpstreamError(t *testing.T, u *trackingUpstream, err error) {
	t.Helper()

	prev := newUpstream
	newUpstream = func(addr string, _ *upstream.Options) (upstream.Upstream, error) {
		if addr == u.addr {
			return u, err
		}

		return nil, fmt.Errorf("unexpected upstream %q", addr)
	}
	t.Cleanup(func() { newUpstream = prev })
}

func TestNewClosesUpstreamReturnedWithError(t *testing.T) {
	u := &trackingUpstream{addr: "bad-main.test"}
	stubUpstreamError(t, u, fmt.Errorf("partial construction"))

	_, err := New(context.Background(), Config{Upstreams: []string{u.addr}})
	if err == nil {
		t.Fatal("New should fail when the upstream constructor returns an error")
	}
	if !u.closed.Load() {
		t.Fatal("upstream returned with an error was not closed")
	}
}

func TestNewClosesBootstrapResolverReturnedWithError(t *testing.T) {
	u := &trackingUpstream{addr: "bad-bootstrap.test"}

	prev := newBootstrapResolver
	newBootstrapResolver = func(string, *upstream.Options) (*upstream.UpstreamResolver, error) {
		return &upstream.UpstreamResolver{Upstream: u}, fmt.Errorf("not a bootstrap")
	}
	t.Cleanup(func() { newBootstrapResolver = prev })

	_, err := New(context.Background(), Config{Bootstrap: []string{u.addr}})
	if err == nil {
		t.Fatal("New should fail when the bootstrap constructor returns an error")
	}
	if !u.closed.Load() {
		t.Fatal("bootstrap resolver returned with an error was not closed")
	}
}

// TestNewClosesUpstreamsOnRuleError: a rule-parse failure must not leave the
// main and fallback upstreams built before it.
func TestNewClosesUpstreamsOnRuleError(t *testing.T) {
	mainUp := &trackingUpstream{addr: "main.test"}
	fallbackUp := &trackingUpstream{addr: "fallback.test"}
	stubUpstreams(t, mainUp, fallbackUp)

	_, err := New(context.Background(), Config{
		Upstreams:         []string{mainUp.addr},
		FallbackUpstreams: []string{fallbackUp.addr},
		Rules:             []Rule{{Domains: []string{"bad/domain"}, Upstream: "1.1.1.1"}},
	})
	if err == nil {
		t.Fatal("New should fail on a rule domain containing a slash")
	}
	if !mainUp.closed.Load() {
		t.Error("main upstream was not closed")
	}
	if !fallbackUp.closed.Load() {
		t.Error("fallback upstream was not closed")
	}
}

// TestNewClosesUpstreamsOnListError: a filter-list load failure must close the
// main, fallback, and per-domain upstream configurations built before it.
func TestNewClosesUpstreamsOnListError(t *testing.T) {
	mainUp := &trackingUpstream{addr: "main.test"}
	fallbackUp := &trackingUpstream{addr: "fallback.test"}
	stubUpstreams(t, mainUp, fallbackUp)

	_, err := New(context.Background(), Config{
		Upstreams:         []string{mainUp.addr},
		FallbackUpstreams: []string{fallbackUp.addr},
		Rules:             []Rule{{Domains: []string{"rule.test"}, Upstream: "1.1.1.1"}},
		Filter: FilterConfig{
			Enabled: true,
			Lists:   []filter.List{{Source: filepath.Join(t.TempDir(), "missing.txt")}},
		},
	})
	if err == nil {
		t.Fatal("New should fail when a filter list cannot be read")
	}
	if !mainUp.closed.Load() {
		t.Error("main upstream was not closed")
	}
	if !fallbackUp.closed.Load() {
		t.Error("fallback upstream was not closed")
	}
}
