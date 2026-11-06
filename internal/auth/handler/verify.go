package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/auth/challenge"
	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/auth/service"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

// NewVerify returns the verify-role API behind the client access key.
// Every authenticated call spends a token from its client's bucket.
// The origin-verify wrapper is applied in main.
func NewVerify(svc *service.Service, clients *client.Service, limiter *ratelimit.Limiter) http.Handler {
	h := &verify{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/factors/pin", h.enrollPIN)
	mux.HandleFunc("DELETE /api/factors/pin/{subjectId}", h.deletePIN)
	mux.HandleFunc("GET /api/factors/{subjectId}", h.factors)
	mux.HandleFunc("POST /api/webauthn/registrations", h.beginRegistration)
	mux.HandleFunc("POST /api/webauthn/registrations/{registrationId}", h.finishRegistration)
	mux.HandleFunc("DELETE /api/webauthn/credentials/{credentialId}", h.deleteCredential)
	mux.HandleFunc("POST /api/challenges", h.beginChallenge)
	mux.HandleFunc("POST /api/challenges/{challengeId}/verify", h.verifyChallenge)
	return httpx.RequireKey("x-internal-access-key", clients.KeyFunc())(limitClient(limiter)(mux))
}

type verify struct{ svc *service.Service }

// caller returns the client put in the context by RequireKey.
func caller(w http.ResponseWriter, r *http.Request) (client.Client, bool) {
	c, ok := client.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", nil)
	}
	return c, ok
}

func (h *verify) enrollPIN(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		SubjectID string `json:"subjectId"`
		PIN       string `json:"pin"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		writeServiceError(w, err)
		return
	}
	if err := h.svc.EnrollPIN(r.Context(), c, body.SubjectID, body.PIN); err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, struct{}{})
}

func (h *verify) deletePIN(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(w, r)
	if !ok {
		return
	}
	if err := h.svc.DeletePIN(r.Context(), c, r.PathValue("subjectId")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *verify) factors(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(w, r)
	if !ok {
		return
	}
	view, err := h.svc.ListFactors(r.Context(), c, r.PathValue("subjectId"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, view)
}

func (h *verify) beginRegistration(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		SubjectID   string `json:"subjectId"`
		DisplayName string `json:"displayName"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		writeServiceError(w, err)
		return
	}
	reg, creation, err := h.svc.BeginRegistration(r.Context(), c, body.SubjectID, body.DisplayName)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"registrationId": reg.ID, "publicKey": creation.Response})
}

func (h *verify) finishRegistration(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		Credential json.RawMessage `json:"credential"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		writeServiceError(w, err)
		return
	}
	if len(body.Credential) == 0 || string(body.Credential) == "null" {
		writeServiceError(w, fmt.Errorf("%w: credential is required", httpx.ErrBadJSON))
		return
	}
	credID, err := h.svc.FinishRegistration(r.Context(), c, r.PathValue("registrationId"), body.Credential)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"credentialId": credID})
}

func (h *verify) deleteCredential(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(w, r)
	if !ok {
		return
	}
	// A missing subjectId fails id validation: 400 invalid_id.
	if err := h.svc.DeleteCredential(r.Context(), c, r.URL.Query().Get("subjectId"), r.PathValue("credentialId")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *verify) beginChallenge(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		SubjectID string           `json:"subjectId"`
		Purpose   string           `json:"purpose"`
		Factor    challenge.Factor `json:"factor"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		writeServiceError(w, err)
		return
	}
	ch, assertion, err := h.svc.BeginChallenge(r.Context(), c, body.SubjectID, body.Purpose, body.Factor)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	resp := map[string]any{"challengeId": ch.ID, "expiresAt": ch.ExpiresAt.UTC().Format(time.RFC3339)}
	if assertion != nil {
		resp["publicKey"] = assertion.Response
	}
	httpx.WriteJSON(w, http.StatusCreated, resp)
}

func (h *verify) verifyChallenge(w http.ResponseWriter, r *http.Request) {
	c, ok := caller(w, r)
	if !ok {
		return
	}
	var body struct {
		PIN        *string         `json:"pin"`
		Credential json.RawMessage `json:"credential"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		writeServiceError(w, err)
		return
	}
	hasCred := len(body.Credential) > 0 && string(body.Credential) != "null"
	var (
		tok service.Token
		err error
	)
	switch {
	case body.PIN != nil && !hasCred:
		tok, err = h.svc.VerifyPIN(r.Context(), c, r.PathValue("challengeId"), *body.PIN)
	case body.PIN == nil && hasCred:
		tok, err = h.svc.VerifyWebAuthn(r.Context(), c, r.PathValue("challengeId"), body.Credential)
	default:
		// Exactly one of pin or credential is required.
		err = fmt.Errorf("%w: exactly one of pin or credential", httpx.ErrBadJSON)
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"token": tok.Token, "expiresAt": tok.ExpiresAt.UTC().Format(time.RFC3339), "factor": tok.Factor,
	})
}
