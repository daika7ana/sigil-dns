//go:build gui && windows

package gui

import (
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// go-gui v0.89.1 sets NOTIFYICON_VERSION_4 but never NIF_SHOWTIP.  Windows
// suppresses the standard tooltip in that version unless the flag is present,
// so the icon has no hover text.  These calls set it on go-gui's icon.
//
// shortcut: this reaches into go-gui's private tray window (class prefix and
// icon id).  Re-check it on a go-gui upgrade, together with the menu-ID
// workaround in tray.go.
const (
	hwndMessage = ^uintptr(2) // HWND_MESSAGE = -3
	nimModify   = 0x00000001
	nimSetVer   = 0x00000004
	nifTip      = 0x00000004
	nifShowTip  = 0x00000080
	trayWindow  = "go-gui-tray-window-"
	// trayIDsProbed bounds the icon-id search.  go-gui allocates a new id per
	// Create and never reuses one, so a tray toggled off and on is id 2, 3, …
	trayIDsProbed = 64
)

var (
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	procFindWindowExW    = user32.NewProc("FindWindowExW")
	procGetClassNameW    = user32.NewProc("GetClassNameW")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
)

// notifyIconDataW mirrors NOTIFYICONDATAW, including the version-4 tail.
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             uintptr
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            uintptr
	szTip            [128]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	uVersion         uint32
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     uintptr
}

// applyTrayTooltip gives go-gui's tray icon the standard tooltip.  It is
// best-effort: if the private window cannot be found, the icon simply keeps
// its current (tooltip-less) state.
func applyTrayTooltip(tip string) {
	hwnd := findTrayWindow()
	if hwnd == 0 {
		return
	}

	text, _ := syscall.UTF16FromString(tip)

	nid := notifyIconDataW{
		cbSize:   uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:     hwnd,
		uVersion: 4,
	}
	copy(nid.szTip[:], text)

	// NIM_SETVERSION succeeds only for an icon that exists, so probing finds
	// the live id even after an off/on toggle moved it past 1.
	for id := uint32(1); id <= trayIDsProbed; id++ {
		nid.uID = id
		if r, _, _ := procShellNotifyIconW.Call(nimSetVer, uintptr(unsafe.Pointer(&nid))); r == 0 {
			continue
		}

		nid.uFlags = nifTip | nifShowTip
		procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid)))

		return
	}
}

// findTrayWindow returns go-gui's message-only tray window.  Message-only
// windows are not enumerated by EnumWindows, but they are children of
// HWND_MESSAGE.
func findTrayWindow() (hwnd uintptr) {
	self := uint32(os.Getpid())
	for h := findWindowEx(hwndMessage, 0); h != 0; h = findWindowEx(hwndMessage, h) {
		var pid uint32
		procGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
		if pid == self && trayClass(h) {
			return h
		}
	}

	return 0
}

// findWindowEx wraps FindWindowExW with an empty class/title.
func findWindowEx(parent, after uintptr) (hwnd uintptr) {
	h, _, _ := procFindWindowExW.Call(parent, after, 0, 0)

	return h
}

// trayClass reports whether hwnd's class is one of go-gui's tray windows.
func trayClass(hwnd uintptr) (ok bool) {
	var buf [128]uint16
	n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return false
	}

	return strings.HasPrefix(syscall.UTF16ToString(buf[:n]), trayWindow)
}
