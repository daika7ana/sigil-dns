//go:build gui

package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"time"

	"github.com/daika7ana/dns-client/internal/ipc"
)

// readTimeout bounds one status read so a stuck server cannot hang the poller.
const readTimeout = 8 * time.Second

// trustKind is the outcome of the service-identity check (SPEC §15.1/§15.3.12).
type trustKind int

const (
	// trustVerified means the status source is the dns-client service (on
	// Windows: the pipe server PID matches the SCM-reported ProcessId).
	trustVerified trustKind = iota
	// trustUnverified means the identity could not be confirmed, so the
	// data is not shown.
	trustUnverified
)

// snapshot is one read of the service status.  It carries only sanitized data
// (SPEC §15.2): labels, flags, counts, hosts, and IP literals — never ids or
// credential-bearing URLs, which are not even sent on the status pipe.
type snapshot struct {
	updated time.Time
	status  *ipc.StatusDTO
	summary *ipc.ConfigSummary
	info    *ipc.Info
	trust   trustKind
	running bool
	err     string
}

// statusSource reads the sanitized status.  The real source speaks the status
// pipe; tests inject a fake.
type statusSource interface {
	Read(ctx context.Context) (snapshot, error)
}

// pipeSource reads the status pipe/socket at endpoint.
type pipeSource struct {
	endpoint string
}

// newSource builds the production status source.  A package variable so a test
// can substitute a fake.
var newSource = func(endpoint string) statusSource {
	if endpoint == "" {
		endpoint = ipc.StatusEndpoint()
	}

	return &pipeSource{endpoint: endpoint}
}

// exchange performs one request/response over conn, decoding the payload into
// out.  It mirrors ipc.Client.call but over a caller-held connection, so the
// identity check and the read share one pipe instance (no re-dial window for a
// squatter to win).
func exchange(conn net.Conn, method ipc.Method, out any) (err error) {
	_ = conn.SetDeadline(time.Now().Add(readTimeout))

	if err = json.NewEncoder(conn).Encode(ipc.Request{
		Version: ipc.ProtocolVersion,
		Method:  method,
	}); err != nil {
		return err
	}

	var resp ipc.Response
	if err = json.NewDecoder(conn).Decode(&resp); err != nil {
		return err
	}
	if resp.Version != ipc.ProtocolVersion {
		return ipc.ErrVersion
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	if out != nil && len(resp.Data) > 0 {
		return json.Unmarshal(resp.Data, out)
	}

	return nil
}

// readPayload fetches the three sanitized reads over a verified connection.
// A failed read leaves that field nil; the view renders a dash for it.
func readPayload(conn net.Conn) (status *ipc.StatusDTO, summary *ipc.ConfigSummary, info *ipc.Info) {
	var st ipc.StatusDTO
	if exchange(conn, ipc.MethodStatus, &st) == nil {
		status = &st
	}

	var sum ipc.ConfigSummary
	if exchange(conn, ipc.MethodConfigSummary, &sum) == nil {
		summary = &sum
	}

	var in ipc.Info
	if exchange(conn, ipc.MethodInfo, &in) == nil {
		info = &in
	}

	return status, summary, info
}
