package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/daika7ana/sigil-dns/internal/state"
)

// testEndpoint returns a unique, platform-appropriate endpoint: a named pipe on
// Windows, a temp Unix socket elsewhere.
func testEndpoint(t *testing.T) (endpoint string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\sigildns-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}

	return filepath.Join(t.TempDir(), "ipc.sock")
}

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

// fakeRestorer is an in-memory ipc.Restorer.
type fakeRestorer struct {
	err   error
	calls int
}

func (f *fakeRestorer) Restore(context.Context) (err error) {
	f.calls++

	return f.err
}

// fakeRefresher is an in-memory ipc.ListRefresher.
type fakeRefresher struct {
	err   error
	calls int
}

func (f *fakeRefresher) RefreshLists(context.Context) (err error) {
	f.calls++

	return f.err
}

// TestHandleRefreshLists covers the admin-only refresh_lists method: rejected
// for a non-admin caller, unsupported without a refresher, and dispatched when
// wired.
func TestHandleRefreshLists(t *testing.T) {
	ctx := context.Background()

	r := &fakeRefresher{}
	srv := NewServer(&fakeLifecycle{}, nil).SetListRefresher(r)

	if resp := srv.Handle(ctx, Caller{Admin: false}, Request{Version: ProtocolVersion, Method: MethodRefreshLists}); resp.OK ||
		resp.Error != ErrForbidden.Error() {
		t.Fatalf("refresh_lists non-admin: %+v", resp)
	}
	if r.calls != 0 {
		t.Fatal("non-admin caller reached the refresher")
	}

	if resp := srv.Handle(ctx, Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodRefreshLists}); !resp.OK {
		t.Fatalf("refresh_lists admin: %+v", resp)
	}
	if r.calls != 1 {
		t.Fatalf("refresh calls = %d, want 1", r.calls)
	}

	// Without a refresher the method is unsupported, not a panic.
	if resp := NewServer(&fakeLifecycle{}, nil).Handle(ctx, Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodRefreshLists}); resp.OK ||
		resp.Error != ErrUnsupported.Error() {
		t.Fatalf("refresh_lists without handler: %+v", resp)
	}
}

// fakeClearer is an in-memory ipc.CacheClearer.
type fakeClearer struct {
	err   error
	calls int
}

func (f *fakeClearer) ClearCache(context.Context) (err error) {
	f.calls++

	return f.err
}

// TestHandleClearCache covers the admin-only clear_cache method: rejected for a
// non-admin caller, unsupported without a handler, and dispatched when wired.
func TestHandleClearCache(t *testing.T) {
	ctx := context.Background()

	c := &fakeClearer{}
	srv := NewServer(&fakeLifecycle{}, nil).SetCacheClearer(c)

	if resp := srv.Handle(ctx, Caller{Admin: false}, Request{Version: ProtocolVersion, Method: MethodClearCache}); resp.OK ||
		resp.Error != ErrForbidden.Error() {
		t.Fatalf("clear_cache non-admin: %+v", resp)
	}
	if c.calls != 0 {
		t.Fatal("non-admin caller reached the cleaner")
	}

	if resp := srv.Handle(ctx, Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodClearCache}); !resp.OK {
		t.Fatalf("clear_cache admin: %+v", resp)
	}
	if c.calls != 1 {
		t.Fatalf("clear calls = %d, want 1", c.calls)
	}

	if resp := NewServer(&fakeLifecycle{}, nil).Handle(ctx, Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodClearCache}); resp.OK ||
		resp.Error != ErrUnsupported.Error() {
		t.Fatalf("clear_cache without handler: %+v", resp)
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	req := Request{Version: ProtocolVersion, Method: MethodStatus}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var gotReq Request
	if err = json.Unmarshal(data, &gotReq); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if gotReq.Version != ProtocolVersion || gotReq.Method != MethodStatus {
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
	for _, m := range []Method{MethodEnable, MethodDisable, MethodReload, MethodRestore} {
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

func TestHandleRestore(t *testing.T) {
	r := &fakeRestorer{}
	srv := NewServer(&fakeLifecycle{}, nil).SetRestorer(r)

	if resp := srv.Handle(context.Background(), Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodRestore}); !resp.OK {
		t.Fatalf("restore: %+v", resp)
	}
	if r.calls != 1 {
		t.Fatalf("restore calls = %d, want 1", r.calls)
	}

	// Without a restorer the method is unsupported, not a panic.
	if resp := NewServer(&fakeLifecycle{}, nil).Handle(context.Background(), Caller{Admin: true}, Request{Version: ProtocolVersion, Method: MethodRestore}); resp.OK ||
		resp.Error != ErrUnsupported.Error() {
		t.Fatalf("restore without handler: %+v", resp)
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

// panicLifecycle panics in Enable to exercise the per-request recover.
type panicLifecycle struct {
	fakeLifecycle
}

func (p *panicLifecycle) Enable(context.Context) error { panic("boom") }

// TestServeConnRecoversPanicAndStaysUsable proves a panicking handler yields a
// generic error Response (not a dropped connection/EOF) and the connection
// keeps serving afterwards.
func TestServeConnRecoversPanicAndStaysUsable(t *testing.T) {
	srv := NewServer(&panicLifecycle{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	go srv.serveConn(context.Background(), server, Caller{Admin: true})

	enc := json.NewEncoder(client)
	dec := json.NewDecoder(client)

	// 1. The panicking method returns the generic error, not EOF.
	_ = client.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := enc.Encode(Request{Version: ProtocolVersion, Method: MethodEnable}); err != nil {
		t.Fatalf("encode enable: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode panic response (connection died?): %v", err)
	}
	if resp.OK || resp.Error != ErrInternal.Error() {
		t.Fatalf("panic response = %+v, want generic %q", resp, ErrInternal)
	}

	// 2. The same connection still serves a subsequent good request.
	_ = client.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := enc.Encode(Request{Version: ProtocolVersion, Method: MethodStatus}); err != nil {
		t.Fatalf("encode status: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp2 Response
	if err := dec.Decode(&resp2); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if !resp2.OK {
		t.Fatalf("status response = %+v, want ok", resp2)
	}
}

// slowLifecycle sleeps in Status to simulate a long-running handler (such as
// reload's restore + re-enable + health check) that exceeds requestTimeout.
type slowLifecycle struct {
	fakeLifecycle
	delay time.Duration
}

func (s *slowLifecycle) Status() (*state.State, error) {
	time.Sleep(s.delay)

	return s.fakeLifecycle.Status()
}

// TestServeConnSlowHandlerNotEOF is the regression guard: the write deadline
// must bound only the response WRITE, not the whole handler, or a slow but
// successful method (e.g. reload) is cut off and the client sees EOF.  The
// write deadline is still armed immediately before Encode, so a genuinely
// blocked/unwritable peer is still bounded.
func TestServeConnSlowHandlerNotEOF(t *testing.T) {
	srv := NewServer(&slowLifecycle{delay: 400 * time.Millisecond},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.requestTimeout = 100 * time.Millisecond

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	go srv.serveConn(context.Background(), server, Caller{Admin: true})

	enc := json.NewEncoder(client)
	dec := json.NewDecoder(client)

	_ = client.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if err := enc.Encode(Request{Version: ProtocolVersion, Method: MethodStatus}); err != nil {
		t.Fatalf("encode status: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode slow response (EOF = write deadline armed too early): %v", err)
	}
	if !resp.OK {
		t.Fatalf("slow response = %+v, want ok", resp)
	}
}

func startTestServer(t *testing.T, lc Lifecycle, auth Authorizer) (endpoint string) {
	t.Helper()

	endpoint = testEndpoint(t)
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

	conn, err := Dial(ctx, endpoint)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var st *state.State
	if err = Exchange(conn, MethodStatus, &st); err != nil {
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

	// restore is admin-only too: the read-only status pipe must reject it.
	if err = c.Restore(context.Background()); err == nil || !strings.Contains(err.Error(), ErrForbidden.Error()) {
		t.Fatalf("Restore as non-admin: err = %v, want forbidden", err)
	}
}

func TestDefaultEndpointNonEmpty(t *testing.T) {
	if DefaultEndpoint() == "" {
		t.Fatal("DefaultEndpoint is empty")
	}
	if StatusEndpoint() == "" {
		t.Fatal("StatusEndpoint is empty")
	}
	if DefaultEndpoint() == StatusEndpoint() {
		t.Fatal("control and status endpoints must differ")
	}
}

func TestServeRequiresAuthorizer(t *testing.T) {
	endpoint := testEndpoint(t)
	ln, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	if err = Serve(context.Background(), ln, NewServer(&fakeLifecycle{}, nil), nil); !errors.Is(err, ErrNoAuthorizer) {
		t.Fatalf("Serve with nil authorizer = %v, want ErrNoAuthorizer", err)
	}
}

func TestStatusSanitized(t *testing.T) {
	lc := &fakeLifecycle{st: &state.State{
		Mode:      state.ModeOn,
		Intercept: state.MechanismNRPT,
		Upstream:  "https://dns.nextdns.io/SECRETID",
		Dirty:     true,
	}}
	srv := NewServer(lc, nil).SanitizeStatus()

	resp := srv.Handle(context.Background(), Caller{Admin: false}, Request{Version: ProtocolVersion, Method: MethodStatus})
	if !resp.OK {
		t.Fatalf("status: %+v", resp)
	}
	if strings.Contains(string(resp.Data), "SECRETID") {
		t.Fatalf("sanitized status leaked active_upstream: %s", resp.Data)
	}

	var dto StatusDTO
	if err := json.Unmarshal(resp.Data, &dto); err != nil {
		t.Fatalf("unmarshal DTO: %v", err)
	}
	if !dto.Enabled || dto.Mode != string(state.ModeOn) || dto.Mechanism != string(state.MechanismNRPT) || !dto.Dirty {
		t.Fatalf("DTO = %+v", dto)
	}
}

func TestClientRejectsBadServerVersion(t *testing.T) {
	endpoint := testEndpoint(t)
	ln, err := Listen(endpoint)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		c, aErr := ln.Accept()
		if aErr != nil {
			return
		}
		defer func() { _ = c.Close() }()

		var req Request
		_ = json.NewDecoder(c).Decode(&req)
		_ = json.NewEncoder(c).Encode(Response{Version: 999, OK: true})
	}()

	conn, err := Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	var st *state.State
	if err = Exchange(conn, MethodStatus, &st); !errors.Is(err, ErrVersion) {
		t.Fatalf("bad server version err = %v, want ErrVersion", err)
	}
}

func TestOversizedRequestRejected(t *testing.T) {
	endpoint := startTestServer(t, &fakeLifecycle{}, AdminAuthorizer)

	conn, err := Dial(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	payload := `{"version":1,"method":"` + strings.Repeat("A", maxRequestBytes+1024) + `"}`
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, _ = conn.Write([]byte(payload))

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var resp Response
	derr := json.NewDecoder(conn).Decode(&resp)
	if derr == nil && resp.OK {
		t.Fatal("oversized request was accepted")
	}
}

func TestRequestTimeout(t *testing.T) {
	srv := NewServer(&fakeLifecycle{}, nil)
	srv.requestTimeout = 50 * time.Millisecond

	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	done := make(chan struct{})
	go func() {
		srv.serveConn(context.Background(), server, Caller{Admin: true})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serveConn did not time out on an idle client")
	}
}

// errListener fails Accept a fixed number of times before yielding conn.
type errListener struct {
	conn          net.Conn
	errsRemaining int
}

func (l *errListener) Accept() (net.Conn, error) {
	if l.errsRemaining > 0 {
		l.errsRemaining--

		return nil, errors.New("transient accept error")
	}
	if l.conn != nil {
		c := l.conn
		l.conn = nil

		return c, nil
	}

	return nil, net.ErrClosed
}

func (l *errListener) Close() error   { return nil }
func (l *errListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestServeRetriesTransientAccept(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()

	ln := &errListener{errsRemaining: 2, conn: server}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, NewServer(&fakeLifecycle{}, nil), AdminAuthorizer) }()

	// The server should recover and serve the connection.
	if err := json.NewEncoder(client).Encode(Request{Version: ProtocolVersion, Method: MethodStatus}); err != nil {
		t.Fatalf("send: %v", err)
	}

	_ = client.SetReadDeadline(time.Now().Add(3 * time.Second))
	var resp Response
	if err := json.NewDecoder(client).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK {
		t.Fatalf("response = %+v", resp)
	}

	cancel()
	_ = client.Close()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestServeGivesUpAfterRepeatedAcceptFailures(t *testing.T) {
	ln := &errListener{errsRemaining: maxAcceptFailures}

	err := Serve(context.Background(), ln, NewServer(&fakeLifecycle{}, nil), AdminAuthorizer)
	if err == nil {
		t.Fatal("Serve should give up after repeated Accept failures")
	}
}
