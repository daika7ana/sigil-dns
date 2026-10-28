package resolver

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/daika7ana/sigil-dns/internal/filter"
)

// writeList writes a filter list to a temp file and returns its path.
func writeList(t *testing.T, text string) (path string) {
	t.Helper()

	path = filepath.Join(t.TempDir(), "list.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("writing list: %v", err)
	}

	return path
}

// startFilteredResolver builds and starts a resolver with one inline list.
func startFilteredResolver(t *testing.T, upstreamAddr, listPath, blockResponse string) (r *Resolver, qaddr string) {
	t.Helper()

	r, err := New(context.Background(), Config{
		Logger:    slog.Default(),
		Upstreams: []string{upstreamAddr},
		Filter: FilterConfig{
			Enabled:       true,
			Lists:         []filter.List{{Source: listPath}},
			BlockResponse: blockResponse,
			TTL:           3600,
		},
		Listen: []string{"127.0.0.1:0"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err = r.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown(context.Background()) })

	addrs := r.UDPAddrs()
	if len(addrs) != 1 {
		t.Fatalf("UDPAddrs = %v", addrs)
	}

	return r, addrs[0]
}

// TestNewCancelsListLoading: the caller's context bounds the initial filter
// list download, so a canceled start cannot wait for the fetch timeout.
func TestNewCancelsListLoading(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() {
		_, err := New(ctx, Config{
			Logger:    slog.Default(),
			Upstreams: []string{"127.0.0.1:1"},
			Filter: FilterConfig{
				Enabled: true,
				Lists:   []filter.List{{Source: srv.URL}},
			},
			Listen: []string{"127.0.0.1:0"},
		})
		done <- err
	}()

	<-started
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("New succeeded with a canceled list download")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("New did not return after the context was canceled")
	}
}

func TestFilterEndToEnd(t *testing.T) {
	upstream, hits := startUpstream(t, "192.0.2.1")
	list := writeList(t, "||blocked.test^\n||rw.test^$dnsrewrite=1.2.3.4\n")

	r, qaddr := startFilteredResolver(t, upstream, list, BlockResponseZero)

	// Allowed name is forwarded.
	resp := query(t, qaddr, "allowed.test.", dns.TypeA)
	if got := firstA(t, resp); got != "192.0.2.1" {
		t.Errorf("allowed answer = %s, want 192.0.2.1", got)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("upstream hits = %d, want 1", n)
	}

	// Blocked name is answered locally and never forwarded.
	resp = query(t, qaddr, "blocked.test.", dns.TypeA)
	if got := firstA(t, resp); got != "0.0.0.0" {
		t.Errorf("blocked answer = %s, want 0.0.0.0", got)
	}
	if resp.RecursionAvailable != true {
		t.Error("blocked response RA not set")
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("blocked name reached upstream: hits = %d", n)
	}

	// $dnsrewrite is applied locally.
	resp = query(t, qaddr, "rw.test.", dns.TypeA)
	if got := firstA(t, resp); got != "1.2.3.4" {
		t.Errorf("rewrite answer = %s, want 1.2.3.4", got)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("rewrite name reached upstream: hits = %d", n)
	}

	// The synthetic probe still short-circuits before filtering.
	resp = query(t, qaddr, r.SyntheticName(), dns.TypeA)
	if got := firstA(t, resp); got != probeIPv4 {
		t.Errorf("synthetic answer = %s, want %s", got, probeIPv4)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("synthetic probe reached upstream: hits = %d", n)
	}
}

// TestHostsBlockAllQueryTypes: a hosts block (0.0.0.0) must not be bypassed by
// asking for another record type, as browsers do for AAAA/HTTPS.
func TestHostsBlockAllQueryTypes(t *testing.T) {
	upstream, hits := startUpstream(t, "192.0.2.1")
	list := writeList(t, "0.0.0.0 hosts-blocked.test\n")

	_, qaddr := startFilteredResolver(t, upstream, list, BlockResponseZero)

	resp := query(t, qaddr, "hosts-blocked.test.", dns.TypeA)
	if got := firstA(t, resp); got != "0.0.0.0" {
		t.Errorf("A answer = %s, want 0.0.0.0", got)
	}

	for _, qtype := range []uint16{dns.TypeAAAA, dns.TypeHTTPS} {
		resp = query(t, qaddr, "hosts-blocked.test.", qtype)
		if resp.Rcode != dns.RcodeSuccess {
			t.Errorf("qtype %d rcode = %s, want NOERROR", qtype, dns.RcodeToString[resp.Rcode])
		}
		if len(resp.Answer) != 0 {
			t.Errorf("qtype %d answer = %v, want empty", qtype, resp.Answer)
		}
	}

	if n := hits.Load(); n != 0 {
		t.Fatalf("hosts-blocked name reached upstream: hits = %d", n)
	}
}

// TestRewriteAnswersAllMatchingRewrites: multiple A rules produce all their
// addresses, mixed A/AAAA rules answer only the requested family, and a family
// with no rewrite gets an empty NOERROR instead of leaking upstream.
func TestRewriteAnswersAllMatchingRewrites(t *testing.T) {
	upstream, hits := startUpstream(t, "192.0.2.1")
	list := writeList(t, "||multi.test^$dnsrewrite=1.2.3.4\n"+
		"||multi.test^$dnsrewrite=1.2.3.5\n"+
		"||multi.test^$dnsrewrite=2001:db8::1\n")

	_, qaddr := startFilteredResolver(t, upstream, list, BlockResponseZero)

	resp := query(t, qaddr, "multi.test.", dns.TypeA)
	got := map[string]bool{}
	for _, rr := range resp.Answer {
		if a, ok := rr.(*dns.A); ok {
			got[a.A.String()] = true
		}
	}
	if len(got) != 2 || !got["1.2.3.4"] || !got["1.2.3.5"] {
		t.Fatalf("A answers = %v, want 1.2.3.4 and 1.2.3.5", got)
	}

	resp = query(t, qaddr, "multi.test.", dns.TypeAAAA)
	if len(resp.Answer) != 1 {
		t.Fatalf("AAAA answers = %v, want one", resp.Answer)
	}
	aaaa, ok := resp.Answer[0].(*dns.AAAA)
	if !ok || aaaa.AAAA.String() != "2001:db8::1" {
		t.Fatalf("AAAA answer = %v, want 2001:db8::1", resp.Answer[0])
	}

	// A type with no rewrite must not be forwarded to the real upstream.
	resp = query(t, qaddr, "multi.test.", dns.TypeHTTPS)
	if resp.Rcode != dns.RcodeSuccess || len(resp.Answer) != 0 {
		t.Fatalf("HTTPS answer = rcode %s, %v; want empty NOERROR",
			dns.RcodeToString[resp.Rcode], resp.Answer)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("rewritten name reached upstream: hits = %d", n)
	}
}

// TestRewriteDoesNotCountAsBlocked: a $dnsrewrite answer is a real answer, not
// a block, so it must not inflate the status page's blocked counter.
func TestRewriteDoesNotCountAsBlocked(t *testing.T) {
	upstream, _ := startUpstream(t, "192.0.2.1")
	list := writeList(t, "||rw.test^$dnsrewrite=1.2.3.4\n||blocked.test^\n")

	r, qaddr := startFilteredResolver(t, upstream, list, BlockResponseZero)

	resp := query(t, qaddr, "rw.test.", dns.TypeA)
	if got := firstA(t, resp); got != "1.2.3.4" {
		t.Fatalf("rewrite answer = %s, want 1.2.3.4", got)
	}
	if got := r.Stats().Blocked; got != 0 {
		t.Fatalf("rewrite counted as blocked: %d", got)
	}

	resp = query(t, qaddr, "blocked.test.", dns.TypeA)
	if got := firstA(t, resp); got != "0.0.0.0" {
		t.Fatalf("blocked answer = %s, want 0.0.0.0", got)
	}
	if got := r.Stats().Blocked; got != 1 {
		t.Fatalf("blocked count = %d, want 1", got)
	}
}

// TestRewriteAcceptsIPv4MappedAAAA: an explicit 128-bit rewrite value is a
// valid AAAA answer and must not turn into an empty response.
func TestRewriteAcceptsIPv4MappedAAAA(t *testing.T) {
	upstream, hits := startUpstream(t, "192.0.2.1")
	list := writeList(t, "||mapped.test^$dnsrewrite=::ffff:192.0.2.1\n")

	_, qaddr := startFilteredResolver(t, upstream, list, BlockResponseZero)

	resp := query(t, qaddr, "mapped.test.", dns.TypeAAAA)
	if len(resp.Answer) != 1 {
		t.Fatalf("AAAA answers = %v, want one", resp.Answer)
	}
	aaaa, ok := resp.Answer[0].(*dns.AAAA)
	if !ok {
		t.Fatalf("answer is %T, want *dns.AAAA", resp.Answer[0])
	}
	if !aaaa.AAAA.Equal(net.ParseIP("::ffff:192.0.2.1")) {
		t.Fatalf("AAAA = %s, want ::ffff:192.0.2.1", aaaa.AAAA)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("mapped rewrite reached upstream: hits = %d", n)
	}
}

func TestFilterHostsAndNXDOMAIN(t *testing.T) {
	upstream, hits := startUpstream(t, "192.0.2.1")
	list := writeList(t, "0.0.0.0 hosts-blocked.test\n")

	_, qaddr := startFilteredResolver(t, upstream, list, BlockResponseZero)

	resp := query(t, qaddr, "hosts-blocked.test.", dns.TypeA)
	if got := firstA(t, resp); got != "0.0.0.0" {
		t.Errorf("hosts block answer = %s, want 0.0.0.0", got)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("hosts-blocked name reached upstream: hits = %d", n)
	}

	upstream2, hits2 := startUpstream(t, "192.0.2.1")
	list2 := writeList(t, "||blocked.test^\n")
	_, qaddr2 := startFilteredResolver(t, upstream2, list2, BlockResponseNXDOMAIN)

	resp = query(t, qaddr2, "blocked.test.", dns.TypeA)
	if resp.Rcode != dns.RcodeNameError {
		t.Errorf("nxdomain rcode = %s, want NXDOMAIN", dns.RcodeToString[resp.Rcode])
	}
	if len(resp.Answer) != 0 {
		t.Errorf("nxdomain answer = %v, want empty", resp.Answer)
	}
	if n := hits2.Load(); n != 0 {
		t.Fatalf("nxdomain-blocked name reached upstream: hits = %d", n)
	}
}
