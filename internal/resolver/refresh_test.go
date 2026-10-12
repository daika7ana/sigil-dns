package resolver

import (
	"context"
	"os"
	"testing"

	"github.com/miekg/dns"
)

// TestRefreshListsRebuildsFilter verifies RefreshLists re-reads the configured
// list source, rebuilds the filter engine, and clears the resolver cache.
func TestRefreshListsRebuildsFilter(t *testing.T) {
	upstream, _ := startUpstream(t, "192.0.2.1")
	list := writeList(t, "||blocked.test^\n")

	r, qaddr := startFilteredResolver(t, upstream, list, BlockResponseZero)

	if got := firstA(t, query(t, qaddr, "blocked.test.", dns.TypeA)); got != "0.0.0.0" {
		t.Fatalf("before refresh blocked.test = %s, want 0.0.0.0", got)
	}

	// Swap the list source on disk, then refresh: the engine must reflect the
	// new contents.
	if err := os.WriteFile(list, []byte("||other.test^\n"), 0o600); err != nil {
		t.Fatalf("rewriting list: %v", err)
	}
	if err := r.RefreshLists(context.Background()); err != nil {
		t.Fatalf("RefreshLists: %v", err)
	}

	if got := firstA(t, query(t, qaddr, "other.test.", dns.TypeA)); got != "0.0.0.0" {
		t.Errorf("after refresh other.test = %s, want 0.0.0.0", got)
	}
	if got := firstA(t, query(t, qaddr, "blocked.test.", dns.TypeA)); got != "192.0.2.1" {
		t.Errorf("after refresh blocked.test = %s, want forwarded 192.0.2.1", got)
	}
}
