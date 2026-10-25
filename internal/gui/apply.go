//go:build gui

package gui

import (
	"context"
	"errors"
	"time"

	gogui "github.com/go-gui-org/go-gui/gui"
)

// errUnsupported is the GUI-local sentinel for "elevation is unavailable on
// this platform".  The elevate adapter maps elevate.ErrUnsupported onto it so
// the view/action layer never imports internal/elevate.
var errUnsupported = errors.New("elevation is not supported on this platform")

// errRevision is the GUI-local sentinel for a config-changed conflict, mapped
// from elevate.ErrRevision by the adapter.
var errRevision = errors.New("config changed since it was read")

// applyResult is the sanitized outcome of one elevated action.  It never
// carries a resolver id or upstream URL: the helper's messages are secret-free
// by contract (SPEC §15.2/§15.3).
type applyResult struct {
	message string
	errText string
	ok      bool
}

// profileOpKind names the single structural profile operation a request may
// carry.  The helper rejects more than one per request.
type profileOpKind int

const (
	opAdd profileOpKind = iota
	opRename
	opDelete
)

// profileOp is one structural profile operation.  name is the new name (add),
// the source name (rename), or the target name (delete); to is the rename
// destination.
type profileOp struct {
	kind profileOpKind
	name string
	to   string
}

// applyRequest is the GUI-local description of one action to apply.  It is the
// seam between the view and the elevation layer; the adapter turns it into an
// elevate.Request (elevate_adapter.go).
type applyRequest struct {
	action           guiAction
	expectedRevision string

	// draft is the edited profile a save (or a rename) carries.  nil when the
	// action has no field patch.  The resolver id inside it only ever holds a
	// value the user typed; it never travels over IPC (SPEC §15.2).
	draft *profileDraft

	// op is the structural profile operation (add/rename/delete), nil otherwise.
	op *profileOp

	// activeSwitch names a profile to make active, nil when unchanged.
	activeSwitch *string

	// log is the Logging page's edited level and path, nil when the action
	// carries no log patch (LOGGING_PLAN §4.7).
	log *logDraft
}

// applyFunc performs one elevated action and returns its outcome.  The
// production value is elevateApply (elevate_adapter.go); tests inject a fake.
type applyFunc func(ctx context.Context, req applyRequest) (applyResult, error)

// runAction launches a lifecycle or service action through the apply seam.
func runAction(ctx gogui.EventCtx, action guiAction) {
	runRequest(ctx.Window, applyRequest{action: action})
}

// runSaveProfile launches the profile-save action, carrying the draft.
func runSaveProfile(ctx gogui.EventCtx, draft profileDraft) {
	d := draft
	runRequest(ctx.Window, applyRequest{action: actionSaveProfile, draft: &d})
}

// runProfileOp launches a structural profile operation, carrying a draft patch
// only when there are unsaved editor edits.
func runProfileOp(w *gogui.Window, op profileOp, draft *profileDraft) {
	runRequest(w, applyRequest{action: structuralAction(op.kind), op: &op, draft: draft})
}

// runSetActive launches the "make this profile active" action.
func runSetActive(w *gogui.Window, name string) {
	runRequest(w, applyRequest{action: actionSetActive, activeSwitch: &name})
}

// structuralAction maps a structural op onto its GUI action.
func structuralAction(k profileOpKind) (a guiAction) {
	switch k {
	case opAdd:
		return actionAddProfile
	case opRename:
		return actionRenameProfile
	default:
		return actionDeleteProfile
	}
}

// runRequest launches an action off the frame thread.  It marks the UI busy
// synchronously (frame thread), runs the blocking call on a goroutine, and
// posts the outcome back with QueueCommand.  The goroutine never touches the
// window or App; only the queued callback does.
func runRequest(w *gogui.Window, req applyRequest) {
	app := gogui.State[App](w)
	if app.busy || app.apply == nil {
		return
	}

	if req.expectedRevision == "" && app.snap.summary != nil {
		req.expectedRevision = app.snap.summary.Revision
	}

	app.busy = true
	app.busyNote = waitingLabel(req.action)
	app.busyAction = req.action

	apply := app.apply
	winCtx := w.Ctx()

	w.InvalidateLayout()

	go func() {
		res, err := apply(winCtx, req)

		w.QueueCommand(func(win *gogui.Window) {
			a := gogui.State[App](win)
			if res.ok {
				a.reflectFilteringAction(req.action)
			}
			a.busy = false
			a.busyNote = ""
			a.busyAction = ""
			a.showToast(win, noticeFor(req.action, res, err))
			// Re-poll at once.  The protection banner reflects the service,
			// never an optimistic guess; only the pause toggle above flips
			// immediately, and the full read replaces it with the real expiry.
			requestRefresh(a.refresh)
			win.InvalidateLayout()
		})
	}()
}

// filteringPauseDuration mirrors internal/lifecycle.FilteringPauseDuration.
//
// shortcut: the GUI cannot import lifecycle without pulling the resolver into
// the config client; keep the two values in sync.
const filteringPauseDuration = 15 * time.Minute

// reflectFilteringAction flips the visible pause state from a successful
// pause/resume, so the control changes on the click instead of waiting for the
// next status read.  The poller's full read replaces it with the service's
// exact expiry.
func (a *App) reflectFilteringAction(action guiAction) {
	if a.snap.status == nil || a.snap.status.Stats == nil {
		return
	}
	if action != actionPauseFiltering && action != actionResumeFiltering {
		return
	}

	// Copy the DTO and its stats instead of mutating them: the poller still
	// holds the same pointers in its last-applied snapshot, and mutating those
	// off the frame thread's copy would race with its next comparison.
	st := *a.snap.status
	stats := *st.Stats
	stats.PausedUntil = nil
	if action == actionPauseFiltering {
		until := time.Now().Add(filteringPauseDuration)
		stats.PausedUntil = &until
	}
	st.Stats = &stats
	a.snap.status = &st
}

// noticeKind is the severity of an action-result toast.
type noticeKind int

const (
	noticeInfo noticeKind = iota
	noticeSuccess
	noticeWarning
	noticeError
)

// noticeMessage is one action result shown as a transient toast.
type noticeMessage struct {
	kind  noticeKind
	title string
	body  string
}

// defaultToastTTL is how long a toast stays before auto-dismissing.
const defaultToastTTL = 5 * time.Second

// showToast replaces the current toast and schedules its auto-dismiss.  The
// timer runs off the frame thread and posts the clear back with QueueCommand;
// a later toast (or a manual dismiss) bumps the sequence so a stale timer is a
// no-op.
func (a *App) showToast(w *gogui.Window, n noticeMessage) {
	ttl := a.toastTTL
	if ttl <= 0 {
		ttl = defaultToastTTL
	}

	n.body = alertBody(n.title, n.body)
	a.toast = &n
	a.toastSeq++
	seq := a.toastSeq
	winCtx := w.Ctx()

	go func() {
		timer := time.NewTimer(ttl)
		defer timer.Stop()

		select {
		case <-winCtx.Done():
			return
		case <-timer.C:
		}

		w.QueueCommand(func(win *gogui.Window) {
			st := gogui.State[App](win)
			if st.toastSeq != seq {
				return // replaced or already dismissed
			}
			st.toast = nil
			win.InvalidateLayout()
		})
	}()
}

// dismissToast clears the toast by hand and invalidates any pending timer.
func dismissToast(ctx gogui.EventCtx) {
	app := gogui.State[App](ctx.Window)
	app.toast = nil
	app.toastSeq++
	ctx.Window.InvalidateLayout()
}

// noticeFor maps an action outcome onto the notice to show.  Pure, so the
// cancelled / failure / success handling is unit-testable without a window.
func noticeFor(action guiAction, res applyResult, err error) noticeMessage {
	switch {
	case errors.Is(err, errUnsupported):
		return noticeMessage{
			kind:  noticeWarning,
			title: "Not supported",
			body:  "Managing the service is not available on this platform.",
		}
	case errors.Is(err, errRevision):
		return noticeMessage{
			kind:  noticeWarning,
			title: "Configuration changed",
			body:  "The configuration changed since this window loaded it. Try again.",
		}
	case err != nil:
		// A launch/communication failure is not a user cancellation: the
		// helper reports UAC declines as a Result, not an error.  Keep the
		// cause visible instead of claiming nothing happened.
		return noticeMessage{
			kind:  noticeError,
			title: failTitle(action),
			body:  alertBody(failTitle(action), err.Error()),
		}
	case looksCancelled(res):
		// UAC declined.  Calm and non-scary: nothing was changed.
		return noticeMessage{
			kind:  noticeWarning,
			title: "Administrator approval cancelled",
			body:  "No changes were made.",
		}
	case !res.ok:
		return noticeMessage{
			kind:  noticeError,
			title: failTitle(action),
			body:  alertBody(failTitle(action), res.errText),
		}
	default:
		return noticeMessage{
			kind:  noticeSuccess,
			title: doneTitle(action),
			body:  alertBody(doneTitle(action), res.message),
		}
	}
}

// looksCancelled reports a real UAC decline.  `internal/elevate` returns
// "elevation cancelled" as a Result for that one case; matching the exact
// message keeps "context canceled" from a helper that already wrote the config
// from being misreported as a user cancellation.
func looksCancelled(res applyResult) bool {
	switch normaliseMessage(res.errText) {
	case "elevation cancelled", "user cancelled":
		return true
	}

	return false
}
