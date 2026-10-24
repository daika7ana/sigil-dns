//go:build gui && windows

package gui

// Icon resource / Win32 constants.
const (
	iconResourceID = 2 // rsrc: manifest id 1, then the .ico group (see go:generate)
	imageIcon      = 1
	wmSetIcon      = 0x0080
	iconSmall      = 0
	iconBig        = 1

	smCXIcon   = 11
	smCYIcon   = 12
	smCXSmIcon = 49
	smCYSmIcon = 50
)

var (
	procLoadImageW             = user32.NewProc("LoadImageW")
	procSendMessageW           = user32.NewProc("SendMessageW")
	procDestroyIcon            = user32.NewProc("DestroyIcon")
	procGetDpiForWindow        = user32.NewProc("GetDpiForWindow")
	procGetDpiForSystem        = user32.NewProc("GetDpiForSystem")
	procGetSystemMetricsForDpi = user32.NewProc("GetSystemMetricsForDpi")

	// The window and the HICONs we installed are cached for the process life.
	// LoadImage without LR_SHARED returns caller-owned handles, so keeping them
	// here is what keeps the icons alive; old handles are destroyed only after
	// the new one is set.
	iconWindow  uintptr
	iconHandles [2]uintptr
	iconDPI     uintptr
)

// trayIconPixels is the notification area's icon size at the system DPI.  The
// shell draws that size, so handing it a matching PNG keeps the icon 1:1
// instead of letting the shell scale a larger one.
func trayIconPixels() int {
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		dpi = 96
	}

	return iconMetric(smCXSmIcon, uint32(dpi))
}

// installWindowIcons replaces the toolkit's single large icon with the exe's
// multi-size icon group, at the size each slot uses: the title bar and window
// menu take ICON_SMALL, the taskbar and Alt-Tab take ICON_BIG.  go-gui sets one
// 256px HICON for both, so Windows scales it down and the small copy looks
// stair-stepped.
func installWindowIcons() {
	if iconWindow == 0 {
		iconWindow = findWindowAny(windowTitle)
	}
	if iconWindow == 0 {
		return
	}
	dpi, _, _ := procGetDpiForWindow.Call(iconWindow)
	if dpi == 0 {
		dpi = 96
	}
	if dpi == iconDPI {
		return
	}

	hInst, _, _ := procGetModuleHandleW.Call(0)
	set := func(slot, cx, cy int) bool {
		// No LR_SHARED: that flag returns the first cached image regardless of
		// the requested size, so the second (small) load would hand back the big
		// HICON.  A caller-owned handle can be loaded at each size.
		h, _, _ := procLoadImageW.Call(hInst, iconResourceID, imageIcon,
			uintptr(cx), uintptr(cy), 0)
		if h == 0 {
			return false
		}
		procSendMessageW.Call(iconWindow, wmSetIcon, uintptr(slot), h)
		if old := iconHandles[slot]; old != 0 {
			procDestroyIcon.Call(old)
		}
		iconHandles[slot] = h

		return true
	}

	big := set(iconBig, iconMetric(smCXIcon, uint32(dpi)), iconMetric(smCYIcon, uint32(dpi)))
	small := set(iconSmall, iconMetric(smCXSmIcon, uint32(dpi)), iconMetric(smCYSmIcon, uint32(dpi)))
	if big && small {
		iconDPI = dpi
	}
}

// iconMetric resolves an icon metric at dpi, falling back to the process's
// own awareness when the per-DPI call is unavailable.
func iconMetric(index int, dpi uint32) int {
	if dpi != 0 {
		if v, _, _ := procGetSystemMetricsForDpi.Call(uintptr(index), uintptr(dpi)); v != 0 {
			return int(v)
		}
	}

	if v, _, _ := procGetSystemMetrics.Call(uintptr(index)); v != 0 {
		return int(v)
	}

	return 0
}
