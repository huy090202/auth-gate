package portal

import (
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/viewerip"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	"github.com/huy090202/auth-gate/internal/portal/avatar"
	"github.com/huy090202/auth-gate/internal/portal/hibp"
	"github.com/huy090202/auth-gate/internal/portal/mail"
	"github.com/huy090202/auth-gate/internal/portal/turnstile"
)

// Deps is what the handlers need from outside.
type Deps struct {
	Config    Config
	DB        *dynamodb.Client
	Limiter   *ratelimit.Limiter
	AuthGate  *authgate.Client
	Tokens    *authgate.Verifier
	Mail      mail.Sender
	Turnstile turnstile.Verifier
	Passwords hibp.Checker
	Avatars   avatar.Store
	Now       func() time.Time
}

// App holds the dependencies of the handlers.
type App struct {
	Deps
	Store *Store
}

// New returns the Portal's HTTP handler.
func New(d Deps) http.Handler { return newApp(d).handler() }

func newApp(d Deps) *App {
	return &App{Deps: d, Store: &Store{DB: d.DB, Table: d.Config.Table, Now: d.Now}}
}

// handler is the mux every route of the Portal registers on. Later tasks add
// their route groups before the "/api/" catch-all.
func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", notFound)
	return webapi.NoStore(webapi.RequireJSON(mux))
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	webapi.WriteErr(w, &webapi.Error{Status: http.StatusNotFound, Code: "not_found"})
}

// clientIP is the viewer address CloudFront sets. When it is missing the caller
// is "unknown", so a gap never lifts a limit and no caller-controlled header
// picks a key. In Dev there is no CloudFront, so the socket address is used.
func (a *App) clientIP(r *http.Request) (ip string, ok bool) {
	if a.Config.Dev {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			return host, true
		}
		return "unknown", false
	}
	if ip := viewerip.FromHeader(r.Header); ip != "" {
		return ip, true
	}
	return "unknown", false
}
