//go:build gui && !windows

package gui

import (
	"context"
	"time"

	"github.com/daika7ana/dns-client/internal/ipc"
)

// Read dials the status socket and performs the sanitized reads.  Off Windows
// there is no SCM, so the source is trusted by construction ("trusted (dev)",
// SPEC §15.1).
func (s *pipeSource) Read(ctx context.Context) (snapshot, error) {
	conn, err := ipc.Dial(ctx, s.endpoint)
	if err != nil {
		return snapshot{trust: trustVerified, updated: time.Now(),
			err: "status source not reachable"}, nil
	}
	defer func() { _ = conn.Close() }()

	snap := snapshot{trust: trustVerified, running: true, updated: time.Now()}
	snap.status, snap.summary, snap.info = readPayload(conn)

	return snap, nil
}
