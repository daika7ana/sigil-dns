//go:build windows

package service

import (
	"bytes"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/daika7ana/sigil-dns/internal/state"
	"golang.org/x/sys/windows/svc"
)

// TestStopLogLevels pins where a failed restore is reported: a plain stop is an
// error (after one bounded retry) with a recovery hint, a system shutdown is a
// warning (the state stays dirty and the next start retries, so it is not
// actionable at shutdown).
func TestStopLogLevels(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cmd       svc.Cmd
		level     string
		msg       string
		wantCalls int
	}{
		{
			name: "stop", cmd: svc.Stop, level: "level=ERROR",
			msg:       `msg="service stop failed; run 'sigildns restore' to undo interception"`,
			wantCalls: 2,
		},
		{name: "shutdown", cmd: svc.Shutdown, level: "level=WARN", msg: `msg="interception restore deferred`, wantCalls: 1},
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
			h.stop(tc.cmd)

			if lc.disableCalls != tc.wantCalls {
				t.Fatalf("Disable calls = %d, want %d", lc.disableCalls, tc.wantCalls)
			}

			got := buf.String()
			for _, want := range []string{tc.level, tc.msg} {
				if !strings.Contains(got, want) {
					t.Fatalf("log = %q, want %s", got, want)
				}
			}
		})
	}
}

// TestPreShutdownRestores pins the pre-shutdown arm: the notification must
// restore interception under the preshutdown budget (not the shorter legacy
// stop budget) and exit cleanly, so the SCM runs no recovery actions while the
// machine is going down.
func TestPreShutdownRestores(t *testing.T) {
	lc := &fakeAgentLC{st: &state.State{Mode: state.ModeOn}}
	h := &handler{agent: newTestAgent(t, lc)}

	reqCh := make(chan svc.ChangeRequest)
	statusCh := make(chan svc.Status, 8)

	type result struct {
		specific bool
		code     uint32
	}
	done := make(chan result, 1)
	go func() {
		specific, code := h.Execute(nil, reqCh, statusCh)
		done <- result{specific, code}
	}()

	// Once the service reports Running the handler is in its select loop, so the
	// send below is received by the arm under test.
	for running := false; !running; {
		select {
		case st := <-statusCh:
			running = st.State == svc.Running
		case <-time.After(5 * time.Second):
			t.Fatal("service did not reach Running")
		}
	}

	select {
	case reqCh <- svc.ChangeRequest{Cmd: svc.PreShutdown}:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not accept the pre-shutdown request")
	}

	select {
	case got := <-done:
		if got.specific || got.code != 0 {
			t.Fatalf("Execute = (%v, %d), want (false, 0)", got.specific, got.code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after the pre-shutdown notification")
	}

	if lc.disableCalls != 1 {
		t.Fatalf("Disable calls = %d, want 1", lc.disableCalls)
	}
	if lc.disableBudget <= shutdownRestoreBudget {
		t.Fatalf("restore budget = %v, want the preshutdown budget above %v", lc.disableBudget, shutdownRestoreBudget)
	}
}

// TestPendingRequest pins the peek used when a start fails: it must see a
// request only while one is actually waiting, and a closed channel is not one.
func TestPendingRequest(t *testing.T) {
	ch := make(chan svc.ChangeRequest, 1)

	if _, ok := pendingRequest(ch); ok {
		t.Fatal("pendingRequest reported a request on an idle channel")
	}

	ch <- svc.ChangeRequest{Cmd: svc.PreShutdown}
	if c, ok := pendingRequest(ch); !ok || c.Cmd != svc.PreShutdown {
		t.Fatalf("pendingRequest = (%v, %v), want the queued pre-shutdown", c.Cmd, ok)
	}

	close(ch)
	if _, ok := pendingRequest(ch); ok {
		t.Fatal("pendingRequest reported a request on a closed channel")
	}
}

// TestStartRefusedDuringShutdown pins the start-time guard: a start that races
// an in-progress shutdown must return before starting the agent, so it never
// enables interception on a machine that is going down.
func TestStartRefusedDuringShutdown(t *testing.T) {
	prev := shutdownProbe
	shutdownProbe = func() bool { return true }

	t.Cleanup(func() { shutdownProbe = prev })

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	h := &handler{agent: &Agent{logger: logger}}
	ch := make(chan svc.Status, 4)

	svcErr, exitCode := h.Execute(nil, nil, ch)
	if svcErr || exitCode != 0 {
		t.Fatalf("Execute() = (%v, %d), want (false, 0)", svcErr, exitCode)
	}

	var first svc.Status
	var states []svc.State
	for len(ch) > 0 {
		st := <-ch
		if len(states) == 0 {
			first = st
		}

		states = append(states, st.State)
	}

	want := []svc.State{svc.StartPending, svc.StopPending}
	if !slices.Equal(states, want) {
		t.Fatalf("states = %v, want %v", states, want)
	}

	// The registration bit is what makes the SCM send pre-shutdown at all, so a
	// test that injects the request directly cannot catch its loss.
	if first.Accepts&svc.AcceptPreShutdown == 0 {
		t.Fatalf("StartPending accepts = %v, want pre-shutdown registered", first.Accepts)
	}

	if got := buf.String(); !strings.Contains(got, "not starting") {
		t.Fatalf("log = %q, want it to contain %q", got, "not starting")
	}
}
