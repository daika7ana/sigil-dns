package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/daika7ana/dns-client/internal/state"
)

// Lifecycle is the subset of the daemon the IPC server can drive.  It is an
// interface so the server is testable and so the service can expose its own
// lifecycle.Lifecycle.
type Lifecycle interface {
	// Status returns the persisted state, or state.ErrNoState.
	Status() (st *state.State, err error)

	// Enable applies interception.
	Enable(ctx context.Context) (err error)

	// Disable removes interception.
	Disable(ctx context.Context) (err error)
}

// Authorizer derives the identity of a connected peer.  It is injected by the
// transport (or by tests); it must never trust the request body.
type Authorizer func(conn net.Conn) Caller

// Server dispatches IPC requests.
type Server struct {
	lc     Lifecycle
	logger *slog.Logger
}

// NewServer returns a Server backed by lc.
func NewServer(lc Lifecycle, logger *slog.Logger) (s *Server) {
	if logger == nil {
		logger = slog.Default().With("component", "ipc")
	}

	return &Server{lc: lc, logger: logger}
}

// Handle validates and dispatches a single request.  It never panics on
// untrusted input.
func (s *Server) Handle(ctx context.Context, caller Caller, req Request) (resp Response) {
	if req.Version != ProtocolVersion {
		return errorResponse(ErrVersion)
	}
	if !req.Method.valid() {
		return errorResponse(ErrMethod)
	}

	// Defence in depth: the SDDL is the primary boundary, but reject
	// state-changing methods from non-admin callers here too.
	if (req.Method == MethodEnable || req.Method == MethodDisable) && !caller.Admin {
		return errorResponse(ErrForbidden)
	}

	switch req.Method {
	case MethodStatus:
		st, err := s.lc.Status()
		if err != nil {
			if errors.Is(err, state.ErrNoState) {
				return okResponse(nil)
			}

			return errorResponse(err)
		}

		data, err := json.Marshal(st)
		if err != nil {
			return errorResponse(err)
		}

		return okResponse(data)

	case MethodEnable:
		if err := s.lc.Enable(ctx); err != nil {
			return errorResponse(err)
		}

		return okResponse(nil)

	case MethodDisable:
		if err := s.lc.Disable(ctx); err != nil {
			return errorResponse(err)
		}

		return okResponse(nil)

	default:
		return errorResponse(ErrMethod)
	}
}

// okResponse builds a success response.
func okResponse(data json.RawMessage) (resp Response) {
	return Response{Version: ProtocolVersion, OK: true, Data: data}
}

// errorResponse builds a failure response.
func errorResponse(err error) (resp Response) {
	return Response{Version: ProtocolVersion, Error: err.Error()}
}

// Serve accepts connections on ln and serves each until it closes.  It returns
// nil when ctx is canceled.
func Serve(ctx context.Context, ln net.Listener, srv *Server, auth Authorizer) (err error) {
	if auth == nil {
		auth = DefaultAuthorizer
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		conn, aErr := ln.Accept()
		if aErr != nil {
			if ctx.Err() != nil {
				return nil
			}

			return aErr
		}

		wg.Add(1)
		go func(c net.Conn) {
			defer wg.Done()
			defer func() { _ = c.Close() }()

			srv.serveConn(ctx, c, auth(c))
		}(conn)
	}
}

// serveConn reads requests from a single client until EOF.  Garbage input is
// answered with a malformed-request error rather than dropping the connection
// silently.
func (s *Server) serveConn(ctx context.Context, conn net.Conn, caller Caller) {
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}

			_ = enc.Encode(errorResponse(ErrMalformed))

			return
		}

		if err := enc.Encode(s.Handle(ctx, caller, req)); err != nil {
			return
		}
	}
}
