package console

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/huy090202/auth-gate/internal/console/authgate"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
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

// stepUpRoutes serves /api/stepup/*, for a full session only: an enrolling one
// needs no token, and a half one has not signed in.
func (a *App) stepUpRoutes(mux *http.ServeMux) {
	full := a.session(StageFull)
	mux.Handle("POST /api/stepup/start", a.audited("stepup-start", nil, full(handlerFunc(a.stepUpStart))))
	mux.Handle("POST /api/stepup/verify", a.audited("stepup-verify", nil, full(handlerFunc(a.stepUpVerify))))
}

// stepUpStart opens a challenge for the action's purpose and remembers its id in
// the session. An action outside the allow-list never reaches AuthGate, and the
// admin-* actions are for masters only.
func (a *App) stepUpStart(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	var in struct{ Action, Factor string }
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	purpose, ok := stepUpActions[in.Action]
	if !ok {
		return &apiError{Status: http.StatusBadRequest, Code: "invalid_action"}
	}
	setAuditTarget(r.Context(), in.Action)
	if strings.HasPrefix(in.Action, "admin-") && admin.Role != RoleMaster {
		return &apiError{Status: http.StatusForbidden, Code: "forbidden"}
	}
	if in.Factor != "pin" && in.Factor != "webauthn" {
		return &apiError{Status: http.StatusBadRequest, Code: "invalid_factor"}
	}
	ch, err := a.AuthGate.CreateChallenge(r.Context(), admin.ID, purpose, in.Factor)
	if err != nil {
		return agError(err)
	}
	if err := a.Store.SetChallenge(r.Context(), x.Hash, ch.ID); err != nil {
		return sessionGone(err)
	}
	out := map[string]any{"challengeId": ch.ID, "expiresAt": ch.ExpiresAt}
	if len(ch.PublicKey) > 0 {
		out["publicKey"] = ch.PublicKey
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// stepUpVerify answers the session's own challenge and hands the token to the
// browser. It does not spend the token's jti: the action does, after its guards.
func (a *App) stepUpVerify(w http.ResponseWriter, r *http.Request) error {
	x, _ := sessionFrom(r.Context())
	var in struct {
		ChallengeID string          `json:"challengeId"`
		PIN         *string         `json:"pin"`
		Credential  json.RawMessage `json:"credential"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if x.ChallengeID == "" || in.ChallengeID != x.ChallengeID {
		return errChallengeGone
	}
	tok, err := a.AuthGate.VerifyChallenge(r.Context(), in.ChallengeID, in.PIN, in.Credential)
	if err != nil {
		return agError(err)
	}
	exp, ok := unverifiedExp(tok)
	if !ok {
		slog.Warn("console: step-up token without a readable exp")
		return &apiError{Status: http.StatusBadGateway, Code: "authgate_unavailable"}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stepUpToken": tok, "expiresAt": exp})
	return nil
}

// unverifiedExp reads the exp claim for the browser to show. The token came from
// AuthGate in answer to our own call and is fully checked when an action uses it.
func unverifiedExp(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0).UTC(), true
}
