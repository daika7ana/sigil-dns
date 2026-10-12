//go:build gui

package gui

import (
	"path/filepath"
	"testing"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/providers"
)

// TestFileSummaryReadsSavedConfig proves the GUI's offline read-back returns the
// saved configuration.  This is the persistence bug: with the service stopped
// (or not yet installed) the editor had no way to see a config saved from the
// GUI, so it fell back to the built-in default and a reopen looked reset.
func TestFileSummaryReadsSavedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")

	if got := fileSummary(path); got != nil {
		t.Fatalf("missing file: summary = %+v, want nil", got)
	}

	cfg := config.Default()
	p := cfg.Profiles[cfg.Profile]
	p.Provider = providers.Cloudflare
	p.Variant = "family"
	p.Protocol = string(providers.DoH)
	p.Bootstrap = []string{"1.1.1.3", "1.0.0.3"}
	cfg.Profiles[cfg.Profile] = p

	if err := cfg.Save(path); err != nil {
		t.Fatalf("save config: %v", err)
	}

	got := fileSummary(path)
	if got == nil || !got.ConfigValid {
		t.Fatalf("summary = %+v, want valid", got)
	}
	if got.Provider != providers.Cloudflare || got.Variant != "family" {
		t.Fatalf("provider/variant = %q/%q, want cloudflare/family", got.Provider, got.Variant)
	}
	if len(got.Bootstrap) != 2 || got.Bootstrap[0] != "1.1.1.3" || got.Bootstrap[1] != "1.0.0.3" {
		t.Fatalf("bootstrap = %v, want the saved list", got.Bootstrap)
	}
	if got.Revision == "" {
		t.Fatal("revision is empty, want the file token")
	}
	if len(got.Profiles) != 1 || got.Profiles[0].Name != cfg.Profile {
		t.Fatalf("profiles = %+v, want the saved profile list", got.Profiles)
	}
}
