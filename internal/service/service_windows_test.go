//go:build windows

package service

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/daika7ana/sigil-dns/internal/state"
)

// TestStopLogLevels pins where a failed restore is reported: a plain stop is an
// error, a system shutdown is a warning (the state stays dirty and the next
// start retries, so it is not actionable at shutdown).
func TestStopLogLevels(t *testing.T) {
	for _, tc := range []struct {
		name     string
		shutdown bool
		level    string
		msg      string
	}{
		{name: "stop", shutdown: false, level: "level=ERROR", msg: `msg="service stop failed"`},
		{name: "shutdown", shutdown: true, level: "level=WARN", msg: `msg="interception restore deferred`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			lc := &fakeAgentLC{
				st:         &state.State{Mode: state.ModeOn, Dirty: true},
				disableErr: errors.New("restore failed"),
			}
			agent := NewAgent(lc, logger, testEndpoint(t, "stop"))
			agent.SetStatusEndpoint("") // no status pipe to wake

			h := &handler{agent: agent}
			h.stop(tc.shutdown)

			got := buf.String()
			for _, want := range []string{tc.level, tc.msg} {
				if !strings.Contains(got, want) {
					t.Fatalf("log = %q, want %s", got, want)
				}
			}
		})
	}
}
