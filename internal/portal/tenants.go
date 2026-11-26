package portal

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

type tenantView struct {
	TenantID string `json:"tenantId"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	// Suspended is true while the operator has suspended the workspace: it is read-only.
	Suspended bool `json:"suspended"`
}

// mountTenants serves the user's workspace list and each workspace's audit log.
func (a *App) mountTenants(mux *http.ServeMux) {
	full := a.session(StageFull)
	mux.Handle("GET /api/tenants", full(webapi.HandlerFunc(a.listTenants)))
	// An organisation's log is for its owners and admins (spec section 6).
	mux.Handle("GET /api/t/{tenantId}/audit", full(a.member(RoleOwner, RoleAdmin)(webapi.HandlerFunc(a.listTenantAudit))))
}

// listTenants returns the workspaces the user belongs to, the personal one first.
// It reads the user's own membership items, so it lists no tenant they are not in.
// An organisation being deleted is not listed: its deletion is run again here,
// and a failure is logged, as the next visit tries again (spec section 4).
func (a *App) listTenants(w http.ResponseWriter, r *http.Request) error {
	_, u := sessionFrom(r.Context())
	ms, err := a.Store.ListMemberships(r.Context(), u.ID)
	if err != nil {
		return err
	}
	out := make([]tenantView, 0, len(ms))
	for _, m := range ms {
		t, err := a.Store.GetTenant(r.Context(), m.TenantID)
		if errors.Is(err, ErrTenantNotFound) {
			continue // a membership whose tenant is gone is left over, not listed
		}
		if err != nil {
			return err
		}
		if t.Kind == KindOrg && t.DeletingAt != nil {
			if err := a.finishOrgDeletion(r.Context(), t, u.ID); err != nil {
				slog.Warn("portal: organisation deletion not resumed", "tenant", t.ID, "err", err)
			}
			continue
		}
		out = append(out, tenantView{TenantID: t.ID, Kind: t.Kind, Name: t.Name, Role: m.Role, Suspended: t.SuspendedAt != nil})
	}
	slices.SortFunc(out, func(x, y tenantView) int {
		if px, py := x.Kind == "personal", y.Kind == "personal"; px != py {
			if px {
				return -1
			}
			return 1
		}
		if c := strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)); c != 0 {
			return c
		}
		return strings.Compare(x.TenantID, y.TenantID)
	})
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tenants": out})
	return nil
}

type auditView struct {
	ActorID   string            `json:"actorId"`
	ActorName string            `json:"actorName,omitempty"`
	Action    string            `json:"action"`
	Target    string            `json:"target"`
	Result    string            `json:"result"`
	IP        string            `json:"ip"`
	Detail    map[string]string `json:"detail,omitempty"`
	At        string            `json:"at"`
}

// listTenantAudit pages the workspace's log, newest first. The cursor is the
// one the previous page returned; anything else is 400 invalid_cursor.
func (a *App) listTenantAudit(w http.ResponseWriter, r *http.Request) error {
	return a.writeAuditPage(w, r, memberFrom(r.Context()).TenantID)
}

// writeAuditPage answers one page of a workspace's log; the member route and the
// operator route share it so their JSON is the same.
func (a *App) writeAuditPage(w http.ResponseWriter, r *http.Request, tenantID string) error {
	entries, next, err := a.Store.ListAudit(r.Context(), tenantID, r.URL.Query().Get("cursor"))
	if errors.Is(err, ErrInvalidCursor) {
		return apiErr(http.StatusBadRequest, "invalid_cursor")
	}
	if err != nil {
		return err
	}
	out := make([]auditView, 0, len(entries))
	for _, e := range entries {
		out = append(out, auditView{
			ActorID: e.ActorID, ActorName: e.ActorName, Action: e.Action, Target: e.Target, Result: e.Result,
			IP: e.IP, Detail: e.Detail, At: ts(e.At),
		})
	}
	res := map[string]any{"entries": out}
	if next != "" {
		res["nextCursor"] = next
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}
