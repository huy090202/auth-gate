package portal

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

type memberKeyT struct{}

type tenantKeyT struct{}

// tenantFrom returns the tenant a.member read for the route.
func tenantFrom(ctx context.Context) Tenant {
	t, _ := ctx.Value(tenantKeyT{}).(Tenant)
	return t
}

// memberFrom returns the membership a.member stored.
func memberFrom(ctx context.Context) Membership {
	m, _ := ctx.Value(memberKeyT{}).(Membership)
	return m
}

// member guards a route under /api/t/{tenantId}/. It runs inside a.session. The
// tenant is the one in the URL, never the session's active tenant. The
// membership is read with a consistent read on every request: a tenant id that
// is malformed, unknown or not the user's answers the same 404 not_found (so
// the answer does not say which tenants exist), and a role not in roles is 403
// forbidden. With no roles, any member passes. The tenant is read after the
// membership: one that is gone, or an organisation being deleted (unless
// allowDeleting), is the same 404 (a personal workspace being deleted by account
// deletion keeps its own 409 on the routes that write to it), so a member's open tab stops working once deletion starts.
func (a *App) memberAny(allowDeleting bool, roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, u := sessionFrom(r.Context())
			tenantID := r.PathValue("tenantId")
			if id.ValidateULID(tenantID) != nil {
				notFound(w, r)
				return
			}
			m, err := a.Store.GetMembership(r.Context(), tenantID, u.ID)
			if errors.Is(err, ErrNotMember) {
				notFound(w, r)
				return
			}
			if err != nil {
				webapi.WriteErr(w, err)
				return
			}
			t, err := a.Store.GetTenant(r.Context(), tenantID)
			if errors.Is(err, ErrTenantNotFound) || (err == nil && t.Kind == KindOrg && t.DeletingAt != nil && !allowDeleting) {
				notFound(w, r)
				return
			}
			if err != nil {
				webapi.WriteErr(w, err)
				return
			}
			setAuditTenant(r.Context(), tenantID)
			if len(roles) > 0 && !slices.Contains(roles, m.Role) {
				webapi.WriteErr(w, &webapi.Error{Status: http.StatusForbidden, Code: "forbidden"})
				return
			}
			ctx := context.WithValue(context.WithValue(r.Context(), memberKeyT{}, m), tenantKeyT{}, t)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// member is memberAny for a tenant that is not being deleted.
func (a *App) member(roles ...string) func(http.Handler) http.Handler {
	return a.memberAny(false, roles...)
}
