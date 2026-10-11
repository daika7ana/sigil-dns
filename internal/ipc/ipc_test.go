package ipc

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daika7ana/dns-client/internal/state"
)

// fakeLifecycle is an in-memory ipc.Lifecycle.
type fakeLifecycle struct {
	st           *state.State
	stErr        error
	enableErr    error
	disableErr   error
	mu           sync.Mutex
	enableCalls  int
	disableCalls int
}

func (f *fakeLifecycle) Status() (st *state.State, err error) { return f.st, f.stErr }

func (f *fakeLifecycle) Enable(context.Context) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enableCalls++

	return f.enableErr
}

func (f *fakeLifecycle) Disable(context.Context) (err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disableCalls++

	return f.disableErr
}

func TestEnvelopeRoundTrip(t *testing.T) {
	req := Request{Version: ProtocolVersion, Method: MethodStatus, Params: json.RawMessage(`{"x":1}`)}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var gotReq Request
	if err = json.Unmarshal(data, &gotReq); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if gotReq.Version != ProtocolVersion || gotReq.Method != MethodStatus || string(gotReq.Params) != `{"x":1}` {
		t.Fatalf("request round-trip mismatch: %+v", gotReq)
	}

	resp := okResponse(json.RawMessage(`{"ok":true}`))
	if !resp.OK || resp.Version != ProtocolVersion {
		t.Fatalf("okResponse = %+v", resp)
	}

	data, err = json.Marshal(errorResponse(ErrForbidden))
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var gotResp Response
	if err = json.Unmarshal(data, &gotResp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if gotResp.OK || gotResp.Error != ErrForbidden.Error() {
		t.Fatalf("error response round-trip mismatch: %+v", gotResp)
	}
}

func TestHandleRejectsVersionAndMethod(t *testing.T) {
	srv := NewServer(&fakeLifecycle{}, nil)
	admin := Caller{Admin: true}

	if resp := srv.Handle(context.Background(), admin, Request{Version: 999, Method: MethodStatus}); resp.OK ||
		resp.Error != ErrVersion.Error() {
		t.Errorf("bad version: %+v", resp)
	}

	if resp := srv.Handle(context.Background(), admin, Request{Version: ProtocolVersion, Method: "frobnicate"}); resp.OK ||
		resp.Error != ErrMethod.Error() {
		t.Errorf("unknown method: %+v", resp)
	}
}

func TestHandleAuthorization(t *testing.T) {
	lc := &fakeLifecycle{}
	srv := NewServer(lc, nil)
	ctx := context.Background()

	// Read-only status is allowed for any caller.
	if resp := srv.Handle(ctx, Caller{Admin: false}, Request{Version: ProtocolVersion, Method: MethodStatus}); !resp.OK {
		t.Errorf("status denied for non-admin: %+v", resp)
	}

	// State-changing methods require admin.
	for _, m := range []Method{MethodEnable, MethodDisable} {
		if resp := srv.Handle(ctx, Caller{Admin: false}, Request{Version: ProtocolVersion, Method: m}); resp.OK ||
			resp.Error != ErrForbidden.Error() {
			t.Errorf("%s non-admin: %+v", m, resp)
		}
	}
	if lc.enableCalls != 0 || lc.disableCalls != 0 {
		t.Fatal("non-admin caller reached the lifecycle")
	}

	// Admin may drive them.
	if resp := srv.Handle(ctx, Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodEnable}); !resp.OK {
		t.Errorf("admin enable: %+v", resp)
	}
	if lc.enableCalls != 1 {
		t.Fatalf("enableCalls = %d, want 1", lc.enableCalls)
	}
}

func TestHandleStatusNoState(t *testing.T) {
	srv := NewServer(&fakeLifecycle{stErr: state.ErrNoState}, nil)

	resp := srv.Handle(context.Background(), Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodStatus})
	if !resp.OK {
		t.Fatalf("status with no state: %+v", resp)
	}
}

func TestServeConnGarbage(t *testing.T) {
	srv := NewServer(&fakeLifecycle{}, nil)

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	go srv.serveConn(context.Background(), server, Caller{Admin: true})

	if _, err := client.Write([]byte("this is not json\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp Response
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.OK || !strings.Contains(resp.Error, ErrMalformed.Error()) {
		t.Fatalf("garbage response = %+v", resp)
	}
}

func startTestServer(t *testing.T, lc Lifecycle, auth Authorizer) (endpoint string) {
	t.Helper()

	endpoint = filepath.Join(t.TempDir(), "ipc.sock")
	ln, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, NewServer(lc, nil), auth) }()

	t.Cleanup(func() {
		cancel()
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Serve did not return")
		}
	})

	return endpoint
}

func TestClientRoundTrip(t *testing.T) {
	lc := &fakeLifecycle{st: &state.State{Mode: state.ModeOn, Dirty: true}}
	endpoint := startTestServer(t, lc, func(net.Conn) Caller { return Caller{Admin: true} })
	c := NewClient(endpoint)

	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st == nil || st.Mode != state.ModeOn || !st.Dirty {
		t.Fatalf("Status = %+v", st)
	}

	if err = c.Enable(ctx); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err = c.Disable(ctx); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if lc.enableCalls != 1 || lc.disableCalls != 1 {
		t.Fatalf("calls: enable=%d disable=%d", lc.enableCalls, lc.disableCalls)
	}
}

func TestClientForbidden(t *testing.T) {
	endpoint := startTestServer(t, &fakeLifecycle{}, func(net.Conn) Caller { return Caller{Admin: false} })
	c := NewClient(endpoint)

	err := c.Enable(context.Background())
	if err == nil || !strings.Contains(err.Error(), ErrForbidden.Error()) {
		t.Fatalf("Enable as non-admin: err = %v, want forbidden", err)
	}
}

func TestDefaultEndpointNonEmpty(t *testing.T) {
	if DefaultEndpoint() == "" {
		t.Fatal("DefaultEndpoint is empty")
	}
}
