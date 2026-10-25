package logging

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// timeFormat is the log timestamp: local time, sorted by eye, without RFC3339's
// "T" or nanoseconds but with the UTC offset, so a log spanning a DST change
// stays unambiguous.
const timeFormat = "2006-01-02 15:04:05.000 -0700"

// NewTextHandler renders "[time][LEVEL] message key=value".  Unlike slog's
// TextHandler the message is free text and the timestamp is human-sized; the
// attributes keep their key=value form.
func NewTextHandler(w io.Writer, level slog.Level) slog.Handler {
	return &textHandler{w: w, mu: &sync.Mutex{}, level: level}
}

// textHandler formats whole entries itself; slog's TextHandler cannot reorder or
// unquote its built-in fields.
type textHandler struct {
	w      io.Writer
	mu     *sync.Mutex // serializes writes and is shared by WithAttrs/WithGroup
	level  slog.Level
	groups []string
	// withAttrs is the rendered form of the attributes added by WithAttrs.  It
	// is rendered eagerly, so a later WithGroup does not retroactively nest them
	// — the ordering slog's own TextHandler gives.
	withAttrs string
}

// Enabled implements slog.Handler.
func (h *textHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }

// Handle implements slog.Handler.  The whole entry is built in one builder and
// written once, so the rotating writer never sees a partial line.
func (h *textHandler) Handle(_ context.Context, r slog.Record) (err error) {
	var b strings.Builder
	b.WriteByte('[')
	b.WriteString(r.Time.Format(timeFormat))
	b.WriteString("][")
	b.WriteString(r.Level.String())
	b.WriteByte(']')
	if msg := oneLine(strings.TrimSpace(r.Message)); msg != "" {
		b.WriteByte(' ')
		b.WriteString(msg)
	}
	b.WriteString(h.withAttrs)
	r.Attrs(func(a slog.Attr) bool {
		appendAttr(&b, h.groups, a)

		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()

	_, err = io.WriteString(h.w, b.String())

	return err
}

// WithAttrs implements slog.Handler.
func (h *textHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	var b strings.Builder
	b.WriteString(h.withAttrs)
	for _, a := range attrs {
		appendAttr(&b, h.groups, a)
	}

	return &textHandler{
		w: h.w, mu: h.mu, level: h.level, groups: h.groups,
		withAttrs: b.String(),
	}
}

// WithGroup implements slog.Handler.
func (h *textHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	return &textHandler{
		w: h.w, mu: h.mu, level: h.level, withAttrs: h.withAttrs,
		groups: append(append([]string{}, h.groups...), name),
	}
}

// oneLine keeps a message on one line and free of terminal escapes, which the
// rotating writer, the GUI's line-oriented viewer and anyone running
// Get-Content all assume.  A plain message is returned untouched; one carrying
// a control byte is rendered the way slog's TextHandler quoted it, minus the
// surrounding quotes.
func oneLine(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' || c == 0x7f {
			q := strconv.Quote(s)

			return q[1 : len(q)-1]
		}
	}

	return s
}

// appendAttr writes one " key=value" field, flattening groups with dotted keys
// like slog's TextHandler.
func appendAttr(b *strings.Builder, groups []string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	a.Value = a.Value.Resolve()

	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			groups = append(groups, a.Key)
		}
		for _, ga := range a.Value.Group() {
			appendAttr(b, groups, ga)
		}

		return
	}

	b.WriteByte(' ')
	for _, g := range groups {
		b.WriteString(g)
		b.WriteByte('.')
	}
	b.WriteString(a.Key)
	b.WriteByte('=')
	appendValue(b, a.Value)
}

// appendValue writes a value the way slog's TextHandler does: strings quoted
// only when needed, everything else as its string form.
func appendValue(b *strings.Builder, v slog.Value) {
	if v.Kind() == slog.KindString {
		// dnsproxy emits DNS message dumps as tab-separated "line" attributes;
		// spaces read better than the two-character \t escape.
		b.WriteString(quoteIfNeeded(strings.ReplaceAll(v.String(), "\t", tabReplacement)))

		return
	}
	if v.Kind() == slog.KindTime {
		b.WriteString(quoteIfNeeded(v.Time().Format(time.RFC3339Nano)))

		return
	}

	b.WriteString(quoteIfNeeded(v.String()))
}

// quoteIfNeeded leaves a bare value that stays one field, mirroring slog's
// TextHandler.
func quoteIfNeeded(s string) (out string) {
	if s == "" {
		return `""`
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < ' ' || c > '~' || c == ' ' || c == '=' {
			return strconv.Quote(s)
		}
	}

	return s
}
