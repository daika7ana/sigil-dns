//go:build gui && windows

package gui

import (
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// TestTrayHostControlWindow covers the runtime half of A3 without a desktop:
// the message-only control window is findable, only one host can hold the
// session mutex, and WM_CLOSE ends the message loop.  Icon creation needs a
// real shell and is not covered here.
func TestTrayHostControlWindow(t *testing.T) {
	if !acquireTrayHost() {
		t.Skip("another tray host is running")
	}
	defer procCloseHandle.Call(trayHostMutex)

	// The mutex is process-wide: a second acquire in this process must fail.
	if acquireTrayHost() {
		procCloseHandle.Call(trayHostMutex)
		t.Fatal("a second host acquired the session mutex")
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hwnd, err := createTrayHostWindow()
	if err != nil {
		t.Fatal(err)
	}
	defer procDestroyWindow.Call(hwnd)

	if got := trayHostWindow(); got != hwnd {
		t.Fatalf("control window = %#x, want %#x", got, hwnd)
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}()

	var msg msgW
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	if trayHostWindow() != 0 {
		t.Fatal("control window is still findable after WM_CLOSE")
	}
}
