//go:build windows

package elevate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/daika7ana/dns-client/internal/config"
	"github.com/daika7ana/dns-client/internal/ipc"
	"github.com/daika7ana/dns-client/internal/service"
)

// Bounded waits (SPEC §15.2): the helper must never hang the UI or the SCM.
const (
	// helperTimeout bounds the whole elevated helper run.
	helperTimeout = 3 * time.Minute

	// serviceStartTimeout bounds waiting for the service to become running.
	serviceStartTimeout = 30 * time.Second

	// pipeReadyTimeout bounds waiting for the control pipe to accept.
	pipeReadyTimeout = 15 * time.Second

	// pollInterval is the pause between state polls.
	pollInterval = 250 * time.Millisecond

	// dialTimeout bounds a single control-pipe dial.
	dialTimeout = 2 * time.Second
)

// ShellExecuteExW flags and show command.
const (
	seeMaskNoCloseProcess = 0x00000040 // SEE_MASK_NOCLOSEPROCESS
	seeMaskNoAsync        = 0x00000100 // SEE_MASK_NOASYNC
	swShowNormal          = 1
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW.  Field order and alignment must
// match the C struct; Go inserts the same padding on amd64.
type shellExecuteInfo struct {
	cbSize       uint32
	fMask        uint32
	hwnd         windows.Handle
	lpVerb       *uint16
	lpFile       *uint16
	lpParameters *uint16
	lpDirectory  *uint16
	nShow        int32
	hInstApp     windows.Handle
	lpIDList     uintptr
	lpClass      *uint16
	hkeyClass    windows.Handle
	dwHotKey     uint32
	hIcon        windows.Handle
	hProcess     windows.Handle
}

// shell32.ShellExecuteExW is loaded lazily so a missing shell is a runtime error,
// not an import-time failure.
var procShellExecuteExW = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// Apply stages req, elevates via UAC, and collects the helper's verdict.  It
// runs unelevated.  A UAC cancellation is reported as a Result (not an error) so
// the UI can render it like any other refusal.
func Apply(ctx context.Context, req Request) (res Result, err error) {
	if err = req.Validate(); err != nil {
		return Result{}, err
	}

	// Best-effort revision check before prompting UAC; the helper enforces it
	// for real.  If the unelevated process cannot stat the (ACL'd) config the
	// revision is empty, so this stays a no-op rather than a false positive.
	if req.ExpectedRevision != "" {
		if actual := ipc.Revision(config.DefaultPath()); actual != "" && actual != req.ExpectedRevision {
			return Result{Err: ErrRevision.Error()}, ErrRevision
		}
	}

	dir, err := stagingDir()
	if err != nil {
		return Result{}, err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("creating staging dir: %w", err)
	}
	if err = secureUserOnly(dir); err != nil {
		return Result{}, err
	}

	reqPath := filepath.Join(dir, req.UUID+".req")
	resPath := filepath.Join(dir, req.UUID+".res")
	_ = os.Remove(resPath)

	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, err
	}

	sealed, err := dpapiProtect(body)
	if err != nil {
		return Result{}, err
	}
	if err = os.WriteFile(reqPath, sealed, 0o600); err != nil {
		return Result{}, fmt.Errorf("writing request: %w", err)
	}
	if err = secureUserOnly(reqPath); err != nil {
		return Result{}, err
	}
	defer func() { _ = os.Remove(reqPath) }()

	exe, err := os.Executable()
	if err != nil {
		return Result{}, fmt.Errorf("locating executable: %w", err)
	}

	proc, err := shellExecuteRunas(exe, `--apply "`+reqPath+`"`)
	if err != nil {
		if errors.Is(err, windows.ERROR_CANCELLED) {
			return Result{Err: "elevation cancelled"}, nil
		}

		return Result{}, err
	}
	if err = waitForExit(ctx, proc); err != nil {
		return Result{}, err
	}

	data, err := os.ReadFile(resPath)
	if err != nil {
		return Result{}, fmt.Errorf("helper did not report a result: %w", err)
	}
	_ = os.Remove(resPath)

	if err = json.Unmarshal(data, &res); err != nil {
		return Result{}, fmt.Errorf("decoding helper result: %w", err)
	}

	return res, nil
}

// HelperMain is the elevated entry point (--apply <reqPath>).  It never trusts
// the request: it re-reads and re-validates the config, applies only the
// allowlisted patch, then drives the service over the verified control pipe.  It
// writes its verdict to the sibling .res file.
func HelperMain(reqPath string) (err error) {
	elevated, err := isElevated()
	if err != nil {
		return fmt.Errorf("checking elevation: %w", err)
	}
	if !elevated {
		return errors.New("--apply must run elevated")
	}

	data, err := os.ReadFile(reqPath)
	if err != nil {
		return fmt.Errorf("reading request: %w", err)
	}
	// Delete the request immediately: it may carry settings, and the elevated
	// admin token must not leave it behind.
	_ = os.Remove(reqPath)

	plain, err := dpapiUnprotect(data)
	if err != nil {
		return fmt.Errorf("unprotecting request: %w", err)
	}

	var req Request
	if err = json.Unmarshal(plain, &req); err != nil {
		return writeResult(reqPath, Result{Err: "malformed request"})
	}

	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()

	return writeResult(reqPath, runAction(ctx, req))
}

// runAction performs the (elevated) work and returns a verdict.
func runAction(ctx context.Context, req Request) (res Result) {
	if err := req.Validate(); err != nil {
		return Result{Err: err.Error()}
	}

	// enable/disable force the mode; only apply takes the caller's patch.
	switch req.Action {
	case ActionEnable:
		on := config.ModeOn
		req.Patch = Patch{Mode: &on}
	case ActionDisable:
		off := config.ModeOff
		req.Patch = Patch{Mode: &off}
	case ActionApply, ActionRestore:
	}

	changed := false

	if req.Action != ActionRestore {
		path := config.DefaultPath()

		var before []byte
		if raw, rErr := os.ReadFile(path); rErr == nil {
			before = raw
		}

		cfg, err := config.Load(path)
		if err != nil {
			if !errors.Is(err, config.ErrNoConfig) {
				return Result{Err: "config could not be loaded"}
			}

			cfg = config.Default()
		}

		if err = applyRequest(cfg, req, ipc.Revision(path)); err != nil {
			return Result{Err: err.Error()}
		}

		// Save validates and ACLs the file.
		if err = cfg.Save(path); err != nil {
			return Result{Err: "config could not be saved"}
		}

		after, rErr := os.ReadFile(path)
		changed = rErr == nil && !bytes.Equal(before, after)
	}

	pid, err := ensureService(ctx)
	if err != nil {
		return Result{Err: "service is not available: " + err.Error()}
	}

	msg, err := driveControl(ctx, req, pid, changed)
	if err != nil {
		return Result{Err: err.Error()}
	}

	return Result{OK: true, Message: msg}
}

// driveControl verifies the control-pipe server identity, then issues the IPC
// calls for the action.
func driveControl(ctx context.Context, req Request, pid uint32, changed bool) (msg string, err error) {
	if err = waitControlServer(ctx, pid); err != nil {
		return "", err
	}

	client := ipc.NewClient(ipc.PipeName)

	switch req.Action {
	case ActionRestore:
		if err = client.Restore(ctx); err != nil {
			return "", err
		}

		return "interception restored", nil

	case ActionEnable:
		if _, err = client.Reload(ctx); err != nil {
			return "", err
		}
		if err = client.Enable(ctx); err != nil {
			return "", err
		}

		return "interception enabled", nil

	case ActionDisable:
		if err = client.Disable(ctx); err != nil {
			return "", err
		}

		return "interception disabled", nil

	case ActionApply:
		if !changed {
			return "config unchanged", nil
		}
		if _, err = client.Reload(ctx); err != nil {
			return "", err
		}

		return "config applied", nil

	default:
		return "", fmt.Errorf("unknown action %q", req.Action)
	}
}

// ensureService returns the running service's process id, starting the service
// if needed and waiting for it to report a pid.
func ensureService(ctx context.Context) (pid uint32, err error) {
	st, pid, err := queryService()
	if err != nil {
		return 0, err
	}

	if st != svc.Running {
		if startErr := service.Start(); startErr != nil &&
			!errors.Is(startErr, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return 0, startErr
		}
	}

	deadline := time.Now().Add(serviceStartTimeout)

	for {
		st, pid, err = queryService()
		if err == nil && st == svc.Running && pid != 0 {
			return pid, nil
		}
		if time.Now().After(deadline) {
			return 0, errors.New("timed out waiting for the service to start")
		}

		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// queryService reads the service state and pid from the SCM.
func queryService() (st svc.State, pid uint32, err error) {
	m, err := mgr.Connect()
	if err != nil {
		return 0, 0, fmt.Errorf("connecting to SCM: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(service.Name)
	if err != nil {
		return 0, 0, fmt.Errorf("opening service: %w", err)
	}
	defer func() { _ = s.Close() }()

	status, err := s.Query()
	if err != nil {
		return 0, 0, fmt.Errorf("querying service: %w", err)
	}

	return status.State, status.ProcessId, nil
}

// waitControlServer dials the control pipe until it accepts and its server
// process matches the SCM-reported pid.  It refuses an unverified server.
func waitControlServer(ctx context.Context, wantPID uint32) (err error) {
	deadline := time.Now().Add(pipeReadyTimeout)

	var last error

	for {
		dctx, cancel := context.WithTimeout(ctx, dialTimeout)
		conn, dErr := ipc.Dial(dctx, ipc.PipeName)
		cancel()

		if dErr == nil {
			pid, ok := ipc.ServerPID(conn)
			_ = conn.Close()

			switch {
			case ok && pid == wantPID:
				return nil
			case ok:
				last = fmt.Errorf("control pipe served by pid %d, want %d", pid, wantPID)
			default:
				last = errors.New("control pipe identity could not be verified")
			}
		} else {
			last = dErr
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("control pipe not ready: %w", last)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// shellExecuteRunas launches file with the "runas" verb (UAC) and returns its
// process handle.
func shellExecuteRunas(file, params string) (h windows.Handle, err error) {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return 0, err
	}

	filePtr, err := windows.UTF16PtrFromString(file)
	if err != nil {
		return 0, err
	}

	paramPtr, err := windows.UTF16PtrFromString(params)
	if err != nil {
		return 0, err
	}

	sei := shellExecuteInfo{
		fMask:        seeMaskNoCloseProcess | seeMaskNoAsync,
		lpVerb:       verb,
		lpFile:       filePtr,
		lpParameters: paramPtr,
		nShow:        swShowNormal,
	}
	sei.cbSize = uint32(unsafe.Sizeof(sei))

	r, _, callErr := procShellExecuteExW.Call(uintptr(unsafe.Pointer(&sei)))
	if r == 0 {
		return 0, fmt.Errorf("ShellExecuteEx: %w", callErr)
	}

	return sei.hProcess, nil
}

// waitForExit waits for the elevated process to exit, bounded by ctx.
func waitForExit(ctx context.Context, h windows.Handle) (err error) {
	defer func() { _ = windows.CloseHandle(h) }()

	for {
		ev, wErr := windows.WaitForSingleObject(h, uint32(pollInterval.Milliseconds()))
		if wErr != nil {
			return fmt.Errorf("waiting for helper: %w", wErr)
		}
		if ev == windows.WAIT_OBJECT_0 {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// stagingDir is the per-user staging directory for request/result files.
func stagingDir() (dir string, err error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		return "", errors.New("LOCALAPPDATA is not set")
	}

	return filepath.Join(base, "dns-client", "staging"), nil
}

// secureUserOnly sets a protected DACL granting the current user, SYSTEM, and
// Administrators full control.  The user SID is required so the unelevated UI
// can read the result; SYSTEM/Administrators let the elevated helper serve it.
func secureUserOnly(path string) (err error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("reading user sid: %w", err)
	}

	sddl := fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GA;;;SY)(A;;GA;;;BA)", user.User.Sid.String())

	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("parsing staging sddl: %w", err)
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("reading staging dacl: %w", err)
	}

	err = windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	)
	if err != nil {
		return fmt.Errorf("setting acl on %s: %w", path, err)
	}

	return nil
}

// dpapiProtect encrypts plain with the current user's DPAPI key.
func dpapiProtect(plain []byte) (out []byte, err error) {
	var in windows.DataBlob
	if len(plain) > 0 {
		in.Size = uint32(len(plain))
		in.Data = &plain[0]
	}

	var blob windows.DataBlob
	if err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &blob); err != nil {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}

	out = append([]byte(nil), unsafe.Slice(blob.Data, blob.Size)...)
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(blob.Data)))

	return out, nil
}

// dpapiUnprotect decrypts data with the current user's DPAPI key.
func dpapiUnprotect(data []byte) (plain []byte, err error) {
	var in windows.DataBlob
	if len(data) > 0 {
		in.Size = uint32(len(data))
		in.Data = &data[0]
	}

	var blob windows.DataBlob
	if err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &blob); err != nil {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}

	plain = append([]byte(nil), unsafe.Slice(blob.Data, blob.Size)...)
	_, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(blob.Data)))

	return plain, nil
}

// isElevated reports whether the current token is a member of Administrators.
func isElevated() (elevated bool, err error) {
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return false, err
	}

	member, err := windows.GetCurrentProcessToken().IsMember(admin)
	if err != nil {
		return false, err
	}

	return member, nil
}

// writeResult writes res next to reqPath and locks it down for the user.
func writeResult(reqPath string, res Result) (err error) {
	body, err := json.Marshal(res)
	if err != nil {
		return err
	}

	resPath := strings.TrimSuffix(reqPath, ".req") + ".res"
	if resPath == reqPath {
		resPath = reqPath + ".res"
	}

	if err = os.WriteFile(resPath, body, 0o600); err != nil {
		return fmt.Errorf("writing result: %w", err)
	}

	return secureUserOnly(resPath)
}
