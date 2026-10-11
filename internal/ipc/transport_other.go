//go:build !windows

package ipc

import (
	"context"
	"net"
	"os"
	"path/filepath"
)

// DefaultEndpoint is the Unix socket used off Windows (Windows uses named
// pipes).
func DefaultEndpoint() (endpoint string) {
	return filepath.Join(os.TempDir(), "dns-client-ipc.sock")
}

// StatusEndpoint is the read-only status socket used off Windows.
func StatusEndpoint() (endpoint string) {
	return filepath.Join(os.TempDir(), "dns-client-ipc-status.sock")
}

// Listen creates the control Unix socket, removing any stale socket first.
func Listen(endpoint string) (ln net.Listener, err error) {
	return listenUnix(endpoint)
}

// ListenStatus creates the status Unix socket.  Off Windows both sockets share
// the 0600 mode; the read-only/admin distinction is enforced by the server's
// authorizer, not the filesystem.
func ListenStatus(endpoint string) (ln net.Listener, err error) {
	return listenUnix(endpoint)
}

// listenUnix listens on a Unix socket restricted to the current user (0600).
func listenUnix(endpoint string) (ln net.Listener, err error) {
	_ = os.Remove(endpoint)

	ln, err = net.Listen("unix", endpoint)
	if err != nil {
		return nil, err
	}

	// Dev-only hardening: block other local users.
	_ = os.Chmod(endpoint, 0o600)

	return ln, nil
}

// Dial connects to the non-Windows IPC endpoint.
func Dial(ctx context.Context, endpoint string) (conn net.Conn, err error) {
	var d net.Dialer

	return d.DialContext(ctx, "unix", endpoint)
}
