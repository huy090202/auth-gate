package console

import (
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

// Deps is what the handlers need from outside. Later tasks add AuthGate and Tokens.
type Deps struct {
	Config  Config
	DB      *dynamodb.Client
	Limiter *ratelimit.Limiter
	Now     func() time.Time
}

// App holds the dependencies of the handlers.
type App struct {
	Deps
	Store *Store
}

// authLimit is the per-IP bucket on /api/auth/*: 10 at once, then one per 6 seconds.
var authLimit = ratelimit.Settings{Burst: 10, PerSecond: 1.0 / 6}

// New returns the Console's HTTP handler.
func New(d Deps) http.Handler {
	a := &App{Deps: d, Store: &Store{DB: d.DB, Table: d.Config.Table}}
	mux := http.NewServeMux()
	mux.Handle("/api/auth/", a.limitAuth(http.HandlerFunc(notFound)))
	mux.HandleFunc("/api/", notFound)
	return noStore(requireJSON(mux))
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	writeErr(w, &apiError{Status: http.StatusNotFound, Code: "not_found"})
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
			writeErr(w, &apiError{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: d.RetryAfterSec})
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
	if ip := ViewerIP(r.Header); ip != "" {
		return ip
	}
	slog.Warn("console: no viewer address on an auth request; using the shared bucket")
	return "unknown"
}
