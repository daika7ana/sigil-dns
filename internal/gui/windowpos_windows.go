//go:build gui && windows

package gui

import (
	"os"
	"unsafe"

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

// focusWindow raises the titled window and gives it the foreground, so a fresh
// launch is not left behind other windows.  Best-effort: Windows' foreground
// lock can refuse the request (the window then just flashes in the taskbar),
// so a failure is not treated as an error.
func focusWindow(title string) {
	hwnd := findWindow(title)
	if hwnd == 0 {
		return
	}

	procShowWindow.Call(hwnd, swShow)
	procSetForegroundWin.Call(hwnd)
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
	procSetForegroundWin.Call(hwnd)
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
