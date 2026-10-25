// Package logging builds the service's optional file logger: a redacting,
// size-rotating sink.  It fails open: an unusable file falls back to stderr and
// never blocks resolution (LOGGING_PLAN §1.5).  Only the service writes the
// log; the GUI and CLI are read-only.
package logging

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/daika7ana/sigil-dns/internal/config"
	"github.com/daika7ana/sigil-dns/internal/redact"
	"github.com/daika7ana/sigil-dns/internal/winacl"
)

// DefaultPath returns the machine-wide log path next to config.yaml/state.json
// under the ProgramData KnownFolder.  It returns "" when the directory cannot be
// resolved.
func DefaultPath() (p string) {
	dir := config.SystemDir()
	if dir == "" {
		return ""
	}

	return filepath.Join(dir, "sigildns.log")
}

// Options configures Open.
type Options struct {
	// Level is off, error, info, or debug.
	Level string

	// Path is the log file path; empty means DefaultPath.
	Path string

	// MaxSizeMB triggers a rotation and KeepMB is the newest window retained.
	MaxSizeMB int
	KeepMB    int

	// Secrets are resolver ids the redacting handler must scrub.
	Secrets []string
}

// Open builds a logger from opts.  A level of off returns a discard logger and
// opens no file.  If the file cannot be created, opened, or ACL'd, it returns a
// redacting stderr logger and a non-nil error: callers may report the error and
// keep using the returned logger (fail-open).  The returned closer is nil for a
// discarded or stderr logger.
func Open(opts Options) (l *slog.Logger, closer io.Closer, err error) {
	level, off, levelErr := parseLevel(opts.Level)
	if levelErr != nil {
		return stderrLogger(slog.LevelInfo, opts.Secrets), nil, levelErr
	}
	if off {
		return slog.New(slog.DiscardHandler), nil, nil
	}

	path := strings.TrimSpace(opts.Path)
	if path == "" {
		path = DefaultPath()
		if path == "" {
			return stderrLogger(level, opts.Secrets), nil, errors.New("system log directory is unavailable")
		}
	}

	// No O_APPEND: on Windows an append handle lacks FILE_WRITE_DATA, which
	// makes Ftruncate fail with ACCESS_DENIED and freezes rotation.  The
	// rotatingWriter seeks to EOF before every write instead (single writer).
	_, statErr := os.Stat(path)
	fresh := errors.Is(statErr, fs.ErrNotExist)

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return stderrLogger(level, opts.Secrets), nil, fmt.Errorf("opening log file: %w", err)
	}

	// A log with an inherited ACL must never be written to (it can carry query
	// diagnostics); fail open instead of writing to an unprotected file.
	if aErr := winacl.SecureFile(path); aErr != nil {
		_ = f.Close()
		// Only remove a file this call created; never delete a pre-existing one.
		if fresh {
			_ = os.Remove(path)
		}

		return stderrLogger(level, opts.Secrets), nil, fmt.Errorf("securing log file: %w", aErr)
	}

	info, sErr := f.Stat()
	if sErr != nil {
		_ = f.Close()

		return stderrLogger(level, opts.Secrets), nil, fmt.Errorf("sizing log file: %w", sErr)
	}

	max := clampSize(opts.MaxSizeMB, config.DefaultLogMaxSizeMB)
	w := &rotatingWriter{
		file:      f,
		path:      path,
		maxBytes:  mebibytes(max),
		keepBytes: mebibytes(clampKeep(opts.KeepMB, config.DefaultLogKeepMB, max)),
		size:      info.Size(),
	}

	return slog.New(redact.Handler(NewTextHandler(w, level), opts.Secrets)), w, nil
}

// UpdateSecrets replaces the redaction seeds of a logger built by Open, so a
// reload can track a new active profile's resolver ids.  It is safe for
// concurrent use and a no-op for a logger without swappable seeds.
func UpdateSecrets(l *slog.Logger, secrets []string) {
	if l == nil {
		return
	}

	redact.SetSecrets(l.Handler(), secrets)
}

// Secrets returns the redaction seeds currently installed on a logger built by
// Open, or nil for any other logger.  It is safe for concurrent use.
func Secrets(l *slog.Logger) (out []string) {
	if l == nil {
		return nil
	}

	return redact.Secrets(l.Handler())
}

// parseLevel maps a config level string to a slog level.  off (and empty)
// reports off=true.
func parseLevel(s string) (level slog.Level, off bool, err error) {
	switch strings.TrimSpace(strings.ToLower(s)) {
	case "", config.LogLevelOff:
		return 0, true, nil
	case config.LogLevelError:
		return slog.LevelError, false, nil
	case config.LogLevelInfo:
		return slog.LevelInfo, false, nil
	case config.LogLevelDebug:
		return slog.LevelDebug, false, nil
	default:
		return 0, false, fmt.Errorf("unknown log level %q", s)
	}
}

// tabReplacement is what the handler substitutes for each TAB in a string
// value.  dnsproxy emits DNS message dumps as tab-separated "line" attributes
// and the two-character \t escape would be unreadable.
const tabReplacement = "    "

// stderrLogger is the fail-open fallback: redacted, stderr only.
func stderrLogger(level slog.Level, secrets []string) (l *slog.Logger) {
	return slog.New(redact.Handler(NewTextHandler(os.Stderr, level), secrets))
}

// mebibytes converts MiB to bytes.
func mebibytes(n int) (b int64) { return int64(n) << 20 }

// clampSize returns n or the default when n is non-positive, and bounds it so
// the mebibytes shift cannot overflow an int64.
func clampSize(n, def int) (out int) {
	if n <= 0 {
		return def
	}
	if n > config.MaxLogSizeMB {
		return config.MaxLogSizeMB
	}

	return n
}

// clampKeep returns keep or the default when keep is non-positive, and never
// more than max.
func clampKeep(keep, def, max int) (out int) {
	if keep <= 0 {
		keep = def
	}
	if max > 0 && keep > max {
		keep = max
	}

	return keep
}
