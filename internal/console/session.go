package console

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/websession"
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

// setCookie sets or clears the session cookie.
func (a *App) setCookie(w http.ResponseWriter, value string, maxAge int) {
	a.guard().SetCookie(w, value, maxAge)
}

func hashToken(token string) string { return websession.HashToken(token) }

func (a *App) guard() websession.Guard {
	return websession.Guard{
		Store: a.Store.sessions(), CookieName: a.cookieName(), Secure: !a.Config.Dev, Now: a.now,
		Load: func(ctx context.Context, x websession.Session) (any, int, bool, error) {
			admin, err := a.Store.GetAdmin(ctx, x.Extra["username"])
			if errors.Is(err, ErrAdminNotFound) {
				return nil, 0, false, nil
			}
			if err != nil {
				return nil, 0, false, err
			}
			if admin.ID != x.Extra["adminId"] || admin.DisabledAt != nil {
				return nil, 0, false, nil
			}
			return admin, admin.SessionEpoch, true, nil
		},
		Known: func(ctx context.Context, acct any) {
			admin := acct.(Admin)
			setAuditActor(ctx, admin.ID, admin.Username)
		},
		Attach: func(ctx context.Context, x websession.Session, acct any) context.Context {
			return context.WithValue(ctx, ctxKey{}, sessionCtx{s: fromWeb(x), a: acct.(Admin)})
		},
	}
}

// startSession opens a session for the admin and sets the cookie.
func (a *App) startSession(ctx context.Context, w http.ResponseWriter, admin Admin, st Stage) error {
	return a.guard().Start(ctx, w, Session{AdminID: admin.ID, Username: admin.Username, Stage: st, Epoch: admin.SessionEpoch}.web())
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
	x := Session{AdminID: old.AdminID, Username: old.Username, Stage: st, Epoch: epoch, FactorProved: old.FactorProved}
	nx, err := a.guard().Reissue(ctx, w, old.web(), x.web())
	if err != nil {
		return Session{}, err
	}
	return fromWeb(nx), nil
}

var errUnauthorized = websession.ErrUnauthorized

// endSession deletes the session, clears its cookie and answers 401.
func (a *App) endSession(w http.ResponseWriter, r *http.Request, x Session) error {
	return a.guard().End(w, r, x.web())
}

// session guards a route that accepts the given stages. The admin is read with
// a consistent read on every request, so a reset, clear or disable shows on the
// next call: the session is refused when the admin is gone or disabled, or its
// epoch no longer matches.
func (a *App) session(stages ...Stage) func(http.Handler) http.Handler {
	return a.guard().Middleware(sessionExtra, stages...)
}
