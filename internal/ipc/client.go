package ipc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/daika7ana/dns-client/internal/state"
)

// clientTimeout bounds a single client call so a stuck server cannot hang the
// caller.
const clientTimeout = 10 * time.Second

// Client talks to a running dns-client service over the IPC transport.
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

// Endpoint returns the endpoint this client dials.
func (c *Client) Endpoint() (endpoint string) { return c.endpoint }

// Status asks the service for the persisted state.  A nil state means no state
// file exists yet.  This is the full (admin/control-pipe) view.
func (c *Client) Status(ctx context.Context) (st *state.State, err error) {
	err = c.call(ctx, MethodStatus, &st)

	return st, err
}

// StatusSummary asks the status pipe for the sanitized status DTO.
func (c *Client) StatusSummary(ctx context.Context) (dto *StatusDTO, err error) {
	err = c.call(ctx, MethodStatus, &dto)

	return dto, err
}

// Enable asks the service to enable interception.
func (c *Client) Enable(ctx context.Context) (err error) {
	return c.call(ctx, MethodEnable, nil)
}

// Disable asks the service to disable interception.
func (c *Client) Disable(ctx context.Context) (err error) {
	return c.call(ctx, MethodDisable, nil)
}

// call performs one request/response exchange with a bounded timeout.
func (c *Client) call(ctx context.Context, method Method, out any) (err error) {
	ctx, cancel := context.WithTimeout(ctx, clientTimeout)
	defer cancel()

	conn, err := Dial(ctx, c.endpoint)
	if err != nil {
		return fmt.Errorf("dialing %s: %w", c.endpoint, err)
	}
	defer func() { _ = conn.Close() }()

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
