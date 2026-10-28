package state

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

	// The file is intentionally kept (unlinking it would race a new owner), so
	// a release must be followed by a clean re-acquire of the same path.
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("lock file missing after release: %v", err)
	}

	l2, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock after release: %v", err)
	}
	if err = l2.Release(); err != nil {
		t.Fatalf("Release 2: %v", err)
	}
}

// TestLockIgnoresLeftoverFile: only the OS lock confers ownership.  A file
// left behind by a crash (dead or unreadable pid) must not block recovery.
func TestLockIgnoresLeftoverFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json.lock")

	if err := os.WriteFile(path, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("writing leftover lock: %v", err)
	}

	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("AcquireLock over leftover file: %v", err)
	}
	defer func() { _ = l.Release() }()

	// The pid stays diagnostic: the current owner rewrites it.  Read through
	// the owning handle: Windows blocks a second handle from the locked region.
	buf := make([]byte, 64)
	n, err := l.file.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("reading lock: %v", err)
	}
	data := buf[:n]
	if want := strconv.Itoa(os.Getpid()); !strings.Contains(string(data), want) {
		t.Fatalf("lock pid = %q, want %q", strings.TrimSpace(string(data)), want)
	}
}

const (
	lockHelperPath    = "SIGILDNS_TEST_LOCK_PATH"
	lockHelperGate    = "SIGILDNS_TEST_LOCK_GATE"
	lockHelperSignal  = "SIGILDNS_TEST_LOCK_SIGNAL"
	lockHelperDone    = "SIGILDNS_TEST_LOCK_DONE"
	lockHelperHold    = "SIGILDNS_TEST_LOCK_HOLD"
	lockHelperRelease = "SIGILDNS_TEST_LOCK_RELEASE"
)

// TestLockHelperProcess is the child half of the cross-process lock tests; it
// is inert in a normal test run.
func TestLockHelperProcess(t *testing.T) {
	path := os.Getenv(lockHelperPath)
	if path == "" {
		return
	}

	waitForLockFile(os.Getenv(lockHelperGate))

	l, err := AcquireLock(path)
	switch {
	case err == nil:
		fmt.Println("LOCK:acquired")
	case errors.Is(err, ErrLocked):
		fmt.Println("LOCK:locked")
	default:
		fmt.Println("LOCK:error", err)
	}

	if err == nil {
		if sig := os.Getenv(lockHelperSignal); sig != "" {
			_ = os.WriteFile(sig, []byte("held"), 0o600)
		}
	}

	// Mark the attempt complete before holding, so the parent never waits on a
	// winner that is itself waiting to be released.
	if done := os.Getenv(lockHelperDone); done != "" {
		_ = os.WriteFile(done, []byte("done"), 0o600)
	}

	if err != nil {
		return
	}

	switch os.Getenv(lockHelperHold) {
	case "":
	case "forever":
		time.Sleep(time.Hour)
	case "wait":
		waitForLockFile(os.Getenv(lockHelperRelease))
	}
	_ = l.Release()
}

// waitForLockFile blocks until path exists; an empty path returns immediately.
func waitForLockFile(path string) {
	if path == "" {
		return
	}
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lockHelperCmd builds a child that runs TestLockHelperProcess against path.
func lockHelperCmd(path string) (cmd *exec.Cmd) {
	cmd = exec.Command(os.Args[0], "-test.run=^TestLockHelperProcess$")
	cmd.Env = append(os.Environ(), lockHelperPath+"="+path)

	return cmd
}

// TestLockAcrossProcesses reproduces the crash-recovery race: a leftover lock
// file plus several processes starting at once must still yield exactly one
// owner.  The OS lock, not the file's existence, decides.
func TestLockAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json.lock")
	gate := filepath.Join(dir, "gate")
	release := filepath.Join(dir, "release")

	if err := os.WriteFile(path, []byte("2147483647\n"), 0o600); err != nil {
		t.Fatalf("writing leftover lock: %v", err)
	}

	const procs = 8

	cmds := make([]*exec.Cmd, procs)
	outs := make([]bytes.Buffer, procs)
	errs := make([]bytes.Buffer, procs)
	done := make([]string, procs)

	for i := range cmds {
		done[i] = filepath.Join(dir, fmt.Sprintf("done-%d", i))
		cmds[i] = lockHelperCmd(path)
		cmds[i].Env = append(cmds[i].Env,
			lockHelperGate+"="+gate,
			lockHelperDone+"="+done[i],
			lockHelperRelease+"="+release,
			lockHelperHold+"=wait",
		)
		cmds[i].Stdout = &outs[i]
		cmds[i].Stderr = &errs[i]

		if err := cmds[i].Start(); err != nil {
			t.Fatalf("starting helper %d: %v", i, err)
		}
	}

	// Leave no orphaned winner behind if an assertion below fails.
	t.Cleanup(func() {
		for _, cmd := range cmds {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	// Release all helpers into the lock attempt at once.
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatalf("writing gate: %v", err)
	}

	// Every helper has now either acquired the lock or observed ErrLocked.
	deadline := time.Now().Add(30 * time.Second)
	for _, d := range done {
		for {
			if _, err := os.Stat(d); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("helper did not finish its attempt: %s", d)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	if err := os.WriteFile(release, []byte("go"), 0o600); err != nil {
		t.Fatalf("writing release: %v", err)
	}

	acquired := 0

	for i, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper %d: %v\n%s", i, err, errs[i].String())
		}
		if strings.Contains(outs[i].String(), "LOCK:acquired") {
			acquired++
		}
	}

	if acquired != 1 {
		t.Fatalf("%d processes acquired the lock, want exactly 1", acquired)
	}
}

// TestLockReleasedOnProcessDeath: a killed owner's OS lock is released even
// though its file remains, so recovery never depends on a hand-deleted file.
func TestLockReleasedOnProcessDeath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json.lock")
	signal := filepath.Join(dir, "held")

	cmd := lockHelperCmd(path)
	cmd.Env = append(cmd.Env, lockHelperSignal+"="+signal, lockHelperHold+"=forever")

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting helper: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(signal); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()

			t.Fatalf("helper did not acquire the lock:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if _, err := AcquireLock(path); !errors.Is(err, ErrLocked) {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()

		t.Fatalf("live child's lock: got %v, want ErrLocked", err)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing helper: %v", err)
	}
	_ = cmd.Wait()

	l, err := AcquireLock(path)
	if err != nil {
		t.Fatalf("lock not released after process death: %v", err)
	}
	_ = l.Release()
}
