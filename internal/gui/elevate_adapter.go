//go:build gui

package gui

import (
	"context"
	"errors"

	"github.com/daika7ana/dns-client/internal/elevate"
)

// elevateApply is the production applyFunc.  It is the only place that imports
// internal/elevate, so the rest of the GUI stays testable without it and
// without a Windows host.
//
// One call is one explicit user action, gated behind the elevate one-shot
// helper: a single UAC prompt, no persistent broker (SPEC §15.1/§15.3).
func elevateApply(ctx context.Context, action guiAction, expectedRevision string) (applyResult, error) {
	res, err := elevate.Apply(ctx, elevate.Request{
		Action:           elevate.Action(action),
		ExpectedRevision: expectedRevision,
		UUID:             newUUID(),
	})
	if err != nil {
		switch {
		case errors.Is(err, elevate.ErrUnsupported):
			return applyResult{}, errUnsupported
		case errors.Is(err, elevate.ErrRevision):
			return applyResult{}, errRevision
		}

		return applyResult{}, err
	}

	return applyResult{ok: res.OK, message: res.Message, errText: res.Err}, nil
}
