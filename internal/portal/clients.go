package portal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// clientID returns the {id} of the route, or 404 when it cannot be a client's.
// A segment that is not a plain id never goes into an AuthGate path.
func clientID(r *http.Request) (string, error) {
	v := r.PathValue("id")
	if id.ValidateULID(v) != nil {
		return "", apiErr(http.StatusNotFound, "not_found")
	}
	return v, nil
}

// subjectID returns the {subjectId} of the route: AuthGate's rule minus what
// could change a path (webapi.ValidSubject).
func subjectID(r *http.Request) (string, error) {
	v := r.PathValue("subjectId")
	if !webapi.ValidSubject(v) {
		return "", apiErr(http.StatusBadRequest, "invalid_id")
	}
	return v, nil
}

// tenantAG is AuthGate's portal API for the workspace in the URL. The tenant
// comes from the membership a.member checked, never from the body or the session.
func (a *App) tenantAG(r *http.Request) *authgate.TenantClient {
	return a.AuthGate.Tenant(memberFrom(r.Context()).TenantID)
}

// mountClients serves a workspace's clients. Every route runs the session guard,
// then the membership guard on the URL's tenant (the isolation of the Portal:
// a tenant that is not the caller's answers 404 before AuthGate is called), and
// AuthGate's own owner check stands behind it.
func (a *App) mountClients(mux *http.ServeMux) {
	full := a.session(StageFull)
	const base = "/api/t/{tenantId}/clients"
	all := a.member()
	editors := a.member(RoleOwner, RoleAdmin, RoleMember)
	admins := a.member(RoleOwner, RoleAdmin)
	route := func(pattern, action string, guard func(http.Handler) http.Handler, h http.Handler) {
		mux.Handle(pattern, a.audited(action, full(guard(a.writable(h)))))
	}
	read := func(pattern string, h webapi.HandlerFunc) { mux.Handle(pattern, full(all(h))) }
	read("GET "+base, a.listClients)
	read("GET "+base+"/{id}", a.getClient)
	route("POST "+base, "client-create", editors, webapi.HandlerFunc(a.createClient))
	route("PATCH "+base+"/{id}", "client-update", editors, webapi.HandlerFunc(a.updateClient))
	route("POST "+base+"/{id}/keys", "client-rotate-key", editors, a.clientWrite("rotate-key", http.StatusCreated,
		func(ctx context.Context, t *authgate.TenantClient, id string) (json.RawMessage, error) {
			return t.RotateKey(ctx, id)
		}))
	route("DELETE "+base+"/{id}/keys/previous", "client-revoke-previous-keys", editors, a.clientWrite("revoke-previous-keys", http.StatusOK,
		func(ctx context.Context, t *authgate.TenantClient, id string) (json.RawMessage, error) {
			raw, err := t.RevokePreviousKeys(ctx, id)
			var out struct {
				Revoked int `json:"revoked"`
			}
			if err == nil && json.Unmarshal(raw, &out) == nil {
				setAuditDetail(ctx, "revoked", strconv.Itoa(out.Revoked))
			}
			return raw, err
		}))
	route("POST "+base+"/{id}/disable", "client-disable", admins, a.clientWrite("disable-client", http.StatusOK,
		func(ctx context.Context, t *authgate.TenantClient, id string) (json.RawMessage, error) {
			return t.Disable(ctx, id)
		}))
	route("POST "+base+"/{id}/enable", "client-enable", admins, webapi.HandlerFunc(a.enableClient))
	route("DELETE "+base+"/{id}/subjects/{subjectId}", "client-purge-subject", admins, webapi.HandlerFunc(a.purgeSubject))
	route("DELETE "+base+"/{id}", "client-delete", admins, a.clientWrite("delete-client", http.StatusNoContent,
		func(ctx context.Context, t *authgate.TenantClient, id string) (json.RawMessage, error) {
			return nil, t.Delete(ctx, id)
		}))
}

func (a *App) listClients(w http.ResponseWriter, r *http.Request) error {
	raw, err := a.tenantAG(r).List(r.Context(), r.URL.Query().Get("cursor"))
	if err != nil {
		return webapi.AuthGateError(err)
	}
	webapi.WriteRaw(w, http.StatusOK, raw)
	return nil
}

func (a *App) getClient(w http.ResponseWriter, r *http.Request) error {
	cid, err := clientID(r)
	if err != nil {
		return err
	}
	raw, err := a.tenantAG(r).Get(r.Context(), cid)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	webapi.WriteRaw(w, http.StatusOK, raw)
	return nil
}

type pinPolicyBody struct {
	Length      int `json:"length"`
	MaxAttempts int `json:"maxAttempts"`
	LockoutSec  int `json:"lockoutSec"`
}

// createClient registers a client in the workspace. Only the fields a user may
// set are passed on; owner, rateLimit and anything else is dropped, and
// AuthGate forces the rest. A workspace being deleted takes no new client.
func (a *App) createClient(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Name        string         `json:"name"`
		RPID        string         `json:"rpId"`
		Origins     []string       `json:"origins"`
		PinPolicy   *pinPolicyBody `json:"pinPolicy"`
		TokenTTLSec *int           `json:"tokenTtlSec"`
	}
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	t, err := a.Store.GetTenant(ctx, memberFrom(ctx).TenantID)
	if err != nil {
		return err
	}
	if t.DeletingAt != nil {
		return apiErr(http.StatusConflict, "tenant_deleting")
	}
	raw, err := a.tenantAG(r).Create(ctx, in)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	var out struct {
		ClientID string `json:"clientId"`
	}
	_ = json.Unmarshal(raw, &out)
	// A deletion that set deletingAt after the check above lists the workspace's clients
	// after that mark. Either this read sees the mark and the client is deleted here, or the
	// mark came later and the deletion's list sees the client (spec section 4).
	tid := t.ID
	if t, err = a.Store.GetTenant(ctx, tid); err != nil {
		// The caller never gets the key, so the client must not survive.
		deleted := a.deleteOrphan(ctx, tid, out.ClientID)
		slog.Warn("portal: tenant re-read failed after a client create", "tenant", tid, "client", out.ClientID, "deleted", deleted, "err", err)
		return err
	}
	if t.DeletingAt != nil {
		a.deleteOrphan(ctx, t.ID, out.ClientID)
		if t.Kind == KindOrg {
			return apiErr(http.StatusNotFound, "not_found")
		}
		return apiErr(http.StatusConflict, "tenant_deleting")
	}
	// What was typed is recorded only now that AuthGate has accepted it.
	setAuditDetail(ctx, "name", in.Name)
	setAuditDetail(ctx, "rpId", in.RPID)
	setAuditDetail(ctx, "origins", strings.Join(in.Origins, ","))
	setAuditTarget(ctx, out.ClientID)
	webapi.WriteRaw(w, http.StatusCreated, raw)
	return nil
}

// deleteOrphan deletes a client that must not survive: one created in a workspace that
// began deleting meanwhile, or whose key the caller never got. It reports whether the
// client is gone (404 counts). It runs on its own context, so a caller that went away
// cannot cancel it. A failure is logged (never the key): the client then stays live at
// AuthGate until an operator disables or deletes it.
func (a *App) deleteOrphan(ctx context.Context, tenantID, clientID string) bool {
	if clientID == "" {
		slog.Error("portal: client created, id unreadable", "tenant", tenantID)
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sideEffectLimit)
	defer cancel()
	var ae *authgate.Error
	err := a.AuthGate.Tenant(tenantID).Delete(ctx, clientID)
	if err != nil && (!errors.As(err, &ae) || ae.Status != http.StatusNotFound) {
		slog.Error("portal: orphaned client was not deleted", "tenant", tenantID, "client", clientID, "err", err)
		return false
	}
	return true
}

// updateClient patches a client's name, origins, PIN policy and token lifetime.
// rateLimit and rpId are passed on as they came so AuthGate refuses them.
func (a *App) updateClient(w http.ResponseWriter, r *http.Request) error {
	cid, err := clientID(r)
	if err != nil {
		return err
	}
	var in struct {
		Name        *string         `json:"name,omitempty"`
		Origins     *[]string       `json:"origins,omitempty"` // a pointer, so [] is sent as [] and absent stays absent
		PinPolicy   *pinPolicyBody  `json:"pinPolicy,omitempty"`
		TokenTTLSec *int            `json:"tokenTtlSec,omitempty"`
		RateLimit   json.RawMessage `json:"rateLimit,omitempty"`
		RPID        json.RawMessage `json:"rpId,omitempty"`
	}
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	ctx := r.Context()
	raw, err := a.tenantAG(r).Update(ctx, cid, in)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	setAuditTarget(ctx, cid)
	var fields []string
	for k, set := range map[string]bool{
		"name": in.Name != nil, "origins": in.Origins != nil, "pinPolicy": in.PinPolicy != nil, "tokenTtlSec": in.TokenTTLSec != nil,
	} {
		if set {
			fields = append(fields, k)
		}
	}
	slices.Sort(fields)
	setAuditDetail(ctx, "fields", strings.Join(fields, ","))
	webapi.WriteRaw(w, http.StatusOK, raw)
	return nil
}

// clientWrite is a step-up action on a client: the token is checked first and
// spent only when the call is about to go out. An answer that carries a key is
// no-store like every /api answer.
func (a *App) clientWrite(action string, status int, call func(context.Context, *authgate.TenantClient, string) (json.RawMessage, error)) http.Handler {
	return webapi.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		cid, err := clientID(r)
		if err != nil {
			return err
		}
		setAuditTarget(r.Context(), cid)
		x, _ := sessionFrom(r.Context())
		tok, err := a.checkStepUp(r, x, action, cid)
		if err != nil {
			return err
		}
		if err := a.spendStepUp(r, tok); err != nil {
			return err
		}
		raw, err := call(r.Context(), a.tenantAG(r), cid)
		if err != nil {
			return webapi.AuthGateError(err)
		}
		if status == http.StatusNoContent {
			w.WriteHeader(status)
			return nil
		}
		webapi.WriteRaw(w, status, raw)
		return nil
	})
}

func (a *App) enableClient(w http.ResponseWriter, r *http.Request) error {
	cid, err := clientID(r)
	if err != nil {
		return err
	}
	setAuditTarget(r.Context(), cid)
	raw, err := a.tenantAG(r).Enable(r.Context(), cid)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	webapi.WriteRaw(w, http.StatusOK, raw)
	return nil
}

func (a *App) purgeSubject(w http.ResponseWriter, r *http.Request) error {
	cid, err := clientID(r)
	if err != nil {
		return err
	}
	subject, err := subjectID(r)
	if err != nil {
		return err
	}
	target := cid + "/" + subject
	setAuditTarget(r.Context(), target)
	x, _ := sessionFrom(r.Context())
	tok, err := a.checkStepUp(r, x, "purge-subject", target)
	if err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if err := a.tenantAG(r).PurgeSubject(r.Context(), cid, subject); err != nil {
		return webapi.AuthGateError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
