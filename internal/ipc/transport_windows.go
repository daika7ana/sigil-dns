//go:build windows

package ipc

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// PipeName is the fixed IPC endpoint.  Documented in README and SPEC.
const PipeName = `\\.\pipe\dns-client`

// PipeSDDL is the pipe security descriptor:
//
//	D:  discretionary ACL
//	P   protected (no inherited ACEs)
//	(A;;GA;;;BA)  allow Generic All to Built-in Administrators
//	(A;;GA;;;SY)  allow Generic All to Local System / the service account
//
// Authenticated Users are NOT granted access: this is an elevated
// (unelevated->SYSTEM DNS-control) path, so it must not be open to any local
// process (SPEC §4.7).  A separate read-only pipe for unelevated tray status is
// a follow-up.
const PipeSDDL = `D:P(A;;GA;;;BA)(A;;GA;;;SY)`

// DefaultEndpoint returns the named pipe path.
func DefaultEndpoint() (endpoint string) { return PipeName }

// Listen creates the named-pipe listener with the locking SDDL.
func Listen(endpoint string) (ln net.Listener, err error) {
	return winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: PipeSDDL,
		MessageMode:        false,
	})
}

// Dial connects to the named pipe.
func Dial(ctx context.Context, endpoint string) (conn net.Conn, err error) {
	return winio.DialPipeContext(ctx, endpoint)
}

// defaultAuthorizer treats every connected peer as an administrator.  This is
// sound because PipeSDDL only lets Built-in Administrators and SYSTEM connect.
// Per-connection token impersonation (to allow unelevated read-only callers) is
// deferred: go-winio does not expose the pipe handle from net.Conn.
func defaultAuthorizer(net.Conn) Caller { return Caller{Admin: true} }

// DefaultAuthorizer implements the platform default.
var DefaultAuthorizer Authorizer = defaultAuthorizer
