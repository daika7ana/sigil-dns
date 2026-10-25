//go:build gui && windows

package gui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/go-gui-org/go-gui/gui/backend/sni"

	"github.com/daika7ana/sigil-dns/internal/ipc"
)

// The tray icon lives in this small process, not in the GUI.  A go-gui window
// creates an OpenGL context, and on a GPU driver that costs ~200 MB of private
// memory even while the window is hidden; the host has no window and no GL, so
// the idle tray costs ~25-30 MB.  The GUI starts it with --tray and quits when the
// window closes; Open relaunches the GUI.
//
// The host's message-only control window is also its single-instance guard and
// its exit channel.  It is created elevated, like the GUI, so UIPI stops a
// lower-integrity process from posting WM_CLOSE to it.
const (
	trayHostClass = "SigilDNS.TrayHost"

	// trayHostPoll is the live status refresh cadence.  trayHostFull is the
	// full-read cadence, which refreshes the on-disk summary (the log path).
	trayHostPoll = 5 * time.Second
	trayHostFull = 30 * time.Second

	// trayHostActionTimeout bounds Clear DNS cache in the tray callback, so a
	// stuck service cannot block the tray thread for the client's 2 min.
	trayHostActionTimeout = 10 * time.Second

	// trayHostCreateTries/trayHostCreateWait retry icon creation when the
	// shell is not ready yet at sign-in (the logon task can beat Explorer).
	trayHostCreateTries = 3
	trayHostCreateWait  = 2 * time.Second

	// trayHostRetryTries/trayHostRetryWait bound the hand-over between a
	// dying host and its replacement: the new process waits for the old
	// mutex, and the GUI waits for the old control window.
	trayHostRetryTries = 20
	trayHostRetryWait  = 50 * time.Millisecond

	// trayHostStartTimeout bounds the GUI's wait for the new host's icon, so
	// a host that dies on start fails the toggle instead of leaving the
	// switch on with no icon.  It covers the host's full icon-retry window
	// plus process start and the icon-creation call.
	trayHostStartTimeout = trayHostCreateTries*trayHostCreateWait + 3*time.Second

	// trayHostStillActive is the STILL_ACTIVE exit code of a live process.
	trayHostStillActive = 259

	wmClose   = 0x0010
	wmDestroy = 0x0002

	errClassAlreadyExists = 1410
	errAlreadyExists      = 183 // ERROR_ALREADY_EXISTS

	// trayHostMutexName is session-local, so each signed-in user can run a
	// tray host.  The mutex is the single-instance guard: it closes the race
	// where the GUI and the logon task start a host at the same moment.
	trayHostMutexName = `Local\SigilDNS.TrayHost`

	// trayHostReadyProp marks the control window once the icon exists, so the
	// GUI never reports a tray that has only its window.
	trayHostReadyProp = "SigilDnsTrayReady"
)

var (
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procPostMessageW     = user32.NewProc("PostMessageW")
	procSetPropW         = user32.NewProc("SetPropW")
	procGetPropW         = user32.NewProc("GetPropW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procCreateMutexW     = kernel32.NewProc("CreateMutexW")
	procCloseHandle      = kernel32.NewProc("CloseHandle")
)

// wndClassExW mirrors WNDCLASSEXW.
type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

// msgW mirrors MSG.
type msgW struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      pointW
}

type pointW struct{ x, y int32 }

// trayHostWndProc closes the host on WM_CLOSE.  DefWindowProc destroys the
// window on WM_CLOSE, and the resulting WM_DESTROY ends the message loop.
var trayHostWndProc = syscall.NewCallback(func(hwnd, msg, wparam, lparam uintptr) uintptr {
	if msg == wmDestroy {
		procPostQuitMessage.Call(0)

		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wparam, lparam)

	return r
})

// RunTrayHost runs the tray icon process until its Exit action or a WM_CLOSE
// from the GUI's tray switch.  A second launch is a no-op: the session mutex is
// already held.
func RunTrayHost() (err error) {
	detachConsole()

	if !acquireTrayHost() {
		return nil // another host owns the icon
	}
	defer procCloseHandle.Call(trayHostMutex)

	// One message loop on one OS thread, as Win32 requires.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hwnd, err := createTrayHostWindow()
	if err != nil {
		return err
	}
	defer procDestroyWindow.Call(hwnd)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	host := &trayHost{}
	var tr sni.Tray
	var id int

	for tries := 1; ; tries++ {
		id, err = tr.Create(trayConfig(trayStatusText(host.snapshot())), func(action string) {
			host.action(hwnd, action)
		})
		if err == nil {
			break
		}
		if tries >= trayHostCreateTries {
			return fmt.Errorf("tray host: %w", err)
		}
		select {
		case <-time.After(trayHostCreateWait):
		case <-ctx.Done():
			return nil
		}
	}
	defer tr.Remove(id)
	applyTrayTooltip(windowTitle) // sni does not set NIF_SHOWTIP

	// The window exists before the icon does; marking it only now lets the
	// GUI tell a started host from one that failed and is about to exit.
	prop, _ := syscall.UTF16PtrFromString(trayHostReadyProp)
	procSetPropW.Call(hwnd, uintptr(unsafe.Pointer(prop)), 1)

	// shortcut: like go-gui, sni does not re-add the icon after an Explorer
	// restart; toggling the tray setting restarts this host. Upgrade if it
	// becomes a field issue.

	go pollTrayHost(ctx, newSource(), host, func(status string) {
		tr.Update(id, trayConfig(status))
		applyTrayTooltip(windowTitle) // an update rewrites the tip flags
	})

	var msg msgW
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(r) <= 0 {
			return nil // WM_QUIT, or a message-loop error
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// trayHost is the latest sanitized read, shared between the poller goroutine
// and the tray callback thread.
type trayHost struct {
	mu   sync.Mutex
	snap snapshot
}

func (h *trayHost) snapshot() (s snapshot) {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.snap
}

func (h *trayHost) set(s snapshot) {
	h.mu.Lock()
	h.snap = s
	h.mu.Unlock()
}

func (h *trayHost) logPath() (path string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.snap.summary == nil {
		return ""
	}

	return h.snap.summary.LogPath
}

// pollTrayHost keeps the status row current.  A live read is cheap but carries
// no summary, so the summary from the last full read is merged in; a full read
// every trayHostFull refreshes it (the log path can change with the config).
func pollTrayHost(ctx context.Context, src statusSource, host *trayHost, update func(string)) {
	last := ""

	read := func(full bool) {
		var snap snapshot
		if full {
			snap = readOnce(ctx, src)
		} else {
			snap = readLiveOnce(ctx, src)
			prev := host.snapshot()
			snap.summary, snap.info = prev.summary, prev.info
		}
		host.set(snap)

		if status := trayStatusText(snap); status != last {
			last = status
			update(status)
		}
	}

	read(true) // a real state straight away, not "Unknown"

	tick := time.NewTicker(trayHostPoll)
	defer tick.Stop()

	const fullEvery = int(trayHostFull / trayHostPoll)
	for n := 0; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			read((n+1)%fullEvery == 0)
		}
	}
}

// action runs one menu choice on sni's tray thread.  It never raises a window
// unless Open was chosen.
func (h *trayHost) action(hwnd uintptr, action string) {
	switch action {
	case "", trayActionOpen:
		if findWindowByTitle(windowTitle) != 0 {
			focusExistingWindow()
			return
		}
		_ = launchGUI()
	case trayActionLog:
		if path := h.logPath(); path != "" {
			_ = openPath(path)
		}
	case trayActionFlush:
		// This callback runs on the thread that owns the tray menu.  The IPC
		// call can block for the whole timeout, so clear off that thread and
		// keep the icon responsive.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), trayHostActionTimeout)
			defer cancel()
			_ = ipc.NewClient("").ClearCache(ctx)
		}()
	case trayActionExit:
		// Close the window too when it is open, so Exit stops the whole app
		// rather than leaving a window with no icon.
		if win := findWindowByTitle(windowTitle); win != 0 {
			procPostMessageW.Call(win, wmClose, 0, 0)
		}
		procPostMessageW.Call(hwnd, wmClose, 0, 0)
	}
}

// launchGUI opens the window process.  The tray host is elevated, so the child
// inherits the token and no UAC prompt appears.
func launchGUI() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	// Never set HideWindow here: STARTF_USESHOWWINDOW/SW_HIDE overrides the
	// child's first ShowWindow (go-gui's own startup show), so the window stays
	// hidden until a second tray click finds and shows it.  The GUI binary is
	// windowless anyway.
	cmd := exec.Command(exe)
	if err := cmd.Start(); err != nil {
		return err
	}

	return cmd.Process.Release()
}

// trayHostMutex is the acquired single-instance mutex; the process holds it
// for its lifetime, so the OS releases it even after a crash.
var trayHostMutex uintptr

// acquireTrayHost takes the session-local single-instance mutex.  A host that
// is already running makes the new process wait briefly for the name to go
// away: the GUI may be replacing a host that is still exiting, and losing that
// hand-over would leave the tray setting on with no icon.
func acquireTrayHost() bool {
	name, err := syscall.UTF16PtrFromString(trayHostMutexName)
	if err != nil {
		return false
	}
	for tries := 0; tries < trayHostRetryTries; tries++ {
		h, _, callErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
		if h == 0 {
			return false
		}
		if e, ok := callErr.(syscall.Errno); ok && e == errAlreadyExists {
			procCloseHandle.Call(h)
			if tries == trayHostRetryTries-1 {
				return false
			}
			time.Sleep(trayHostRetryWait)
			continue
		}
		trayHostMutex = h

		return true
	}

	return false
}

// trayHostRunning reports whether the control window exists.
func trayHostRunning() bool {
	return trayHostWindow() != 0
}

// trayHostWindow finds the host's message-only control window.  FindWindowW
// cannot see message-only windows; they are children of HWND_MESSAGE.
func trayHostWindow() (hwnd uintptr) {
	class, err := syscall.UTF16PtrFromString(trayHostClass)
	if err != nil {
		return 0
	}
	h, _, _ := procFindWindowExW.Call(
		hwndMessage, 0, uintptr(unsafe.Pointer(class)), 0)
	if h == 0 {
		return 0
	}

	// A dying host can still own the window for one message-loop turn, so
	// treat a window whose process is gone as absent.
	var pid uint32
	procGetWindowThreadPID.Call(h, uintptr(unsafe.Pointer(&pid)))
	if !trayHostAlive(pid) {
		return 0
	}

	return h
}

// trayHostReady reports whether the host's icon exists.  The control window
// alone is created before the icon, so only the property the host sets after
// tr.Create means the tray is usable.
func trayHostReady() bool {
	hwnd := trayHostWindow()
	if hwnd == 0 {
		return false
	}
	prop, err := syscall.UTF16PtrFromString(trayHostReadyProp)
	if err != nil {
		return false
	}
	h, _, _ := procGetPropW.Call(hwnd, uintptr(unsafe.Pointer(prop)))

	return h != 0
}

// trayHostAlive reports whether pid is a live process.  Access denied still
// means the process exists.
func trayHostAlive(pid uint32) bool {
	const processQueryLimitedInformation = 0x1000

	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return err == syscall.ERROR_ACCESS_DENIED
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	var code uint32
	if err = syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}

	return code == trayHostStillActive
}

// startTrayHost launches the host unless it is already running.  It waits for
// the host's icon (up to trayHostStartTimeout), because the caller reverts the
// tray setting when the start fails; the child has to acquire the mutex,
// register its window and create the icon first.
func startTrayHost() error {
	deadline := time.Now().Add(trayHostStartTimeout)

	var cmd *exec.Cmd
	if !trayHostRunning() {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		cmd = exec.Command(exe, "--tray")
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		if err := cmd.Start(); err != nil {
			return err
		}
	}

	for time.Now().Before(deadline) {
		if trayHostReady() {
			return nil
		}
		time.Sleep(trayHostRetryWait)
	}

	if trayHostReady() {
		// A newer start won the race just before the deadline; do not kill
		// the host it is about to report as running.
		return nil
	}

	if cmd != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}

	return fmt.Errorf("tray host: icon did not appear within %v", trayHostStartTimeout)
}

// stopTrayHost asks the host to exit.  It is a no-op when none is running.
func stopTrayHost() {
	hwnd := trayHostWindow()
	if hwnd == 0 {
		return
	}
	procPostMessageW.Call(hwnd, wmClose, 0, 0)

	// The host destroys this window before it releases its mutex and exits.
	// Wait for the window so a quick off/on toggle does not see the dying host
	// and skip the start; a replacement launched after the window is gone waits
	// for the mutex in acquireTrayHost.
	for i := 0; i < trayHostRetryTries && trayHostWindow() != 0; i++ {
		time.Sleep(trayHostRetryWait)
	}
}

// createTrayHostWindow creates the message-only control window.
func createTrayHostWindow() (hwnd uintptr, err error) {
	hinst, _, _ := procGetModuleHandleW.Call(0)
	class, err := syscall.UTF16PtrFromString(trayHostClass)
	if err != nil {
		return 0, err
	}

	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   trayHostWndProc,
		hInstance:     hinst,
		lpszClassName: class,
	}
	if r, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		if e, ok := callErr.(syscall.Errno); !ok || e != errClassAlreadyExists {
			return 0, fmt.Errorf("tray host: register window class: %w", callErr)
		}
	}

	h, _, callErr := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(class)), 0, 0,
		0, 0, 0, 0, hwndMessage, 0, hinst, 0)
	if h == 0 {
		return 0, fmt.Errorf("tray host: create window: %w", callErr)
	}

	return h, nil
}
