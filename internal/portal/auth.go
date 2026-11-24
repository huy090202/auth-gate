package portal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/password"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	"github.com/huy090202/auth-gate/internal/platform/websession"
	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
)

const (
	loginPurpose = "portal-login"
	// failWindow is how long a failure counts towards Turnstile; turnstileAfter
	// is how many failures in the window make it required.
	failWindow     = 15 * time.Minute
	turnstileAfter = 2
)

// loginIPLimit is the setting of the per-IP buckets of the sign-in routes: 10
// at once, then one per 6 seconds. The password (login-ip), the second factor
// (factor-ip, start and verify) and recovery codes (recovery-ip) each have
// their own bucket.
var loginIPLimit = ratelimit.Settings{Burst: 10, PerSecond: 1.0 / 6}

var (
	errInvalidCredentials = apiErr(http.StatusUnauthorized, "invalid_credentials")
	errChallengeGone      = apiErr(http.StatusGone, "challenge_gone")
	errInvalidToken       = apiErr(http.StatusUnauthorized, "invalid_token")
	errUnauthorized       = websession.ErrUnauthorized
)

// ---- windowed failure counters ----

// A failure counter is one item FAIL#<scope>#<key>/META holding a count and a
// ttl that is the end of its window, set by the first failure. DynamoDB's TTL
// deletion is lazy, so a count past its ttl is read as zero.

func failKey(scope, key string) item {
	return item{"pk": s("FAIL#" + scope + "#" + key), "sk": s("META")}
}

// FailCount is the number of failures in the open window of the scope and key.
func (st *Store) FailCount(ctx context.Context, scope, key string, now time.Time) (int, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: failKey(scope, key), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return 0, fmt.Errorf("read failure counter: %w", err)
	}
	if out.Item == nil || expired(out.Item, now) {
		return 0, nil
	}
	return getN(out.Item, "count"), nil
}

// AddFail counts one failure. The first of a window opens it: ttl is now+window.
func (st *Store) AddFail(ctx context.Context, scope, key string, now time.Time) error {
	names := map[string]string{"#ttl": "ttl"}
	for range 3 {
		_, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(st.Table), Key: failKey(scope, key),
			UpdateExpression:          aws.String("ADD #count :one"),
			ConditionExpression:       aws.String(exists + " AND #ttl > :now"),
			ExpressionAttributeNames:  map[string]string{"#count": "count", "#ttl": "ttl"},
			ExpressionAttributeValues: item{":one": n(1), ":now": n(now.Unix())},
		})
		if err == nil {
			return nil
		}
		if !dynamo.IsConditionFailed(err) {
			return fmt.Errorf("add failure: %w", err)
		}
		// No open window: start one, unless another request just did.
		it := failKey(scope, key)
		it["count"], it["ttl"] = n(1), n(now.Add(failWindow).Unix())
		_, err = st.DB.PutItem(ctx, &dynamodb.PutItemInput{
			TableName: aws.String(st.Table), Item: it,
			ConditionExpression:       aws.String("attribute_not_exists(pk) OR #ttl <= :now"),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: item{":now": n(now.Unix())},
		})
		if err == nil {
			return nil
		}
		if !dynamo.IsConditionFailed(err) {
			return fmt.Errorf("add failure: %w", err)
		}
	}
	return fmt.Errorf("add failure: %w", ErrConflict)
}

// ClearFail drops a counter.
func (st *Store) ClearFail(ctx context.Context, scope, key string) error {
	if _, err := st.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(st.Table), Key: failKey(scope, key)}); err != nil {
		return fmt.Errorf("clear failure counter: %w", err)
	}
	return nil
}

// The scopes: the caller's address, and the account as the address typed for it
// (a hash of the normalised email, so a known and an unknown address behave alike).
const (
	failIP   = "ip"
	failAcct = "acct"
)

// turnstileRequired reads both counters before the password is looked at.
func (a *App) turnstileRequired(ctx context.Context, ip, acct string) (bool, error) {
	now := a.now()
	for _, c := range [][2]string{{failIP, ip}, {failAcct, acct}} {
		n, err := a.Store.FailCount(ctx, c[0], c[1], now)
		if err != nil {
			return false, err
		}
		if n >= turnstileAfter {
			return true, nil
		}
	}
	return false, nil
}

// countFailure adds one failure to the address's and the account's windows.
func (a *App) countFailure(ctx context.Context, ip, acct string) error {
	now := a.now()
	if err := a.Store.AddFail(ctx, failIP, ip, now); err != nil {
		return err
	}
	return a.Store.AddFail(ctx, failAcct, acct, now)
}

// failSignIn is a wrong password or code on a live account: the lockout counter
// and the windowed ones. The fifth failure locks the account and mails its owner,
// once.
func (a *App) failSignIn(ctx context.Context, u User, ip, acct string) error {
	now := a.now()
	if err := a.Store.RecordLoginFailure(ctx, u.ID, now); err != nil && !errors.Is(err, ErrUserNotFound) {
		return err
	}
	if u.FailedCount == maxFailures-1 {
		if cur, err := a.Store.GetUser(ctx, u.ID); err == nil && cur.LockedUntil != nil && now.Before(*cur.LockedUntil) {
			a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: "account-locked",
				Vars: map[string]string{"until": cur.LockedUntil.UTC().Format("2006-01-02 15:04 UTC")}})
		}
	}
	return a.countFailure(ctx, ip, acct)
}

// loginTurnstile checks the widget token a sign-in needs once failures piled up.
func (a *App) loginTurnstile(r *http.Request, token string) error {
	err := a.turnstileOK(r, token)
	if e, ok := err.(*webapi.Error); ok && e.Code == "turnstile_failed" {
		return apiErr(http.StatusForbidden, "turnstile_required")
	}
	return err
}

// ---- routes ----

// account sets the audit scope to the session user's personal workspace, where
// account events (sign-in, factors, recovery codes, sign-out) are logged.
func (a *App) account(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, u := sessionFrom(r.Context()); u.PersonalTenantID != "" {
			setAuditTenant(r.Context(), u.PersonalTenantID)
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) mountAuth(mux *http.ServeMux) {
	login, factor, recovery := a.limitIP("login-ip", loginIPLimit), a.limitIP("factor-ip", loginIPLimit), a.limitIP("recovery-ip", loginIPLimit)
	half := a.session(StageHalf)
	mux.Handle("POST /api/auth/login", login(a.audited("login", webapi.HandlerFunc(a.login))))
	mux.Handle("POST /api/auth/second-factor/start", factor(a.audited("second-factor-start", half(a.account(webapi.HandlerFunc(a.secondFactorStart))))))
	mux.Handle("POST /api/auth/second-factor/verify", factor(a.audited("second-factor-verify", half(a.account(webapi.HandlerFunc(a.secondFactorVerify))))))
	mux.Handle("POST /api/auth/recovery", recovery(a.audited("recovery", half(a.account(webapi.HandlerFunc(a.recovery))))))
	mux.Handle("POST /api/auth/logout", a.audited("logout", a.session(StageHalf, StageEnroll, StageFull)(a.account(webapi.HandlerFunc(a.logout)))))
}

// login checks the password and opens a session at the stage the user's
// factors call for. An unknown address, a wrong password, a locked and a
// suspended account all run one password comparison and answer the same bytes.
// So does an unverified account, even with the right password: it is treated
// as a wrong password on a live account (reason "unverified"), so sign-in never
// tells who has registered; the sign-in screen offers the resend instead. The
// attempt is logged in the account's workspace only once the password is right
// and the account is verified: anyone can type an address, and a stranger's
// guesses must not fill its owner's log (they go to the process log). A failure
// on a live account records the lockout; any other failure makes one equal
// write that changes nothing.
func (a *App) login(w http.ResponseWriter, r *http.Request) error {
	var in struct{ Email, Password, TurnstileToken string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	email := NormalizeEmail(in.Email)
	ip, _ := a.clientIP(r)
	acct := emailHash(email)

	need, err := a.turnstileRequired(ctx, ip, acct)
	if err != nil {
		return err
	}
	if need {
		if err := a.loginTurnstile(r, in.TurnstileToken); err != nil {
			return err
		}
	}

	hash := password.DummyHash
	var u User
	found := false
	if len(email) <= maxEmailBytes {
		switch u, err = a.Store.GetUserByEmail(ctx, email); {
		case err == nil:
			found, hash = true, u.PasswordHash
			setAuditActor(ctx, u.ID)
		case !errors.Is(err, ErrUserNotFound):
			return err
		}
	}
	good := a.verifyPassword(in.Password, hash)
	if good && found && u.EmailVerifiedAt != nil && u.PersonalTenantID != "" {
		setAuditTenant(ctx, u.PersonalTenantID)
	}

	now := a.now()
	locked := found && u.LockedUntil != nil && now.Before(*u.LockedUntil)
	live := found && u.SuspendedAt == nil && !locked
	unverified := found && u.EmailVerifiedAt == nil
	if !good || !live || unverified {
		reason := "wrong_password"
		switch {
		case !found:
			reason = "unknown_email"
		case u.SuspendedAt != nil:
			reason = "suspended"
		case locked:
			reason = "locked"
		case unverified && good:
			reason = "unverified"
		}
		setAuditDetail(ctx, "reason", reason)
		if live { // a wrong password on a live account
			if err := a.failSignIn(ctx, u, ip, acct); err != nil {
				return err
			}
		} else {
			if err := a.Store.NoLoginFailure(ctx); err != nil {
				return err
			}
			if err := a.countFailure(ctx, ip, acct); err != nil {
				return err
			}
		}
		return errInvalidCredentials
	}

	// A factor reset whose wait is over is applied now, after the right password.
	if fr, err := a.Store.GetFactorReset(ctx, u.ID); err != nil {
		return err
	} else if fr != nil && !fr.DueAt.After(now) {
		u, err = a.applyFactorReset(ctx, u)
		if err != nil {
			return err
		}
		return a.enrollAfterLogin(ctx, w, u)
	}

	f, err := a.AuthGate.Factors(ctx, u.ID)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	if !f.PIN && len(f.WebAuthn) == 0 {
		return a.enrollAfterLogin(ctx, w, u)
	}
	if err := a.startSession(ctx, w, u, StageHalf); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"next": "second-factor", "factors": map[string]bool{"pin": f.PIN, "webauthn": len(f.WebAuthn) > 0},
	})
	return nil
}

// completeSignIn clears the lockout count and the account's Turnstile window. It
// runs only when a sign-in is complete: a second factor or a recovery code was
// proved, or the account has no factor to prove. A right password alone clears
// nothing, or someone who holds the password could reset the counters that the
// wrong second-factor guesses feed.
func (a *App) completeSignIn(ctx context.Context, u User) error {
	if err := a.Store.ResetLoginFailures(ctx, u.ID); err != nil {
		return err
	}
	return a.Store.ClearFail(ctx, failAcct, emailHash(u.Email))
}

func (a *App) enrollAfterLogin(ctx context.Context, w http.ResponseWriter, u User) error {
	if err := a.completeSignIn(ctx, u); err != nil {
		return err
	}
	if err := a.startSession(ctx, w, u, StageEnroll); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"next": "enroll"})
	return nil
}

// applyFactorReset purges the user's PIN and passkeys at AuthGate, then removes
// the FACTORRESET item and the recovery codes, which no longer match a factor
// the person holds, and ends the user's other sessions. A failure part way
// leaves the item, so the next sign-in finishes the job. It returns the user as
// it is after the epoch moved.
func (a *App) applyFactorReset(ctx context.Context, u User) (User, error) {
	f, err := a.AuthGate.Factors(ctx, u.ID)
	if err != nil {
		return u, webapi.AuthGateError(err)
	}
	if f.PIN {
		if err := a.AuthGate.DeletePIN(ctx, u.ID); err != nil {
			return u, webapi.AuthGateError(err)
		}
	}
	for _, c := range f.WebAuthn {
		if err := a.AuthGate.DeleteCredential(ctx, u.ID, c.CredentialID); err != nil {
			return u, webapi.AuthGateError(err)
		}
	}
	if err := a.Store.ReplaceRecoveryCodes(ctx, u.ID, nil); err != nil {
		return u, err
	}
	if err := a.Store.DeleteFactorReset(ctx, u.ID); err != nil {
		return u, err
	}
	if err := a.Store.EndSessions(ctx, u.ID); err != nil {
		return u, err
	}
	setAuditDetail(ctx, "factorReset", "applied")
	return a.Store.GetUser(ctx, u.ID)
}

// secondFactorStart opens a login challenge and remembers its id in the session.
func (a *App) secondFactorStart(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	var in struct{ Factor string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if in.Factor != "pin" && in.Factor != "webauthn" {
		return apiErr(http.StatusBadRequest, "invalid_factor")
	}
	ch, err := a.AuthGate.CreateChallenge(r.Context(), u.ID, loginPurpose, in.Factor)
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
// not the one stored in this session never reaches AuthGate, so one user cannot
// spend another's attempts. A success also cancels a pending factor reset: the
// person still holds a factor.
func (a *App) secondFactorVerify(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	x, u := sessionFrom(ctx)
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
	claims, err := a.Tokens.Verify(ctx, tok, u.ID, loginPurpose)
	switch {
	case errors.Is(err, authgate.ErrInvalidToken):
		slog.Warn("portal: login token refused")
		return errInvalidToken
	case err != nil:
		return webapi.AuthGateError(err)
	}
	if err := a.Store.UseJTI(ctx, claims.JTI, claims.ExpiresAt); err != nil {
		if errors.Is(err, ErrReplayed) {
			slog.Warn("portal: login token replayed")
			return errInvalidToken
		}
		return err
	}
	if err := a.Store.DeleteFactorReset(ctx, u.ID); err != nil {
		return err
	}
	if err := a.completeSignIn(ctx, u); err != nil {
		return err
	}
	x.FactorProved = true // this session just passed a factor
	if err := a.promote(ctx, w, x, StageFull); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": StageFull})
	return nil
}

// logout deletes the session item and clears the cookie.
func (a *App) logout(w http.ResponseWriter, r *http.Request) error {
	x, _ := sessionFrom(r.Context())
	if err := a.Store.DeleteSession(r.Context(), x.Hash); err != nil {
		return err
	}
	a.guard().SetCookie(w, "", -1)
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
