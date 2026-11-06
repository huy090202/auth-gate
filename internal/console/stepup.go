package console

import (
	"log/slog"
	"net/http"
)

// stepUpActions maps an action to the purpose of the token that authorizes it.
// It is the allow-list and the step-up table of the spec.
var stepUpActions = map[string]string{
	"rotate-key": "console-rotate-key", "revoke-previous-keys": "console-revoke-previous-keys", "disable-client": "console-disable-client",
	"purge-subject": "console-purge-subject", "update-client": "console-update-client",
	"change-password": "console-change-password", "add-factor": "console-add-factor", "remove-factor": "console-remove-factor",
	"admin-create": "console-admin-create", "admin-reset-password": "console-admin-reset-password",
	"admin-clear-factors": "console-admin-clear-factors", "admin-disable": "console-admin-disable",
	"admin-enable": "console-admin-enable", "admin-role": "console-admin-role",
}

const stepUpHeader = "x-step-up-token"

var errStepUp = &apiError{Status: http.StatusForbidden, Code: "step_up_required"}

// requireStepUp checks that the request carries a valid, unused token for the
// action, minted for this admin, and spends it. While enrolling there is nothing
// to step up from, so it passes. It is only called behind a session, so a caller
// without one cannot make the Console fetch AuthGate's keys. Any failure is the
// same 403.
func (a *App) requireStepUp(r *http.Request, s Session, action string) error {
	if s.Stage == StageEnroll {
		return nil
	}
	purpose, ok := stepUpActions[action]
	if !ok {
		slog.Error("console: unknown step-up action", "action", action)
		return &apiError{Status: http.StatusInternalServerError, Code: "internal"}
	}
	tok := r.Header.Get(stepUpHeader)
	if tok == "" {
		return errStepUp
	}
	c, err := a.Tokens.Verify(r.Context(), tok, s.AdminID, purpose)
	if err != nil {
		return errStepUp
	}
	if err := a.Store.UseJTI(r.Context(), c.JTI, c.ExpiresAt); err != nil {
		return errStepUp
	}
	return nil
}
