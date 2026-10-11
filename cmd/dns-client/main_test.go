package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/daika7ana/dns-client/internal/lifecycle"
	"github.com/daika7ana/dns-client/internal/state"
)

// TestServiceLifecycleDegradedEnable verifies the service can boot and run
// crash recovery from state alone, while Enable reports a clear error until the
// config loads (M4).
func TestServiceLifecycleDegradedEnable(t *testing.T) {
	dir := t.TempDir()
	inner := lifecycle.New(lifecycle.Config{StatePath: filepath.Join(dir, "state.json")})

	want := errors.New("configuration unavailable")
	lc := &serviceLifecycle{inner: inner, enableErr: want}

	if err := lc.Enable(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Enable err = %v, want %v", err, want)
	}

	// Recovery works without config: no state means nothing to restore.
	reenable, err := lc.Startup(context.Background())
	if err != nil {
		t.Fatalf("Startup: %v", err)
	}
	if reenable {
		t.Fatal("Startup should not request re-enable with no state")
	}

	if _, err = lc.Status(); !errors.Is(err, state.ErrNoState) {
		t.Fatalf("Status err = %v, want ErrNoState", err)
	}
}
