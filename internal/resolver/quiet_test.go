package resolver

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordSink captures the records that reached the wrapped handler.
type recordSink struct {
	mu      sync.Mutex
	records []slog.Record
}

func (s *recordSink) Enabled(context.Context, slog.Level) (ok bool) { return true }

func (s *recordSink) Handle(_ context.Context, r slog.Record) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.records = append(s.records, r.Clone())

	return nil
}

func (s *recordSink) WithAttrs([]slog.Attr) (out slog.Handler) { return s }

func (s *recordSink) WithGroup(string) (out slog.Handler) { return s }

func (s *recordSink) all() (recs []slog.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]slog.Record(nil), s.records...)
}

// fakeClock is the limiter's injected time source.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() (t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// quietForTest builds the handler directly so the clock can be driven.
func quietForTest(sink slog.Handler, clk *fakeClock) slog.Handler {
	return &quietHandler{next: sink, lim: newUpstreamLimiter(clk.now)}
}

// newTestLogger returns a logger over a fresh sink and clock.
func newTestLogger() (l *slog.Logger, sink *recordSink, clk *fakeClock) {
	sink = &recordSink{}
	clk = &fakeClock{t: time.Unix(0, 0)}

	return slog.New(quietForTest(sink, clk)), sink, clk
}

// suppressedAttr returns the suppressed attribute of a record.
func suppressedAttr(r slog.Record) (n int64, ok bool) {
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "suppressed" {
			n, ok = a.Value.Int64(), true

			return false
		}

		return true
	})

	return n, ok
}

// TestQuietUpstreamErrorsSuppressesWithinWindow is acceptance 1: identical
// errors for one message and upstream emit once inside the window.
func TestQuietUpstreamErrorsSuppressesWithinWindow(t *testing.T) {
	l, sink, _ := newTestLogger()

	for range 3 {
		l.Error("exchange failed", "upstream", "https://dns.example/dns-query")
	}

	recs := sink.all()
	if len(recs) != 1 {
		t.Fatalf("emitted %d records, want 1", len(recs))
	}
	if _, ok := suppressedAttr(recs[0]); ok {
		t.Fatal("first record carries a suppressed attribute")
	}
}

// TestQuietUpstreamErrorsReportsSuppressed is acceptance 2: the next window's
// first record reports what the previous window hid, and a window with nothing
// hidden adds no attribute.
func TestQuietUpstreamErrorsReportsSuppressed(t *testing.T) {
	l, sink, clk := newTestLogger()

	l.Error("exchange failed", "upstream", "https://dns.example/dns-query")
	l.Error("exchange failed", "upstream", "https://dns.example/dns-query")
	l.Error("exchange failed", "upstream", "https://dns.example/dns-query")

	clk.advance(upstreamErrorWindow + time.Second)
	l.Error("exchange failed", "upstream", "https://dns.example/dns-query")

	recs := sink.all()
	if len(recs) != 2 {
		t.Fatalf("emitted %d records after the first window, want 2", len(recs))
	}
	if n, ok := suppressedAttr(recs[1]); !ok || n != 2 {
		t.Fatalf("suppressed = %d, %v; want 2, true", n, ok)
	}

	clk.advance(upstreamErrorWindow + time.Second)
	l.Error("exchange failed", "upstream", "https://dns.example/dns-query")

	recs = sink.all()
	if len(recs) != 3 {
		t.Fatalf("emitted %d records after the second window, want 3", len(recs))
	}
	if _, ok := suppressedAttr(recs[2]); ok {
		t.Fatal("a window that suppressed nothing still added the attribute")
	}
}

// TestQuietUpstreamErrorsSeparatesUpstreams is acceptance 3: one dead upstream
// does not hide another's first failure.
func TestQuietUpstreamErrorsSeparatesUpstreams(t *testing.T) {
	l, sink, _ := newTestLogger()

	l.Error("exchange failed", "upstream", "https://a.example/dns-query")
	l.Error("exchange failed", "upstream", "https://b.example/dns-query")
	l.Error("exchange failed", "upstream", "https://a.example/dns-query")

	if recs := sink.all(); len(recs) != 2 {
		t.Fatalf("emitted %d records, want 2 (one per upstream)", len(recs))
	}
}

// TestQuietUpstreamErrorsSeparatesMessages proves the message is part of the
// key: two different dnsproxy failures on one upstream both get through.
func TestQuietUpstreamErrorsSeparatesMessages(t *testing.T) {
	l, sink, _ := newTestLogger()

	l.Error("exchange failed", "upstream", "https://a.example/dns-query")
	l.Error("response received", "addr", "a.example:853")
	l.Error("exchange failed", "upstream", "https://a.example/dns-query")

	if recs := sink.all(); len(recs) != 2 {
		t.Fatalf("emitted %d records, want 2 (one per message)", len(recs))
	}
}

// TestQuietUpstreamErrorsPassesNonErrorLevels is acceptance 4: lower levels are
// never limited, even when identical.
func TestQuietUpstreamErrorsPassesNonErrorLevels(t *testing.T) {
	l, sink, _ := newTestLogger()

	for range 2 {
		l.Debug("exchange failed", "upstream", "https://a.example/dns-query")
		l.Info("exchange failed", "upstream", "https://a.example/dns-query")
		l.Warn("exchange failed", "upstream", "https://a.example/dns-query")
	}

	if recs := sink.all(); len(recs) != 6 {
		t.Fatalf("emitted %d records, want all 6", len(recs))
	}
}

// TestQuietUpstreamErrorsPassesUnkeyed is acceptance 5: without an upstream or
// addr attribute the record cannot be grouped and is forwarded.
func TestQuietUpstreamErrorsPassesUnkeyed(t *testing.T) {
	l, sink, _ := newTestLogger()

	for range 2 {
		l.Error("exchange failed", "question", "example.com.")
	}

	if recs := sink.all(); len(recs) != 2 {
		t.Fatalf("emitted %d unkeyed records, want both", len(recs))
	}
}

// TestQuietUpstreamErrorsAddrFallback proves dnsproxy's other failure shape
// (addr instead of upstream) is limited too.
func TestQuietUpstreamErrorsAddrFallback(t *testing.T) {
	l, sink, _ := newTestLogger()

	for range 3 {
		l.Error("response received", "addr", "a.example:853")
	}

	if recs := sink.all(); len(recs) != 1 {
		t.Fatalf("emitted %d records with addr, want 1", len(recs))
	}
}

// TestQuietUpstreamErrorsSharesLimiterAcrossClones is acceptance 6: concurrent
// Handle calls, including through a derived logger, stay race-free and share
// one set of windows.
func TestQuietUpstreamErrorsSharesLimiterAcrossClones(t *testing.T) {
	l, sink, _ := newTestLogger()
	derived := l.With("component", "resolver")

	var wg sync.WaitGroup
	for i := range 100 {

		wg.Go(func() {

			if i%2 == 0 {
				l.Error("exchange failed", "upstream", "https://a.example/dns-query")

				return
			}
			derived.Error("exchange failed", "upstream", "https://a.example/dns-query")
		})
	}
	wg.Wait()

	if recs := sink.all(); len(recs) != 1 {
		t.Fatalf("emitted %d records, want 1 for one key", len(recs))
	}
}

// TestQuietUpstreamErrorsFlood reports the user-visible effect: a retry storm
// of identical failures becomes one line.
func TestQuietUpstreamErrorsFlood(t *testing.T) {
	l, sink, _ := newTestLogger()

	const sent = 50
	for range sent {
		l.Error("exchange failed", "upstream", "https://dns.example/dns-query")
	}

	emitted := len(sink.all())
	t.Logf("emitted %d of %d identical ERROR records (before: %d)", emitted, sent, sent)

	if emitted != 1 {
		t.Fatalf("emitted %d records, want 1", emitted)
	}
}

// TestNewQuietsUpstreamErrors proves the resolver's own logger is wrapped, not
// just the handler the other tests build directly: a missing wrap in New would
// leave the flood in place while every unit test still passes.
func TestNewQuietsUpstreamErrors(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))

	r, err := New(context.Background(), Config{
		Logger:    base,
		Upstreams: []string{"1.1.1.1:53"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, ok := r.logger.Handler().(*quietHandler); !ok {
		t.Fatalf("resolver logger handler = %T, want *quietHandler", r.logger.Handler())
	}

	r.logger.Error("exchange failed", "upstream", "1.1.1.1:53")
	r.logger.Error("exchange failed", "upstream", "1.1.1.1:53")

	if got := strings.Count(buf.String(), "exchange failed"); got != 1 {
		t.Fatalf("emitted %d identical records, want 1:\n%s", got, buf.String())
	}
}

// TestQuietUpstreamErrorsStartupDowngrade proves the boot-window re-leveling:
// an upstream ERROR inside the downgrade window reaches the next handler as
// INFO, and the same record past the window stays ERROR.
func TestQuietUpstreamErrorsStartupDowngrade(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	sink := &recordSink{}
	h := &quietHandler{next: sink, lim: newUpstreamLimiter(clk.now), downgradeUntil: clk.now().Add(startupFailoverWindow)}
	l := slog.New(h)

	l.Error("exchange failed", "upstream", "https://dns.example/dns-query")
	clk.advance(startupFailoverWindow)
	l.Error("exchange failed", "upstream", "https://dns.example/dns-query")

	recs := sink.all()
	if len(recs) != 2 {
		t.Fatalf("emitted %d records, want 2", len(recs))
	}
	if recs[0].Level != slog.LevelInfo {
		t.Fatalf("first record level = %v, want INFO", recs[0].Level)
	}
	if recs[1].Level != slog.LevelError {
		t.Fatalf("record past the window level = %v, want ERROR", recs[1].Level)
	}
}

// TestQuietHandlerClonesKeepDowngrade: a logger derived with .With()/WithGroup
// keeps the startup downgrade window.
func TestQuietHandlerClonesKeepDowngrade(t *testing.T) {
	clk := &fakeClock{t: time.Unix(0, 0)}
	h := &quietHandler{next: &recordSink{}, lim: newUpstreamLimiter(clk.now), downgradeUntil: clk.now().Add(startupFailoverWindow)}

	for _, clone := range []slog.Handler{h.WithAttrs(nil), h.WithGroup("g")} {
		if got := clone.(*quietHandler).downgradeUntil; !got.Equal(h.downgradeUntil) {
			t.Fatalf("clone downgradeUntil = %v, want %v", got, h.downgradeUntil)
		}
	}
}

// TestQuietUpstreamErrorsDowngradeKeepsErrorBelowInfo: a logger at level error
// keeps the record, at error, instead of losing it to the re-level.
func TestQuietUpstreamErrorsDowngradeKeepsErrorBelowInfo(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	clk := &fakeClock{t: time.Unix(0, 0)}
	h := &quietHandler{next: base.Handler(), lim: newUpstreamLimiter(clk.now), downgradeUntil: clk.now().Add(startupFailoverWindow)}

	slog.New(h).Error("exchange failed", "upstream", "https://dns.example/dns-query")

	if got := strings.Count(buf.String(), "exchange failed"); got != 1 {
		t.Fatalf("emitted %d records, want 1 (error passthrough):\n%s", got, buf.String())
	}
	if !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("record was not left at error:\n%s", buf.String())
	}
}

// TestNewDowngradesStartupErrorsWhenFallback mirrors the cap's guard through
// New: with a fallback configured the window is active, without one it is not.
func TestNewDowngradesStartupErrorsWhenFallback(t *testing.T) {
	newWith := func(fallback bool) (r *Resolver) {
		t.Helper()
		cfg := Config{Logger: slog.Default(), Upstreams: []string{"1.1.1.1:53"}}
		if fallback {
			cfg.FallbackUpstreams = []string{"9.9.9.11:53"}
			cfg.FailoverAfter = 10 * time.Second
		}
		r, err := New(context.Background(), cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		return r
	}

	if h := newWith(true).logger.Handler().(*quietHandler); h.downgradeUntil.IsZero() {
		t.Fatal("fallback-configured resolver has no downgrade window")
	}
	if h := newWith(false).logger.Handler().(*quietHandler); !h.downgradeUntil.IsZero() {
		t.Fatal("resolver without fallback downgrades upstream errors")
	}
}
