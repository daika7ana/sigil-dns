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
	"io"
	"io/fs"
	"os"
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
	Timestamp time.Time `json:"timestamp"`
	Mode      Mode      `json:"mode"`
	Intercept Mechanism `json:"intercept"`
	Upstream  string    `json:"active_upstream"`
	Bypass    []string  `json:"bypass,omitempty"`

	// NameServers are the bound resolver IPs the NRPT catch-all advertises.
	// Empty on legacy state; the interceptor then uses the dual-stack default.
	NameServers   []string `json:"name_servers,omitempty"`
	Health        *Health  `json:"last_health,omitempty"`
	SchemaVersion int      `json:"schema_version"`
	Dirty         bool     `json:"dirty"`
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

// errLockHeld marks a non-blocking lock attempt that lost the race.
var errLockHeld = errors.New("lock held")

// Lock is a cross-platform single-instance guard.  It is an OS file lock on
// path, held by an open handle for the session; the OS releases it when the
// process dies, so a leftover file is never mistaken for a live owner.
type Lock struct {
	file *os.File
}

// AcquireLock takes the file lock at path without blocking.  It returns
// ErrLocked when another process holds it.  The caller must call Release.
func AcquireLock(path string) (l *Lock, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock: %w", err)
	}

	if err = lockFile(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errLockHeld) {
			return nil, ErrLocked
		}

		return nil, fmt.Errorf("acquiring lock: %w", err)
	}

	// The pid is diagnostic only; the OS lock is authoritative.
	_ = f.Truncate(0)
	_, _ = f.Seek(0, io.SeekStart)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())

	return &Lock{file: f}, nil
}

// AcquireLockSecure takes the lock at path and, on Windows, restricts the lock
// file to SYSTEM + Administrators.  It is for the service state lock; the GUI's
// user-scope lock keeps the user's default ACL.
func AcquireLockSecure(path string) (l *Lock, err error) {
	l, err = AcquireLock(path)
	if err != nil {
		return nil, err
	}

	if err = winacl.SecureFile(path); err != nil {
		_ = l.Release()

		return nil, fmt.Errorf("securing lock: %w", err)
	}

	return l, nil
}

// Release unlocks and closes the lock handle.  The file is deliberately left in
// place: unlinking it after unlocking would let a third process lock a fresh
// inode while a second still owns the old one.
func (l *Lock) Release() (err error) {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil

	return errors.Join(unlockFile(f), f.Close())
}
