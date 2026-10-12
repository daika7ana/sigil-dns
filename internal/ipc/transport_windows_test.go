//go:build windows

package ipc

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/daika7ana/dns-client/internal/state"
)

// TestWindowsPipeRoundTrip exercises the REAL go-winio listener, server, and
// dialer with the production control SDDL.  This is the class of test that
// catches an unreachable pipe (e.g. a bad SDDL / account mismatch).
func TestWindowsPipeRoundTrip(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\dns-client-test-%d`, time.Now().UnixNano())

	ln, err := Listen(name)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	lc := &fakeLifecycle{st: &state.State{Mode: state.ModeOn}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, NewServer(lc, nil), AdminAuthorizer) }()

	conn, err := Dial(ctx, name)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var st *state.State
	if err = Exchange(conn, MethodStatus, &st); err != nil {
		t.Fatalf("Status over pipe: %v", err)
	}
	if st == nil || st.Mode != state.ModeOn {
		t.Fatalf("Status = %+v", st)
	}

	// Disconnect before shutting down: Serve drains in-flight connections
	// (defer wg.Wait), so an open client keeps it from returning until the
	// server-side read deadline expires.
	_ = conn.Close()
	cancel()
	_ = ln.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

// TestWindowsStatusPipeRoundTrip exercises the read-only status pipe: the
// sanitized DTO is readable and enable is forbidden.
func TestWindowsStatusPipeRoundTrip(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\dns-client-status-test-%d`, time.Now().UnixNano())

	ln, err := ListenStatus(name)
	if err != nil {
		t.Fatalf("ListenStatus: %v", err)
	}

	lc := &fakeLifecycle{st: &state.State{Mode: state.ModeOn, Intercept: state.MechanismNRPT}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, NewServer(lc, nil).SanitizeStatus(), ReadOnlyAuthorizer)
	}()

	c := NewClient(name)

	conn, err := Dial(ctx, name)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var dto StatusDTO
	if err = Exchange(conn, MethodStatus, &dto); err != nil {
		t.Fatalf("StatusSummary over status pipe: %v", err)
	}
	if !dto.Enabled {
		t.Fatalf("DTO = %+v", dto)
	}

	if err = c.Enable(ctx); err == nil {
		t.Fatal("Enable on status pipe should be forbidden")
	}

	// Disconnect before shutting down: Serve drains in-flight connections
	// (defer wg.Wait), so an open client keeps it from returning until the
	// server-side read deadline expires.
	_ = conn.Close()
	cancel()
	_ = ln.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}
