//go:build !windows

package ipc

import (
	"context"
	"net"
	"os"
	"path/filepath"
)

// DefaultEndpoint is the Unix socket used off Windows (Windows uses a named
// pipe).
func DefaultEndpoint() (endpoint string) {
	return filepath.Join(os.TempDir(), "dns-client-ipc.sock")
}

// Listen creates the non-Windows IPC listener, removing any stale socket first.
func Listen(endpoint string) (ln net.Listener, err error) {
	_ = os.Remove(endpoint)

	return net.Listen("unix", endpoint)
}

// Dial connects to the non-Windows IPC endpoint.
func Dial(ctx context.Context, endpoint string) (conn net.Conn, err error) {
	var d net.Dialer

	return d.DialContext(ctx, "unix", endpoint)
}

// defaultAuthorizer is a development fallback: off Windows there is no service
// security boundary, so peers are treated as administrators.  Tests inject their
// own Authorizer to exercise the authorization logic.
func defaultAuthorizer(net.Conn) Caller { return Caller{Admin: true} }

// DefaultAuthorizer implements the platform default.
var DefaultAuthorizer Authorizer = defaultAuthorizer
