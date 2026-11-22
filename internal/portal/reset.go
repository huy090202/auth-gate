package portal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/password"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
)

const (
	resetTokenLife       = time.Hour
	factorResetTokenLife = time.Hour
	// factorResetWait is how long a confirmed factor reset waits before it takes effect.
	factorResetWait = 24 * time.Hour
	untilLayout     = "2006-01-02 15:04 UTC"
)

var (
	// forgotIPLimit and factorResetIPLimit: 10 per hour, as resend.
	forgotIPLimit      = ratelimit.Settings{Burst: 10, PerSecond: 1.0 / 360}
	factorResetIPLimit = ratelimit.Settings{Burst: 10, PerSecond: 1.0 / 360}
)

// SetPassword stores a new password hash and moves sessionEpoch up by one in a
// single write, so no session opened before the change survives it.
func (st *Store) SetPassword(ctx context.Context, userID, hash string) error {
	err := st.update(ctx, userID, "SET passwordHash = :h ADD sessionEpoch :one", exists, nil,
		item{":h": s(hash), ":one": n(1)})
	return st.mapNotFound(err, "set password")
}

// PutFactorReset records a confirmed reset due at dueAt. A reset already pending
// is kept as it is and returned, so confirming twice never restarts the wait.
func (st *Store) PutFactorReset(ctx context.Context, userID string, requestedAt, dueAt time.Time) (fr FactorReset, created bool, err error) {
	it := factorResetKey(userID)
	it["requestedAt"], it["dueAt"] = s(ts(requestedAt)), s(ts(dueAt))
	_, err = st.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.Table), Item: it, ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if dynamo.IsConditionFailed(err) {
		cur, gerr := st.GetFactorReset(ctx, userID)
		if gerr != nil {
			return FactorReset{}, false, gerr
		}
		if cur == nil { // cancelled between the two calls
			return FactorReset{}, false, fmt.Errorf("put factor reset: %w", ErrConflict)
		}
		return *cur, false, nil
	}
	if err != nil {
		return FactorReset{}, false, fmt.Errorf("put factor reset: %w", err)
	}
	return FactorReset{RequestedAt: requestedAt, DueAt: dueAt}, true, nil
}

// mailable is true for the accounts that may be sent a recovery link: verified
// and not suspended. Anything else silently gets nothing.
func mailable(u User) bool { return u.EmailVerifiedAt != nil && u.SuspendedAt == nil }

// sendRecoveryLink stores a token of the purpose for u and mails its link.
func (a *App) sendRecoveryLink(ctx context.Context, u User, purpose, kind, path string, life time.Duration) error {
	token, hash, err := a.newToken()
	if err != nil {
		return err
	}
	if err := a.Store.PutToken(ctx, Token{Hash: hash, Purpose: purpose, UserID: u.ID, ExpiresAt: a.now().Add(life)}); err != nil {
		return err
	}
	a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: kind,
		Vars: map[string]string{"link": a.Config.PublicBaseURL + path + "#token=" + token}})
	return nil
}

// recoveryRequest is the shared body of forgot and factor-reset/request: same
// check order as sign-up, and an answer of 202 whatever the lookup finds.
func (a *App) recoveryRequest(ipScope string, ipLimit ratelimit.Settings, purpose, kind, path string, life time.Duration) func(http.ResponseWriter, *http.Request) error {
	return func(w http.ResponseWriter, r *http.Request) error {
		var in struct{ Email, TurnstileToken string }
		if err := webapi.DecodeBody(r, &in); err != nil {
			return err
		}
		email, ok := parseEmail(in.Email)
		if !ok {
			return apiErr(http.StatusBadRequest, "invalid_email")
		}
		if err := a.spendIP(r, ipScope, ipLimit); err != nil {
			return err
		}
		if err := a.turnstileOK(r, in.TurnstileToken); err != nil {
			return err
		}
		if err := a.limitEmail(r.Context(), email); err != nil {
			return err
		}
		switch u, err := a.Store.GetUserByEmail(r.Context(), email); {
		case err == nil && mailable(u):
			if err := a.sendRecoveryLink(r.Context(), u, purpose, kind, path, life); err != nil {
				slog.Error("portal: recovery email not issued", "kind", kind, "err", err)
			}
		case err == nil, errors.Is(err, ErrUserNotFound):
		default:
			slog.Error("portal: recovery lookup failed", "kind", kind, "err", err)
		}
		httpx.WriteJSON(w, http.StatusAccepted, checkEmail())
		return nil
	}
}

// recoveryUser is the account a spent recovery token belongs to. One that is
// gone, unverified or suspended is an invalid token like any other.
func (a *App) recoveryUser(ctx context.Context, purpose, token string) (User, error) {
	invalid := apiErr(http.StatusBadRequest, "invalid_token")
	t, err := a.Store.UseToken(ctx, purpose, hashToken(token), a.now())
	if errors.Is(err, ErrTokenInvalid) {
		return User{}, invalid
	}
	if err != nil {
		return User{}, err
	}
	u, err := a.Store.GetUser(ctx, t.UserID)
	if errors.Is(err, ErrUserNotFound) || (err == nil && !mailable(u)) {
		return User{}, invalid
	}
	if err != nil {
		return User{}, err
	}
	return u, nil
}

// resetPassword spends the link, sets the new password and ends every session.
// The factors stay: the next sign-in still asks for one. The new password is
// checked before the link is spent, so a weak one can be retyped.
func (a *App) resetPassword(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Token, NewPassword string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if in.Token == "" || len(in.Token) > 128 {
		return apiErr(http.StatusBadRequest, "invalid_token")
	}
	if password.Validate(in.NewPassword) != nil {
		return apiErr(http.StatusBadRequest, "invalid_password")
	}
	switch breached, err := a.Passwords.Breached(r.Context(), in.NewPassword); {
	case err != nil:
		slog.Warn("portal: breached-password check failed; letting the password through", "err", err)
	case breached:
		return apiErr(http.StatusBadRequest, "password_breached")
	}
	ctx := r.Context()
	u, err := a.recoveryUser(ctx, PurposeReset, in.Token)
	if err != nil {
		return err
	}
	setAuditActor(ctx, u.ID)
	setAuditTenant(ctx, u.PersonalTenantID)
	hash, err := password.Hash(in.NewPassword)
	if err != nil {
		return err
	}
	if err := a.Store.SetPassword(ctx, u.ID, hash); err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return apiErr(http.StatusBadRequest, "invalid_token")
		}
		return err
	}
	if err := a.Store.ResetLoginFailures(ctx, u.ID); err != nil {
		return err
	}
	a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: "password-changed"})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// confirmFactorReset spends the link and starts the 24-hour wait, which the
// owner is told about. A reset already waiting keeps its earlier due time.
func (a *App) confirmFactorReset(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Token string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if in.Token == "" || len(in.Token) > 128 {
		return apiErr(http.StatusBadRequest, "invalid_token")
	}
	ctx := r.Context()
	u, err := a.recoveryUser(ctx, PurposeFactorReset, in.Token)
	if err != nil {
		return err
	}
	setAuditActor(ctx, u.ID)
	setAuditTenant(ctx, u.PersonalTenantID)
	now := a.now()
	fr, created, err := a.Store.PutFactorReset(ctx, u.ID, now, now.Add(factorResetWait))
	if err != nil {
		return err
	}
	if created {
		a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: "factor-reset-started",
			Vars: map[string]string{"until": fr.DueAt.UTC().Format(untilLayout)}})
	} else {
		setAuditDetail(ctx, "factorReset", "already_pending")
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"dueAt": ts(fr.DueAt)})
	return nil
}

func (a *App) mountReset(mux *http.ServeMux) {
	forgot := a.recoveryRequest("forgot-ip", forgotIPLimit, PurposeReset, "reset", "/reset-password", resetTokenLife)
	frReq := a.recoveryRequest("factor-reset-ip", factorResetIPLimit, PurposeFactorReset, "factor-reset-confirm", "/factor-reset", factorResetTokenLife)
	mux.Handle("POST /api/auth/forgot", a.audited("forgot", webapi.HandlerFunc(a.evenly(answerFloor, forgot))))
	mux.Handle("POST /api/auth/reset-password", a.audited("password-reset", webapi.HandlerFunc(a.resetPassword)))
	mux.Handle("POST /api/auth/factor-reset/request", a.audited("factor-reset-request", webapi.HandlerFunc(a.evenly(answerFloor, frReq))))
	mux.Handle("POST /api/auth/factor-reset/confirm", a.audited("factor-reset-started", webapi.HandlerFunc(a.confirmFactorReset)))
}
