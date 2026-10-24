//go:build windows

package drift

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sys/windows"

	"github.com/daika7ana/sigil-dns/internal/intercept"
	"github.com/daika7ana/sigil-dns/internal/state"
)

// Registry keys watched for DNS server rewrites.  RegNotifyChangeKeyValue with
// subtree watching catches per-interface NameServer edits that
// NotifyIpInterfaceChange misses (GPO refresh, MDM, VPN, other DNS tools).
const (
	tcpipParameters  = `SYSTEM\CurrentControlSet\Services\Tcpip\Parameters`
	tcpip6Parameters = `SYSTEM\CurrentControlSet\Services\Tcpip6\Parameters`

	// registryDebounce coalesces bursts of registry writes.
	registryDebounce = 2 * time.Second

	// interceptTimeout bounds a re-apply/disable while the lifecycle interlock
	// is held, so a hung PowerShell call cannot block the control surface.
	// Enable runs up to three scripts (snapshot, add, fallback), and a cold
	// DnsClient provider is slow, so leave room rather than auto-disabling a
	// healthy host on the first cold call.
	interceptTimeout = 60 * time.Second
)

// NewSystemReconciler returns a Reconciler wired to the Windows registry watcher
// and NRPT interception.  interlock must be the mutex the service lifecycle
// holds for enable/disable/reload, so a re-apply cannot race a disable.
func NewSystemReconciler(statePath string, logger *slog.Logger, onDisable func(), interlock *sync.Mutex) (r *Reconciler) {
	s := &system{statePath: statePath, interlock: interlock}

	return NewReconciler(Options{
		Prober: s, Applier: s, Disabler: s, Logger: logger, OnDisable: onDisable,
	})
}

// system implements Prober, Applier, and Disabler against the persisted state.
type system struct {
	statePath string
	interlock *sync.Mutex
}

// lock returns the matching unlock, or a no-op when no interlock is wired.
func (s *system) lock() (unlock func()) {
	if s.interlock == nil {
		return func() {}
	}
	s.interlock.Lock()

	return s.interlock.Unlock
}

// Probe reads the expected state from the state file and the actual state from
// the OS.  When interception is off it reports nothing to reconcile.
func (s *system) Probe(ctx context.Context) (obs Observation, err error) {
	st, err := state.NewStore(s.statePath).Load()
	if err != nil {
		if errors.Is(err, state.ErrNoState) {
			return Observation{}, nil
		}

		return Observation{}, err
	}
	if st.Mode != state.ModeOn {
		return Observation{}, nil
	}

	nrpt, nErr := intercept.ProbeNRPT(ctx)
	if nErr != nil {
		return Observation{}, nErr
	}

	// A legacy state has no NameServers; compare namespaces only.
	var servers map[string][]string
	if len(st.NameServers) > 0 {
		servers = map[string][]string{"": st.NameServers}
	}

	return Observation{
		Expected: State{
			NRPTOurs:   true,
			Namespaces: expectedNamespaces(st.Bypass),
			Servers:    servers,
		},
		Actual: State{
			NRPTOurs:      nrpt.Ours,
			Namespaces:    nrpt.Namespaces,
			Servers:       nrpt.Servers,
			NRPTConflicts: nrpt.Conflicts,
		},
	}, nil
}

// Apply re-applies NRPT interception.  It holds the lifecycle interlock across
// the state read and the rule add, so a concurrent disable cannot remove the
// rules between the mode check and the re-apply.
func (s *system) Apply(ctx context.Context) (err error) {
	unlock := s.lock()
	defer unlock()

	st, err := state.NewStore(s.statePath).Load()
	if err != nil {
		return err
	}
	if st.Mode != state.ModeOn {
		return nil
	}

	// Bound every PowerShell call: this path holds the lifecycle interlock, and
	// a hung DnsClient provider must not block every IPC action forever.
	callCtx, cancel := context.WithTimeout(ctx, interceptTimeout)
	defer cancel()

	return intercept.New(nil).Enable(callCtx, st.Bypass, st.NameServers)
}

// Disable removes interception and clears the state after persistent drift.
func (s *system) Disable(ctx context.Context) (err error) {
	unlock := s.lock()
	defer unlock()

	callCtx, cancel := context.WithTimeout(ctx, interceptTimeout)
	defer cancel()

	if err = intercept.New(nil).Disable(callCtx); err != nil {
		return err
	}

	store := state.NewStore(s.statePath)
	if err = store.MarkClean(); err != nil && !errors.Is(err, state.ErrNoState) {
		// MarkClean quarantines and replaces a corrupt file itself, so no
		// special case is needed here.
		return err
	}

	return nil
}

// watchRegistry watches both interface parameter keys and nudges the reconciler.
func watchRegistry(ctx context.Context, nudge chan<- struct{}) {
	var wg sync.WaitGroup

	for _, path := range []string{tcpipParameters, tcpip6Parameters} {
		wg.Add(1)

		go func(p string) {
			defer wg.Done()

			watchKey(ctx, p, nudge)
		}(path)
	}

	wg.Wait()
}

// watchKey sends on nudge (debounced) whenever path or a subkey changes.
func watchKey(ctx context.Context, path string, nudge chan<- struct{}) {
	key, err := openNotifyKey(path)
	if err != nil {
		return
	}
	defer func() { _ = windows.RegCloseKey(key) }()

	ev, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		return
	}
	defer func() { _ = windows.CloseHandle(ev) }()

	notifyFilter := uint32(windows.REG_NOTIFY_CHANGE_NAME | windows.REG_NOTIFY_CHANGE_LAST_SET)

	for ctx.Err() == nil {
		if err = windows.RegNotifyChangeKeyValue(key, true, notifyFilter, ev, true); err != nil {
			return
		}

		if !waitChange(ctx, ev) {
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(registryDebounce):
		}

		select {
		case nudge <- struct{}{}:
		default:
		}
	}
}

// waitChange blocks until the change event fires or ctx is canceled.
func waitChange(ctx context.Context, ev windows.Handle) (ok bool) {
	for {
		status, err := windows.WaitForSingleObject(ev, 500)
		if err != nil {
			return false
		}
		if status == windows.WAIT_OBJECT_0 {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
}

// openNotifyKey opens path under HKLM for change notification.
func openNotifyKey(path string) (h windows.Handle, err error) {
	sub, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}

	err = windows.RegOpenKeyEx(windows.HKEY_LOCAL_MACHINE, sub, 0, windows.KEY_NOTIFY|windows.KEY_READ, &h)

	return h, err
}
