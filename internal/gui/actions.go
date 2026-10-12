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

	// Profile actions.  All four map onto elevate.ActionApply; the structural
	// verbs additionally set AddProfile/RenameProfile/DeleteProfile.
	actionSaveProfile   guiAction = "profile:save"
	actionAddProfile    guiAction = "profile:add"
	actionRenameProfile guiAction = "profile:rename"
	actionDeleteProfile guiAction = "profile:delete"

	// actionSetActive makes a profile the active one via elevate.Patch.
	actionSetActive guiAction = "profile:active"

	// actionRefreshLists re-downloads the active profile's filter lists.  It is
	// a service-only action: it carries no config patch and drives the running
	// service over the verified control pipe (elevate.ActionRefreshLists).
	actionRefreshLists guiAction = "lists:refresh"
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

// actionMeta is the presentation for one action: its button label and
// hierarchy, the in-flight note, and the success/failure toast titles.  One
// table keeps an action's strings and variant together.
type actionMeta struct {
	label   string
	variant gogui.ButtonVariant
	waiting string
	done    string
	fail    string
}

// actionMetas is the presentation for the known actions.  Enable/Restore are
// the primary call to action; Disable/Rename/the service-management buttons are
// deliberately Secondary (routine, not destructive, and their disabled state
// reads as greyed rather than a pale filled-primary); Uninstall/Delete are the
// destructive actions.
var actionMetas = map[guiAction]actionMeta{
	actionEnable: {"Enable", gogui.ButtonPrimary,
		"Waiting for elevation to enable...", "Enabled", "Could not enable"},
	actionRestore: {"Restore now", gogui.ButtonPrimary,
		"Waiting for elevation to restore...", "Restored", "Could not restore"},
	actionDisable: {"Disable", gogui.ButtonSecondary,
		"Waiting for elevation to disable...", "Disabled", "Could not disable"},
	actionInstall: {"Install", gogui.ButtonSecondary,
		"Waiting for elevation to install the service...", "Service installed", "Could not install the service"},
	actionUninstall: {"Uninstall", gogui.ButtonDanger,
		"Waiting for elevation to uninstall the service...", "Service removed", "Could not remove the service"},
	actionSaveProfile: {"Save", gogui.ButtonPrimary,
		"Waiting for elevation to save the profile...", "Profile saved", "Could not save the profile"},
	actionAddProfile: {"Add", gogui.ButtonPrimary,
		"Waiting for elevation to add the profile...", "Profile added", "Could not add the profile"},
	actionRenameProfile: {"Rename", gogui.ButtonSecondary,
		"Waiting for elevation to rename the profile...", "Profile renamed", "Could not rename the profile"},
	actionDeleteProfile: {"Delete", gogui.ButtonDanger,
		"Waiting for elevation to delete the profile...", "Profile deleted", "Could not delete the profile"},
	actionSetActive: {"Set active", gogui.ButtonPrimary,
		"Waiting for elevation to set the active profile...", "Active profile set", "Could not set the active profile"},
	actionRefreshLists: {"Update blocklists", gogui.ButtonSecondary,
		"Waiting for the service to update blocklists...", "Blocklists updated", "Could not update blocklists"},
}

// metaFor returns an action's presentation, or the unknown-action defaults.
func metaFor(a guiAction) actionMeta {
	if m, ok := actionMetas[a]; ok {
		return m
	}

	return actionMeta{variant: gogui.ButtonPrimary,
		waiting: "Waiting for elevation...", done: "Done", fail: "Action failed"}
}

func actionLabel(a guiAction) string                { return metaFor(a).label }
func actionVariant(a guiAction) gogui.ButtonVariant { return metaFor(a).variant }
func waitingLabel(a guiAction) string               { return metaFor(a).waiting }
func doneTitle(a guiAction) string                  { return metaFor(a).done }
func failTitle(a guiAction) string                  { return metaFor(a).fail }
