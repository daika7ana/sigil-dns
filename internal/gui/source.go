//go:build gui

package gui

import (
	"context"
	"net"
	"reflect"
	"time"

	"github.com/daika7ana/sigil-dns/internal/config"
	"github.com/daika7ana/sigil-dns/internal/ipc"
)

// readTimeout bounds one status read so a stuck server cannot hang the poller.
const readTimeout = 8 * time.Second

// trustKind is the outcome of the service-identity check (SPEC §15.1/§15.3.12).
type trustKind int

const (
	// trustVerified means the status source is the sigildns service (on
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

	// installed reports whether the sigildns service is registered with the
	// SCM, and scmState is its state name ("running", "stopped",
	// "not installed", "unsupported", ...).  Off Windows there is no SCM, so
	// the state is "unsupported".
	installed bool
	scmState  string
}

// samePoll reports whether two reads would produce the same layout.  The
// updated timestamp is ignored: it changes on every poll but only rewrites
// header text, so a fresh reading of otherwise-unchanged data must not force a
// relayout (which would re-snap glyph baselines mid-scroll).
func samePoll(a, b snapshot) bool {
	a.updated = time.Time{}
	b.updated = time.Time{}

	return reflect.DeepEqual(a, b)
}

// liveOutcome is what a live probe found before any payload is read.  It keeps
// the shared SCM + pipe logic in one place while letting Read and ReadLive
// decide separately what (if anything) to fetch.
type liveOutcome int

const (
	// liveRunning: the service is running and the status pipe is verified, so
	// a conn is open and carries the sanitized reads.
	liveRunning liveOutcome = iota
	// liveStopped: the service is installed-but-stopped or not installed, so
	// the full read falls back to the on-disk config (offlineSummary).
	liveStopped
	// liveUnavailable: the SCM query, dial, or identity check failed, so no
	// data is shown and the config is not read.
	liveUnavailable
)

// statusSource reads the sanitized status.  The real source speaks the status
// pipe; tests inject a fake.  Read fetches everything; ReadLive is the cheap
// hot-path read (SCM state + status DTO only; never config.yaml).
type statusSource interface {
	Read(ctx context.Context) (snapshot, error)
	ReadLive(ctx context.Context) (snapshot, error)
}

// pipeSource reads the status pipe/socket at endpoint.
type pipeSource struct {
	endpoint string
}

// newSource builds the production status source for the default status endpoint.
func newSource() statusSource {
	return &pipeSource{endpoint: ipc.StatusEndpoint()}
}

// exchange performs one request/response over conn, decoding the payload into
// out.  It owns conn and applies a read deadline before delegating to the shared
// wire protocol in ipc.Exchange, so the identity check and the read share one
// pipe instance (no re-dial window for a squatter to win).
func exchange(conn net.Conn, method ipc.Method, out any) (err error) {
	_ = conn.SetDeadline(time.Now().Add(readTimeout))

	return ipc.Exchange(conn, method, out)
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

// offlineSummary returns the sanitized summary of the on-disk config (a missing
// file yields the built-in defaults).  It is the GUI's read-back path while the
// service is not running (stopped or not yet installed): without it a save made
// offline is never visible again, because the editor falls back to a stale
// gui.json cache or the built-in default — so saved settings appeared to reset
// on restart.  It mirrors ipc.Server.configSummary and never exposes ids or
// credential-bearing URLs (ipc.Summarize strips them).
func offlineSummary() *ipc.ConfigSummary {
	return fileSummary(config.SystemPath())
}

// fileSummary loads the config at path and reduces it to the sanitized summary
// via ipc.LoadSummary: a missing file yields the built-in defaults, a broken
// file a secret-free error summary.  The empty-path guard keeps the callers'
// "no file" case explicit.
func fileSummary(path string) *ipc.ConfigSummary {
	if path == "" {
		return nil
	}

	sum := ipc.LoadSummary(path)

	return &sum
}
