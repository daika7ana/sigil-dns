//go:build windows

package service

import "golang.org/x/sys/windows"

// SM_SHUTTINGDOWN is the GetSystemMetrics index for "the session is
// shutting down" (winuser.h); x/sys does not export it.
const smShuttingDown = 0x2000

// shutdownProbe reports whether this session is shutting down
// (SM_SHUTTINGDOWN).  Cheap enough to call once at service start, and swappable
// in tests.
var shutdownProbe = func() bool {
	r, _, _ := windows.NewLazySystemDLL("user32.dll").NewProc("GetSystemMetrics").Call(smShuttingDown)

	return r != 0
}
