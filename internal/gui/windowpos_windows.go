//go:build gui && windows

package gui

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// go-gui exposes no window-position API (WindowCfg has only Width/Height and
// Window has only WindowSize), so the on-screen position is remembered through
// Win32 directly.  The window is found by enumerating this process's top-level
// windows and matching the title; the coordinates are screen pixels including
// the frame, so GetWindowRect and SetWindowPos round-trip.
var (
	user32                 = windows.NewLazySystemDLL("user32.dll")
	procEnumWindows        = user32.NewProc("EnumWindows")
	procGetWindowTextW     = user32.NewProc("GetWindowTextW")
	procGetWindowThreadPID = user32.NewProc("GetWindowThreadProcessId")
	procGetWindowRect      = user32.NewProc("GetWindowRect")
	procSetWindowPos       = user32.NewProc("SetWindowPos")
	procIsWindowVisible    = user32.NewProc("IsWindowVisible")
	procGetSystemMetrics   = user32.NewProc("GetSystemMetrics")
	procShowWindow         = user32.NewProc("ShowWindow")
	procSetForegroundWin   = user32.NewProc("SetForegroundWindow")
)

// SetWindowPos flags: keep the current size, z-order and activation.
const (
	swpNoSize     = 0x0001
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
)

// ShowWindow command: activate and show without changing size or position.
const swShow = 5

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

// findWindow returns the HWND of this process's visible top-level window whose
// text equals title, or 0 if none is found.  Matching the process id first
// keeps it from latching onto another application's window of the same title.
func findWindow(title string) (hwnd uintptr) {
	self := uint32(os.Getpid())

	cb := windows.NewCallback(func(h, _ uintptr) uintptr {
		var pid uint32
		procGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
		if pid != self {
			return 1 // keep enumerating
		}
		if r, _, _ := procIsWindowVisible.Call(h); r == 0 {
			return 1
		}

		buf := make([]uint16, 256)
		n, _, _ := procGetWindowTextW.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		if n == 0 || windows.UTF16ToString(buf) != title {
			return 1
		}

		hwnd = h

		return 0 // stop
	})

	procEnumWindows.Call(cb, 0)

	return hwnd
}

// windowPosition reports the current top-left of the titled window.
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

// restoreWindowPosition moves the titled window to (x,y), unless that point
// falls outside the current virtual screen (a monitor was unplugged), in which
// case the OS places the window instead of it opening off-screen.
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
