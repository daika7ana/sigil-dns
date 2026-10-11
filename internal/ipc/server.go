package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/state"
	"github.com/daika7ana/dns-client/internal/version"
)

// Robustness limits for the IPC server (M7).
const (
	// maxRequestBytes caps the bytes read from a single connection.
	maxRequestBytes = 64 << 10

	// defaultMaxConns is the maximum number of concurrent client connections.
	defaultMaxConns = 32

	// defaultRequestTimeout bounds each read and write on a connection.
	defaultRequestTimeout = 10 * time.Second

	// maxAcceptFailures is how many consecutive Accept errors kill the server.
	maxAcceptFailures = 10

	// acceptRetryDelay is the pause between Accept retries.
	acceptRetryDelay = 100 * time.Millisecond
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

// Reloader optionally handles the admin reload method.  It is set on the control
// pipe only.
type Reloader interface {
	// Reload reloads the configuration and returns the new summary.
	Reload(ctx context.Context) (sum ConfigSummary, err error)
}

// Restorer optionally handles the admin restore method (config-independent
// crash/dirty recovery).  It is set on the control pipe only.
type Restorer interface {
	// Restore restores interception from persisted state.
	Restore(ctx context.Context) (err error)
}

// Authorizer derives the identity of a connected peer.  It is injected by the
// transport (or by tests); it must never trust the request body.
type Authorizer func(conn net.Conn) Caller

// AdminAuthorizer grants administrative privileges.  It is safe on the control
// pipe only because that pipe's SDDL restricts connect to SYSTEM and
// Administrators.
func AdminAuthorizer(net.Conn) Caller { return Caller{Admin: true} }

// ReadOnlyAuthorizer denies administrative privileges.  The status pipe is
// served with this authorizer, so enable/disable/reload are rejected.
func ReadOnlyAuthorizer(net.Conn) Caller { return Caller{Admin: false} }

// Server dispatches IPC requests.
type Server struct {
	lc             Lifecycle
	reloader       Reloader
	restorer       Restorer
	logger         *slog.Logger
	configPath     string
	requestTimeout time.Duration
	maxConns       int
	sanitize       bool
}

// NewServer returns a Server backed by lc.
func NewServer(lc Lifecycle, logger *slog.Logger) (s *Server) {
	if logger == nil {
		logger = slog.Default().With("component", "ipc")
	}

	return &Server{
		lc:             lc,
		logger:         logger,
		requestTimeout: defaultRequestTimeout,
		maxConns:       defaultMaxConns,
	}
}

// SanitizeStatus makes the server return the sanitized StatusDTO on status
// requests.  Used for the read-only status pipe.
func (s *Server) SanitizeStatus() *Server {
	s.sanitize = true

	return s
}

// SetConfigPath sets the config file path used by config_summary.  The service
// sets it to the fixed default path.
func (s *Server) SetConfigPath(path string) *Server {
	s.configPath = path

	return s
}

// SetReloader sets the reload handler (control pipe only).
func (s *Server) SetReloader(r Reloader) *Server {
	s.reloader = r

	return s
}

// SetRestorer sets the restore handler (control pipe only).
func (s *Server) SetRestorer(r Restorer) *Server {
	s.restorer = r

	return s
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

	// Single authorization table (SPEC §15.3.2): the SDDL is the primary
	// boundary, but reject admin methods from non-admin callers here too.
	if req.Method.privilege() == Admin && !caller.Admin {
		return errorResponse(ErrForbidden)
	}

	switch req.Method {
	case MethodStatus:
		return s.handleStatus()

	case MethodInfo:
		return marshalOK(Info{
			ProtocolVersion: ProtocolVersion,
			AppVersion:      version.Version,
			Capabilities:    Capabilities,
		})

	case MethodConfigSummary:
		return marshalOK(s.configSummary())

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

	case MethodReload:
		if s.reloader == nil {
			return errorResponse(ErrUnsupported)
		}

		sum, err := s.reloader.Reload(ctx)
		if err != nil {
			return errorResponse(err)
		}

		return marshalOK(sum)

	case MethodRestore:
		if s.restorer == nil {
			return errorResponse(ErrUnsupported)
		}

		if err := s.restorer.Restore(ctx); err != nil {
			return errorResponse(err)
		}

		return okResponse(nil)

	default:
		return errorResponse(ErrMethod)
	}
}

// handleStatus returns the full or sanitized status.
func (s *Server) handleStatus() (resp Response) {
	st, err := s.lc.Status()
	if err != nil {
		if errors.Is(err, state.ErrNoState) {
			return okResponse(nil)
		}

		return errorResponse(err)
	}

	if s.sanitize {
		return marshalOK(sanitizeStatus(st))
	}

	data, err := json.Marshal(st)
	if err != nil {
		return errorResponse(err)
	}

	return okResponse(data)
}

// configSummary loads the config from s.configPath and returns a sanitized
// summary.  A missing file falls back to defaults (valid); a broken file yields
// a secret-free error summary.
func (s *Server) configSummary() (sum ConfigSummary) {
	path := s.configPath

	cfg, err := config.Load(path)
	if err != nil && !errors.Is(err, config.ErrNoConfig) {
		return ConfigSummary{
			ConfigValid: false,
			ConfigError: SanitizedConfigError(err),
			Revision:    Revision(path),
		}
	}

	if cfg == nil {
		cfg = config.Default()
	}

	sum = Summarize(cfg)
	sum.ConfigValid = true
	sum.Revision = Revision(path)

	return sum
}

// marshalOK marshals v into a success response.
func marshalOK(v any) (resp Response) {
	data, err := json.Marshal(v)
	if err != nil {
		return errorResponse(err)
	}

	return okResponse(data)
}

// sanitizeStatus converts persisted state into the safe status DTO.
func sanitizeStatus(st *state.State) (dto StatusDTO) {
	if st == nil {
		return dto
	}

	dto = StatusDTO{
		SchemaVersion: st.SchemaVersion,
		Mode:          string(st.Mode),
		Mechanism:     string(st.Intercept),
		Enabled:       st.Mode == state.ModeOn,
		Dirty:         st.Dirty,
	}

	if st.Health != nil {
		dto.Health = &HealthSummary{
			Ours:   st.Health.Ours,
			IPv4:   st.Health.IPv4,
			IPv6:   st.Health.IPv6,
			Detail: st.Health.Detail,
		}
	}

	return dto
}

// okResponse builds a success response.
func okResponse(data json.RawMessage) (resp Response) {
	return Response{Version: ProtocolVersion, OK: true, Data: data}
}

// errorResponse builds a failure response.
func errorResponse(err error) Response {
	return Response{Version: ProtocolVersion, Error: err.Error()}
}

// Serve accepts connections on ln and serves each until it closes.  auth must
// be non-nil: every listener names its own authorizer (there is no implicit
// fallback, to avoid a latent privilege escalation).  It returns nil when ctx is
// canceled, and an error after maxAcceptFailures consecutive Accept failures.
func Serve(ctx context.Context, ln net.Listener, srv *Server, auth Authorizer) (err error) {
	if srv == nil {
		return errors.New("ipc: nil server")
	}
	if auth == nil {
		return ErrNoAuthorizer
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	sem := make(chan struct{}, srv.maxConns)
	fails := 0

	for {
		conn, aErr := ln.Accept()
		if aErr != nil {
			if ctx.Err() != nil {
				return nil
			}

			fails++
			if fails >= maxAcceptFailures {
				return fmt.Errorf("accept: %w", aErr)
			}

			srv.logger.WarnContext(ctx, "accept failed; retrying", "err", aErr, "failures", fails)

			select {
			case <-ctx.Done():
				return nil
			case <-time.After(acceptRetryDelay):
			}

			continue
		}

		fails = 0

		select {
		case sem <- struct{}{}:
		default:
			_ = conn.Close()
			srv.logger.WarnContext(ctx, "too many connections; dropping")

			continue
		}

		caller := auth(conn)
		wg.Add(1)

		go func(c net.Conn, id Caller) {
			defer wg.Done()
			defer func() {
				<-sem
				_ = c.Close()
			}()

			srv.serveConn(ctx, c, id)
		}(conn, caller)
	}
}

// serveConn reads requests from a single client until EOF, applying read and
// write deadlines and a per-connection byte cap.
func (s *Server) serveConn(ctx context.Context, conn net.Conn, caller Caller) {
	dec := json.NewDecoder(io.LimitReader(conn, maxRequestBytes))
	enc := json.NewEncoder(conn)

	for {
		_ = conn.SetReadDeadline(time.Now().Add(s.requestTimeout))

		var req Request
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}

			_ = conn.SetWriteDeadline(time.Now().Add(s.requestTimeout))
			_ = enc.Encode(errorResponse(ErrMalformed))

			return
		}

		_ = conn.SetWriteDeadline(time.Now().Add(s.requestTimeout))
		if err := enc.Encode(s.Handle(ctx, caller, req)); err != nil {
			return
		}
	}
}
