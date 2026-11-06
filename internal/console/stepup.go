package console

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/console/authgate"
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

// stepUp is a checked, not yet spent, step-up token. A nil *stepUp is what an
// enrolling session gets: there is nothing to spend.
type stepUp struct {
	jti string
	exp time.Time
}

// checkStepUp verifies the request's token for the action (signature, admin,
// purpose, expiry) without spending it, so the handler can run its own guards
// first and a refusal does not cost the admin their token. Without a token it
// is 403 before anything else. An enrolling session needs none; the caller
// decides whether it may act at all (see factorsForWrite). It is only called behind
// a session, so a caller without one cannot make the Console fetch AuthGate's
// keys. Any failure is the same 403.
func (a *App) checkStepUp(r *http.Request, s Session, action string) (*stepUp, error) {
	if s.Stage == StageEnroll {
		return nil, nil
	}
	purpose, ok := stepUpActions[action]
	if !ok {
		slog.Error("console: unknown step-up action", "action", action)
		return nil, &apiError{Status: http.StatusInternalServerError, Code: "internal"}
	}
	tok := r.Header.Get(stepUpHeader)
	if tok == "" {
		return nil, errStepUp
	}
	c, err := a.Tokens.Verify(r.Context(), tok, s.AdminID, purpose)
	if err != nil {
		if !errors.Is(err, authgate.ErrInvalidToken) {
			slog.Warn("console: step-up check failed", "err", err)
		}
		return nil, errStepUp
	}
	return &stepUp{jti: c.JTI, exp: c.ExpiresAt}, nil
}

// spendStepUp uses the token up, immediately before the action it authorizes.
// A token already used is the same 403.
func (a *App) spendStepUp(r *http.Request, t *stepUp) error {
	if t == nil {
		return nil
	}
	if err := a.Store.UseJTI(r.Context(), t.jti, t.exp); err != nil {
		if !errors.Is(err, ErrReplayed) {
			slog.Warn("console: step-up spend failed", "err", err)
		}
		return errStepUp
	}
	return nil
}
