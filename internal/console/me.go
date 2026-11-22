package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/password"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// firstFactorLeaseFor is how long a first-factor lease lasts, counted from the
// claim. It holds only while the write lands before it runs out. A write lands at
// most authgate.Timeout after it is sent (AuthGate's own function timeout,
// Timeout: 10 in infra/template.yaml; the Console's client timeout does not bound
// when a write lands), so the write is sent only within firstFactorSendWindow of
// the claim. The Console's Lambda timeout bounds nothing here: a write sent just
// before it can still land later.
const firstFactorLeaseFor = 25 * time.Second

// firstFactorSendWindow is how long after the claim the first-factor write may
// still be sent: the lease less the time a sent write can take to land, less
// firstFactorSendMargin (the network before AuthGate's clock starts, and clock
// skew between Console instances).
const firstFactorSendWindow = firstFactorLeaseFor - authgate.Timeout - firstFactorSendMargin

const firstFactorSendMargin = 2 * time.Second

// errSendWindowClosed is a first-factor write that was not sent because its send
// window had closed. Nothing went out, so nothing can land and the lease goes.
var errSendWindowClosed = fmt.Errorf("%w: first-factor write not sent, past its send window", authgate.ErrUnavailable)

// firstFactorLease is what beginFirstFactor hands the write. release must be
// called once the write is over, with the write's error; send runs the write.
type firstFactorLease struct {
	release func(writeErr error)
	sendBy  time.Time // the end of the send window; zero when no lease was taken
	now     func() time.Time
}

// send runs the write under a deadline at the end of the send window, so a write
// that cannot go out in time is not sent. One already past it is not tried at all
// and answers errSendWindowClosed; one cut off by the deadline after it may have
// gone out is a no-answer failure, and its lease is kept.
func (l firstFactorLease) send(ctx context.Context, write func(context.Context) error) error {
	if l.sendBy.IsZero() {
		return write(ctx)
	}
	left := l.sendBy.Sub(l.now())
	if left <= 0 {
		return errSendWindowClosed
	}
	ctx, cancel := context.WithTimeout(ctx, left)
	defer cancel()
	return write(ctx)
}

// meRoutes serves /api/me/*. Every write checks the step-up token (a full
// session needs one; an enrolling one passes factorsForWrite instead), runs its own
// guards, and only then spends the token.
func (a *App) meRoutes(mux *http.ServeMux) {
	anyStage := a.session(StageHalf, StageEnroll, StageFull)
	self := a.session(StageEnroll, StageFull)
	mux.Handle("GET /api/me", anyStage(webapi.HandlerFunc(a.me)))
	// The password route is the one write with no step-up in stage enroll, so it
	// shares the login limiter: it is a way to test guesses of the current password.
	mux.Handle("POST /api/me/password", a.limitAuth(a.audited("change-password", self(webapi.HandlerFunc(a.changePassword)))))
	mux.Handle("POST /api/me/pin", a.audited("set-pin", self(webapi.HandlerFunc(a.setPIN))))
	mux.Handle("DELETE /api/me/pin", a.audited("delete-pin", self(webapi.HandlerFunc(a.deletePIN))))
	mux.Handle("POST /api/me/passkeys/start", a.audited("passkey-start", self(webapi.HandlerFunc(a.passkeyStart))))
	mux.Handle("POST /api/me/passkeys/finish", a.audited("passkey-finish", self(webapi.HandlerFunc(a.passkeyFinish))))
	mux.Handle("DELETE /api/me/passkeys/{id}", a.audited("delete-passkey", self(webapi.HandlerFunc(a.deletePasskey))))
}

// me describes the signed-in admin. A session that has not proved a factor yet (half,
// or enrolling without one) has only proved the password, so it sees which kinds of factor exist, not the passkeys themselves.
func (a *App) me(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	out := map[string]any{
		"username": admin.Username, "role": admin.Role, "stage": x.Stage, "mustChangePassword": admin.MustChangePassword,
	}
	f, err := a.AuthGate.Factors(r.Context(), admin.ID)
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
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// factorsForWrite reads the account's factors for a write and applies the rule
// for an enrolling session. One that has not proved a factor itself holds nothing
// but the temporary password: it may act only while the account has no factor
// at all (its first factor, or the password); with a factor present it may be
// the victim's stale session or an attacker's, so it is ended with 401. A full
// session has proved its factor at sign-in.
func (a *App) factorsForWrite(w http.ResponseWriter, r *http.Request, x Session) (authgate.Factors, error) {
	f, err := a.AuthGate.Factors(r.Context(), x.AdminID)
	if err != nil {
		return f, webapi.AuthGateError(err)
	}
	if x.Stage == StageEnroll && !x.FactorProved && (f.PIN || len(f.WebAuthn) > 0) {
		return f, a.endSession(w, r, x)
	}
	return f, nil
}

// factorAdded runs after a factor was added. An enrolling session that added the
// account's first factor has thereby proved it, and is recorded as such.
func (a *App) factorAdded(r *http.Request, x *Session, had authgate.Factors) error {
	if x.Stage != StageEnroll || x.FactorProved || had.PIN || len(had.WebAuthn) > 0 {
		return nil
	}
	if err := a.Store.SetFactorProved(r.Context(), x.Hash); err != nil {
		return sessionGone(err)
	}
	x.FactorProved = true
	return nil
}

// beginFirstFactor reads the account's factors for a factor write. A session that
// enrolls without having proved a factor first takes the admin's first-factor
// lease, and only then reads the factors, so after a lease expires the next
// claimer sees whatever the earlier holder wrote. Of such sessions one holds the
// lease at a time, whatever epoch it opened at; another gets 409
// enrollment_in_progress without an AuthGate call and keeps its session. The
// write goes through the returned lease's send, and its release must be called
// once the write is over, with the write's error: a write that got no answer, or
// an answered 5xx, may still land, so its lease is kept until it expires.
func (a *App) beginFirstFactor(w http.ResponseWriter, r *http.Request, x Session) (authgate.Factors, firstFactorLease, error) {
	if x.Stage != StageEnroll || x.FactorProved {
		f, err := a.factorsForWrite(w, r, x)
		return f, firstFactorLease{release: func(error) {}}, err
	}
	holder := id.New() // not the session token, which must stay secret
	now := a.now()
	held, err := a.Store.ClaimFirstFactorLease(r.Context(), x.Username, holder, now, now.Add(firstFactorLeaseFor))
	switch {
	case errors.Is(err, ErrLeaseHeld):
		wait := int(math.Ceil(held.Sub(now).Seconds()))
		return authgate.Factors{}, firstFactorLease{}, &webapi.Error{Status: http.StatusConflict, Code: "enrollment_in_progress", RetryAfter: max(wait, 1)}
	case errors.Is(err, ErrAdminNotFound):
		return authgate.Factors{}, firstFactorLease{}, errUnauthorized
	case err != nil:
		return authgate.Factors{}, firstFactorLease{}, err
	}
	release := func(writeErr error) {
		if errors.Is(writeErr, authgate.ErrNoAnswer) || errors.Is(writeErr, authgate.ErrServerError) {
			return // the write may still land: let the lease run out
		}
		// The request may be cancelled; the lease should still go.
		if err := a.Store.ReleaseFirstFactorLease(context.WithoutCancel(r.Context()), x.Username, holder); err != nil {
			slog.Warn("console: release first-factor lease", "err", err)
		}
	}
	f, err := a.factorsForWrite(w, r, x)
	if err != nil {
		release(nil)
		return f, firstFactorLease{}, err
	}
	return f, firstFactorLease{release: release, sendBy: now.Add(firstFactorSendWindow), now: a.now}, nil
}

// claimFirstFactor runs, under the lease, just before an enrolling session that
// has not proved a factor writes one at AuthGate. It bumps the admin's
// sessionEpoch on the epoch this session holds and reissues the session at the
// next one, which ends the sessions opened at the old epoch. The lease keeps out
// the ones opened at the new epoch.
func (a *App) claimFirstFactor(w http.ResponseWriter, r *http.Request, x *Session) error {
	if x.Stage != StageEnroll || x.FactorProved {
		return nil
	}
	if err := a.Store.BumpEpoch(r.Context(), x.Username, x.Epoch); err != nil {
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

// settle moves an enrolling session to full once it has proved a factor and
// the password is no longer temporary, and returns the stage it is in.
func (a *App) settle(w http.ResponseWriter, r *http.Request, x Session, mustChange bool) (Stage, error) {
	if x.Stage != StageEnroll || !x.FactorProved || mustChange {
		return x.Stage, nil
	}
	if err := a.promote(r.Context(), w, x, StageFull); err != nil {
		return "", err
	}
	return StageFull, nil
}

func (a *App) changePassword(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "change-password", "")
	if err != nil {
		return err
	}
	var in struct{ CurrentPassword, NewPassword string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if !a.verifyPassword(in.CurrentPassword, admin.PasswordHash) {
		// Without a step-up token to stand in the way, guesses count like sign-in failures.
		if x.Stage == StageEnroll {
			if err := a.Store.RecordLoginFailure(r.Context(), admin.Username, a.now()); err != nil {
				return err
			}
			cur, err := a.Store.GetAdmin(r.Context(), admin.Username)
			if err != nil {
				return err
			}
			if cur.LockedUntil != nil && a.now().Before(*cur.LockedUntil) {
				return a.endSession(w, r, x)
			}
		}
		return &webapi.Error{Status: http.StatusBadRequest, Code: "wrong_password"}
	}
	// The current password has just been verified, so equal strings mean "same as current".
	if in.NewPassword == in.CurrentPassword || password.Validate(in.NewPassword) != nil {
		return &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_password"}
	}
	hash, err := password.Hash(in.NewPassword)
	if err != nil {
		return err
	}
	// Read the factors as late as possible: the compare and the hash above are slow.
	if _, err := a.factorsForWrite(w, r, x); err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	// The epoch moves, which ends every other session of the admin; this one is
	// reissued at the new epoch (and promoted if it has what full needs).
	if err := a.Store.ChangePassword(r.Context(), admin.Username, hash, admin.SessionEpoch); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrAdminNotFound) {
			return errUnauthorized
		}
		return err
	}
	next := x.Stage
	if x.Stage == StageEnroll && x.FactorProved {
		next = StageFull
	}
	if _, err := a.reissue(r.Context(), w, x, next, admin.SessionEpoch+1); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": next})
	return nil
}

func (a *App) setPIN(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
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
	defer func() { lease.release(writeErr) }()
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.claimFirstFactor(w, r, &x); err != nil {
		return err
	}
	if err := lease.send(r.Context(), func(ctx context.Context) error {
		return a.AuthGate.SetPIN(ctx, admin.ID, in.PIN)
	}); err != nil {
		writeErr = err
		return webapi.AuthGateError(err)
	}
	if err := a.factorAdded(r, &x, had); err != nil {
		return err
	}
	st, err := a.settle(w, r, x, admin.MustChangePassword)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": st})
	return nil
}

func (a *App) deletePIN(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "remove-factor", "")
	if err != nil {
		return err
	}
	f, err := a.factorsForWrite(w, r, x)
	if err != nil {
		return err
	}
	if !f.PIN {
		return &webapi.Error{Status: http.StatusConflict, Code: "factor_not_enrolled"}
	}
	if len(f.WebAuthn) == 0 {
		return &webapi.Error{Status: http.StatusConflict, Code: "last_factor"}
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.AuthGate.DeletePIN(r.Context(), admin.ID); err != nil {
		return webapi.AuthGateError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": x.Stage})
	return nil
}

// passkeyStart is where a full session is asked for its step-up token; the finish
// is tied to this start by the registration id kept in the session.
func (a *App) passkeyStart(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
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
	id, publicKey, err := a.AuthGate.StartRegistration(r.Context(), admin.ID, admin.Username)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	if err := a.Store.SetRegistration(r.Context(), x.Hash, id); err != nil {
		return sessionGone(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"registrationId": id, "publicKey": publicKey})
	return nil
}

func (a *App) passkeyFinish(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
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
	defer func() { lease.release(writeErr) }()
	if err := a.claimFirstFactor(w, r, &x); err != nil {
		return err
	}
	var credID string
	err = lease.send(r.Context(), func(ctx context.Context) (err error) {
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
	st, err := a.settle(w, r, x, admin.MustChangePassword)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"credentialId": credID, "stage": st})
	return nil
}

func (a *App) deletePasskey(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "remove-factor", "")
	if err != nil {
		return err
	}
	f, err := a.factorsForWrite(w, r, x)
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	found := false
	for _, c := range f.WebAuthn {
		found = found || c.CredentialID == id
	}
	if !found {
		return &webapi.Error{Status: http.StatusConflict, Code: "factor_not_enrolled"}
	}
	if !f.PIN && len(f.WebAuthn) == 1 {
		return &webapi.Error{Status: http.StatusConflict, Code: "last_factor"}
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.AuthGate.DeleteCredential(r.Context(), admin.ID, id); err != nil {
		return webapi.AuthGateError(err)
	}
	setAuditTarget(r.Context(), id) // not a validated shape: recorded once AuthGate accepted the delete
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": x.Stage})
	return nil
}
