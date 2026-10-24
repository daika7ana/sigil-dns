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

	"github.com/daika7ana/sigil-dns/internal/config"
	"github.com/daika7ana/sigil-dns/internal/intercept"
	"github.com/daika7ana/sigil-dns/internal/ipc"
	"github.com/daika7ana/sigil-dns/internal/lifecycle"
	"github.com/daika7ana/sigil-dns/internal/service"
	"github.com/daika7ana/sigil-dns/internal/state"
	"github.com/daika7ana/sigil-dns/internal/winacl"
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

	// restorePipeTimeout bounds a single control-pipe restore (its own
	// waitControlServer deadline is 15s; the client's reload deadline is 2m, so
	// this caps the whole attempt).
	restorePipeTimeout = 45 * time.Second

	// localRestoreTimeout bounds the config-independent local restore fallback.
	localRestoreTimeout = 60 * time.Second
)

// helperTimeout bounds the ctx-aware work in the elevated helper.  The
// ctx-aware uninstall steps worst case is restore (restorePipeTimeout 45s +
// localRestoreTimeout 60s) plus the post-stop verification (localRestoreTimeout
// 60s) = 165s.  It is NOT a hard upper bound on the whole run: the SCM calls
// (mgr.Connect/Config/Start/Delete) and service.Stop's internal wait are not
// context-bound, so a wedged SCM can outlive this budget.  The helper is a
// bounded, deliberate, one-shot admin process; the parent Apply still bounds
// its wait for the helper's exit independently.

// ShellExecuteExW flags and show command.
const (
	seeMaskNoCloseProcess = 0x00000040 // SEE_MASK_NOCLOSEPROCESS
	seeMaskNoAsync        = 0x00000100 // SEE_MASK_NOASYNC
	swHide                = 0          // SW_HIDE: the headless helper must not flash a console
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

	// Best-effort revision check before prompting UAC.  This runs in the
	// (unelevated) caller, which may be unable to READ the ACL'd config, so the
	// revision is often empty and this stays a no-op rather than a false
	// positive.  The elevated helper enforces the revision for real.
	if req.ExpectedRevision != "" {
		if actual := ipc.Revision(config.SystemPath()); actual != "" && actual != req.ExpectedRevision {
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

	dir, err := stagingDir()
	if err != nil {
		return err
	}
	data, err := readStagingRequest(reqPath, dir)
	if err != nil {
		return err
	}

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

	// Never die without a verdict: a panic must still write .res, or the
	// caller waits out its timeout.  runAction is the only call after this
	// point, so this cannot double-write a result.
	defer func() {
		if r := recover(); r != nil {
			err = writeResult(reqPath, Result{Err: "internal error"})
		}
	}()

	return writeResult(reqPath, runAction(ctx, req))
}

// ServiceStop stops the service and verifies interception is down, so the
// `service stop` CLI command cannot report success while a rule survives.
func ServiceStop() (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()

	if res := stopServiceAndVerify(ctx); res.Err != "" {
		return errors.New(res.Err)
	}

	return nil
}

// ServiceUninstall runs the same ordered, fail-closed uninstall the elevated
// helper drives (restore -> stop -> verify interception down -> delete) so the
// `service uninstall` CLI command cannot delete a registration while
// interception is still live.  It lives here because internal/elevate already
// imports internal/service; the reverse would be an import cycle.
func ServiceUninstall() (err error) {
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()

	if res := uninstallService(ctx); res.Err != "" {
		return errors.New(res.Err)
	}

	return nil
}

// runAction performs the (elevated) work and returns a verdict.
func runAction(ctx context.Context, req Request) (res Result) {
	if err := req.Validate(); err != nil {
		return Result{Err: err.Error()}
	}

	// Service-management actions are handled here; apply is the only action
	// that writes a config patch.
	switch req.Action {
	case ActionServiceInstall:
		return installService()
	case ActionServiceUninstall:
		return uninstallService(ctx)
	case ActionServiceStart:
		if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return Result{Err: "service could not be started"}
		}

		return Result{OK: true, Message: "service started"}
	case ActionServiceStop:
		return stopServiceAndVerify(ctx)
	case ActionClearCache:
		return clearCaches(ctx)
	}

	changed := false
	reload := false

	if req.Action == ActionApply {
		// Resolve the config path from the ProgramData KnownFolder inside the
		// elevated helper: never trust %ProgramData% from the caller's env.
		path := config.SystemPath()
		if path == "" {
			return Result{Err: "system configuration directory is unavailable"}
		}

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

		// Snapshot the pre-apply config so a log-only edit can be told apart
		// from a reload-worthy change (LOGGING_PLAN §1.6).
		snapshot := cfg.Clone()

		if err = applyRequest(cfg, req, ipc.Revision(path)); err != nil {
			return Result{Err: err.Error()}
		}

		// Re-stat immediately before saving to close the check->save TOCTOU
		// window: a write that landed during apply must abort, not be clobbered.
		if err = checkRevision(req.ExpectedRevision, ipc.Revision(path)); err != nil {
			return Result{Err: err.Error()}
		}

		// Save validates and ACLs the file.
		if err = cfg.Save(path); err != nil {
			return Result{Err: "config could not be saved"}
		}

		after, rErr := os.ReadFile(path)
		changed = rErr == nil && !bytes.Equal(before, after)
		reload = reloadRelevant(snapshot, cfg)
	}

	// Restore must never start the service: startup would enable interception
	// again.  Stop a running service first, then prove interception is down;
	// a service whose shutdown restore failed may still leave a live rule.
	if req.Action == ActionRestore {
		if running, _ := serviceRunning(); running {
			res := stopServiceAndVerify(ctx)
			if res.Err != "" {
				return res
			}
			res.Message = "interception restored"

			return res
		}
		if err := localRestore(ctx); err != nil {
			return Result{Err: err.Error()}
		}
		if err := verifyInterceptionDown(ctx); err != nil {
			return Result{Err: err.Error()}
		}

		return Result{OK: true, Message: "interception restored"}
	}

	// Enable/refresh always require a running service (enable starts it).
	// Apply is handled separately below so a pure config edit never starts the
	// service or reports failure for a write that did happen.
	if req.Action != ActionApply {
		pid, err := ensureService(ctx)
		if err != nil {
			return Result{Err: "service is not available: " + err.Error()}
		}

		msg, err := driveControl(ctx, req, pid)
		if err != nil {
			return Result{Err: err.Error()}
		}

		return Result{OK: true, Message: msg}
	}

	msg, err := applyConfigChanged(ctx, changed, reload)
	if err != nil {
		return Result{Err: err.Error()}
	}

	return Result{OK: true, Message: msg}
}

// applyConfigChanged reloads a running service after a config-only apply.  It
// never starts the service: if it is stopped or not installed the saved config
// simply takes effect on the next enable.  reload carries the reload-relevant
// change (Log excluded), so a log-only edit is saved without a reload.
func applyConfigChanged(ctx context.Context, changed, reload bool) (msg string, err error) {
	st, pid, qErr := queryService()
	running := qErr == nil && st == svc.Running && pid != 0

	if !applyShouldReload(running, reload) {
		if !changed {
			return "config unchanged", nil
		}

		return "config saved", nil
	}

	if err = waitControlServer(ctx, pid); err != nil {
		return "", err
	}
	if _, err = ipc.NewClient(ipc.PipeName).Reload(ctx); err != nil {
		return "", err
	}

	return "config applied", nil
}

// installService registers the service against the canonical service binary
// (the non-GUI sibling when running as the GUI exe), not the current process
// image.  It does not load the config or touch interception.
func installService() (res Result) {
	if err := service.Install(); err != nil {
		return Result{Err: "service could not be installed"}
	}

	return Result{OK: true, Message: "service installed"}
}

// stopService stops the service and waits for it to reach the stopped state.
func stopService() (res Result) {
	if err := service.Stop(); err != nil {
		return Result{Err: "service could not be stopped"}
	}

	return Result{OK: true, Message: "service stopped"}
}

// stopServiceAndVerify stops the service, then confirms no loopback rule
// survived.  service.Stop ignores the handler's exit code, so a failed
// shutdown restore would otherwise be reported as success.
func stopServiceAndVerify(ctx context.Context) (res Result) {
	if res = stopService(); res.Err != "" {
		return res
	}
	// The service's shutdown restore is best-effort; run the config-independent
	// restore again so adapter fallback state is cleaned up too, then verify.
	if err := localRestore(ctx); err != nil {
		return Result{Err: err.Error()}
	}
	if err := verifyInterceptionDown(ctx); err != nil {
		return Result{Err: err.Error()}
	}

	return res
}

// restoreViaControl restores interception over the verified control pipe.
// waitControlServer verifies the server identity on one connection, then the
// restore opens a fresh connection; the re-dial window is accepted because
// owning \\.\pipe\sigildns requires SYSTEM+Administrators.
func restoreViaControl(ctx context.Context, pid uint32) (err error) {
	if err = waitControlServer(ctx, pid); err != nil {
		return err
	}

	return ipc.NewClient(ipc.PipeName).Restore(ctx)
}

// serviceRunning reports whether the service is running and its pid.
func serviceRunning() (running bool, pid uint32) {
	if st, p, err := queryService(); err == nil && st == svc.Running {
		return true, p
	}

	return false, 0
}

// loadPersistedState loads the persisted state config-independently.  ok is
// false when the state file is missing or unreadable: that is deliberately NOT
// treated as a "clean" negative witness.
func loadPersistedState() (enabled, dirty, ok bool) {
	path := config.SystemStatePath()
	if path == "" {
		return false, false, false
	}

	pst, err := state.NewStore(path).Load()
	if err != nil || pst == nil {
		return false, false, false
	}

	return pst.Mode == state.ModeOn, pst.Dirty, true
}

// probeNRPTActive positively probes for our NRPT rule.  It fails closed: if the
// probe cannot be run it reports active so callers never delete while
// interception cannot be shown down.
func probeNRPTActive(ctx context.Context) (active bool) {
	nrpt, err := intercept.ProbeNRPT(ctx)
	if err != nil {
		return true
	}

	return nrpt.Ours
}

// localRestore performs a config-independent restore inside the elevated
// helper, the same state-only path crash recovery uses: it reads the persisted
// state and removes interception.  It is the fallback when the control pipe is
// unavailable or fails.
func localRestore(ctx context.Context) (err error) {
	path := config.SystemStatePath()
	if path == "" {
		return errors.New("system state directory is unavailable")
	}

	lc := lifecycle.New(lifecycle.Config{StatePath: path})
	if err = lc.Restore(ctx); err != nil {
		return errors.New("local restore failed")
	}

	return nil
}

// restoreInterception clears interception for uninstall: the verified control
// pipe first (when a service pid is known), then a local, config-independent
// restore as a fallback.  It reports failure only when neither path can clear
// interception.
func restoreInterception(ctx context.Context, pid uint32) (err error) {
	pipe := func() error {
		rctx, cancel := context.WithTimeout(ctx, restorePipeTimeout)
		defer cancel()

		return restoreViaControl(rctx, pid)
	}

	local := func() error {
		lctx, cancel := context.WithTimeout(ctx, localRestoreTimeout)
		defer cancel()

		return localRestore(lctx)
	}

	return restoreInterceptionWith(pid, pipe, local)
}

// verifyInterceptionDown proves no sigildns NRPT rule remains.  It fails
// closed: when the probe cannot be run, or a live rule survives one more local
// restore, it returns a static error so the caller aborts before deleting.
func verifyInterceptionDown(ctx context.Context) (err error) {
	if !probeNRPTActive(ctx) {
		return nil
	}

	lctx, cancel := context.WithTimeout(ctx, localRestoreTimeout)
	defer cancel()

	if lErr := localRestore(lctx); lErr != nil {
		return errors.New("interception is still active")
	}
	if probeNRPTActive(ctx) {
		return errors.New("interception is still active")
	}

	return nil
}

// uninstallService is compound and abort-on-failure.  It clears live
// interception first -- via the verified control pipe when the service is
// running, otherwise through a local config-independent restore -- stops and
// confirms the service, proves interception is down, and only then deletes the
// registration.
func uninstallService(ctx context.Context) (res Result) {
	running, pid := serviceRunning()
	enabled, dirty, active := uninstallStateFrom(running, loadPersistedState, func() bool {
		return probeNRPTActive(ctx)
	})
	restore, reason := uninstallPlan(running, enabled, dirty, active)

	err := uninstallSequence(restore,
		func() error {
			if rErr := restoreInterception(ctx, pid); rErr != nil {
				return errors.New("could not restore interception before uninstall")
			}

			return nil
		},
		func() error {
			if sErr := service.Stop(); sErr != nil && !errors.Is(sErr, windows.ERROR_SERVICE_NOT_ACTIVE) {
				return errors.New("could not stop the service before uninstall")
			}

			return nil
		},
		func() error {
			if vErr := verifyInterceptionDown(ctx); vErr != nil {
				return fmt.Errorf("uninstall aborted: %w", vErr)
			}

			return nil
		},
		func() error {
			if dErr := service.Delete(); dErr != nil {
				return errors.New("could not remove the service registration")
			}

			return nil
		},
	)
	if err != nil {
		return Result{Err: err.Error()}
	}

	return Result{OK: true, Message: "service uninstalled (" + reason + ")"}
}

// clearCaches resets the resolver cache and the Windows DNS cache.  A running
// service owns the resolver cache, so it clears both over the control pipe; a
// stopped service has no in-process cache, so the helper flushes the OS cache
// itself.  It never starts the service: a cache clear must not turn protection
// on behind the user's back.
func clearCaches(ctx context.Context) (res Result) {
	running, pid := serviceRunning()
	if running && pid != 0 {
		if err := waitControlServer(ctx, pid); err != nil {
			return Result{Err: err.Error()}
		}
		if err := ipc.NewClient(ipc.PipeName).ClearCache(ctx); err != nil {
			return Result{Err: err.Error()}
		}

		return Result{OK: true, Message: "DNS cache cleared"}
	}

	if err := lifecycle.FlushDNS(ctx); err != nil {
		return Result{Err: "could not clear the Windows DNS cache"}
	}

	return Result{OK: true, Message: "Windows DNS cache cleared"}
}

// driveControl verifies the control-pipe server identity, then issues the IPC
// calls for the action.
func driveControl(ctx context.Context, req Request, pid uint32) (msg string, err error) {
	if err = waitControlServer(ctx, pid); err != nil {
		return "", err
	}

	client := ipc.NewClient(ipc.PipeName)

	switch req.Action {
	case ActionEnable:
		// Service start already enables interception; Enable is idempotent and
		// leaves a startup enable running.
		if err = client.Enable(ctx); err != nil {
			return "", err
		}

		return "interception enabled", nil

	case ActionRefreshLists:
		if err = client.RefreshLists(ctx); err != nil {
			return "", err
		}

		return "lists refreshed", nil

	default:
		return "", fmt.Errorf("unknown action %q", req.Action)
	}
}

// ensureService returns the running service's process id, starting the service
// if needed and waiting for it to report a pid.
func ensureService(ctx context.Context) (pid uint32, err error) {
	st, _, err := queryService()
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
		nShow:        swHide,
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

	return filepath.Join(base, "SigilDNS", "staging"), nil
}

// stagingSDDL is the protected DACL for staging files: full control to the
// given user SID plus SYSTEM and Administrators.  The user must be present so
// the unelevated UI can read the result; SYSTEM/Administrators let the elevated
// helper (which runs as the same user) serve it.  It is factored out so the
// exact SDDL is unit-tested without Windows DACL canonicalisation.
func stagingSDDL(userSID string) (sddl string) {
	return fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GA;;;SY)(A;;GA;;;BA)", userSID)
}

// secureUserOnly sets a protected DACL granting the current user, SYSTEM, and
// Administrators full control.
func secureUserOnly(path string) (err error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("reading user sid: %w", err)
	}

	if err = winacl.SetDACL(path, stagingSDDL(user.User.Sid.String())); err != nil {
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
