//go:build gui

package gui

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

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

// applyFunc performs one elevated action and returns its outcome.  The
// production value is elevateApply (elevate_adapter.go); tests inject a fake.
type applyFunc func(ctx context.Context, action guiAction, expectedRevision string) (applyResult, error)

// runAction launches an elevated action off the frame thread.  It marks the UI
// busy synchronously (frame thread), runs the blocking call on a goroutine, and
// posts the outcome back with QueueCommand.  The goroutine never touches the
// window or App; only the queued callback does.
func runAction(ctx gogui.EventCtx, action guiAction) {
	w := ctx.Window
	app := gogui.State[App](w)
	if app.busy || app.apply == nil {
		return
	}

	app.busy = true
	app.busyNote = waitingLabel(action)

	rev := ""
	if app.snap.summary != nil {
		rev = app.snap.summary.Revision
	}
	apply := app.apply
	winCtx := w.Ctx()

	w.InvalidateLayout()

	go func() {
		res, err := apply(winCtx, action, rev)

		w.QueueCommand(func(win *gogui.Window) {
			a := gogui.State[App](win)
			a.busy = false
			a.busyNote = ""
			win.Toast(toastFor(action, res, err))
			// Re-poll at once.  The banner reflects the service, never an
			// optimistic guess.
			select {
			case a.refresh <- struct{}{}:
			default:
			}
			win.InvalidateLayout()
		})
	}()
}

// toastFor maps an action outcome onto the toast to show.  Pure, so the
// cancelled / failure / success handling is unit-testable without a window.
func toastFor(action guiAction, res applyResult, err error) gogui.ToastCfg {
	switch {
	case errors.Is(err, errUnsupported):
		return gogui.ToastCfg{
			Severity: gogui.ToastWarning,
			Title:    "Not supported",
			Body:     "Managing the service is not available on this platform.",
		}
	case errors.Is(err, errRevision):
		return gogui.ToastCfg{
			Severity: gogui.ToastWarning,
			Title:    "Configuration changed",
			Body:     "The config changed since this window read it. Refresh and try again.",
		}
	case err != nil || looksCancelled(res):
		// UAC declined, or the elevated launch never completed.  Calm and
		// non-scary: nothing was changed.
		return gogui.ToastCfg{
			Severity: gogui.ToastWarning,
			Title:    "Elevation cancelled",
			Body:     "Nothing changed.",
		}
	case !res.ok:
		return gogui.ToastCfg{
			Severity: gogui.ToastError,
			Title:    failTitle(action),
			Body:     bodyOr(res.errText, "The change could not be applied."),
		}
	default:
		return gogui.ToastCfg{
			Severity: gogui.ToastSuccess,
			Title:    doneTitle(action),
			Body:     bodyOr(res.message, "Done."),
		}
	}
}

// looksCancelled is a hedge until internal/elevate exposes a cancellation
// sentinel: a helper result that names a user decline is treated as the calm
// cancelled outcome rather than a hard error.
func looksCancelled(res applyResult) bool {
	return strings.Contains(strings.ToLower(res.errText), "cancel")
}

// bodyOr returns s, trimmed and length-capped, or def when empty.
func bodyOr(s, def string) (body string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	const maxBody = 200
	if len(s) > maxBody {
		s = strings.TrimSpace(s[:maxBody]) + "..."
	}

	return s
}

// newUUID returns a random RFC 4122 v4 UUID for the elevate request.
func newUUID() (id string) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
