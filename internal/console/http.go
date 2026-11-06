package console

import (
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"

	"github.com/huy090202/auth-gate/internal/console/authgate"
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

// handlerFunc is a handler that returns its failure; ServeHTTP answers it with writeErr.
type handlerFunc func(http.ResponseWriter, *http.Request) error

func (f handlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := f(w, r); err != nil {
		writeErr(w, err)
	}
}

// decodeBody reads the JSON body into dst; a body that is not valid JSON is 400 invalid_json.
func decodeBody(r *http.Request, dst any) error {
	if err := httpx.DecodeJSON(r, dst); err != nil {
		return &apiError{Status: http.StatusBadRequest, Code: "invalid_json"}
	}
	return nil
}

// agExtra is what of an AuthGate error body reaches the browser. Anything else
// AuthGate adds stays here, so a new field there is never published by accident.
var agExtra = []string{"attemptsRemaining", "lockedUntil", "retryAfter"}

// agError turns a failed AuthGate call into the Console's answer. AuthGate
// being down or misconfigured is 502 and the cause stays in the log; its own
// 4xx answers pass through with the allow-listed fields and Retry-After; a
// missing factor is 409 factor_not_enrolled. Anything else is returned as is.
func agError(err error) error {
	var ae *authgate.Error
	switch {
	case errors.Is(err, authgate.ErrUnavailable):
		slog.Warn("console: authgate unavailable", "err", err)
		return &apiError{Status: http.StatusBadGateway, Code: "authgate_unavailable"}
	case errors.As(err, &ae):
		if ae.Code == "factor_not_found" {
			return &apiError{Status: http.StatusConflict, Code: "factor_not_enrolled"}
		}
		var extra map[string]any
		for _, k := range agExtra {
			if v, ok := ae.Extra[k]; ok {
				if extra == nil {
					extra = map[string]any{}
				}
				extra[k] = v
			}
		}
		return &apiError{Status: ae.Status, Code: ae.Code, Extra: extra, RetryAfter: ae.RetryAfter}
	}
	return err
}
