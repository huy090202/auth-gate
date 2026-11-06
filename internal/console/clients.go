package console

import (
	"context"
	"encoding/json"
	"net/http"
)

var errOwnClient = &apiError{Status: http.StatusConflict, Code: "own_client_protected"}

func pathID(r *http.Request) string { return r.PathValue("id") }

// clientRoutes serves /api/clients/* and /api/audit for a full session. The
// catch-all patterns keep the answer to a session at the wrong stage, and to a
// path that is not a route, what they were before the routes existed.
func (a *App) clientRoutes(mux *http.ServeMux) {
	full := a.session(StageFull)
	mux.Handle("GET /api/clients", full(handlerFunc(a.listClients)))
	mux.Handle("GET /api/clients/{id}", full(handlerFunc(a.getClient)))
	mux.Handle("POST /api/clients", a.audited("create-client", nil, full(handlerFunc(a.createClient))))
	mux.Handle("PATCH /api/clients/{id}", a.audited("update-client", pathID, full(handlerFunc(a.updateClient))))
	mux.Handle("POST /api/clients/{id}/keys", a.audited("rotate-key", pathID, full(a.clientWrite("rotate-key", http.StatusCreated,
		func(ctx context.Context, a *App, id string) (json.RawMessage, error) {
			return a.AuthGate.RotateKey(ctx, id)
		}))))
	mux.Handle("DELETE /api/clients/{id}/keys/previous", a.audited("revoke-previous-keys", pathID, full(a.clientWrite("revoke-previous-keys", http.StatusOK,
		func(ctx context.Context, a *App, id string) (json.RawMessage, error) {
			return a.AuthGate.RevokePreviousKeys(ctx, id)
		}))))
	mux.Handle("POST /api/clients/{id}/disable", a.audited("disable-client", pathID, full(a.clientWrite("disable-client", http.StatusOK,
		func(ctx context.Context, a *App, id string) (json.RawMessage, error) {
			return a.AuthGate.DisableClient(ctx, id)
		}))))
	mux.Handle("POST /api/clients/{id}/enable", a.audited("enable-client", pathID, full(handlerFunc(a.enableClient))))
	mux.Handle("DELETE /api/clients/{id}/subjects/{subjectId}", a.audited("purge-subject",
		func(r *http.Request) string { return r.PathValue("id") + "/" + r.PathValue("subjectId") }, full(handlerFunc(a.purgeSubject))))
	mux.Handle("GET /api/audit", full(handlerFunc(a.listAudit)))
	// Not routes yet, or other methods on the routes above: they answer by stage, then 404.
	for _, p := range []string{"/api/clients", "/api/clients/", "/api/audit"} {
		mux.Handle(p, full(http.HandlerFunc(notFound)))
	}
}

// writeRaw passes an AuthGate answer on as it is.
func writeRaw(w http.ResponseWriter, status int, raw json.RawMessage) {
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	//nolint:gosec // G705: AuthGate's own JSON, served as application/json (and no-store), never as HTML.
	_, _ = w.Write(raw)
}

func (a *App) listClients(w http.ResponseWriter, r *http.Request) error {
	raw, err := a.AuthGate.ListClients(r.Context(), r.URL.Query().Get("cursor"))
	if err != nil {
		return agError(err)
	}
	writeRaw(w, http.StatusOK, raw)
	return nil
}

func (a *App) getClient(w http.ResponseWriter, r *http.Request) error {
	raw, err := a.AuthGate.GetClient(r.Context(), r.PathValue("id"))
	if err != nil {
		return agError(err)
	}
	writeRaw(w, http.StatusOK, raw)
	return nil
}

type pinPolicyBody struct {
	Length      int `json:"length"`
	MaxAttempts int `json:"maxAttempts"`
	LockoutSec  int `json:"lockoutSec"`
}

type rateLimitBody struct {
	Burst     int     `json:"burst"`
	PerSecond float64 `json:"perSecond"`
}

// createClient registers a client. Only the fields AuthGate's create takes are
// passed on, under their own names. The answer carries the key, once.
func (a *App) createClient(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name        string         `json:"name"`
		RPID        string         `json:"rpId"`
		Origins     []string       `json:"origins"`
		PinPolicy   *pinPolicyBody `json:"pinPolicy"`
		TokenTTLSec *int           `json:"tokenTtlSec"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	raw, err := a.AuthGate.CreateClient(r.Context(), body)
	if err != nil {
		return agError(err)
	}
	var out struct {
		ClientID string `json:"clientId"`
	}
	_ = json.Unmarshal(raw, &out)
	setAuditTarget(r.Context(), out.ClientID)
	writeRaw(w, http.StatusCreated, raw)
	return nil
}

// updateClient patches a client. The body is decoded the way AuthGate decodes
// it (case-insensitive keys included) and only the decoded fields are passed on
// under their canonical names, so what asks for a step-up is what AuthGate would
// apply, not what the raw keys look like. A rename alone needs no token. rpId is
// passed on as it came so AuthGate refuses it.
func (a *App) updateClient(w http.ResponseWriter, r *http.Request) error {
	x, _ := sessionFrom(r.Context())
	id := r.PathValue("id")
	var in struct {
		Name        *string         `json:"name"`
		Origins     []string        `json:"origins"`
		PinPolicy   *pinPolicyBody  `json:"pinPolicy"`
		TokenTTLSec *int            `json:"tokenTtlSec"`
		RateLimit   *rateLimitBody  `json:"rateLimit"`
		RPID        json.RawMessage `json:"rpId"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	out := map[string]any{}
	if in.Name != nil {
		out["name"] = in.Name
	}
	sensitive := false
	set := func(k string, v any) { out[k], sensitive = v, true }
	if in.Origins != nil {
		set("origins", in.Origins)
	}
	if in.PinPolicy != nil {
		set("pinPolicy", in.PinPolicy)
	}
	if in.TokenTTLSec != nil {
		set("tokenTtlSec", in.TokenTTLSec)
	}
	if in.RateLimit != nil {
		set("rateLimit", in.RateLimit)
	}
	if in.RPID != nil {
		set("rpId", in.RPID)
	}
	var tok *stepUp
	if sensitive {
		var err error
		if tok, err = a.checkStepUp(r, x, "update-client"); err != nil {
			return err
		}
	}
	if id == a.Config.AuthGateClientID {
		return errOwnClient
	}
	body, err := json.Marshal(out)
	if err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	raw, err := a.AuthGate.UpdateClient(r.Context(), id, body)
	if err != nil {
		return agError(err)
	}
	writeRaw(w, http.StatusOK, raw)
	return nil
}

// clientWrite is a step-up action on a client other than the Console's own: the
// token is checked first, the own-client guard next, and the token spent only
// when the call is about to go out. An answer that carries a key is no-store like
// every /api answer.
func (a *App) clientWrite(action string, status int, call func(context.Context, *App, string) (json.RawMessage, error)) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		x, _ := sessionFrom(r.Context())
		tok, err := a.checkStepUp(r, x, action)
		if err != nil {
			return err
		}
		id := r.PathValue("id")
		if id == a.Config.AuthGateClientID {
			return errOwnClient
		}
		if err := a.spendStepUp(r, tok); err != nil {
			return err
		}
		raw, err := call(r.Context(), a, id)
		if err != nil {
			return agError(err)
		}
		writeRaw(w, status, raw)
		return nil
	}
}

func (a *App) enableClient(w http.ResponseWriter, r *http.Request) error {
	raw, err := a.AuthGate.EnableClient(r.Context(), r.PathValue("id"))
	if err != nil {
		return agError(err)
	}
	writeRaw(w, http.StatusOK, raw)
	return nil
}

func (a *App) purgeSubject(w http.ResponseWriter, r *http.Request) error {
	x, _ := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "purge-subject")
	if err != nil {
		return err
	}
	id := r.PathValue("id")
	if id == a.Config.AuthGateClientID {
		return errOwnClient
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.AuthGate.PurgeSubject(r.Context(), id, r.PathValue("subjectId")); err != nil {
		return agError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
