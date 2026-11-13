//go:build gui && windows

package gui

import (
	"runtime"
	"testing"
	"time"
	"unsafe"
)

// TestTrayHostControlWindow covers the parts that need no desktop: the hidden
// control window is findable, one host holds the mutex, WM_CLOSE ends the loop.
// Icon creation needs a real shell and is not covered here.
func TestTrayHostControlWindow(t *testing.T) {
	owned, err := acquireTrayHost()
	if err != nil {
		t.Fatal(err)
	}
	if !owned {
		t.Skip("another tray host is running")
	}
	defer procCloseHandle.Call(trayHostMutex)

	// The mutex is process-wide: a second acquire in this process must fail.
	if second, err := acquireTrayHost(); err != nil || second {
		t.Fatalf("a second host acquired the session mutex (err=%v)", err)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hwnd, err := createTrayHostWindow()
	if err != nil {
		t.Fatal(err)
	}
	defer procDestroyWindow.Call(hwnd)

	// FindWindowW sees top-level windows only: this also pins the window type
	// the TaskbarCreated broadcast needs.
	if got := trayHostWindow(); got != hwnd {
		t.Fatalf("control window = %#x, want %#x (must be a top-level window)", got, hwnd)
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

// TestTrayHostExitPeekKeepsTheCloseRequest pins that the startup wait observes
// WM_CLOSE without eating it; consuming it left the icon appearing after the
// user switched the tray off.
func TestTrayHostExitPeekKeepsTheCloseRequest(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hwnd, err := createTrayHostWindow()
	if err != nil {
		t.Fatal(err)
	}
	defer procDestroyWindow.Call(hwnd)

	procPostMessageW.Call(hwnd, wmClose, 0, 0)

	// Returns at once: the exit request is checked before the shell.
	waitForShellTray(hwnd, time.Second)

	if !exitRequested(hwnd) {
		t.Fatal("the startup wait consumed the WM_CLOSE")
	}
}
