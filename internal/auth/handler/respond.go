// Package handler holds the HTTP handlers for the verify, admin and jwks roles.
package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/auth/challenge"
	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/auth/service"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

// writeServiceError maps service errors to the API error envelope.
func writeServiceError(w http.ResponseWriter, err error) {
	var vf service.VerificationFailed
	var locked service.Locked
	switch {
	case errors.Is(err, id.ErrInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", nil)
	case errors.Is(err, httpx.ErrBadJSON):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", nil)
	case errors.Is(err, service.ErrRegistrationFailed):
		httpx.WriteError(w, http.StatusBadRequest, "registration_failed", nil)
	case errors.Is(err, service.ErrPINRejected):
		httpx.WriteError(w, http.StatusBadRequest, "pin_rejected", nil)
	case errors.As(err, &vf):
		httpx.WriteError(w, http.StatusBadRequest, "verification_failed", map[string]any{"attemptsRemaining": vf.AttemptsRemaining})
	case errors.As(err, &locked):
		httpx.WriteError(w, http.StatusLocked, "locked", map[string]any{"lockedUntil": locked.Until.UTC().Format(time.RFC3339)})
	case errors.Is(err, challenge.ErrGone):
		httpx.WriteError(w, http.StatusGone, "challenge_gone", nil)
	case errors.Is(err, service.ErrWrongFactor):
		httpx.WriteError(w, http.StatusBadRequest, "wrong_factor", nil)
	case errors.Is(err, client.ErrInvalid):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_client", nil)
	case errors.Is(err, client.ErrBadCursor):
		httpx.WriteError(w, http.StatusBadRequest, "invalid_cursor", nil)
	case errors.Is(err, client.ErrRotateConflict):
		httpx.WriteError(w, http.StatusConflict, "rotate_conflict", nil)
	case errors.Is(err, client.ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, "client_not_found", nil)
	case errors.Is(err, service.ErrFactorNotFound):
		httpx.WriteError(w, http.StatusNotFound, "factor_not_found", nil)
	default:
		slog.Error("internal error", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal", nil)
	}
}
