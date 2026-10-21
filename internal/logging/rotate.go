package logging

import (
	"bytes"
	"errors"
	"io"
	"os"

	"github.com/daika7ana/sigil-dns/internal/winacl"
)

// rotatingWriter is a single-file, size-capped sink.  When a write would exceed
// maxBytes it rewrites the file keeping the newest keepBytes, trimmed forward to
// a line boundary, then appends (LOGGING_PLAN §1.7).
type rotatingWriter struct {
	file      *os.File
	path      string
	maxBytes  int64
	keepBytes int64
	size      int64
}

// Write implements io.Writer.  The file is opened without O_APPEND (see Open),
// so the offset is explicitly moved to EOF before writing.  Dropping O_APPEND
// means append atomicity now relies on the in-process slog handler mutex; a
// second --service process (a manual debug run) sharing the file could
// overwrite lines instead of interleaving them.
func (w *rotatingWriter) Write(p []byte) (n int, err error) {
	if w.maxBytes > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err = w.rotate(); err != nil {
			return 0, err
		}
	}

	if _, err = w.file.Seek(0, io.SeekEnd); err != nil {
		return 0, err
	}

	n, err = w.file.Write(p)
	w.size += int64(n)

	return n, err
}

// Close implements io.Closer.
func (w *rotatingWriter) Close() (err error) { return w.file.Close() }

// rotate keeps the newest keepBytes of the file's tail, starting at a line
// boundary, and rewrites the file in place.
func (w *rotatingWriter) rotate() (err error) {
	// Re-stat first: a drifted cached size would make ReadAt run past EOF and
	// return a short, zero-filled buffer.
	if info, sErr := w.file.Stat(); sErr == nil {
		w.size = info.Size()
	}

	keep := min(w.keepBytes, w.size)
	if keep <= 0 {
		return w.rewrite(nil)
	}

	buf := make([]byte, keep)
	n, err := w.file.ReadAt(buf, w.size-keep)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	buf = buf[:n]

	// Drop the partial first line (if any) so the kept window starts on a line
	// boundary.
	if i := bytes.IndexByte(buf, '\n'); i >= 0 {
		buf = buf[i+1:]
	}

	return w.rewrite(buf)
}

// rewrite truncates the file and writes content as the new body, then re-applies
// the ACL.  A re-ACL failure is ignored so logging never blocks (fail-open).
func (w *rotatingWriter) rewrite(content []byte) (err error) {
	if err = w.file.Truncate(0); err != nil {
		return err
	}
	if _, err = w.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err = w.file.Write(content); err != nil {
		return err
	}
	w.size = int64(len(content))

	_ = winacl.SecureFile(w.path)

	return nil
}
