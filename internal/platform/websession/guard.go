package websession

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

// ErrUnauthorized is the answer for a missing, ended or refused session.
var ErrUnauthorized = &webapi.Error{Status: http.StatusUnauthorized, Code: "unauthorized"}

// HashToken is the hex sha256 of a cookie token, the key a session is stored under.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// NewToken returns a 32-byte random token and its hash. Only the hash is stored.
func NewToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashToken(token), nil
}

// Guard ties a Store to one app's cookie and account rules. Build it where it
// is used (it is cheap), so a setting that changes later is read afresh.
type Guard struct {
	Store      Store
	CookieName string
	Secure     bool // false only in Dev, where __Host- cookies cannot work over plain HTTP
	Now        func() time.Time

	// Load reads the account the session belongs to, with a consistent read, on
	// every request. ok false refuses the session (account gone, disabled or
	// unverified). epoch is the account's current sessionEpoch.
	Load func(ctx context.Context, x Session) (acct any, epoch int, ok bool, err error)
	// Known, when set, runs once the account is loaded and its epoch matches,
	// before the stage is checked (the app records the actor for its audit log).
	Known func(ctx context.Context, acct any)
	// Limit, when set, runs for every session whose account was loaded and whose
	// epoch matches, whatever its stage, and before the stage is checked: a
	// request the route then refuses for its stage has still spent. A non-nil
	// error answers the request (rate limits).
	Limit func(ctx context.Context, x Session, acct any) error
	// Attach returns the request context carrying the session and account.
	Attach func(ctx context.Context, x Session, acct any) context.Context
}

func (g Guard) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// SetCookie sets the session cookie, replacing one this response already set: a
// request that reissues and then promotes must send one cookie, not two of one
// name whose order every hop would have to keep.
func (g Guard) SetCookie(w http.ResponseWriter, value string, maxAge int) {
	h := w.Header()
	var kept []string
	for _, v := range h.Values("Set-Cookie") {
		if !strings.HasPrefix(v, g.CookieName+"=") {
			kept = append(kept, v)
		}
	}
	h.Del("Set-Cookie")
	for _, v := range kept {
		h.Add("Set-Cookie", v)
	}
	//nolint:gosec // Secure is only off in Dev, where __Host- cannot work over plain HTTP.
	http.SetCookie(w, &http.Cookie{
		Name: g.CookieName, Value: value, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: g.Secure, SameSite: http.SameSiteStrictMode,
	})
}

// Start opens a session x (Hash and times are set here) and sets the cookie.
func (g Guard) Start(ctx context.Context, w http.ResponseWriter, x Session) error {
	token, hash, err := NewToken()
	if err != nil {
		return err
	}
	now := g.now()
	x.Hash, x.CreatedAt, x.LastSeenAt = hash, now, now
	if err := g.Store.Put(ctx, x, now); err != nil {
		return err
	}
	g.SetCookie(w, token, 0)
	return nil
}

// Reissue replaces the session old by x under a new token and returns x as stored, so a token seen at
// one stage is worthless at the next. Within the same stage the original start
// time stays, so the stage's time limit is not extended; a new stage restarts
// the clock. The swap is one transaction, so a session signed out meanwhile is
// not brought back (it gives 401).
func (g Guard) Reissue(ctx context.Context, w http.ResponseWriter, old, x Session) (Session, error) {
	token, hash, err := NewToken()
	if err != nil {
		return Session{}, err
	}
	now := g.now()
	x.Hash, x.CreatedAt, x.LastSeenAt = hash, now, now
	if x.Stage == old.Stage {
		x.CreatedAt = old.CreatedAt
	}
	if err := g.Store.Replace(ctx, old.Hash, x, now); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Session{}, ErrUnauthorized
		}
		return Session{}, err
	}
	g.SetCookie(w, token, 0)
	return x, nil
}

// End deletes the session, clears its cookie and answers 401.
func (g Guard) End(w http.ResponseWriter, r *http.Request, x Session) error {
	if err := g.Store.Delete(r.Context(), x.Hash); err != nil {
		return err
	}
	g.SetCookie(w, "", -1)
	return ErrUnauthorized
}

// Reject ends whatever session the request carried and answers 401.
func (g Guard) Reject(w http.ResponseWriter, r *http.Request, hash string) {
	if hash != "" {
		if err := g.Store.Delete(r.Context(), hash); err != nil {
			webapi.WriteErr(w, err)
			return
		}
	}
	g.SetCookie(w, "", -1)
	webapi.WriteErr(w, ErrUnauthorized)
}

// Middleware guards a route that accepts the given stages. The account is read
// on every request, so a reset, clear, suspension or disable shows on the next
// call: the session is refused when the account is refused by Load or its epoch
// no longer matches. extra names the app's attributes Get should read.
func (g Guard) Middleware(extra []string, stages ...Stage) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(g.CookieName)
			if err != nil || c.Value == "" {
				g.Reject(w, r, "")
				return
			}
			hash := HashToken(c.Value)
			x, err := g.Store.Get(r.Context(), hash, extra...)
			if errors.Is(err, ErrNotFound) {
				g.Reject(w, r, "")
				return
			}
			if err != nil {
				webapi.WriteErr(w, err)
				return
			}
			now := g.now()
			if !now.Before(x.ExpiresAt()) {
				g.Reject(w, r, hash)
				return
			}
			acct, epoch, ok, err := g.Load(r.Context(), x)
			if err != nil {
				webapi.WriteErr(w, err)
				return
			}
			if !ok || epoch != x.Epoch {
				g.Reject(w, r, hash)
				return
			}
			if g.Known != nil {
				g.Known(r.Context(), acct)
			}
			if g.Limit != nil {
				if err := g.Limit(r.Context(), x, acct); err != nil {
					webapi.WriteErr(w, err)
					return
				}
			}
			if !stageAllowed(x.Stage, stages) {
				webapi.WriteErr(w, stageError(x.Stage))
				return
			}
			if x.Stage == StageFull && now.Sub(x.LastSeenAt) > TouchAfter {
				x.LastSeenAt = now
				if err := g.Store.Touch(r.Context(), x); err != nil {
					webapi.WriteErr(w, err)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(g.Attach(r.Context(), x, acct)))
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
