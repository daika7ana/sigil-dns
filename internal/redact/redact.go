// Package redact reduces DNS upstream URLs to their non-secret parts so they
// can be logged safely.  Resolver ids (NextDNS/ControlD profile ids) live in the
// URL path (DoH) or as a hostname label (DoT/DoQ), so anything beyond scheme +
// host is treated as sensitive.
package redact

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
)

// Upstream returns u reduced to scheme + host, with path, query, fragment, and
// userinfo removed (e.g. "https://dns.nextdns.io/<id>" -> "https://dns.nextdns.io").
//
// A bare "host:port" is returned with any trailing path removed.  sdns:// stamps
// are opaque secrets and are fully redacted.
func Upstream(u string) (s string) {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}

	if !strings.Contains(u, "://") {
		// Bare host:port (or IP:port); drop any defensive trailing path.
		host, _, _ := strings.Cut(u, "/")

		return host
	}

	parsed, err := url.Parse(u)
	if err != nil {
		// Fall back to a conservative manual strip.
		scheme, rest, _ := strings.Cut(u, "://")
		host, _, _ := strings.Cut(rest, "/")

		return scheme + "://" + host
	}

	if parsed.Scheme == "sdns" {
		// DNS stamps embed keys/addresses; never emit their contents.
		return "sdns://<redacted>"
	}

	if parsed.Host == "" {
		return parsed.Scheme + "://"
	}

	return parsed.Scheme + "://" + parsed.Host
}

// urlPattern matches a scheme://... token up to whitespace or a closing
// bracket/quote, so embedded upstream/list URLs in a message are scrubbed.
var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>()\[\]]+`)

// Handler wraps next so every logged message and attribute value is scrubbed of
// credentials before it is formatted: seeded resolver ids (bare and
// hostname-label forms) and URL userinfo/query/sdns:// content.  DNS query
// names are ordinary values and are preserved (LOGGING_PLAN §3).
//
// The returned handler's seed set can be replaced at runtime via SetSecrets,
// which is safe to call while the handler is logging.
func Handler(next slog.Handler, secrets []string) (h slog.Handler) {
	store := &seedStore{}
	store.set(secrets)

	return &handler{next: next, store: store}
}

// SetSecrets atomically replaces the seeded literals on a handler returned by
// Handler.  It is a no-op for any other handler and is safe for concurrent use.
func SetSecrets(h slog.Handler, secrets []string) {
	if s, ok := h.(interface{ setSecrets([]string) }); ok {
		s.setSecrets(secrets)
	}
}

// Secrets returns the currently seeded literals of a handler returned by
// Handler, or nil for any other handler.  It is safe for concurrent use.
func Secrets(h slog.Handler) (out []string) {
	if s, ok := h.(interface{ secretsList() []string }); ok {
		return s.secretsList()
	}

	return nil
}

// cleanSecrets normalizes seeds, adding the lowercased DoT/DoQ hostname-label
// form, dropping duplicates, and ordering longest-first so an id that is a
// prefix of another never corrupts the longer one.
func cleanSecrets(secrets []string) (cleaned []string) {
	cleaned = make([]string, 0, len(secrets)*2)
	seen := map[string]struct{}{}

	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		for _, v := range []string{s, strings.ToLower(s)} {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			cleaned = append(cleaned, v)
		}
	}

	sort.Slice(cleaned, func(i, j int) bool { return len(cleaned[i]) > len(cleaned[j]) })

	return cleaned
}

// seedStore holds the current seed set behind an atomic pointer so it can be
// replaced while concurrent handles read it.
type seedStore struct {
	secrets atomic.Pointer[[]string]
}

// set replaces the stored seeds.
func (s *seedStore) set(secrets []string) {
	cleaned := cleanSecrets(secrets)
	s.secrets.Store(&cleaned)
}

// list returns the current seeds (nil when none).
func (s *seedStore) list() (out []string) {
	if p := s.secrets.Load(); p != nil {
		return *p
	}

	return nil
}

// handler is the redacting slog.Handler.  WithAttrs/WithGroup delegate to next
// so grouping and accumulated attributes stay correct; only the values are
// scrubbed on the way through.  Clones share the same seedStore.
type handler struct {
	next  slog.Handler
	store *seedStore
}

// Enabled implements slog.Handler.
func (h *handler) Enabled(ctx context.Context, level slog.Level) (ok bool) {
	return h.next.Enabled(ctx, level)
}

// Handle implements slog.Handler; it scrubs the record's message and its own
// attributes, then formats through next.
func (h *handler) Handle(ctx context.Context, r slog.Record) (err error) {
	out := slog.NewRecord(r.Time, r.Level, h.scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(h.scrubAttr(a))

		return true
	})

	return h.next.Handle(ctx, out)
}

// WithAttrs implements slog.Handler.
func (h *handler) WithAttrs(attrs []slog.Attr) (out slog.Handler) {
	cleaned := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		cleaned[i] = h.scrubAttr(a)
	}

	return &handler{next: h.next.WithAttrs(cleaned), store: h.store}
}

// WithGroup implements slog.Handler.
func (h *handler) WithGroup(name string) (out slog.Handler) {
	return &handler{next: h.next.WithGroup(name), store: h.store}
}

// setSecrets replaces the shared seed set (SetSecrets).
func (h *handler) setSecrets(secrets []string) { h.store.set(secrets) }

// secretsList returns the shared seed set (Secrets).
func (h *handler) secretsList() []string { return h.store.list() }

// scrubAttr scrubs a single attribute value (recursing into groups).
func (h *handler) scrubAttr(a slog.Attr) (out slog.Attr) {
	a.Value = h.scrubValue(a.Value)

	return a
}

// scrubValue scrubs a value by kind.  Errors and Stringers are rendered and
// scrubbed too, because a wrapped error can embed a URL.
func (h *handler) scrubValue(v slog.Value) (out slog.Value) {
	v = v.Resolve()

	switch v.Kind() {
	case slog.KindString:
		return slog.StringValue(h.scrub(v.String()))
	case slog.KindGroup:
		attrs := v.Group()
		cleaned := make([]slog.Attr, len(attrs))
		for i, a := range attrs {
			cleaned[i] = h.scrubAttr(a)
		}

		return slog.GroupValue(cleaned...)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.StringValue(h.scrub(x.Error()))
		case string:
			return slog.StringValue(h.scrub(x))
		case fmt.Stringer:
			return slog.StringValue(h.scrub(x.String()))
		}

		// Any other container (map, slice, struct) is rendered and scrubbed
		// rather than passed through verbatim.
		return slog.StringValue(h.scrub(fmt.Sprintf("%v", v.Any())))
	}

	return v
}

// scrub removes URL credentials and seeded secrets from s.
func (h *handler) scrub(s string) (out string) {
	out = urlPattern.ReplaceAllStringFunc(s, scrubURL)
	for _, sec := range h.store.list() {
		out = strings.ReplaceAll(out, sec, "<redacted>")
	}

	return out
}

// scrubURL rebuilds a URL token without userinfo, query, or fragment; an
// sdns:// stamp is fully redacted.
func scrubURL(raw string) (out string) {
	if strings.HasPrefix(strings.ToLower(raw), "sdns://") {
		return "sdns://<redacted>"
	}

	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}

	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	u.RawFragment = ""

	return u.String()
}
