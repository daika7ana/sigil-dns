//go:build windows

package ipc

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// Named pipe endpoints.  Both are documented in README and SPEC §2/§4.7.
const (
	// PipeName is the control pipe: state-changing methods, admin/SYSTEM only.
	PipeName = `\\.\pipe\dns-client`

	// StatusPipeName is the read-only status pipe.
	StatusPipeName = `\\.\pipe\dns-client-status`
)

// PipeSDDL is the control-pipe security descriptor: Generic All to SYSTEM (the
// service) and Administrators only.
//
//	D: discretionary ACL, P: protected (no inherited ACEs)
//	(A;;GA;;;SY) SYSTEM            (A;;GA;;;BA) Built-in Administrators
const PipeSDDL = `D:P(A;;GA;;;SY)(A;;GA;;;BA)`

// StatusPipeSDDL is the status-pipe security descriptor: Generic All to SYSTEM
// and Generic Read+Write to Authenticated Users.  AU needs both GR and GW
// because go-winio dials the pipe with GENERIC_READ|GENERIC_WRITE.
//
//	D:P(A;;GA;;;SY)(A;;GRGW;;;AU)
const StatusPipeSDDL = `D:P(A;;GA;;;SY)(A;;GRGW;;;AU)`

// DefaultEndpoint returns the control-pipe path.
func DefaultEndpoint() (endpoint string) { return PipeName }

// StatusEndpoint returns the read-only status-pipe path.
func StatusEndpoint() (endpoint string) { return StatusPipeName }

// Listen creates the control-pipe listener with the admin-only SDDL.
func Listen(endpoint string) (ln net.Listener, err error) {
	return listenPipe(endpoint, PipeSDDL)
}

// ListenStatus creates the read-only status-pipe listener.
func ListenStatus(endpoint string) (ln net.Listener, err error) {
	return listenPipe(endpoint, StatusPipeSDDL)
}

// listenPipe opens a named-pipe listener with the given SDDL.
func listenPipe(endpoint, sddl string) (ln net.Listener, err error) {
	return winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: sddl,
		MessageMode:        false,
	})
}

// Dial connects to the named pipe.
func Dial(ctx context.Context, endpoint string) (conn net.Conn, err error) {
	return winio.DialPipeContext(ctx, endpoint)
}

// Note on peer identity: go-winio's pipe connection exposes Fd(), so
// ImpersonateNamedPipeClient is possible.  We do NOT use impersonation: the
// admin/status split plus each pipe's SDDL provides the boundary, and the
// server's per-method check is the defence-in-depth layer.
