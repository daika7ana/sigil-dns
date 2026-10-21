package resolver

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

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

	r, err := New(Config{
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
