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
	"time"
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

func (a *App) setCookie(w http.ResponseWriter, value string, maxAge int) {
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
	token, hash, err := newToken()
	if err != nil {
		return err
	}
	now := a.now()
	x := Session{Hash: hash, AdminID: old.AdminID, Username: old.Username, Stage: st,
		Epoch: old.Epoch, CreatedAt: now, LastSeenAt: now}
	if err := a.Store.PutSession(ctx, x, now); err != nil {
		return err
	}
	if err := a.Store.DeleteSession(ctx, old.Hash); err != nil {
		return err
	}
	a.setCookie(w, token, 0)
	return nil
}

var errUnauthorized = &apiError{Status: http.StatusUnauthorized, Code: "unauthorized"}

// reject ends whatever session the request carried and answers 401.
func (a *App) reject(w http.ResponseWriter, r *http.Request, hash string) {
	if hash != "" {
		if err := a.Store.DeleteSession(r.Context(), hash); err != nil {
			writeErr(w, err)
			return
		}
	}
	a.setCookie(w, "", -1)
	writeErr(w, errUnauthorized)
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
				writeErr(w, err)
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
				writeErr(w, err)
				return
			}
			if admin.ID != x.AdminID || admin.DisabledAt != nil || admin.SessionEpoch != x.Epoch {
				a.reject(w, r, hash)
				return
			}
			if !stageAllowed(x.Stage, stages) {
				writeErr(w, stageError(x.Stage))
				return
			}
			if x.Stage == StageFull && now.Sub(x.LastSeenAt) > touchAfter {
				x.LastSeenAt = now
				if err := a.Store.touchSession(r.Context(), x); err != nil {
					writeErr(w, err)
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

func stageError(have Stage) *apiError {
	switch have {
	case StageHalf:
		return &apiError{Status: http.StatusUnauthorized, Code: "second_factor_required"}
	case StageEnroll:
		return &apiError{Status: http.StatusForbidden, Code: "enrollment_required"}
	default:
		return &apiError{Status: http.StatusConflict, Code: "already_verified"}
	}
}
