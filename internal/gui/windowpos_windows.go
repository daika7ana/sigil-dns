//go:build gui && windows

package gui

import (
	"os"
	"time"
	"unsafe"

	gogui "github.com/go-gui-org/go-gui/gui"
	"golang.org/x/sys/windows"
)

// go-gui exposes no window-position API (WindowCfg has only Width/Height and
// Window has only WindowSize), so the position is remembered through Win32
// directly.  The window is found by title and checked against this process id.
// The position is read from GetWindowRect while the window is visible and
// normal, and applied with SetWindowPos.  Both use screen coordinates, so a
// top/left taskbar cannot creep the window, and restoring the position can
// never change the window size.
var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	procFindWindowW        = user32.NewProc("FindWindowW")
	procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procIsIconic           = user32.NewProc("IsIconic")
	procIsZoomed           = user32.NewProc("IsZoomed")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procShowWindow         = user32.NewProc("ShowWindow")
	procSetForegroundWin   = user32.NewProc("SetForegroundWindow")
	procAttachThreadInput  = user32.NewProc("AttachThreadInput")
	procBringWindowToTop   = user32.NewProc("BringWindowToTop")
	procSetFocus           = user32.NewProc("SetFocus")
	procSwitchToWindow     = user32.NewProc("SwitchToThisWindow")
	procGetGUIThreadInfo   = user32.NewProc("GetGUIThreadInfo")
)

// ShowWindow commands.
const (
	swShow     = 5
	swRestore  = 9
	swMaximize = 3
)

// SetWindowPos flags: move only — keep the size, z-order and activation.
const (
	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
)

// GetSystemMetrics indices for the virtual screen (all monitors).
const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

// winRect mirrors the Win32 RECT.
type winRect struct {
	left, top, right, bottom int32
}

// findWindowByTitle returns the top-level window with this exact title,
// whichever process owns it.
func findWindowByTitle(title string) (hwnd uintptr) {
	p, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0
	}
	h, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(p)))

	return h
}

// findWindowAny returns this process's window of that title, hidden or not.
func findWindowAny(title string) (hwnd uintptr) {
	h := findWindowByTitle(title)
	if h == 0 {
		return 0
	}

	self := uint32(os.Getpid())
	var pid uint32
	procGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
	if pid != self {
		return 0
	}

	return h
}

// findWindow returns the visible window only: the minimize check must not
// query IsIconic on a window already hidden to the tray.
func findWindow(title string) (hwnd uintptr) {
	h := findWindowAny(title)
	if h == 0 {
		return 0
	}
	if r, _, _ := procIsWindowVisible.Call(h); r == 0 {
		return 0
	}

	return h
}

// windowMinimized reports whether the titled window is currently minimized.
// A minimized window keeps WS_VISIBLE, so findWindow still locates it.
func windowMinimized(title string) bool {
	hwnd := findWindow(title)
	if hwnd == 0 {
		return false
	}

	r, _, _ := procIsIconic.Call(hwnd)

	return r != 0
}

// windowPosition reports the visible window's top-left in screen pixels.
// Callers only use it while the window is visible and normal; GetWindowRect
// returns the screen rect for that case and SetWindowPos accepts the same
// coordinates, so a taskbar cannot creep the window between launches.
func windowPosition(title string) (x, y int, ok bool) {
	hwnd := findWindow(title)
	if hwnd == 0 {
		return 0, 0, false
	}

	var r winRect
	if ret, _, _ := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ret == 0 {
		return 0, 0, false
	}

	return int(r.left), int(r.top), true
}

// restoreWindowPosition moves the titled window to screen point (x,y), unless
// it falls outside the current virtual screen (a monitor was unplugged), in
// which case the OS places the window instead of it opening off-screen.  It
// uses SetWindowPos, not SetWindowPlacement, so it only moves the window and
// can never overwrite the size chosen at startup.
func restoreWindowPosition(title string, x, y int) {
	if !onVirtualScreen(x, y) {
		return
	}
	hwnd := findWindow(title)
	if hwnd == 0 {
		return
	}

	procSetWindowPos.Call(hwnd, 0, uintptr(x), uintptr(y), 0, 0,
		swpNoSize|swpNoZOrder|swpNoActivate)
}

// focusWindow raises this process's window of that title and gives it the
// foreground.
func focusWindow(title string) { focusHWND(findWindow(title)) }

// focusHWND raises hwnd and gives it the foreground, so a fresh launch or a
// second launch is not left behind other windows.  Best-effort: Windows'
// foreground lock can refuse the request.
//
// A plain SetForegroundWindow is not enough here.  go-gui drops mouse input to
// an unfocused window, and a process started by Task Scheduler never holds the
// foreground, so a tray click can open a window that is visible but dead until
// the user activates it by hand.  SetForegroundWindow cannot be trusted to say
// whether it worked: when the foreground lock refuses it, Windows only flashes
// the taskbar button and can still return success, which is why the real
// foreground window is checked instead.  Two fallbacks follow: raise it from
// inside the foreground thread, then ask for it the way a taskbar click does.
// Take an hwnd, not a title: the second-launch path must raise a window owned
// by another process.
func focusHWND(hwnd uintptr) {
	if hwnd == 0 {
		return
	}

	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWin.Call(hwnd)
	if activated(hwnd) {
		return
	}

	raiseAttached(hwnd)
	if activated(hwnd) {
		return
	}

	procSwitchToWindow.Call(hwnd, 1)
}

// activated reports whether hwnd reached the foreground and the keyboard
// focus, and gives it the focus if not.  SetForegroundWindow's return value is
// not a reliable answer to that question, and reaching the foreground alone is
// not enough: a window can be active with no focus window in its thread, which
// is the state that leaves go-gui dropping input.
func activated(hwnd uintptr) bool {
	procSetFocus.Call(hwnd) // no-op when the window already has it

	return uintptr(windows.GetForegroundWindow()) == hwnd && hasFocus(hwnd)
}

// hasFocus reports whether hwnd owns the keyboard focus of its thread.
func hasFocus(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	tid, _, _ := procGetWindowThreadPID.Call(hwnd)

	var g guiThreadInfo
	g.Size = uint32(unsafe.Sizeof(g))
	if r, _, _ := procGetGUIThreadInfo.Call(tid, uintptr(unsafe.Pointer(&g))); r == 0 {
		return false
	}

	return g.Focus == hwnd
}

// raiseAttached takes the foreground from inside the foreground thread's input
// queue: the usual workaround for a refused SetForegroundWindow.  The attach
// must join this thread, not the window's: it is this thread that then calls
// SetForegroundWindow, and a second launch raises another process's window.
func raiseAttached(hwnd uintptr) {
	fg := uintptr(windows.GetForegroundWindow())
	ftid, _, _ := procGetWindowThreadPID.Call(fg)
	tid := uintptr(windows.GetCurrentThreadId())
	if fg == 0 || ftid == 0 || ftid == tid {
		return
	}
	if r, _, _ := procAttachThreadInput.Call(ftid, tid, 1); r == 0 {
		return
	}
	defer procAttachThreadInput.Call(ftid, tid, 0)

	procBringWindowToTop.Call(hwnd)
	procSetForegroundWin.Call(hwnd)
	procSetFocus.Call(hwnd)
}

// ensureFocus gives the window the keyboard focus, and retries shortly after
// when Windows has not finished handing the foreground over.  Without the
// focus go-gui drops every left-button event: the window is visible, active and
// draggable, and its contents do nothing.
func ensureFocus(w *gogui.Window) {
	hwnd := findWindow(windowTitle)
	if hwnd == 0 {
		return
	}

	focusWindow(windowTitle)
	if !hasFocus(hwnd) {
		refocusLater(w, 4)
	}
}

// refocusLater retries the activation a few times, on the UI thread.
//
// Windows hands the foreground to a just-shown window asynchronously, so a
// SetFocus issued in the same message as Show lands before the handover is
// complete and is dropped — leaving the window active but focusless.  Retrying
// after the handover is what a second tray click (or focusing another window
// and coming back) did by accident.  It stops as soon as the window has the
// focus, and also once another window has the foreground: the user has moved
// on, and taking it back would be the app fighting them.
func refocusLater(w *gogui.Window, tries int) {
	time.AfterFunc(120*time.Millisecond, func() {
		w.QueueCommand(func(_ *gogui.Window) {
			hwnd := findWindow(windowTitle)
			if hwnd == 0 || hasFocus(hwnd) || uintptr(windows.GetForegroundWindow()) != hwnd {
				return
			}

			focusWindow(windowTitle)
			if tries > 1 {
				refocusLater(w, tries-1)
			}
		})
	})
}

// focusExistingWindow is the second launch's recovery path: it restores the
// first instance's window even when close-to-tray left it hidden.  The first
// instance's EventFocused handler clears its own hidden/tray state.
func focusExistingWindow() {
	hwnd := findWindowByTitle(windowTitle)
	if hwnd == 0 {
		return
	}

	zoomed, _, _ := procIsZoomed.Call(hwnd)
	if r, _, _ := procIsIconic.Call(hwnd); r != 0 {
		procShowWindow.Call(hwnd, swRestore)
	} else {
		procShowWindow.Call(hwnd, swShow)
	}
	if zoomed != 0 {
		// SW_RESTORE un-maximizes a window minimized from the maximized
		// state; put it back the way the user left it.
		procShowWindow.Call(hwnd, swMaximize)
	}
	// A refused foreground leaves the window visible but unfocused, where
	// go-gui drops input; focusHWND retries with the foreground-thread attach.
	focusHWND(hwnd)
}

// windowVisible reports whether the titled window is actually on screen,
// independent of go-gui's internal hidden flag (a second launch can show the
// window without updating it).
func windowVisible(title string) bool {
	hwnd := findWindowAny(title)
	if hwnd == 0 {
		return false
	}
	r, _, _ := procIsWindowVisible.Call(hwnd)

	return r != 0
}

// windowZoomed reports whether the titled window is maximized.
func windowZoomed(title string) bool {
	hwnd := findWindowAny(title)
	if hwnd == 0 {
		return false
	}
	r, _, _ := procIsZoomed.Call(hwnd)

	return r != 0
}

// maximizeWindow maximizes the titled window.  It is used after a restore to
// preserve a maximized window that was hidden to the tray.
func maximizeWindow(title string) {
	hwnd := findWindowAny(title)
	if hwnd == 0 {
		return
	}
	procShowWindow.Call(hwnd, swMaximize)
}

// onVirtualScreen reports whether (x,y) lies within the union of all monitors.
// Missing metrics fail open, so an unsupported setup still restores.
func onVirtualScreen(x, y int) (ok bool) {
	metric := func(i int) (v int) {
		r, _, _ := procGetSystemMetrics.Call(uintptr(i))

		return int(int32(r)) //nolint:gosec // metrics are small signed values
	}

	vx, vy := metric(smXVirtualScreen), metric(smYVirtualScreen)
	vw, vh := metric(smCXVirtualScreen), metric(smCYVirtualScreen)
	if vw <= 0 || vh <= 0 {
		return true
	}

	return x >= vx && x < vx+vw && y >= vy && y < vy+vh
}

// guiThreadInfo mirrors Win32 GUITHREADINFO.
type guiThreadInfo struct {
	Size     uint32
	Flags    uint32
	Active   uintptr
	Focus    uintptr
	Capture  uintptr
	Menu     uintptr
	MoveSize uintptr
	Caret    uintptr
	Rect     winRect
}
