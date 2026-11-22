package handler

import (
	"fmt"
	"net/http"

	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
)

// ownerView is a tenant's counters as the operator sees them.
type ownerView struct {
	TenantID    string `json:"tenantId"`
	ClientCount int    `json:"clientCount"`
	ClientQuota int    `json:"clientQuota"`
	SuspendedAt string `json:"suspendedAt,omitempty"`
}

func writeOwner(w http.ResponseWriter, o client.Owner, err error) {
	if err != nil {
		writeServiceError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ownerView{
		TenantID: o.TenantID, ClientCount: o.ClientCount, ClientQuota: o.ClientQuota, SuspendedAt: rfc3339(o.SuspendedAt),
	})
}

func (h *admin) getOwner(w http.ResponseWriter, r *http.Request) {
	o, err := h.svc.GetOwner(r.Context(), r.PathValue("tenantId"))
	writeOwner(w, o, err)
}

// patchOwner changes the quota, the only field an operator sets on a tenant.
func (h *admin) patchOwner(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientQuota *int `json:"clientQuota"`
	}
	if err := httpx.DecodeJSON(r, &body); err != nil {
		writeServiceError(w, err)
		return
	}
	if body.ClientQuota == nil {
		writeServiceError(w, fmt.Errorf("%w: clientQuota is required", client.ErrInvalid))
		return
	}
	o, err := h.svc.SetQuota(r.Context(), r.PathValue("tenantId"), *body.ClientQuota)
	writeOwner(w, o, err)
}

// suspendOwner and unsuspendOwner ignore the request body, so an empty one works.
func (h *admin) suspendOwner(w http.ResponseWriter, r *http.Request) {
	o, err := h.svc.SuspendOwner(r.Context(), r.PathValue("tenantId"))
	writeOwner(w, o, err)
}

func (h *admin) unsuspendOwner(w http.ResponseWriter, r *http.Request) {
	o, err := h.svc.UnsuspendOwner(r.Context(), r.PathValue("tenantId"))
	writeOwner(w, o, err)
}
