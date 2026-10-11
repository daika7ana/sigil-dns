// Package elevate implements the one-shot elevated helper (SPEC §15.1/§15.2).
//
// The unelevated UI cannot change the config or the service mode directly: the
// config file is ACL'd to SYSTEM/Administrators.  Instead it drops a
// DPAPI-encrypted request into a per-user staging directory, launches this same
// binary elevated (UAC) with --apply <path>, and reads the verdict back from a
// sibling .res file.  The helper re-reads and re-validates everything itself and
// applies only an allowlisted patch: the request body is never trusted.
//
// The frozen API is:
//
//	Apply(ctx, req) (Result, error)  // unelevated: stage, elevate, collect
//	HelperMain(path) error           // elevated: verify, apply, write result
package elevate

import (
	"errors"
	"fmt"

	"github.com/daika7ana/dns-client/internal/config"
)

// Action is the requested one-shot operation.
type Action string

// The allowlisted actions.  Anything else is rejected.
const (
	// ActionEnable persists mode=on, reloads, then enables interception.
	ActionEnable Action = "enable"

	// ActionDisable persists mode=off, then disables interception.
	ActionDisable Action = "disable"

	// ActionRestore restores interception from persisted state.  It does not
	// touch the config and works even when the config is broken.
	ActionRestore Action = "restore"

	// ActionApply persists a config patch, reloading only if the config changed.
	ActionApply Action = "apply"
)

// valid reports whether a is a known action.
func (a Action) valid() (ok bool) {
	switch a {
	case ActionEnable, ActionDisable, ActionRestore, ActionApply:
		return true
	default:
		return false
	}
}

// Patch is the allowlisted set of config fields a request may change.  Only the
// top-level mode is writable; every other field is ignored even if present in
// the decoded request, so the helper can never be walked into writing arbitrary
// config.
type Patch struct {
	Mode *string `json:"mode,omitempty"`
}

// Request is the staged request body.  It is untrusted input: Validate and the
// per-field allowlist are the boundary.
type Request struct {
	Action Action `json:"action"`

	Patch Patch `json:"patch,omitempty"`

	// ExpectedRevision is the config revision (mtime+size) the UI last read.
	// When set, the helper refuses to write if the file changed underneath it.
	ExpectedRevision string `json:"expected_revision,omitempty"`

	// UUID names the staging files (<uuid>.req / <uuid>.res).
	UUID string `json:"uuid"`
}

// Result is the helper's verdict, written to <uuid>.res.
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Err     string `json:"err,omitempty"`
}

// ErrUnsupported is returned by the non-Windows facade.
var ErrUnsupported = errors.New("the elevated helper is only supported on Windows")

// ErrRevision is returned when the on-disk config changed since the UI read it.
var ErrRevision = errors.New("config changed, refresh")

// Validate checks a request without touching the filesystem.  It is the single
// gate every action passes through, on both sides of the elevation boundary.
func (r Request) Validate() (err error) {
	if !r.Action.valid() {
		return fmt.Errorf("unknown action %q", r.Action)
	}

	if r.UUID == "" {
		return errors.New("missing request id")
	}

	if r.Patch.Mode != nil {
		switch *r.Patch.Mode {
		case config.ModeOn, config.ModeOff:
		default:
			return fmt.Errorf("invalid mode %q", *r.Patch.Mode)
		}
	}

	return nil
}

// checkRevision returns ErrRevision when expected is non-empty and differs from
// the actual on-disk revision.  An empty expected means "no expectation" (the
// caller did not read a revision, e.g. a first-time enable).
func checkRevision(expected, actual string) (err error) {
	if expected == "" || expected == actual {
		return nil
	}

	return fmt.Errorf("%w (expected %s, found %s)", ErrRevision, expected, actual)
}

// applyPatch mutates cfg with the allowlisted patch fields only.
func applyPatch(cfg *config.Config, p Patch) {
	if p.Mode != nil {
		cfg.Mode = *p.Mode
	}
}

// applyRequest is the pure config-mutation path used by the helper: validate,
// enforce the expected revision, apply the allowlisted patch, then validate the
// result.  It is factored out so it is unit-testable off Windows.
func applyRequest(cfg *config.Config, req Request, actualRevision string) (err error) {
	if err = req.Validate(); err != nil {
		return err
	}
	if err = checkRevision(req.ExpectedRevision, actualRevision); err != nil {
		return err
	}

	applyPatch(cfg, req.Patch)

	return cfg.Validate()
}
