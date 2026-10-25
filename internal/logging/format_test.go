package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestTextHandlerFormat(t *testing.T) {
	var buf bytes.Buffer
	h := NewTextHandler(&buf, slog.LevelDebug)

	ts := time.Date(2026, 10, 25, 6, 56, 45, 787000000, time.FixedZone("CEST", 2*60*60))
	rec := slog.NewRecord(ts, slog.LevelWarn, "interception restore deferred", 0)
	rec.AddAttrs(
		slog.String("err", "powershell: exit status 1: boom"),
		slog.Int("count", 3),
		slog.String("plain", "no-spaces"),
		slog.String("empty", ""),
	)
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	want := "[2026-10-25 06:56:45.787 +0200][WARN] interception restore deferred " +
		`err="powershell: exit status 1: boom" count=3 plain=no-spaces empty=""` + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("log = %q, want %q", got, want)
	}
}

func TestTextHandlerAttrsAndGroups(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(NewTextHandler(&buf, slog.LevelDebug)).With("component", "resolver").WithGroup("q")

	l.Info("query", "name", "example.com")

	want := "["
	got := buf.String()
	if !strings.HasSuffix(got, "][INFO] query component=resolver q.name=example.com\n") {
		t.Fatalf("log = %q, want suffix %q", got, "][INFO] query component=resolver q.name=example.com\n")
	}
	if !strings.HasPrefix(got, want) {
		t.Fatalf("log = %q, want a [time] prefix", got)
	}
}

func TestTextHandlerLevelFilter(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(NewTextHandler(&buf, slog.LevelInfo))

	l.Debug("hidden")
	if buf.Len() != 0 {
		t.Fatalf("debug record written at level info: %q", buf.String())
	}

	l.Warn("shown")
	if !strings.Contains(buf.String(), "][WARN] shown\n") {
		t.Fatalf("warn record missing: %q", buf.String())
	}
}

// TestTextHandlerOneLine guards the line-oriented readers: an embedded newline
// must not split one entry into two.
func TestTextHandlerOneLine(t *testing.T) {
	var buf bytes.Buffer
	slog.New(NewTextHandler(&buf, slog.LevelInfo)).Error("first\nsecond\r\nthird")

	got := buf.String()
	if strings.Count(got, "\n") != 1 || !strings.HasSuffix(got, `first\nsecond\r\nthird`+"\n") {
		t.Fatalf("log = %q", got)
	}
}

// TestTextHandlerEscapesControlBytes guards log injection: a control byte in a
// message must reach the file as text, never as a real terminal escape.
func TestTextHandlerEscapesControlBytes(t *testing.T) {
	var buf bytes.Buffer
	slog.New(NewTextHandler(&buf, slog.LevelInfo)).Error("a\x1b[2Jb")

	got := buf.String()
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("raw escape kept: %q", got)
	}
	if !strings.HasSuffix(got, `a\x1b[2Jb`+"\n") {
		t.Fatalf("log = %q", got)
	}
}

func TestQuoteIfNeeded(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", `""`},
		{"plain", "plain"},
		{"has space", `"has space"`},
		{"a=b", `"a=b"`},
		{"ü", `"ü"`},
	} {
		if got := quoteIfNeeded(tc.in); got != tc.want {
			t.Errorf("quoteIfNeeded(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
