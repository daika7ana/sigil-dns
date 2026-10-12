//go:build gui

package gui

import (
	gogui "github.com/go-gui-org/go-gui/gui"
)

// guiAction is one user-initiated action.  Its string value is exactly the
// elevate.Action it maps to for the lifecycle actions (SPEC §15.4 phase 6);
// the service and profile actions are GUI-local names mapped to their real
// elevate requests in elevate_adapter.go.
type guiAction string

const (
	actionEnable  guiAction = "enable"
	actionDisable guiAction = "disable"
	actionRestore guiAction = "restore"

	// Service-management actions (Windows SCM).  They map onto the elevated
	// helper's service_* actions with an empty patch.
	actionInstall   guiAction = "service:install"
	actionUninstall guiAction = "service:uninstall"
	actionStart     guiAction = "service:start"
	actionStop      guiAction = "service:stop"

	// Profile actions.  All four map onto elevate.ActionApply; the structural
	// verbs additionally set AddProfile/RenameProfile/DeleteProfile.
	actionSaveProfile   guiAction = "profile:save"
	actionAddProfile    guiAction = "profile:add"
	actionRenameProfile guiAction = "profile:rename"
	actionDeleteProfile guiAction = "profile:delete"

	// actionSetActive makes a profile the active one via elevate.Patch.
	actionSetActive guiAction = "profile:active"
)

// powerState is the Status page's single power control: its on/off state,
// whether it can be toggled, whether the crash-recovery Restore button is
// offered, and a guidance note.
type powerState struct {
	on        bool
	canToggle bool
	restore   bool
	note      string
}

// planPower maps the state onto the single power control.  This is the one
// place the state->control matrix lives, so it is pure and directly testable.
//
// The control mirrors Enable/Disable (which already start the service and
// apply/remove interception), so there is no separate Start/Stop.  Restore is
// offered ONLY for the crash-leftover case (kindDirty); it is never offered
// while the resolver is enabled, so one click can no longer tear down a
// working session.
func planPower(kind statusKind, installed, running, on, busy, trusted bool) (ps powerState) {
	ps.on = on
	ps.canToggle = trusted && installed && !busy
	ps.restore = kind == kindDirty && trusted && !busy

	switch {
	case !trusted:
		ps.note = "Actions are unavailable until the dns-client service identity is verified."
	case busy:
		// The in-flight note replaces the guidance while an action runs.
	case kind == kindDirty:
		ps.note = "Interception is still applied from a previous session."
	case !installed:
		ps.note = "Install the dns-client service in Settings to turn on protection."
	case kind == kindStopped && running:
		ps.note = "The service is running but protection is off."
	case kind == kindStopped:
		ps.note = "Turning this on starts the service and enables protection."
	case kind == kindDegraded:
		ps.note = "The resolver needs attention. Check Settings."
	}

	return ps
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
	case actionInstall:
		return "Install"
	case actionUninstall:
		return "Uninstall"
	case actionStart:
		return "Start"
	case actionStop:
		return "Stop"
	case actionSaveProfile:
		return "Save"
	case actionAddProfile:
		return "Add"
	case actionRenameProfile:
		return "Rename"
	case actionDeleteProfile:
		return "Delete"
	case actionSetActive:
		return "Set active"
	default:
		return ""
	}
}

// actionVariant picks the button hierarchy.  Enable/Restore are the primary
// call to action; Disable/Stop/Rename are deliberately secondary (routine, not
// destructive).  Uninstall/Delete are the destructive actions.  The service
// management buttons stay Secondary so their disabled state reads as greyed
// rather than a pale filled-primary.
func actionVariant(a guiAction) (v gogui.ButtonVariant) {
	switch a {
	case actionDisable, actionStop, actionRenameProfile,
		actionInstall, actionStart:
		return gogui.ButtonSecondary
	case actionUninstall, actionDeleteProfile:
		return gogui.ButtonDanger
	default:
		return gogui.ButtonPrimary
	}
}

// waitingLabel is the in-flight note shown while the elevated call runs.
func waitingLabel(a guiAction) (s string) {
	switch a {
	case actionEnable:
		return "Waiting for elevation to enable..."
	case actionRestore:
		return "Waiting for elevation to restore..."
	case actionDisable:
		return "Waiting for elevation to disable..."
	case actionInstall:
		return "Waiting for elevation to install the service..."
	case actionUninstall:
		return "Waiting for elevation to uninstall the service..."
	case actionStart:
		return "Waiting for elevation to start the service..."
	case actionStop:
		return "Waiting for elevation to stop the service..."
	case actionSaveProfile:
		return "Waiting for elevation to save the profile..."
	case actionAddProfile:
		return "Waiting for elevation to add the profile..."
	case actionRenameProfile:
		return "Waiting for elevation to rename the profile..."
	case actionDeleteProfile:
		return "Waiting for elevation to delete the profile..."
	case actionSetActive:
		return "Waiting for elevation to set the active profile..."
	default:
		return "Waiting for elevation..."
	}
}

// doneTitle is the success toast title for an action.
func doneTitle(a guiAction) (title string) {
	switch a {
	case actionEnable:
		return "Enabled"
	case actionRestore:
		return "Restored"
	case actionDisable:
		return "Disabled"
	case actionInstall:
		return "Service installed"
	case actionUninstall:
		return "Service removed"
	case actionStart:
		return "Service started"
	case actionStop:
		return "Service stopped"
	case actionSaveProfile:
		return "Profile saved"
	case actionAddProfile:
		return "Profile added"
	case actionRenameProfile:
		return "Profile renamed"
	case actionDeleteProfile:
		return "Profile deleted"
	case actionSetActive:
		return "Active profile set"
	default:
		return "Done"
	}
}

// failTitle is the failure toast title for an action.
func failTitle(a guiAction) (title string) {
	switch a {
	case actionEnable:
		return "Could not enable"
	case actionRestore:
		return "Could not restore"
	case actionDisable:
		return "Could not disable"
	case actionInstall:
		return "Could not install the service"
	case actionUninstall:
		return "Could not remove the service"
	case actionStart:
		return "Could not start the service"
	case actionStop:
		return "Could not stop the service"
	case actionSaveProfile:
		return "Could not save the profile"
	case actionAddProfile:
		return "Could not add the profile"
	case actionRenameProfile:
		return "Could not rename the profile"
	case actionDeleteProfile:
		return "Could not delete the profile"
	case actionSetActive:
		return "Could not set the active profile"
	default:
		return "Action failed"
	}
}
