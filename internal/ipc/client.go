package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// clientTimeout bounds a single client call so a stuck server cannot hang the
// caller.
const clientTimeout = 10 * time.Second

// reloadTimeout bounds calls that bind or restore interception (reload, enable,
// disable, restore), health check included.
const reloadTimeout = 2 * time.Minute

// Client talks to a running sigildns service over the IPC transport.
type Client struct {
	endpoint string
}

// NewClient returns a Client for endpoint.  An empty endpoint uses the
// platform default.
func NewClient(endpoint string) (c *Client) {
	if endpoint == "" {
		endpoint = DefaultEndpoint()
	}

	return &Client{endpoint: endpoint}
}

// ConfigSummary asks for the sanitized config summary.  It is available on both
// pipes but only ever returns redacted data.
func (c *Client) ConfigSummary(ctx context.Context) (sum *ConfigSummary, err error) {
	err = c.call(ctx, MethodConfigSummary, &sum)

	return sum, err
}

// Reload asks the service (control pipe) to reload its configuration and returns
// the new summary.
func (c *Client) Reload(ctx context.Context) (sum *ConfigSummary, err error) {
	err = c.callTimeout(ctx, MethodReload, &sum, reloadTimeout)

	return sum, err
}

// Enable asks the service to enable interception.
func (c *Client) Enable(ctx context.Context) (err error) {
	return c.callTimeout(ctx, MethodEnable, nil, reloadTimeout)
}

// Restore asks the service (control pipe) to restore interception from persisted
// state.  It is config-independent crash/dirty recovery.
func (c *Client) Restore(ctx context.Context) (err error) {
	return c.callTimeout(ctx, MethodRestore, nil, reloadTimeout)
}

// RefreshLists asks the service (control pipe) to re-download its filter lists
// and rebuild the filter engine.  Downloads can be slow, so it uses the long
// reload timeout.
func (c *Client) RefreshLists(ctx context.Context) (err error) {
	return c.callTimeout(ctx, MethodRefreshLists, nil, reloadTimeout)
}

// ClearCache asks the service (control pipe) to clear its resolver cache and
// the Windows DNS cache.
func (c *Client) ClearCache(ctx context.Context) (err error) {
	return c.callTimeout(ctx, MethodClearCache, nil, reloadTimeout)
}

// call performs one request/response exchange with the default timeout.
func (c *Client) call(ctx context.Context, method Method, out any) (err error) {
	return c.callTimeout(ctx, method, out, clientTimeout)
}

// callTimeout performs one request/response exchange bounded by timeout.  The
// deadline is applied to the connection so a slow or silent server cannot hang
// the caller.
func (c *Client) callTimeout(ctx context.Context, method Method, out any, timeout time.Duration) (err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := Dial(ctx, c.endpoint)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", c.endpoint, err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(timeout))

	return Exchange(conn, method, out)
}

// Exchange performs one request/response exchange over an already-dialed conn.
// The caller owns conn (including its deadline).  It is exported so non-Client
// callers (the GUI's raw-pipe reader, tests) share the exact wire protocol and
// error strings.
func Exchange(conn net.Conn, method Method, out any) (err error) {
	if err = json.NewEncoder(conn).Encode(Request{Version: ProtocolVersion, Method: method}); err != nil {
		return fmt.Errorf("sending %s: %w", method, err)
	}

	var resp Response
	if err = json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("reading %s response: %w", method, err)
	}
	if resp.Version != ProtocolVersion {
		return fmt.Errorf("%s: server response: %w", method, ErrVersion)
	}
	if !resp.OK {
		return fmt.Errorf("%s: %s", method, resp.Error)
	}

	if out != nil && len(resp.Data) > 0 {
		if err = json.Unmarshal(resp.Data, out); err != nil {
			return fmt.Errorf("decoding %s data: %w", method, err)
		}
	}

	return nil
}
