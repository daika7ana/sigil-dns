package state

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := NewStore(path)

	if _, err := s.Load(); !errors.Is(err, ErrNoState) {
		t.Fatalf("Load on empty: got %v, want ErrNoState", err)
	}

	want := &State{
		Mode:        ModeOn,
		Intercept:   MechanismNRPT,
		Upstream:    "https://freedns.controld.com/p0",
		Dirty:       true,
		NameServers: []string{"127.0.0.1"},
	}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if !got.Dirty || got.Mode != ModeOn || got.Upstream != want.Upstream {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if len(got.NameServers) != 1 || got.NameServers[0] != "127.0.0.1" {
		t.Errorf("NameServers = %v, want [127.0.0.1]", got.NameServers)
	}
	if got.Timestamp.IsZero() {
		t.Error("Timestamp not set")
	}
}

// TestSaveReplacesCorruptFile pins A6: Save must overwrite an unparseable state
// file instead of returning its load error.
func TestSaveReplacesCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := NewStore(path)
	if _, err := s.Load(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load = %v, want ErrCorrupt", err)
	}
	if err := s.Save(&State{Mode: ModeOff}); err != nil {
		t.Fatalf("Save over corrupt state: %v", err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got.Mode != ModeOff {
		t.Fatalf("Mode = %q, want %q", got.Mode, ModeOff)
	}
}

// TestLoadRejectsUnknownMode pins A6: valid JSON with no usable mode cannot
// gate recovery and must be treated as corrupt.
func TestLoadRejectsUnknownMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStore(path).Load(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load = %v, want ErrCorrupt", err)
	}
}

func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json.lock")

	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock: %v", err)
	}

	if _, err = AcquireLock(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("second AcquireLock: got %v, want ErrLocked", err)
	}

	if err = l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}

	if _, err = os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Release did not remove the lock file: %v", err)
	}

	l2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	if err = l2.Release(); err != nil {
		t.Fatalf("Release 2: %v", err)
	}
}

func TestLockStaleReclaimed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json.lock")

	// A dead pid: max int32 is not a live process on Linux or Windows.
	if err := os.WriteFile(path, []byte("2147483647\n"), 0o600); err != nil {
		t.Fatalf("writing stale lock: %v", err)
	}
	if processAlive(2147483647) {
		t.Skip("pid 2147483647 unexpectedly alive")
	}

	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock over stale lock: %v", err)
	}

	// The lock must now record our own pid.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading reclaimed lock: %v", err)
	}
	if want := strconv.Itoa(os.Getpid()); !strings.Contains(string(data), want) {
		t.Fatalf("reclaimed lock pid = %q, want %q", strings.TrimSpace(string(data)), want)
	}

	if err = l.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// A lock whose pid cannot be read has no live writer (the pid is written right
// after the create), so recovery reclaims it instead of stranding the machine.
func TestLockUnreadableReclaimed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json.lock")

	if err := os.WriteFile(path, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("writing unreadable lock: %v", err)
	}

	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock over unreadable lock: %v", err)
	}
	defer func() { _ = l.Release() }()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading reclaimed lock: %v", err)
	}
	if want := strconv.Itoa(os.Getpid()); !strings.Contains(string(data), want) {
		t.Fatalf("reclaimed lock pid = %q, want %q", strings.TrimSpace(string(data)), want)
	}
}
