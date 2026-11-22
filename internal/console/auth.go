package console

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/password"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

const loginPurpose = "console-login"

var (
	errInvalidCredentials = &webapi.Error{Status: http.StatusUnauthorized, Code: "invalid_credentials"}
	errChallengeGone      = &webapi.Error{Status: http.StatusGone, Code: "challenge_gone"}
	errInvalidToken       = &webapi.Error{Status: http.StatusUnauthorized, Code: "invalid_token"}
)

// authRoutes serves /api/auth/*; the caller has already rate limited it.
func (a *App) authRoutes() http.Handler {
	mux := http.NewServeMux()
	half := a.session(StageHalf)
	mux.Handle("POST /api/auth/login", a.audited("login", webapi.HandlerFunc(a.login)))
	mux.Handle("POST /api/auth/second-factor/start", a.audited("second-factor-start", half(webapi.HandlerFunc(a.secondFactorStart))))
	mux.Handle("POST /api/auth/second-factor/verify", a.audited("second-factor-verify", half(webapi.HandlerFunc(a.secondFactorVerify))))
	mux.Handle("POST /api/auth/logout", a.audited("logout", a.session(StageHalf, StageEnroll, StageFull)(webapi.HandlerFunc(a.logout))))
	mux.HandleFunc("/", notFound)
	return mux
}

// login checks the password and opens a session at the stage the admin's
// factors call for. An unknown name, a wrong password, a locked and a disabled
// account all run one password comparison and answer the same bytes. Only a
// wrong password on a live, unlocked account counts as a failure.
func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Username, Password string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	hash := password.DummyHash
	var admin Admin
	found := false
	if name, err := NormalizeUsername(in.Username); err == nil {
		admin, err = a.Store.GetAdmin(ctx, name)
		switch {
		case err == nil:
			found, hash = true, admin.PasswordHash
			setAuditActor(ctx, admin.ID, admin.Username)
		case !errors.Is(err, ErrAdminNotFound):
			return err
		}
	}
	good := a.verifyPassword(in.Password, hash)

	now := a.now()
	live := found && admin.DisabledAt == nil && (admin.LockedUntil == nil || !now.Before(*admin.LockedUntil))
	if !good || !live {
		// The reason is for the audit item only; the answer is the same four ways.
		reason := "wrong_password"
		switch {
		case !found:
			reason = "unknown_user"
		case admin.DisabledAt != nil:
			reason = "disabled"
		case !live:
			reason = "locked"
		}
		setAuditDetail(ctx, "reason", reason)
		if live { // a wrong password on a live account
			if err := a.Store.RecordLoginFailure(ctx, admin.Username, now); err != nil && !errors.Is(err, ErrAdminNotFound) {
				return err
			}
		}
		return errInvalidCredentials
	}
	if admin.FailedCount > 0 || admin.LockedUntil != nil {
		if err := a.Store.ResetLoginFailures(ctx, admin.Username); err != nil {
			return err
		}
	}
	f, err := a.AuthGate.Factors(ctx, admin.ID)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	if !f.PIN && len(f.WebAuthn) == 0 {
		if err := a.startSession(ctx, w, admin, StageEnroll); err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"next": "enroll"})
		return nil
	}
	if err := a.startSession(ctx, w, admin, StageHalf); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"next": "second-factor", "factors": map[string]bool{"pin": f.PIN, "webauthn": len(f.WebAuthn) > 0},
	})
	return nil
}

// secondFactorStart opens a login challenge and remembers its id in the session.
func (a *App) secondFactorStart(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	var in struct{ Factor string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if in.Factor != "pin" && in.Factor != "webauthn" {
		return &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_factor"}
	}
	ch, err := a.AuthGate.CreateChallenge(r.Context(), admin.ID, loginPurpose, in.Factor)
	if err != nil {
		return webapi.AuthGateError(err)
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

// secondFactorVerify answers the session's own challenge. A challenge id that is
// not the one stored in this session never reaches AuthGate, so one admin cannot
// spend another's attempts.
func (a *App) secondFactorVerify(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	x, admin := sessionFrom(ctx)
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
	tok, err := a.AuthGate.VerifyChallenge(ctx, in.ChallengeID, in.PIN, in.Credential)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	claims, err := a.Tokens.Verify(ctx, tok, admin.ID, loginPurpose)
	switch {
	case errors.Is(err, authgate.ErrInvalidToken):
		slog.Warn("console: login token refused")
		return errInvalidToken
	case err != nil:
		return webapi.AuthGateError(err)
	}
	if err := a.Store.UseJTI(ctx, claims.JTI, claims.ExpiresAt); err != nil {
		if errors.Is(err, ErrReplayed) {
			slog.Warn("console: login token replayed")
			return errInvalidToken
		}
		return err
	}
	next := StageFull
	if admin.MustChangePassword {
		next = StageEnroll
	}
	x.FactorProved = true // this session just passed a factor
	if err := a.promote(ctx, w, x, next); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": next})
	return nil
}

// logout deletes the session item and clears the cookie.
func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	x, _ := sessionFrom(r.Context())
	if err := a.Store.DeleteSession(r.Context(), x.Hash); err != nil {
		return err
	}
	a.setCookie(w, "", -1)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// sessionGone maps a vanished session (signed out while the request ran) to 401.
func sessionGone(err error) error {
	if errors.Is(err, ErrSessionNotFound) {
		return errUnauthorized
	}
	return err
}
