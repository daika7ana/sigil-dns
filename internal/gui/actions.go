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
	actionRestore guiAction = "restore"

	// Service-management actions (Windows SCM).  They map onto the elevated
	// helper's service_* actions with an empty patch.  The card's Start runs
	// Enable (which starts the service and applies interception); Stop maps
	// onto the service's own graceful shutdown, which removes interception and
	// leaves the service installed.
	actionInstall     guiAction = "service:install"
	actionUninstall   guiAction = "service:uninstall"
	actionServiceStop guiAction = "service:stop"

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

	// actionClearCache clears the resolver cache and the Windows DNS cache.
	// It carries no config patch and drives the running service over the
	// verified control pipe (elevate.ActionClearCache).
	actionClearCache guiAction = "cache:clear"

	// actionSaveLog writes the Logging page's level and path.  It maps onto
	// elevate.ActionApply and carries only the log patch (LOGGING_PLAN §4.7).
	actionSaveLog guiAction = "log:save"
)

// powerState is the header's crash-recovery affordance: whether the one-click
// Restore is offered and the guidance note shown under the app name.
type powerState struct {
	restore bool
	note    string
}

// planPower maps the state onto the header's Restore affordance and note.  This
// is the one place the state->header matrix lives, so it is pure and directly
// testable.
//
// Restore is offered ONLY for the crash-leftover case (kindDirty, interception
// still applied while it is off); it is never offered beside a healthy session,
// so one click can no longer tear down a working one.  Routine protection is
// turned on by starting the service in Settings.
func planPower(kind statusKind, installed, running, busy, trusted bool) (ps powerState) {
	ps.restore = kind == kindDirty && trusted && !busy

	switch {
	case !trusted:
		ps.note = "Actions are unavailable until the Sigil DNS service identity is verified."
	case busy:
		// The in-flight note replaces the guidance while an action runs.
	case kind == kindDirty:
		ps.note = "Your DNS settings are still redirected from a previous session."
	case !installed:
		ps.note = "Install the Sigil DNS service in Settings to turn on protection."
	case kind == kindStopped && running:
		ps.note = "The service is running but protection is off."
	case kind == kindStopped:
		ps.note = "Start the service in Settings to turn on protection."
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
// the primary call to action; Stop/Rename/the service-management buttons are
// deliberately Secondary (routine, not destructive, and their disabled state
// reads as greyed rather than a pale filled-primary); Uninstall/Delete are the
// destructive actions.
var actionMetas = map[guiAction]actionMeta{
	actionEnable: {"Enable", gogui.ButtonPrimary,
		"Waiting for administrator approval to enable protection\u2026", "Enabled", "Could not enable protection"},
	actionRestore: {"Restore now", gogui.ButtonPrimary,
		"Waiting for administrator approval to restore DNS settings\u2026", "DNS settings restored", "Could not restore DNS settings"},
	actionInstall: {"Install", gogui.ButtonSecondary,
		"Waiting for administrator approval to install the service\u2026", "Service installed", "Could not install the service"},
	actionUninstall: {"Uninstall", gogui.ButtonDanger,
		"Waiting for administrator approval to uninstall the service\u2026", "Service removed", "Could not remove the service"},
	actionServiceStop: {"Stop", gogui.ButtonSecondary,
		"Waiting to stop the service\u2026", "Service stopped", "Could not stop the service"},
	actionSaveProfile: {"Save", gogui.ButtonPrimary,
		"Waiting for administrator approval to save the profile\u2026", "Profile saved", "Could not save the profile"},
	actionAddProfile: {"Add", gogui.ButtonPrimary,
		"Waiting for administrator approval to add the profile\u2026", "Profile added", "Could not add the profile"},
	actionRenameProfile: {"Rename", gogui.ButtonSecondary,
		"Waiting for administrator approval to rename the profile\u2026", "Profile renamed", "Could not rename the profile"},
	actionDeleteProfile: {"Delete", gogui.ButtonDanger,
		"Waiting for administrator approval to delete the profile\u2026", "Profile deleted", "Could not delete the profile"},
	actionSetActive: {"Set active", gogui.ButtonPrimary,
		"Waiting for administrator approval to change the active profile\u2026", "Active profile changed", "Could not change the active profile"},
	actionRefreshLists: {"Update filter lists", gogui.ButtonSecondary,
		"Waiting for the service to update filter lists\u2026", "Filter lists updated", "Could not update filter lists"},
	actionClearCache: {"Clear DNS cache", gogui.ButtonSecondary,
		"Waiting for the service to clear the DNS cache\u2026", "DNS cache cleared", "Could not clear the DNS cache"},
	actionSaveLog: {"Apply", gogui.ButtonPrimary,
		"Waiting for administrator approval to save logging settings\u2026", "Logging settings saved", "Could not save logging settings"},
}

// metaFor returns an action's presentation, or the unknown-action defaults.
func metaFor(a guiAction) actionMeta {
	if m, ok := actionMetas[a]; ok {
		return m
	}

	return actionMeta{variant: gogui.ButtonPrimary,
		waiting: "Waiting for administrator approval\u2026", done: "Action completed", fail: "The action could not be completed"}
}

func actionLabel(a guiAction) string                { return metaFor(a).label }
func actionVariant(a guiAction) gogui.ButtonVariant { return metaFor(a).variant }
func waitingLabel(a guiAction) string               { return metaFor(a).waiting }
func doneTitle(a guiAction) string                  { return metaFor(a).done }
func failTitle(a guiAction) string                  { return metaFor(a).fail }
