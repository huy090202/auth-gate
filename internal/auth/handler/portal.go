package handler

import (
	"net/http"

	"github.com/huy090202/auth-gate/internal/auth/service"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
)

// NewPortal returns the portal API: the routes a tenant's backend (the
// Portal) calls with the portal key and an x-tenant-id. It has its own key
// check; the admin key does not open it, and it serves no admin route.
func NewPortal(svc *service.Service, portalKey string) http.Handler {
	p := &portal{svc: svc}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/portal/clients", p.tenant(p.create))
	mux.HandleFunc("GET /api/portal/clients", p.tenant(p.list))
	mux.HandleFunc("GET /api/portal/clients/{id}", p.tenant(p.get))
	mux.HandleFunc("PATCH /api/portal/clients/{id}", p.tenant(p.patch))
	mux.HandleFunc("POST /api/portal/clients/{id}/disable", p.tenant(p.disable))
	mux.HandleFunc("POST /api/portal/clients/{id}/enable", p.tenant(p.enable))
	mux.HandleFunc("POST /api/portal/clients/{id}/keys", p.tenant(p.rotate))
	mux.HandleFunc("DELETE /api/portal/clients/{id}/keys/previous", p.tenant(p.revokePrevious))
	mux.HandleFunc("DELETE /api/portal/clients/{id}/subjects/{subjectId}", p.tenant(p.purge))
	mux.HandleFunc("DELETE /api/portal/clients/{id}", p.tenant(p.delete))
	return httpx.RequireKey("x-internal-access-key", httpx.StaticKey(portalKey))(mux)
}

type portal struct{ svc *service.Service }

type tenantHandler func(t *service.Tenant, w http.ResponseWriter, r *http.Request)

// tenant scopes a route to the tenant named by x-tenant-id; a missing or
// non-ULID value answers 400 invalid_tenant.
func (p *portal) tenant(next tenantHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		t, err := p.svc.Tenant(r.Header.Get("x-tenant-id"))
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_tenant", nil)
			return
		}
		next(t, w, r)
	}
}

func (p *portal) create(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	in, err := decodeCreate(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	c, key, err := t.Create(r.Context(), in)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeCreated(w, c, key)
}

func (p *portal) list(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	items, next, err := t.List(r.Context(), r.URL.Query().Get("cursor"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeList(w, items, next)
}

func (p *portal) get(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	c, err := t.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewOf(c))
}

// patch takes name, origins, pinPolicy and tokenTtlSec; rateLimit and rpId answer 400 invalid_client.
func (p *portal) patch(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	patch, err := decodePatch(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	c, err := t.Update(r.Context(), r.PathValue("id"), patch)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewOf(c))
}

// disable and enable ignore the request body, so an empty one works.
func (p *portal) disable(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	c, err := t.Disable(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewOf(c))
}

func (p *portal) enable(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	c, err := t.Enable(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, viewOf(c))
}

func (p *portal) rotate(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	key, err := t.RotateKey(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeKey(w, key)
}

func (p *portal) revokePrevious(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	n, err := t.RevokePreviousKeys(r.Context(), r.PathValue("id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeRevoked(w, n)
}

func (p *portal) purge(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	if err := t.PurgeSubject(r.Context(), r.PathValue("id"), r.PathValue("subjectId")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (p *portal) delete(t *service.Tenant, w http.ResponseWriter, r *http.Request) {
	if err := t.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
