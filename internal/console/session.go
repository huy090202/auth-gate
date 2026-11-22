package console

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

const (
	cookieName    = "__Host-console_session"
	devCookieName = "console_session"
)

type ctxKey struct{}

type sessionCtx struct {
	s Session
	a Admin
}

// sessionFrom returns the session and admin the session middleware stored.
func sessionFrom(ctx context.Context) (Session, Admin) {
	v, _ := ctx.Value(ctxKey{}).(sessionCtx)
	return v.s, v.a
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// name is the cookie name. __Host- needs HTTPS, so Dev uses a plain name.
func (a *App) cookieName() string {
	if a.Config.Dev {
		return devCookieName
	}
	return cookieName
}

// setCookie sets the session cookie, replacing one this response already set: a
// request that reissues and then promotes must send one cookie, not two of one
// name whose order every hop would have to keep.
func (a *App) setCookie(w http.ResponseWriter, value string, maxAge int) {
	h := w.Header()
	var kept []string
	for _, v := range h.Values("Set-Cookie") {
		if !strings.HasPrefix(v, a.cookieName()+"=") {
			kept = append(kept, v)
		}
	}
	h.Del("Set-Cookie")
	for _, v := range kept {
		h.Add("Set-Cookie", v)
	}
	//nolint:gosec // Secure is only off in Dev, where __Host- cannot work over plain HTTP.
	http.SetCookie(w, &http.Cookie{
		Name: a.cookieName(), Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: !a.Config.Dev, SameSite: http.SameSiteStrictMode,
	})
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// newToken returns a 32-byte random token and its hash. Only the hash is stored.
func newToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token), nil
}

// startSession opens a session for the admin and sets the cookie.
func (a *App) startSession(ctx context.Context, w http.ResponseWriter, admin Admin, st Stage) error {
	token, hash, err := newToken()
	if err != nil {
		return err
	}
	now := a.now()
	x := Session{Hash: hash, AdminID: admin.ID, Username: admin.Username, Stage: st,
		Epoch: admin.SessionEpoch, CreatedAt: now, LastSeenAt: now}
	if err := a.Store.PutSession(ctx, x, now); err != nil {
		return err
	}
	a.setCookie(w, token, 0)
	return nil
}

// promote moves a session to another stage under a new token, so a token seen
// at one stage is worthless at the next. The clock restarts and the old item goes.
func (a *App) promote(ctx context.Context, w http.ResponseWriter, old Session, st Stage) error {
	_, err := a.reissue(ctx, w, old, st, old.Epoch)
	return err
}

// reissue replaces a session by one at the given stage and epoch under a new
// token. Within the same stage the original start time stays, so the stage's
// time limit is not extended. The swap is one transaction, so a session signed
// out meanwhile is not brought back (it gives 401).
func (a *App) reissue(ctx context.Context, w http.ResponseWriter, old Session, st Stage, epoch int) (Session, error) {
	token, hash, err := newToken()
	if err != nil {
		return Session{}, err
	}
	now := a.now()
	x := Session{Hash: hash, AdminID: old.AdminID, Username: old.Username, Stage: st,
		Epoch: epoch, FactorProved: old.FactorProved, CreatedAt: now, LastSeenAt: now}
	if st == old.Stage {
		x.CreatedAt = old.CreatedAt
	}
	if err := a.Store.ReplaceSession(ctx, old.Hash, x, now); err != nil {
		return Session{}, sessionGone(err)
	}
	a.setCookie(w, token, 0)
	return x, nil
}

var errUnauthorized = &webapi.Error{Status: http.StatusUnauthorized, Code: "unauthorized"}

// endSession deletes the session, clears its cookie and answers 401.
func (a *App) endSession(w http.ResponseWriter, r *http.Request, x Session) error {
	if err := a.Store.DeleteSession(r.Context(), x.Hash); err != nil {
		return err
	}
	a.setCookie(w, "", -1)
	return errUnauthorized
}

// reject ends whatever session the request carried and answers 401.
func (a *App) reject(w http.ResponseWriter, r *http.Request, hash string) {
	if hash != "" {
		if err := a.Store.DeleteSession(r.Context(), hash); err != nil {
			webapi.WriteErr(w, err)
			return
		}
	}
	a.setCookie(w, "", -1)
	webapi.WriteErr(w, errUnauthorized)
}

// session guards a route that accepts the given stages. The admin is read with
// a consistent read on every request, so a reset, clear or disable shows on the
// next call: the session is refused when the admin is gone or disabled, or its
// epoch no longer matches.
func (a *App) session(stages ...Stage) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(a.cookieName())
			if err != nil || c.Value == "" {
				a.reject(w, r, "")
				return
			}
			hash := hashToken(c.Value)
			x, err := a.Store.GetSession(r.Context(), hash)
			if errors.Is(err, ErrSessionNotFound) {
				a.reject(w, r, "")
				return
			}
			if err != nil {
				webapi.WriteErr(w, err)
				return
			}
			now := a.now()
			if !now.Before(x.expiresAt()) {
				a.reject(w, r, hash)
				return
			}
			admin, err := a.Store.GetAdmin(r.Context(), x.Username)
			if errors.Is(err, ErrAdminNotFound) {
				a.reject(w, r, hash)
				return
			}
			if err != nil {
				webapi.WriteErr(w, err)
				return
			}
			if admin.ID != x.AdminID || admin.DisabledAt != nil || admin.SessionEpoch != x.Epoch {
				a.reject(w, r, hash)
				return
			}
			setAuditActor(r.Context(), admin.ID, admin.Username)
			if !stageAllowed(x.Stage, stages) {
				webapi.WriteErr(w, stageError(x.Stage))
				return
			}
			if x.Stage == StageFull && now.Sub(x.LastSeenAt) > touchAfter {
				x.LastSeenAt = now
				if err := a.Store.touchSession(r.Context(), x); err != nil {
					webapi.WriteErr(w, err)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, sessionCtx{s: x, a: admin})))
		})
	}
}

func stageAllowed(have Stage, want []Stage) bool {
	for _, w := range want {
		if w == have {
			return true
		}
	}
	return false
}

func stageError(have Stage) *webapi.Error {
	switch have {
	case StageHalf:
		return &webapi.Error{Status: http.StatusUnauthorized, Code: "second_factor_required"}
	case StageEnroll:
		return &webapi.Error{Status: http.StatusForbidden, Code: "enrollment_required"}
	default:
		return &webapi.Error{Status: http.StatusConflict, Code: "already_verified"}
	}
}
