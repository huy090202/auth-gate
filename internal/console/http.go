package console

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
)

// apiError is an error that writeErr turns into the JSON envelope.
type apiError struct {
	Status     int
	Code       string
	Extra      map[string]any
	RetryAfter int // seconds; sets Retry-After when positive
}

func (e *apiError) Error() string { return strconv.Itoa(e.Status) + " " + e.Code }

// writeErr answers an *apiError as its envelope; anything else is logged and
// answered 500 internal, so a detail never reaches the browser.
func writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	if !errors.As(err, &ae) {
		slog.Error("console: unhandled error", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", nil)
		return
	}
	if ae.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ae.RetryAfter))
	}
	httpx.WriteError(w, ae.Status, ae.Code, ae.Extra)
}

// requireJSON refuses any request other than GET or HEAD whose Content-Type is
// not application/json (a charset parameter is fine). A cross-site form post
// cannot send that type without a CORS preflight.
func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "application/json" {
				writeErr(w, &apiError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// noStore keeps browsers and CloudFront from caching an API answer.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
