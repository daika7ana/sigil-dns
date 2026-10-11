package elevate

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/daika7ana/dns-client/internal/config"
)

func TestValidate(t *testing.T) {
	on := config.ModeOn
	bad := "bogus"

	cases := []struct {
		name    string
		req     Request
		wantErr bool
	}{
		{"enable", Request{Action: ActionEnable, UUID: "u"}, false},
		{"disable", Request{Action: ActionDisable, UUID: "u"}, false},
		{"restore", Request{Action: ActionRestore, UUID: "u"}, false},
		{"apply mode", Request{Action: ActionApply, UUID: "u", Patch: Patch{Mode: &on}}, false},
		{"unknown action", Request{Action: "frobnicate", UUID: "u"}, true},
		{"missing uuid", Request{Action: ActionEnable}, true},
		{"bad mode", Request{Action: ActionApply, UUID: "u", Patch: Patch{Mode: &bad}}, true},
	}

	for _, tc := range cases {
		if err := tc.req.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("%s: Validate() err = %v, wantErr = %v", tc.name, err, tc.wantErr)
		}
	}
}

func TestCheckRevision(t *testing.T) {
	if err := checkRevision("", "123-4"); err != nil {
		t.Fatalf("empty expected should not fail: %v", err)
	}
	if err := checkRevision("123-4", "123-4"); err != nil {
		t.Fatalf("matching revision: %v", err)
	}
	if err := checkRevision("123-4", "999-9"); !errors.Is(err, ErrRevision) {
		t.Fatalf("mismatched revision = %v, want ErrRevision", err)
	}
}

// TestApplyRequestAllowlist verifies that only mode is writable, even when the
// decoded request carries extra (ignored) fields, and that a stale revision and
// an invalid result are rejected.
func TestApplyRequestAllowlist(t *testing.T) {
	var req Request
	raw := `{"action":"apply","uuid":"u","profile":"evil","patch":{"mode":"on","upstream":"https://evil.example/SECRET"}}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	cfg := config.Default()
	beforeProfile := cfg.Profile
	beforeProfiles := len(cfg.Profiles)

	if err := applyRequest(cfg, req, "rev-1"); err != nil {
		t.Fatalf("applyRequest: %v", err)
	}
	if cfg.Mode != config.ModeOn {
		t.Fatalf("mode = %q, want %q", cfg.Mode, config.ModeOn)
	}
	if cfg.Profile != beforeProfile || len(cfg.Profiles) != beforeProfiles {
		t.Fatal("non-allowlisted patch field was applied")
	}

	// A stale expected revision aborts before any mutation.
	req.ExpectedRevision = "rev-1"
	if err := applyRequest(config.Default(), req, "rev-2"); !errors.Is(err, ErrRevision) {
		t.Fatalf("stale revision = %v, want ErrRevision", err)
	}
}
