package portal

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// userLimit is the signed-in bucket: 60 at once, then one per second.
var userLimit = ratelimit.Settings{Burst: 60, PerSecond: 1}

func rateLimited(retryAfter int) *webapi.Error {
	return &webapi.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: retryAfter}
}

// limitIP spends one token per request from the caller's bucket RL#<scope>#<ip>.
// A refusal is not stored (it is unbounded), so the log is the only trace.
func (a *App) limitIP(scope string, set ratelimit.Settings) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip, ok := a.clientIP(r)
			if !ok && !a.Config.Dev {
				slog.Warn("portal: no viewer address; using the shared bucket", "scope", scope)
			}
			if d := a.Limiter.Allow(r.Context(), scope+"#"+ip, set); !d.Allowed {
				slog.Warn("portal: rate limited", "scope", scope, "ip", ip, "route", truncate(r.Method+" "+r.URL.Path, maxAuditTarget))
				webapi.WriteErr(w, rateLimited(d.RetryAfterSec))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// spendUser spends one token from the user's bucket. The session guard calls
// it for every request of a session in stage full, so no route can leave it
// out; a half or enroll session is not metered here (its routes sit behind
// limitIP).
func (a *App) spendUser(ctx context.Context, userID string) error {
	if d := a.Limiter.Allow(ctx, "user#"+userID, userLimit); !d.Allowed {
		slog.Warn("portal: user rate limited", "user", userID)
		return rateLimited(d.RetryAfterSec)
	}
	return nil
}
