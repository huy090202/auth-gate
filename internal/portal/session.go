package portal

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/auditlog"
	"github.com/huy090202/auth-gate/internal/platform/websession"
)

const (
	cookieName    = "__Host-portal_session"
	devCookieName = "portal_session"
)

type ctxKey struct{}

type sessionCtx struct {
	s Session
	u User
}

// sessionFrom returns the session and user the session middleware stored.
func sessionFrom(ctx context.Context) (Session, User) {
	v, _ := ctx.Value(ctxKey{}).(sessionCtx)
	return v.s, v.u
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// cookieName is the cookie name. __Host- needs HTTPS, so Dev uses a plain name.
func (a *App) cookieName() string {
	if a.Config.Dev {
		return devCookieName
	}
	return cookieName
}

func hashToken(token string) string { return websession.HashToken(token) }

// guard is the shared session machinery configured for the Portal: the user is
// read with a consistent read on every request, so a password reset, suspension
// or deletion shows on the next call; the session is refused when the user is
// gone, unverified or suspended, or its epoch no longer matches. A session in
// stage full spends the user's bucket (burst 60, then one per second) before
// anything else runs.
func (a *App) guard() websession.Guard {
	return websession.Guard{
		Store: a.Store.sessions(), CookieName: a.cookieName(), Secure: !a.Config.Dev, Now: a.now,
		Load: func(ctx context.Context, x websession.Session) (any, int, bool, error) {
			u, err := a.Store.GetUser(ctx, x.Extra["userId"])
			if errors.Is(err, ErrUserNotFound) {
				return nil, 0, false, nil
			}
			if err != nil {
				return nil, 0, false, err
			}
			if u.EmailVerifiedAt == nil || u.SuspendedAt != nil {
				return nil, 0, false, nil
			}
			return u, u.SessionEpoch, true, nil
		},
		Known: func(ctx context.Context, acct any) { auditlog.SetActor(ctx, acct.(User).ID, acct.(User).DisplayName) },
		Limit: func(ctx context.Context, _ websession.Session, acct any) error {
			return a.spendUser(ctx, acct.(User).ID)
		},
		Attach: func(ctx context.Context, x websession.Session, acct any) context.Context {
			return context.WithValue(ctx, ctxKey{}, sessionCtx{s: fromWeb(x), u: acct.(User)})
		},
	}
}

// session guards a route that accepts the given stages.
func (a *App) session(stages ...Stage) func(http.Handler) http.Handler {
	return a.guard().Middleware(sessionExtra, stages...)
}

// startSession opens a session for the user and sets the cookie. The active
// workspace starts as the user's personal one.
func (a *App) startSession(ctx context.Context, w http.ResponseWriter, u User, st Stage) error {
	return a.guard().Start(ctx, w, Session{UserID: u.ID, Stage: st, Epoch: u.SessionEpoch, ActiveTenantID: u.PersonalTenantID}.web())
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
	x := Session{UserID: old.UserID, Stage: st, Epoch: epoch, FactorProved: old.FactorProved, ActiveTenantID: old.ActiveTenantID}
	nx, err := a.guard().Reissue(ctx, w, old.web(), x.web())
	if err != nil {
		return Session{}, err
	}
	return fromWeb(nx), nil
}

// endSession deletes the session, clears its cookie and answers 401.
func (a *App) endSession(w http.ResponseWriter, r *http.Request, x Session) error {
	return a.guard().End(w, r, x.web())
}
