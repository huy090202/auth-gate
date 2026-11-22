package portal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// stepUpActions is the allow-list of actions that ask for a step-up, mapped to
// the purpose of the token that authorises each: "portal-" and the action.
var stepUpActions = func() map[string]string {
	m := map[string]string{}
	for _, a := range []string{
		"rotate-key", "revoke-previous-keys", "disable-client", "purge-subject", "delete-client",
		"change-password", "change-email", "add-factor", "remove-factor", "regenerate-recovery-codes", "delete-account",
	} {
		m[a] = "portal-" + a
	}
	return m
}()

// targetKind is what an action's step-up token is bound to besides the user.
type targetKind int

const (
	clientTarget  targetKind = iota // a client id
	subjectTarget                   // <clientId>/<subjectId>
	accountTarget                   // the user's own id
)

// stepUpTargets says what each action is bound to. Every action takes a target.
var stepUpTargets = map[string]targetKind{
	"rotate-key": clientTarget, "revoke-previous-keys": clientTarget, "disable-client": clientTarget, "delete-client": clientTarget,
	"purge-subject":   subjectTarget,
	"change-password": accountTarget, "change-email": accountTarget, "add-factor": accountTarget,
	"remove-factor": accountTarget, "regenerate-recovery-codes": accountTarget, "delete-account": accountTarget,
}

var errInvalidTarget = apiErr(http.StatusBadRequest, "invalid_target")

// stepUpPurpose is the purpose of the challenge, and so of the token, for an
// action on a target: the action's own purpose, then ":" and the first 32 hex
// characters of sha256 of the target. The target is checked by the same rules
// as the door of the route it will be used on.
func stepUpPurpose(action, target string) (purpose string, err error) {
	base, ok := stepUpActions[action]
	if !ok {
		return "", apiErr(http.StatusBadRequest, "invalid_action")
	}
	if !validTarget(stepUpTargets[action], target) {
		return "", errInvalidTarget
	}
	sum := sha256.Sum256([]byte(target))
	return base + ":" + hex.EncodeToString(sum[:])[:32], nil
}

func validTarget(kind targetKind, target string) bool {
	switch kind {
	case clientTarget, accountTarget:
		return id.ValidateULID(target) == nil
	case subjectTarget:
		client, subject, ok := strings.Cut(target, "/")
		return ok && id.ValidateULID(client) == nil && webapi.ValidSubject(subject)
	}
	return false
}

const stepUpHeader = "x-step-up-token"

var errStepUp = apiErr(http.StatusForbidden, "step_up_required")

// stepUp is a checked, not yet spent, step-up token. A nil *stepUp is what an
// enrolling session gets: there is nothing to spend.
type stepUp struct {
	jti string
	exp time.Time
}

// checkStepUp verifies the request's token for the action (signature, user,
// purpose, expiry) without spending it, so the handler can run its own guards
// first and a refusal does not cost the user their token. Without a token it
// is 403 before anything else. An enrolling session needs none; the caller
// decides whether it may act at all (see factorsForWrite). It is only called
// behind a session, so a caller without one cannot make the Portal fetch
// AuthGate's keys. Any failure of the token is the same 403; AuthGate's keys
// being out of reach is 502 authgate_unavailable. The token must have been
// issued for this action on this target; target is "" for an account action,
// which is bound to the session's own user.
func (a *App) checkStepUp(r *http.Request, s Session, action, target string) (*stepUp, error) {
	if s.Stage == StageEnroll {
		return nil, nil
	}
	if target == "" && stepUpTargets[action] == accountTarget {
		target = s.UserID
	}
	purpose, err := stepUpPurpose(action, target)
	if err != nil {
		// The handler names its own action and a target it has already checked.
		slog.Error("portal: bad step-up action or target", "action", action)
		return nil, apiErr(http.StatusInternalServerError, "internal")
	}
	tok := r.Header.Get(stepUpHeader)
	if tok == "" {
		return nil, errStepUp
	}
	c, err := a.Tokens.Verify(r.Context(), tok, s.UserID, purpose)
	if errors.Is(err, authgate.ErrUnavailable) {
		// The keys could not be fetched: AuthGate is down, the token may be fine.
		return nil, webapi.AuthGateError(err)
	}
	if err != nil {
		if !errors.Is(err, authgate.ErrInvalidToken) {
			slog.Warn("portal: step-up check failed", "err", err)
		}
		return nil, errStepUp
	}
	return &stepUp{jti: c.JTI, exp: c.ExpiresAt}, nil
}

// spendStepUp uses the token up, immediately before the action it authorises.
// A token already used is the same 403.
func (a *App) spendStepUp(r *http.Request, t *stepUp) error {
	if t == nil {
		return nil
	}
	if err := a.Store.UseJTI(r.Context(), t.jti, t.exp); err != nil {
		if !errors.Is(err, ErrReplayed) {
			slog.Warn("portal: step-up spend failed", "err", err)
		}
		return errStepUp
	}
	return nil
}

// mountStepUp serves /api/stepup/*, for a full session only: an enrolling one
// needs no token, and a half one has not signed in.
func (a *App) mountStepUp(mux *http.ServeMux) {
	full := a.session(StageFull)
	mux.Handle("POST /api/stepup/start", a.audited("stepup-start", full(a.account(webapi.HandlerFunc(a.stepUpStart)))))
	mux.Handle("POST /api/stepup/verify", a.audited("stepup-verify", full(a.account(webapi.HandlerFunc(a.stepUpVerify)))))
}

// stepUpStart opens a challenge for the action's purpose on its target and
// remembers its id in the session. An action outside the allow-list, or a target
// that is not of the action's kind, never reaches AuthGate. An account action
// may leave the target out (it is the user's own id) but may not name another.
func (a *App) stepUpStart(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	var in struct{ Action, Factor, Target string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if _, ok := stepUpActions[in.Action]; !ok {
		return apiErr(http.StatusBadRequest, "invalid_action")
	}
	target := in.Target
	if stepUpTargets[in.Action] == accountTarget {
		if target == "" {
			target = u.ID
		}
		if target != u.ID {
			target = "" // not the user's own: refused below as an invalid target
		}
	}
	// The purpose is worked out first and its error held, so the answer order stays
	// invalid_factor, invalid_target. Only what has passed its check is recorded:
	// people paste passwords into the wrong box, and the audit is read by others.
	purpose, perr := stepUpPurpose(in.Action, target)
	setAuditDetail(r.Context(), "action", in.Action)
	setAuditTarget(r.Context(), in.Action)
	if in.Factor != "pin" && in.Factor != "webauthn" {
		return apiErr(http.StatusBadRequest, "invalid_factor")
	}
	setAuditDetail(r.Context(), "factor", in.Factor)
	if perr != nil {
		return perr
	}
	setAuditDetail(r.Context(), "target", truncate(target, maxAuditTarget))
	ch, err := a.AuthGate.CreateChallenge(r.Context(), u.ID, purpose, in.Factor)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	if err := a.Store.SetStepUpChallenge(r.Context(), x.Hash, ch.ID, in.Action, in.Factor, target); err != nil {
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
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if x.ChallengeID == "" || in.ChallengeID != x.ChallengeID {
		return errChallengeGone
	}
	for k, v := range map[string]string{"action": x.ChallengeAction, "factor": x.ChallengeFactor, "target": x.ChallengeTarget} {
		if v != "" {
			setAuditDetail(r.Context(), k, v)
		}
	}
	tok, err := a.AuthGate.VerifyChallenge(r.Context(), in.ChallengeID, in.PIN, in.Credential)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	exp, ok := authgate.UnverifiedExp(tok)
	if !ok {
		slog.Warn("portal: step-up token without a readable exp")
		return apiErr(http.StatusBadGateway, "authgate_unavailable")
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stepUpToken": tok, "expiresAt": exp})
	return nil
}
