package portal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/firstfactor"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
)

// mountMe registers /api/me/*. Every write checks the step-up token (a full
// session needs one; an enrolling one passes factorsForWrite instead), runs its
// own guards, and only then spends the token.
func (a *App) mountMe(mux *http.ServeMux) {
	anyStage := a.session(StageHalf, StageEnroll, StageFull)
	self := a.session(StageEnroll, StageFull)
	full := a.session(StageFull)
	h := func(name string, stage func(http.Handler) http.Handler, f func(http.ResponseWriter, *http.Request) error) http.Handler {
		return a.audited(name, stage(a.account(webapi.HandlerFunc(f))))
	}
	mux.Handle("GET /api/me", anyStage(webapi.HandlerFunc(a.me)))
	mux.Handle("POST /api/me/pin", h("set-pin", self, a.setPIN))
	mux.Handle("DELETE /api/me/pin", h("delete-pin", self, a.deletePIN))
	mux.Handle("POST /api/me/passkeys/start", h("passkey-start", self, a.passkeyStart))
	mux.Handle("POST /api/me/passkeys/finish", h("passkey-finish", self, a.passkeyFinish))
	mux.Handle("DELETE /api/me/passkeys/{id}", h("delete-passkey", self, a.deletePasskey))
	mux.Handle("POST /api/me/recovery-codes", h("regenerate-recovery-codes", full, a.regenerateRecoveryCodes))
}

// me describes the signed-in user. A session that has not proved a factor yet
// (half, or enrolling without one) has only proved the password, so it sees
// which kinds of factor exist, not the passkeys themselves.
func (a *App) me(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	out := map[string]any{
		"userId": u.ID, "email": u.Email, "displayName": u.DisplayName, "lang": u.Lang, "stage": x.Stage,
		"personalTenantId": u.PersonalTenantID,
	}
	if u.AvatarKey != "" {
		out["avatarUrl"] = "/" + u.AvatarKey
	}
	f, err := a.AuthGate.Factors(r.Context(), u.ID)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	if x.Stage == StageHalf || (x.Stage == StageEnroll && !x.FactorProved) {
		out["factors"] = map[string]bool{"pin": f.PIN, "webauthn": len(f.WebAuthn) > 0}
	} else {
		if f.WebAuthn == nil {
			f.WebAuthn = []authgate.Credential{}
		}
		out["factors"] = f
	}
	left, err := a.Store.CountRecoveryCodes(r.Context(), u.ID)
	if err != nil {
		return err
	}
	out["recoveryCodesLeft"] = left
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// factorsForWrite reads the account's factors for a write and applies the rule
// for an enrolling session. One that has not proved a factor itself holds
// nothing but the password: it may act only while the account has no factor at
// all (its first factor); with a factor present it may be the victim's stale
// session or an attacker's, so it is ended with 401. A full session has proved
// its factor at sign-in; a recovery session has proved a code.
func (a *App) factorsForWrite(w http.ResponseWriter, r *http.Request, x Session) (authgate.Factors, error) {
	f, err := a.AuthGate.Factors(r.Context(), x.UserID)
	if err != nil {
		return f, webapi.AuthGateError(err)
	}
	if x.Stage == StageEnroll && !x.FactorProved && (f.PIN || len(f.WebAuthn) > 0) {
		return f, a.endSession(w, r, x)
	}
	return f, nil
}

func noFactor(f authgate.Factors) bool { return !f.PIN && len(f.WebAuthn) == 0 }

// factorAdded runs after a factor was added. An enrolling session that added the
// account's first factor has thereby proved it, and is recorded as such.
func (a *App) factorAdded(r *http.Request, x *Session, had authgate.Factors) error {
	if x.Stage != StageEnroll || x.FactorProved || !noFactor(had) {
		return nil
	}
	if err := a.Store.SetFactorProved(r.Context(), x.Hash); err != nil {
		return sessionGone(err)
	}
	x.FactorProved = true
	return nil
}

// beginFirstFactor reads the account's factors for a factor write. A session that
// enrolls without having proved a factor first takes the user's first-factor
// lease, and only then reads the factors, so after a lease expires the next
// claimer sees whatever the earlier holder wrote. Of such sessions one holds the
// lease at a time, whatever epoch it opened at; another gets 409
// enrollment_in_progress without an AuthGate call and keeps its session. The
// write goes through the returned lease's send, and its release must be called
// once the write is over, with the write's error: a write that got no answer, or
// an answered 5xx, may still land, so its lease is kept until it expires.
func (a *App) beginFirstFactor(w http.ResponseWriter, r *http.Request, x Session) (authgate.Factors, firstfactor.Lease, error) {
	if x.Stage != StageEnroll || x.FactorProved {
		f, err := a.factorsForWrite(w, r, x)
		return f, firstfactor.None(), err
	}
	lease, err := firstfactor.Acquire(r.Context(), firstfactor.Account{
		Claim: func(ctx context.Context, holder string, now, until time.Time) (time.Time, error) {
			return a.Store.ClaimFirstFactorLease(ctx, x.UserID, holder, now, until)
		},
		Release: func(ctx context.Context, holder string) error {
			return a.Store.ReleaseFirstFactorLease(ctx, x.UserID, holder)
		},
	}, a.now)
	switch {
	case errors.Is(err, ErrUserNotFound):
		return authgate.Factors{}, firstfactor.Lease{}, errUnauthorized
	case err != nil:
		return authgate.Factors{}, firstfactor.Lease{}, err
	}
	f, err := a.factorsForWrite(w, r, x)
	if err != nil {
		lease.Release(nil)
		return f, firstfactor.Lease{}, err
	}
	return f, lease, nil
}

// claimFirstFactor runs, under the lease, just before an enrolling session that
// has not proved a factor writes one at AuthGate. It bumps the user's
// sessionEpoch on the epoch this session holds and reissues the session at the
// next one, which ends the sessions opened at the old epoch. The lease keeps out
// the ones opened at the new epoch.
func (a *App) claimFirstFactor(w http.ResponseWriter, r *http.Request, x *Session) error {
	if x.Stage != StageEnroll || x.FactorProved {
		return nil
	}
	if err := a.Store.BumpEpoch(r.Context(), x.UserID, x.Epoch); err != nil {
		if errors.Is(err, ErrConflict) {
			return a.endSession(w, r, *x)
		}
		return err
	}
	nx, err := a.reissue(r.Context(), w, *x, StageEnroll, x.Epoch+1)
	if err != nil {
		return err
	}
	*x = nx
	return nil
}

// settle moves an enrolling session to full once it has proved a factor, and
// returns the stage it is in.
func (a *App) settle(w http.ResponseWriter, r *http.Request, x Session) (Stage, error) {
	if x.Stage != StageEnroll || !x.FactorProved {
		return x.Stage, nil
	}
	if err := a.promote(r.Context(), w, x, StageFull); err != nil {
		return "", err
	}
	return StageFull, nil
}

// firstFactorCodes issues the recovery codes once the account's first factor is
// in place, so a failed factor write never leaves codes nobody was shown. If the
// store write fails the factor stays, the answer carries no codes and
// recoveryCodesLeft reads 0, so the person is prompted to regenerate them.
func (a *App) firstFactorCodes(ctx context.Context, u User, had authgate.Factors) []string {
	if !noFactor(had) {
		return nil
	}
	codes, err := a.issueRecoveryCodes(ctx, u.ID)
	if err != nil {
		slog.Error("portal: first-factor recovery codes not stored", "err", err)
		return nil
	}
	return codes
}

// factorAddedMail tells the owner a factor was added, unless it was the first one,
// which the person is enrolling right now.
func (a *App) factorAddedMail(r *http.Request, u User, had authgate.Factors) {
	if !noFactor(had) {
		a.mailBestEffort(r.Context(), pmail.Message{To: u.Email, Lang: u.Lang, Kind: "factor-added"})
	}
}

func (a *App) setPIN(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "add-factor", "")
	if err != nil {
		return err
	}
	var in struct{ PIN string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	had, lease, err := a.beginFirstFactor(w, r, x)
	if err != nil {
		return err
	}
	var writeErr error
	defer func() { lease.Release(writeErr) }()
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.claimFirstFactor(w, r, &x); err != nil {
		return err
	}
	if err := lease.Send(r.Context(), func(ctx context.Context) error {
		return a.AuthGate.SetPIN(ctx, u.ID, in.PIN)
	}); err != nil {
		writeErr = err
		return webapi.AuthGateError(err)
	}
	if err := a.factorAdded(r, &x, had); err != nil {
		return err
	}
	codes := a.firstFactorCodes(r.Context(), u, had)
	a.factorAddedMail(r, u, had)
	st, err := a.settle(w, r, x)
	if err != nil {
		return err
	}
	out := map[string]any{"stage": st}
	if codes != nil {
		out["recoveryCodes"] = codes
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (a *App) deletePIN(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "remove-factor", "")
	if err != nil {
		return err
	}
	f, err := a.factorsForWrite(w, r, x)
	if err != nil {
		return err
	}
	if !f.PIN {
		return apiErr(http.StatusConflict, "factor_not_enrolled")
	}
	if len(f.WebAuthn) == 0 {
		return apiErr(http.StatusConflict, "last_factor")
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.AuthGate.DeletePIN(r.Context(), u.ID); err != nil {
		return webapi.AuthGateError(err)
	}
	a.mailBestEffort(r.Context(), pmail.Message{To: u.Email, Lang: u.Lang, Kind: "factor-removed"})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": x.Stage})
	return nil
}

// passkeyStart is where a full session is asked for its step-up token; the finish
// is tied to this start by the registration id kept in the session.
func (a *App) passkeyStart(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "add-factor", "")
	if err != nil {
		return err
	}
	if _, err := a.factorsForWrite(w, r, x); err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	regID, publicKey, err := a.AuthGate.StartRegistration(r.Context(), u.ID, u.Email)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	if err := a.Store.SetRegistration(r.Context(), x.Hash, regID); err != nil {
		return sessionGone(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"registrationId": regID, "publicKey": publicKey})
	return nil
}

func (a *App) passkeyFinish(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	var in struct {
		RegistrationID string          `json:"registrationId"`
		Credential     json.RawMessage `json:"credential"`
	}
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if x.RegistrationID == "" || in.RegistrationID != x.RegistrationID {
		return errChallengeGone
	}
	had, lease, err := a.beginFirstFactor(w, r, x)
	if err != nil {
		return err
	}
	var writeErr error
	defer func() { lease.Release(writeErr) }()
	if err := a.claimFirstFactor(w, r, &x); err != nil {
		return err
	}
	var credID string
	err = lease.Send(r.Context(), func(ctx context.Context) (err error) {
		credID, err = a.AuthGate.FinishRegistration(ctx, in.RegistrationID, in.Credential)
		return err
	})
	if err != nil {
		writeErr = err
		return webapi.AuthGateError(err)
	}
	if err := a.factorAdded(r, &x, had); err != nil {
		return err
	}
	codes := a.firstFactorCodes(r.Context(), u, had)
	a.factorAddedMail(r, u, had)
	st, err := a.settle(w, r, x)
	if err != nil {
		return err
	}
	out := map[string]any{"credentialId": credID, "stage": st}
	if codes != nil {
		out["recoveryCodes"] = codes
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (a *App) deletePasskey(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "remove-factor", "")
	if err != nil {
		return err
	}
	f, err := a.factorsForWrite(w, r, x)
	if err != nil {
		return err
	}
	credID := r.PathValue("id")
	found := false
	for _, c := range f.WebAuthn {
		found = found || c.CredentialID == credID
	}
	if !found {
		return apiErr(http.StatusConflict, "factor_not_enrolled")
	}
	if !f.PIN && len(f.WebAuthn) == 1 {
		return apiErr(http.StatusConflict, "last_factor")
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.AuthGate.DeleteCredential(r.Context(), u.ID, credID); err != nil {
		return webapi.AuthGateError(err)
	}
	setAuditTarget(r.Context(), credID) // not a validated shape: recorded once AuthGate accepted the delete
	a.mailBestEffort(r.Context(), pmail.Message{To: u.Email, Lang: u.Lang, Kind: "factor-removed"})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": x.Stage})
	return nil
}
