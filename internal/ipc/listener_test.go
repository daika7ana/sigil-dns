package ipc

import (
	"net"
	"testing"
	"time"
)

// hangingListener is a net.Listener whose Close blocks until released,
// mimicking the go-winio named-pipe listener hang on an outstanding
// ConnectNamedPipe (microsoft/go-winio#357).
type hangingListener struct{ release chan struct{} }

func (h *hangingListener) Accept() (net.Conn, error) { <-h.release; return nil, net.ErrClosed }
func (h *hangingListener) Close() error              { <-h.release; return nil }
func (h *hangingListener) Addr() net.Addr            { return nil }

// TestCloseListenerBounded proves a listener Close that never returns cannot
// wedge shutdown: CloseListener gives up at its bound.
func TestCloseListenerBounded(t *testing.T) {
	ln := &hangingListener{release: make(chan struct{})}

	done := make(chan struct{})
	go func() {
		CloseListener(ln, 50*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("CloseListener blocked past its bound")
	}

	close(ln.release) // let the abandoned Close goroutine exit
}

// TestCloseListenerNilAndFast covers the nil guard and the normal fast path.
func TestCloseListenerNilAndFast(t *testing.T) {
	CloseListener(nil, time.Second) // must not panic

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	CloseListener(ln, time.Second)
}
