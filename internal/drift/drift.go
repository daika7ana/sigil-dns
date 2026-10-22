// Package drift implements interception drift detection and reconciliation
// (SPEC §4.6).
//
// NotifyIpInterfaceChange only fires on adapter up/down, so a registry watcher
// on the Tcpip/Tcpip6 interface keys plus a periodic reconcile loop compare the
// expected interception state with reality: re-apply on drift, or warn and
// auto-disable on persistent drift.  Conflicting NRPT rules (e.g. a VPN's) are
// surfaced as a warning, never fought.
//
// The decision logic is a pure function so it is fully testable off Windows.
package drift

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Defaults for the reconcile loop (SPEC §4.6 specifies 30-60 s).
const (
	defaultInterval    = 45 * time.Second
	defaultMaxAttempts = 3
)

// State is an abstraction of the interception state used for comparisons.
type State struct {
	// NRPTConflicts are foreign equal-or-more-specific NRPT rules.
	NRPTConflicts []string

	// NRPTOurs is true when our NRPT rule is present.
	NRPTOurs bool
}

// Decision is the outcome of comparing expected and actual state.
type Decision int

// Decisions.
const (
	// DecisionOK means the state matches.
	DecisionOK Decision = iota

	// DecisionApply means drift was detected and interception must be re-applied.
	DecisionApply

	// DecisionConflict means a foreign rule conflicts; warn, do not fight.
	DecisionConflict
)

// Decide compares the expected state with the actual state.  It is pure.
//
//   - a foreign NRPT rule conflicts only when we rely on NRPT -> DecisionConflict;
//   - our missing NRPT rule -> DecisionApply;
//   - otherwise -> DecisionOK.
func Decide(expected, actual State) (d Decision) {
	if expected.NRPTOurs && len(actual.NRPTConflicts) > 0 {
		return DecisionConflict
	}

	if expected.NRPTOurs && !actual.NRPTOurs {
		return DecisionApply
	}

	return DecisionOK
}

// Observation is what a Prober reports: the expected state and the observed one.
type Observation struct {
	Expected State
	Actual   State
}

// Prober observes the current interception state.
type Prober interface {
	Probe(ctx context.Context) (obs Observation, err error)
}

// Applier re-applies interception on drift.
type Applier interface {
	Apply(ctx context.Context) (err error)
}

// Disabler turns interception off (used on persistent drift).
type Disabler interface {
	Disable(ctx context.Context) (err error)
}

// Options configures a Reconciler.
type Options struct {
	Prober      Prober
	Applier     Applier
	Disabler    Disabler
	Logger      *slog.Logger
	Interval    time.Duration
	MaxAttempts int

	// OnDisable runs after the auto-disable attempt on persistent drift so the
	// service host can stop (a running service is never left unprotected).
	OnDisable func()
}

// Reconciler periodically compares expected and actual interception state and
// reconciles drift.  It also exposes Start/Stop so it can be a service Watcher.
type Reconciler struct {
	prober      Prober
	applier     Applier
	disabler    Disabler
	logger      *slog.Logger
	interval    time.Duration
	maxAttempts int
	onDisable   func()

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewReconciler returns a Reconciler for opts.
func NewReconciler(opts Options) (r *Reconciler) {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default().With("component", "drift")
	}

	interval := opts.Interval
	if interval <= 0 {
		interval = defaultInterval
	}

	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultMaxAttempts
	}

	return &Reconciler{
		prober:      opts.Prober,
		applier:     opts.Applier,
		disabler:    opts.Disabler,
		logger:      logger,
		interval:    interval,
		maxAttempts: maxAttempts,
		onDisable:   opts.OnDisable,
	}
}

// Run reconciles immediately and then on every tick or nudge, until ctx is
// canceled.  It returns nil when it stops, including after auto-disabling on
// persistent drift.
func (r *Reconciler) Run(ctx context.Context, nudge <-chan struct{}) (err error) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	attempts := 0
	for {
		if r.reconcile(ctx, &attempts) {
			return nil
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		case <-nudge:
		}
	}
}

// reconcile performs one comparison and its action.  It returns true when the
// loop should stop (persistent drift, after auto-disabling).
func (r *Reconciler) reconcile(ctx context.Context, attempts *int) (stop bool) {
	if r.prober == nil {
		// No platform hooks (e.g. a non-Windows build): nothing to reconcile.
		return false
	}

	obs, err := r.prober.Probe(ctx)
	if err != nil {
		r.logger.WarnContext(ctx, "drift probe failed", "err", err)

		return false
	}

	switch Decide(obs.Expected, obs.Actual) {
	case DecisionOK:
		if *attempts > 0 {
			r.logger.InfoContext(ctx, "drift resolved")
		}
		*attempts = 0

	case DecisionConflict:
		r.logger.WarnContext(ctx,
			"conflicting NRPT rule detected; not fighting it (VPN/other DNS tool)",
			"rules", obs.Actual.NRPTConflicts,
		)
		*attempts = 0

	case DecisionApply:
		*attempts++

		if *attempts >= r.maxAttempts {
			r.logger.ErrorContext(ctx,
				"persistent drift; auto-disabling interception",
				"attempts", *attempts,
			)

			if r.disabler != nil {
				if dErr := r.disabler.Disable(ctx); dErr != nil {
					// Do not stop with interception still active: that would
					// leave DNS pointed at a dead resolver.  Keep the service
					// up and retry from a clean attempt count.
					r.logger.ErrorContext(ctx, "auto-disable failed; keeping service up", "err", dErr)
					*attempts = 0

					return false
				}
			}

			if r.onDisable != nil {
				r.onDisable()
			}

			return true
		}

		r.logger.WarnContext(ctx, "interception drift detected; re-applying", "attempt", *attempts)

		if r.applier != nil {
			if aErr := r.applier.Apply(ctx); aErr != nil {
				r.logger.ErrorContext(ctx, "re-apply failed", "err", aErr)
			}
		}
	}

	return false
}

// Start launches the reconcile loop and the registry watcher in the background.
func (r *Reconciler) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.cancel != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel

	nudge := make(chan struct{}, 1)

	r.wg.Add(2)
	go func() {
		defer r.wg.Done()
		_ = r.Run(ctx, nudge)
	}()
	go func() {
		defer r.wg.Done()
		watchRegistry(ctx, nudge)
	}()
}

// Stop stops the reconcile loop and the registry watcher.
func (r *Reconciler) Stop() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()

	if cancel == nil {
		return
	}

	cancel()
	r.wg.Wait()
}
