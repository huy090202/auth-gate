// Package webapi is the JSON envelope, the middleware and the AuthGate error
// mapping shared by the Console and the Portal.
package webapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
)

// Error is an error that WriteErr turns into the JSON envelope.
type Error struct {
	Status     int
	Code       string
	Extra      map[string]any
	RetryAfter int // seconds; sets Retry-After when positive
}

func (e *Error) Error() string { return strconv.Itoa(e.Status) + " " + e.Code }

// WriteErr answers an *Error as its envelope; anything else is logged and
// answered 500 internal, so a detail never reaches the browser.
func WriteErr(w http.ResponseWriter, err error) {
	var ae *Error
	if !errors.As(err, &ae) {
		slog.Error("webapi: unhandled error", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", nil)
		return
	}
	if ae.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ae.RetryAfter))
	}
	httpx.WriteError(w, ae.Status, ae.Code, ae.Extra)
}

// RequireJSON refuses any request other than GET or HEAD whose Content-Type is
// not application/json (a charset parameter is fine). A cross-site form post
// cannot send that type without a CORS preflight.
func RequireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || mt != "application/json" {
				WriteErr(w, &Error{Status: http.StatusUnsupportedMediaType, Code: "unsupported_media_type"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// NoStore keeps browsers and CloudFront from caching an API answer.
func NoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// HandlerFunc is a handler that returns its failure; ServeHTTP answers it with WriteErr.
type HandlerFunc func(http.ResponseWriter, *http.Request) error

func (f HandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := f(w, r); err != nil {
		WriteErr(w, err)
	}
}

// DecodeBody reads the JSON body into dst; a body that is not valid JSON is 400 invalid_json.
func DecodeBody(r *http.Request, dst any) error {
	if err := httpx.DecodeJSON(r, dst); err != nil {
		return &Error{Status: http.StatusBadRequest, Code: "invalid_json"}
	}
	return nil
}

// authGateExtra is what of an AuthGate error body reaches the browser. Anything else
// AuthGate adds stays here, so a new field there is never published by accident.
var authGateExtra = []string{"attemptsRemaining", "lockedUntil", "retryAfter"}

// AuthGateError turns a failed AuthGate call into the apps' answer. AuthGate
// being down or misconfigured is 502 and the cause stays in the log; its own
// 4xx answers pass through with the allow-listed fields and Retry-After; a
// missing factor is 409 factor_not_enrolled. Anything else is returned as is.
func AuthGateError(err error) error {
	var ae *authgate.Error
	switch {
	case errors.Is(err, authgate.ErrUnavailable):
		slog.Warn("webapi: authgate unavailable", "err", err)
		return &Error{Status: http.StatusBadGateway, Code: "authgate_unavailable"}
	case errors.As(err, &ae):
		if ae.Code == "factor_not_found" {
			return &Error{Status: http.StatusConflict, Code: "factor_not_enrolled"}
		}
		var extra map[string]any
		for _, k := range authGateExtra {
			if v, ok := ae.Extra[k]; ok {
				if extra == nil {
					extra = map[string]any{}
				}
				extra[k] = v
			}
		}
		return &Error{Status: ae.Status, Code: ae.Code, Extra: extra, RetryAfter: ae.RetryAfter}
	}
	return err
}

// WriteRaw passes an AuthGate answer on as it is.
func WriteRaw(w http.ResponseWriter, status int, raw json.RawMessage) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//nolint:gosec // G705: AuthGate's own JSON, served as application/json (and no-store), never as HTML.
	_, _ = w.Write(raw)
}

// ValidSubject is the rule for an end user's id in a path: AuthGate's (1 to 256
// bytes) minus what could change the path. No '/', '?', '#', '%', control
// character, and not a dot segment.
func ValidSubject(v string) bool {
	bad := v == "" || len(v) > 256 || v == "." || v == ".." || strings.ContainsAny(v, "/?#%")
	for _, c := range []byte(v) {
		bad = bad || c < 0x20 || c == 0x7f
	}
	return !bad
}
