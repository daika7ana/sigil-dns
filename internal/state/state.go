// Package state persists the service-owned state file: interception mode,
// dirty marker, active upstream, and the last health result.
//
// The file is written atomically (temp file + fsync + rename) and is
// schema-versioned.  The dirty marker records that interception may still be
// applied, so callers must restore before starting a new session.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/daika7ana/sigil-dns/internal/winacl"
)

// SchemaVersion is the current state file schema version.  Bump it on any
// incompatible field change.
const SchemaVersion = 1

// Mode is the desired interception mode.
type Mode string

// Mode values.
const (
	ModeOn  Mode = "on"
	ModeOff Mode = "off"
)

// Mechanism is the interception mechanism in use.
type Mechanism string

// Mechanism values.
const (
	MechanismNRPT Mechanism = "nrpt"
)

// ErrNoState is returned by Load when no state file exists yet.
var ErrNoState = errors.New("no state file")

// ErrCorrupt is returned by Load when the state file cannot be parsed.
var ErrCorrupt = errors.New("state file is corrupt")

// ErrLocked is returned by AcquireLock when another instance holds the lock.
var ErrLocked = errors.New("another Sigil DNS instance is running")

// Health is the last two-stage health-check result.
type Health struct {
	Detail string `json:"detail,omitempty"`
	Ours   bool   `json:"ours"`
	IPv4   bool   `json:"ipv4"`
	IPv6   bool   `json:"ipv6"`
}

// State is the persisted state.
type State struct {
	Timestamp     time.Time `json:"timestamp"`
	Mode          Mode      `json:"mode"`
	Intercept     Mechanism `json:"intercept"`
	Upstream      string    `json:"active_upstream"`
	Bypass        []string  `json:"bypass,omitempty"`

	// NameServers are the bound resolver IPs the NRPT catch-all advertises.
	// Empty on legacy state; the interceptor then uses the dual-stack default.
	NameServers   []string  `json:"name_servers,omitempty"`
	Health        *Health   `json:"last_health,omitempty"`
	SchemaVersion int       `json:"schema_version"`
	Dirty         bool      `json:"dirty"`
}

// Store reads and writes the state file at a fixed path.
type Store struct {
	path string
}

// NewStore returns a Store backed by path.
func NewStore(path string) *Store { return &Store{path: path} }

// Load reads the state file.  It returns ErrNoState if the file does not exist.
func (s *Store) Load() (st *State, err error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoState
		}

		return nil, fmt.Errorf("reading state: %w", err)
	}

	st = &State{}
	if err = json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("%w: parsing state: %w", ErrCorrupt, err)
	}
	if st.Mode != ModeOn && st.Mode != ModeOff {
		// Valid JSON without a usable mode (e.g. "{}") cannot gate recovery.
		return nil, fmt.Errorf("%w: unknown mode %q", ErrCorrupt, st.Mode)
	}

	return st, nil
}

// Save writes st atomically.  An unreadable existing file is replaced: a
// corrupt state must not be able to wedge recovery.
func (s *Store) Save(st *State) (err error) {
	if st.SchemaVersion == 0 {
		st.SchemaVersion = SchemaVersion
	}
	st.Timestamp = time.Now().UTC()

	return s.writeAtomic(st)
}

// writeAtomic marshals st and writes it atomically and ACL-protected.
func (s *Store) writeAtomic(st *State) (err error) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state: %w", err)
	}

	if err = winacl.WriteFileAtomic(s.path, data); err != nil {
		return fmt.Errorf("writing state: %w", err)
	}

	return nil
}

// MarkClean records the state as off and clears the dirty marker.  It returns
// ErrNoState when no state file exists (callers decide whether that is an
// error).  A corrupt file is quarantined as <path>.corrupt and replaced with a
// clean off state, so every caller (including drift auto-disable) recovers.
func (s *Store) MarkClean() (err error) {
	st, err := s.Load()
	if errors.Is(err, ErrCorrupt) {
		_ = os.Rename(s.path, s.path+".corrupt")

		return s.Save(&State{
			SchemaVersion: SchemaVersion,
			Mode:          ModeOff,
			Intercept:     MechanismNRPT,
		})
	}
	if err != nil {
		return err
	}
	st.Mode = ModeOff
	st.Dirty = false

	return s.Save(st)
}

// LockPath returns the single-instance lock path that accompanies statePath.
func LockPath(statePath string) string { return statePath + ".lock" }

// Lock is a cross-platform single-instance guard backed by an exclusive lock
// file containing the owning pid.  The lock is the file's existence (O_EXCL
// create); the handle is closed after the pid is written.
type Lock struct {
	path string
}

// AcquireLock creates path exclusively.  It returns ErrLocked if a live process
// already holds it.  A lock left behind by a dead process (e.g. after kill -9)
// is reclaimed so crash recovery can proceed.  The caller must call Release.
func AcquireLock(path string) (l *Lock, err error) {
	l, err = tryAcquire(path)
	if err == nil {
		return l, nil
	}
	if !errors.Is(err, ErrLocked) {
		return nil, err
	}

	// Conflict: the owner may be dead.  The pid is written immediately after the
	// exclusive create, so a lock that cannot be read yet is normally mid-create:
	// wait for it briefly.  Still unreadable after that, it has no live writer,
	// and it is reclaimed — refusing would leave a crashed session's lock in
	// place, and with it interception on and nothing answering (recovery must
	// never depend on a hand-deleted file).
	pid, pErr := readOwnerPid(path)
	for try := 0; try < lockReadTries && pErr != nil && !errors.Is(pErr, fs.ErrNotExist); try++ {
		time.Sleep(lockReadWait)
		pid, pErr = readOwnerPid(path)
	}
	if pErr == nil && processAlive(pid) {
		return nil, ErrLocked
	}

	_ = os.Remove(path)

	l, err = tryAcquire(path)
	if err != nil {
		return nil, err
	}

	return l, nil
}

// A lock that cannot be read is waited on before it is reclaimed: the owner
// writes its pid right after creating the file, so this covers the create->
// write window without mistaking a live lock for a stale one.
const (
	lockReadTries = 5
	lockReadWait  = 20 * time.Millisecond
)

// tryAcquire performs the exclusive create.
func tryAcquire(path string) (l *Lock, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, ErrLocked
		}

		return nil, fmt.Errorf("acquiring lock: %w", err)
	}

	if _, err = fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		_ = f.Close()
		_ = os.Remove(path)

		return nil, fmt.Errorf("writing lock: %w", err)
	}

	if err = f.Close(); err != nil {
		_ = os.Remove(path)

		return nil, fmt.Errorf("closing lock: %w", err)
	}

	return &Lock{path: path}, nil
}

// readOwnerPid reads the pid recorded in the lock file.
func readOwnerPid(path string) (pid int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parsing lock pid: %w", err)
	}

	return pid, nil
}

// Release removes the lock file.
func (l *Lock) Release() (err error) {
	if l == nil {
		return nil
	}

	return os.Remove(l.path)
}
