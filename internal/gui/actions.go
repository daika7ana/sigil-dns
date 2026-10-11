//go:build gui

package gui

import (
	gogui "github.com/go-gui-org/go-gui/gui"
)

// guiAction is one user-initiated service action.  Its string value is exactly
// the elevate.Action it maps to (SPEC §15.4 phase 6).
type guiAction string

const (
	actionEnable  guiAction = "enable"
	actionDisable guiAction = "disable"
	actionRestore guiAction = "restore"
)

// actionPlan is the set of actions a state offers.  At most one primary (the
// obvious next step) and one optional secondary; blocked means the state
// refuses all actions, with reason shown to the user.
type actionPlan struct {
	primary   guiAction
	secondary guiAction
	blocked   bool
	reason    string
}

// planActions maps a state onto its action plan.  This is the one place the
// state->action matrix lives, so it is pure and directly testable.
//
//	running           -> Disable (a normal action, not danger)
//	stopped           -> Enable
//	dirty             -> Restore now (primary) + Disable
//	degraded          -> Restore now (primary) + Disable
//	unverified        -> blocked, with an explanation
func planActions(kind statusKind) actionPlan {
	switch kind {
	case kindUnverified:
		return actionPlan{
			blocked: true,
			reason:  "Actions are unavailable until the dns-client service identity is verified.",
		}
	case kindStopped:
		return actionPlan{primary: actionEnable}
	case kindDirty, kindDegraded:
		return actionPlan{primary: actionRestore, secondary: actionDisable}
	default: // kindRunning
		return actionPlan{primary: actionDisable}
	}
}

// actionLabel is the button text for an action.
func actionLabel(a guiAction) (label string) {
	switch a {
	case actionEnable:
		return "Enable"
	case actionRestore:
		return "Restore now"
	case actionDisable:
		return "Disable"
	default:
		return ""
	}
}

// actionVariant picks the button hierarchy.  Enable/Restore are the primary
// call to action; Disable is deliberately secondary (this is a routine action,
// not a destructive one).
func actionVariant(a guiAction) (v gogui.ButtonVariant) {
	if a == actionDisable {
		return gogui.ButtonSecondary
	}

	return gogui.ButtonPrimary
}

// waitingLabel is the in-flight note shown while the elevated call runs.
func waitingLabel(a guiAction) (s string) {
	switch a {
	case actionEnable:
		return "Waiting for elevation to enable..."
	case actionRestore:
		return "Waiting for elevation to restore..."
	default:
		return "Waiting for elevation to disable..."
	}
}

// doneTitle is the success toast title for an action.
func doneTitle(a guiAction) (title string) {
	switch a {
	case actionEnable:
		return "Enabled"
	case actionRestore:
		return "Restored"
	default:
		return "Disabled"
	}
}

// failTitle is the failure toast title for an action.
func failTitle(a guiAction) (title string) {
	switch a {
	case actionEnable:
		return "Could not enable"
	case actionRestore:
		return "Could not restore"
	default:
		return "Could not disable"
	}
}
