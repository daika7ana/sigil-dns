//go:build gui && !windows

package gui

import (
	"context"
	"net"
	"time"

	"github.com/daika7ana/sigil-dns/internal/ipc"
)

// liveBase dials the status socket shared by Read and ReadLive.  Off Windows
// there is no SCM, so a live connection is trusted by construction
// ("trusted (dev)", SPEC §15.1) and the state is reported as "unsupported".  A
// failed dial is treated as stopped so the full read falls back to the on-disk
// config.
func (s *pipeSource) liveBase(ctx context.Context) (snapshot, net.Conn, liveOutcome) {
	conn, err := ipc.Dial(ctx, s.endpoint)
	if err != nil {
		return snapshot{trust: trustVerified, updated: time.Now(),
			scmState: "unsupported"}, nil, liveStopped
	}

	return snapshot{trust: trustVerified, running: true, updated: time.Now(),
		scmState: "unsupported"}, conn, liveRunning
}
