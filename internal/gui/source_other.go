//go:build gui && !windows

package gui

import (
	"context"
	"net"
	"time"

	"github.com/daika7ana/dns-client/internal/ipc"
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

// Read is the full read: the status DTO, the config summary, and the service
// info.  While the socket is unreachable it falls back to the on-disk config
// (offlineSummary).
func (s *pipeSource) Read(ctx context.Context) (snapshot, error) {
	snap, conn, outcome := s.liveBase(ctx)
	switch outcome {
	case liveStopped:
		snap.summary = offlineSummary()
	case liveRunning:
		defer func() { _ = conn.Close() }()
		snap.status, snap.summary, snap.info = readPayload(conn)
	}

	return snap, nil
}

// ReadLive is Read without the heavy config data: just the single status DTO.
// It never reads config.yaml — the poller calls it on the hot path.
func (s *pipeSource) ReadLive(ctx context.Context) (snapshot, error) {
	snap, conn, outcome := s.liveBase(ctx)
	if outcome != liveRunning {
		return snap, nil
	}
	defer func() { _ = conn.Close() }()

	var st ipc.StatusDTO
	if exchange(conn, ipc.MethodStatus, &st) == nil {
		snap.status = &st
	}

	return snap, nil
}
