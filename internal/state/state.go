// Package state persists the service-owned state file: interception mode,
// adapter snapshot, dirty marker, active upstream, and the last health result.
//
// The file is written atomically (temp file + fsync + rename) and is
// schema-versioned.  The snapshot must never be overwritten while the dirty
// marker is set; callers must restore first (see Save).
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
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
	MechanismNRPT    Mechanism = "nrpt"
	MechanismAdapter Mechanism = "adapter"
)

// ErrNoState is returned by Load when no state file exists yet.
var ErrNoState = errors.New("no state file")

// ErrDirtySnapshot is returned by Save when it would overwrite a snapshot while
// the existing state is dirty.  Restore from the persisted snapshot first.
var ErrDirtySnapshot = errors.New("refusing to overwrite snapshot while dirty")

// ErrLocked is returned by AcquireLock when another instance holds the lock.
var ErrLocked = errors.New("another Sigil DNS instance is running")

// InterfaceDNS is the captured per-interface DNS configuration used by the
// per-adapter fallback mechanism.  GUID is stable; InterfaceIndex is not.
type InterfaceDNS struct {
	GUID string   `json:"guid"`
	IPv4 []string `json:"ipv4,omitempty"`
	IPv6 []string `json:"ipv6,omitempty"`
}

// Snapshot is the pre-interception DNS configuration.
type Snapshot struct {
	Interfaces []InterfaceDNS `json:"interfaces"`
}

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
	Snapshot      *Snapshot `json:"snapshot,omitempty"`
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
		return nil, fmt.Errorf("parsing state: %w", err)
	}

	return st, nil
}

// Save writes st atomically.  It refuses to replace the persisted snapshot when
// the existing state is dirty, since re-snapshotting a loopbacked system would
// capture 127.0.0.1 as the "original" DNS configuration.
func (s *Store) Save(st *State) (err error) {
	if st.SchemaVersion == 0 {
		st.SchemaVersion = SchemaVersion
	}
	st.Timestamp = time.Now().UTC()

	if old, lErr := s.Load(); lErr == nil {
		if old.Dirty && st.Snapshot != nil && !reflect.DeepEqual(old.Snapshot, st.Snapshot) {
			return ErrDirtySnapshot
		}
	} else if !errors.Is(lErr, ErrNoState) {
		return lErr
	}

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

	// Conflict: the owner may be dead.  Reclaim only if the recorded pid is not
	// alive; a missing or unreadable lock is retried once to cover the race
	// where the owner released it between our attempts.
	pid, pErr := readOwnerPid(path)
	if pErr == nil && processAlive(pid) {
		return nil, ErrLocked
	}

	_ = os.Remove(path)

	l, err = tryAcquire(path)
	if err != nil {
		return nil, ErrLocked
	}

	return l, nil
}

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
