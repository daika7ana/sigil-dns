//go:build gui

package gui

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/daika7ana/sigil-dns/internal/ipc"
)

// countingSource counts full and live reads and returns fixed snapshots, so the
// poller's cadence and merge can be observed without a service or a pipe.
type countingSource struct {
	mu    sync.Mutex
	full  snapshot
	live  snapshot
	fullN int
	liveN int
}

func (c *countingSource) Read(context.Context) (snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fullN++

	return c.full, nil
}

func (c *countingSource) ReadLive(context.Context) (snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.liveN++

	return c.live, nil
}

func (c *countingSource) counts() (full, live int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.fullN, c.liveN
}

// liveOnly strips the heavy fields a real live read never fetches.
func liveOnly(s snapshot) snapshot {
	s.summary = nil
	s.info = nil

	return s
}

// pollWindow builds a test window over a fresh status App and starts the poller
// with the given focus flag.
func pollWindow(t *testing.T, src statusSource, focused *atomic.Bool) (*gogui.Window, *App) {
	t.Helper()

	app := statusApp()
	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)
	startPoller(w, src, app.refresh, &app.logView, focused)

	return w, app
}

// waitFor drives frames until cond holds, failing on a generous deadline.
func waitFor(t *testing.T, w *gogui.Window, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		w.FrameFn()
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal("condition not met before deadline")
}

// TestPollerUnchangedLiveDoesNotRepaint proves an unchanged live read pushes
// nothing and requests no repaint: the on-demand loop stays idle.
func TestPollerUnchangedLiveDoesNotRepaint(t *testing.T) {
	snap := sampleSnapshot()
	src := &countingSource{full: snap, live: liveOnly(sampleSnapshot())}
	w, app := pollWindow(t, src, alwaysFocused())

	waitFor(t, w, func() bool {
		full, _ := src.counts()

		return full >= 1 && !app.snap.updated.IsZero()
	})

	before := app.snap.updated
	_, liveBefore := src.counts()

	// Let at least one live tick run without flushing.
	time.Sleep(pollInterval + 500*time.Millisecond)

	if _, liveAfter := src.counts(); liveAfter == liveBefore {
		t.Fatal("no live tick ran")
	}
	if w.FrameFn() {
		t.Fatal("an unchanged live read forced a repaint")
	}
	if !app.snap.updated.Equal(before) {
		t.Fatalf("unchanged live read pushed a snapshot: updated %v -> %v", before, app.snap.updated)
	}
}

// TestPollerChangedLiveRepaints proves a changed live field is pushed and
// repainted.
func TestPollerChangedLiveRepaints(t *testing.T) {
	live := liveOnly(sampleSnapshot())
	live.status.Health = &ipc.HealthSummary{Ours: false}
	src := &countingSource{full: sampleSnapshot(), live: live}
	w, app := pollWindow(t, src, alwaysFocused())

	waitFor(t, w, func() bool {
		return app.snap.status != nil && app.snap.status.Health != nil && !app.snap.status.Health.Ours
	})
}

// TestPollerServiceTransitionFullReads proves a live read whose
// {installed,running,scmState} moved escalates to a full read, so the summary
// source can switch (e.g. offlineSummary once the service stops).
func TestPollerServiceTransitionFullReads(t *testing.T) {
	full := snapshot{trust: trustVerified, installed: true, scmState: "stopped"}
	live := snapshot{trust: trustVerified, installed: true, running: true, scmState: "running"}
	src := &countingSource{full: full, live: live}
	w, _ := pollWindow(t, src, alwaysFocused())

	waitFor(t, w, func() bool {
		f, _ := src.counts()

		return f >= 2
	})
}

// TestPollerPausedWhileUnfocused proves the live tick does nothing while the
// window is unfocused, and resumes once it is focused again.
func TestPollerPausedWhileUnfocused(t *testing.T) {
	snap := sampleSnapshot()
	src := &countingSource{full: snap, live: liveOnly(sampleSnapshot())}
	focused := &atomic.Bool{} // starts unfocused
	w, _ := pollWindow(t, src, focused)

	// The initial read runs regardless of focus.
	waitFor(t, w, func() bool {
		f, _ := src.counts()

		return f >= 1
	})

	time.Sleep(pollInterval + 500*time.Millisecond)

	if _, liveN := src.counts(); liveN != 0 {
		t.Fatalf("unfocused poller performed %d live reads, want 0", liveN)
	}

	focused.Store(true)
	waitFor(t, w, func() bool {
		_, liveN := src.counts()

		return liveN > 0
	})
}

// TestSetPageSignalsRefresh proves switching the visible page asks the poller
// for a full read, so a newly shown page has fresh data.
func TestSetPageSignalsRefresh(t *testing.T) {
	app := statusApp()
	w := gogui.NewTestWindow(t, gogui.WindowCfg{
		Width: defaultWidth, Height: defaultHeight, State: app,
	})
	w.SetView(view)
	w.TestRender(nil)

	if err := w.TestClick("gui:nav:settings"); err != nil {
		t.Fatalf("click settings nav: %v", err)
	}
	if app.page != pageSettings {
		t.Fatalf("page = %v, want settings", app.page)
	}

	select {
	case <-app.refresh:
	default:
		t.Fatal("page switch did not signal a refresh")
	}
}
