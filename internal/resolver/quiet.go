package resolver

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// upstreamErrorWindow is how long one ERROR record per message and upstream is
// allowed through.  dnsproxy logs every failed query at Error, so an
// unreachable upstream turns a client retry storm into a flood of identical
// records; one line per window keeps the failure visible and diagnosable.
const upstreamErrorWindow = 30 * time.Second

// quietUpstreamErrors returns logger with a rate limiter for repeated upstream
// ERROR records.  It wraps the logger's handler, so it sits outside the
// redacting handler; keys may hold the raw upstream URL in memory (they are
// never logged).
func quietUpstreamErrors(logger *slog.Logger) (quiet *slog.Logger) {
	return slog.New(&quietHandler{next: logger.Handler(), lim: newUpstreamLimiter(time.Now)})
}

// quietHandler is the limiting slog.Handler.  WithAttrs/WithGroup delegate to
// next so accumulated attrs and groups stay correct; clones share one limiter,
// so a logger derived with .With() is limited against the same windows.
type quietHandler struct {
	next slog.Handler
	lim  *upstreamLimiter
}

// Enabled implements slog.Handler.
func (h *quietHandler) Enabled(ctx context.Context, level slog.Level) (ok bool) {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler.  Only ERROR records with an upstream/addr
// attribute are limited; everything else passes through untouched.
func (h *quietHandler) Handle(ctx context.Context, r slog.Record) (err error) {
	if r.Level < slog.LevelError {
		return h.next.Handle(ctx, r)
	}

	key, ok := upstreamErrorKey(r)
	if !ok {
		return h.next.Handle(ctx, r)
	}

	suppressed, emit := h.lim.allow(key)
	if !emit {
		return nil
	}
	if suppressed > 0 {
		// The first record of a window stays exactly as dnsproxy built it;
		// later windows carry the count of what the previous one hid.
		r = r.Clone()
		r.AddAttrs(slog.Int64("suppressed", suppressed))
	}

	return h.next.Handle(ctx, r)
}

// WithAttrs implements slog.Handler.
func (h *quietHandler) WithAttrs(attrs []slog.Attr) (out slog.Handler) {
	return &quietHandler{next: h.next.WithAttrs(attrs), lim: h.lim}
}

// WithGroup implements slog.Handler.
func (h *quietHandler) WithGroup(name string) (out slog.Handler) {
	return &quietHandler{next: h.next.WithGroup(name), lim: h.lim}
}

// upstreamErrorKey identifies a record by its message and upstream address.
// The message is an opaque key, never matched against dnsproxy's text, so an
// engine upgrade that rewords a message cannot break the limiter.  A record
// with neither address attribute is not a grouped upstream failure.
func upstreamErrorKey(r slog.Record) (key string, ok bool) {
	var upstream, addr string
	var hasUpstream, hasAddr bool

	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "upstream":
			if !hasUpstream {
				upstream, hasUpstream = a.Value.Resolve().String(), true
			}
		case "addr":
			if !hasAddr {
				addr, hasAddr = a.Value.Resolve().String(), true
			}
		}

		return true
	})

	switch {
	case hasUpstream:
		return r.Message + "\x00" + upstream, true
	case hasAddr:
		return r.Message + "\x00" + addr, true
	default:
		return "", false
	}
}

// upstreamLimiter holds one window per message and upstream.  The map is
// bounded by the configured upstreams times the message kinds dnsproxy emits,
// so it is never pruned.
type upstreamLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	windows map[string]*upstreamWindow
}

// upstreamWindow is the current window for one key.
type upstreamWindow struct {
	last       time.Time
	suppressed int64
}

// newUpstreamLimiter builds a limiter with an injectable clock.
func newUpstreamLimiter(now func() time.Time) *upstreamLimiter {
	return &upstreamLimiter{now: now, windows: make(map[string]*upstreamWindow)}
}

// allow reports whether the record should be emitted and how many records the
// previous window suppressed.
func (l *upstreamLimiter) allow(key string) (suppressed int64, emit bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	w, ok := l.windows[key]
	if !ok {
		l.windows[key] = &upstreamWindow{last: now}

		return 0, true
	}
	if now.Sub(w.last) < upstreamErrorWindow {
		w.suppressed++

		return 0, false
	}

	suppressed = w.suppressed
	w.last = now
	w.suppressed = 0

	return suppressed, true
}
