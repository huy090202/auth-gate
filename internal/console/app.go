package console

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/avatar"
	"github.com/huy090202/auth-gate/internal/platform/password"
	"github.com/huy090202/auth-gate/internal/platform/portalapi"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/viewerip"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// Deps is what the handlers need from outside.
type Deps struct {
	Config   Config
	DB       *dynamodb.Client
	Limiter  *ratelimit.Limiter
	AuthGate *authgate.Client
	Tokens   *authgate.Verifier
	Portal   *portalapi.Client
	// Avatars holds the admins' pictures: S3 on a stage, a local directory in Dev.
	Avatars avatar.Store
	Now     func() time.Time
}

// App holds the dependencies of the handlers.
type App struct {
	Deps
	Store *Store

	// verifyPassword is password.Verify; a field so a test can count the calls.
	verifyPassword func(password, stored string) bool

	// beforeAdminWrite, when set, runs after an /admins action spent its token and
	// before it writes. Tests use it to interleave.
	beforeAdminWrite func()
}

// authLimit is the per-IP bucket on /api/auth/*: 10 at once, then one per 6 seconds.
var authLimit = ratelimit.Settings{Burst: 10, PerSecond: 1.0 / 6}

// New returns the Console's HTTP handler.
func New(d Deps) http.Handler { return newApp(d).handler() }

func newApp(d Deps) *App {
	return &App{Deps: d, Store: &Store{DB: d.DB, Table: d.Config.Table}, verifyPassword: password.Verify}
}

func (a *App) handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/auth/", a.limitAuth(a.authRoutes()))
	a.meRoutes(mux)
	a.stepUpRoutes(mux)
	a.clientRoutes(mux)
	a.operatorRoutes(mux)
	a.adminRoutes(mux)
	mux.HandleFunc("/api/", notFound)
	return webapi.NoStore(webapi.RequireJSON(mux))
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	webapi.WriteErr(w, &webapi.Error{Status: http.StatusNotFound, Code: "not_found"})
}

// limitAuth spends one token per request from the caller's IP bucket. The key is
// the viewer address CloudFront sets; when it is missing every such request
// shares the "unknown" bucket, so a gap never lifts the limit and no
// caller-controlled header picks the key. In Dev there is no CloudFront, so the
// socket address is used.
func (a *App) limitAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := a.clientKey(r)
		d := a.Limiter.Allow(r.Context(), key, authLimit)
		if !d.Allowed {
			// A refusal is not stored (it is unbounded), so the log is the only trace.
			slog.Warn("console: auth request rate limited", "ip", key, "route", truncate(r.Method+" "+r.URL.Path, maxAuditTarget))
			webapi.WriteErr(w, &webapi.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: d.RetryAfterSec})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) clientKey(r *http.Request) string {
	if a.Config.Dev {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			return host
		}
		return "unknown"
	}
	if ip := viewerip.FromHeader(r.Header); ip != "" {
		return ip
	}
	slog.Warn("console: no viewer address on an auth request; using the shared bucket")
	return "unknown"
}
