package handler

import (
	"net/http"
	"strconv"

	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

// limitClient spends one token from the calling client's bucket and answers
// 429 with Retry-After when it is empty. It must run after RequireKey, which
// puts the client in the context.
func limitClient(l *ratelimit.Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, ok := client.FromContext(r.Context())
			if !ok {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", nil)
				return
			}
			if d := l.Allow(r.Context(), c.ID, c.RateLimit); !d.Allowed {
				w.Header().Set("Retry-After", strconv.Itoa(d.RetryAfterSec))
				httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
