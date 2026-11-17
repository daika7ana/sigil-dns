package redact

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// TestHandlerScrubsSecretsAndURLs pins the redaction policy (LOGGING_PLAN §3):
// seeded ids (DoH path and DoT/DoQ hostname-label forms), URL userinfo/query,
// and sdns:// stamps are removed from both the message and attributes, while
// query names survive.
func TestHandlerScrubsSecretsAndURLs(t *testing.T) {
	const id = "SECRETID"

	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	l := slog.New(Handler(base, []string{id}))

	l.Debug("upstream https://dns.nextdns.io/"+id+" resolved",
		"doh", "https://user:pass@dns.nextdns.io/"+id+"?token=abc",
		"dot", id+".dns.nextdns.io:853",
		"stamp", "sdns://AQcAAAAAAAAADS4x",
		"qname", "example.com.",
	)

	out := buf.String()

	for _, leak := range []string{id, "user", "pass", "token=abc", "AQcAAAAAAAAADS4x"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaked %q:\n%s", leak, out)
		}
	}
	if !strings.Contains(out, "example.com.") {
		t.Fatalf("query name was scrubbed:\n%s", out)
	}
	if !strings.Contains(out, "<redacted>") {
		t.Fatalf("no redaction marker in output:\n%s", out)
	}
}

// TestHandlerScrubsWithAttrsAndGroups covers the WithAttrs/WithGroup paths.
func TestHandlerScrubsWithAttrsAndGroups(t *testing.T) {
	const id = "SECRETID"

	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	l := slog.New(Handler(base, []string{id}))

	l.With("with_attr", "https://user:pass@dns.nextdns.io/"+id).
		WithGroup("grp").
		Info("hello", slog.String("in_group", id+".dns.nextdns.io:853"))

	out := buf.String()
	for _, leak := range []string{id, "user", "pass"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaked %q:\n%s", leak, out)
		}
	}
}

// TestWithAttrsDefersScrubbing: WithAttrs must not scrub at call time; values
// are stored as a LogValuer and scrubbed only when a record is emitted.
func TestWithAttrsDefersScrubbing(t *testing.T) {
	const id = "SECRETID"

	var buf bytes.Buffer
	cap := &captureHandler{out: &buf}

	child := Handler(cap, []string{id}).WithAttrs([]slog.Attr{
		slog.String("upstream", "https://dns.nextdns.io/"+id),
	})

	ch, ok := child.(*handler)
	if !ok {
		t.Fatalf("WithAttrs returned %T, want *handler", child)
	}
	next, ok := ch.next.(*captureHandler)
	if !ok || len(next.attrs) != 1 {
		t.Fatalf("next = %#v, want one captured attr", ch.next)
	}
	if k := next.attrs[0].Value.Kind(); k != slog.KindLogValuer {
		t.Fatalf("attr kind = %s, want LogValuer (not scrubbed eagerly)", k)
	}

	slog.New(child).Info("hi")

	out := buf.String()
	if strings.Contains(out, id) {
		t.Fatalf("log leaked %q:\n%s", id, out)
	}
	if !strings.Contains(out, "<redacted>") {
		t.Fatalf("attr was not scrubbed on emission:\n%s", out)
	}
}

// captureHandler records the attrs passed to WithAttrs and renders every attr
// (resolving LogValuers) on Handle.
type captureHandler struct {
	attrs []slog.Attr
	out   io.Writer
}

func (c *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (c *captureHandler) Handle(_ context.Context, r slog.Record) (err error) {
	attrs := append([]slog.Attr{}, c.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)

		return true
	})
	for _, a := range attrs {
		_, _ = fmt.Fprintf(c.out, "%s=%s\n", a.Key, a.Value.Resolve().String())
	}

	return nil
}

func (c *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &captureHandler{attrs: append(append([]slog.Attr{}, c.attrs...), attrs...), out: c.out}
}

func (c *captureHandler) WithGroup(string) slog.Handler { return c }

// TestHandlerScrubsErrors covers a wrapped error that embeds a credential URL.
func TestHandlerScrubsErrors(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	l := slog.New(Handler(base, nil))

	l.Info("boom", "err", &urlError{"https://user:pass@host/x?y=1"})

	out := buf.String()
	for _, leak := range []string{"user", "pass", "y=1"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaked %q:\n%s", leak, out)
		}
	}
}

// TestSetSecretsReseeds verifies the seed set can be replaced at runtime (M1),
// including through a With-attrs clone that shares the store.
func TestSetSecretsReseeds(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := Handler(base, []string{"OLDID"})
	l := slog.New(h)

	SetSecrets(h, []string{"NEWID"})

	l.With("k", "v").Debug("seen NEWID and OLDID")

	out := buf.String()
	if strings.Contains(out, "NEWID") {
		t.Fatalf("new seed not applied:\n%s", out)
	}
	if !strings.Contains(out, "OLDID") {
		t.Fatalf("replaced seed still active:\n%s", out)
	}
}

// TestSetSecretsIgnoresForeignHandler verifies a no-op on an unrelated handler.
func TestSetSecretsIgnoresForeignHandler(t *testing.T) {
	SetSecrets(slog.DiscardHandler, []string{"X"})
}

type urlError struct{ s string }

func (e *urlError) Error() (out string) { return e.s }
