//go:build windows

package drift

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
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
)

// NewSystemReconciler returns a Reconciler wired to the Windows registry watcher
// and the NRPT / per-adapter interception.
func NewSystemReconciler(statePath string, logger *slog.Logger, onDisable func()) (r *Reconciler) {
	s := &system{statePath: statePath}

	return NewReconciler(Options{
		Prober: s, Applier: s, Disabler: s, Logger: logger, OnDisable: onDisable,
	})
}

// system implements Prober, Applier, and Disabler against the persisted state.
type system struct {
	statePath string
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

	if st.Intercept == state.MechanismAdapter {
		return s.probeAdapters(ctx)
	}

	nrpt, nErr := intercept.ProbeNRPT(ctx)
	if nErr != nil {
		return Observation{}, nErr
	}

	return Observation{
		Expected: State{NRPTOurs: true},
		Actual:   State{NRPTOurs: nrpt.Ours, NRPTConflicts: nrpt.Conflicts},
	}, nil
}

// probeAdapters builds the expected (loopback) and actual per-interface state.
func (s *system) probeAdapters(ctx context.Context) (obs Observation, err error) {
	entries, err := intercept.SnapshotAdapters(ctx)
	if err != nil {
		return Observation{}, err
	}

	expected := map[string][]string{}
	actual := map[string][]string{}
	for _, e := range entries {
		key := strconv.Itoa(e.Index)
		expected[key] = []string{"127.0.0.1", "::1"}
		actual[key] = append(append([]string{}, e.IPv4...), e.IPv6...)
	}

	return Observation{Expected: State{Adapters: expected}, Actual: State{Adapters: actual}}, nil
}

// Apply re-applies the active mechanism.
func (s *system) Apply(ctx context.Context) (err error) {
	st, err := state.NewStore(s.statePath).Load()
	if err != nil {
		return err
	}

	if st.Intercept == state.MechanismAdapter {
		entries, aErr := intercept.SnapshotAdapters(ctx)
		if aErr != nil {
			return aErr
		}

		return intercept.ApplyAdapters(ctx, entries)
	}

	return intercept.New(nil).Enable(ctx, st.Bypass)
}

// Disable removes interception and clears the state after persistent drift.
func (s *system) Disable(ctx context.Context) (err error) {
	if err = intercept.New(nil).Disable(ctx); err != nil {
		return err
	}

	store := state.NewStore(s.statePath)
	st, err := store.Load()
	if err != nil {
		return err
	}
	st.Mode = state.ModeOff
	st.Dirty = false

	return store.Save(st)
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
