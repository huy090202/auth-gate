package portal

import (
	"errors"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// operatorLimit is the one bucket of the operator key: 60 at once, then one per second.
var operatorLimit = ratelimit.Settings{Burst: 60, PerSecond: 1}

// mountOperator registers /api/operator/*, the routes the Console calls with the
// operator key. They sit behind the key and a per-key limit, not a session.
func (a *App) mountOperator(mux *http.ServeMux) {
	guard := func(f func(http.ResponseWriter, *http.Request) error) http.Handler {
		// The key is read per request so the config stays the single source.
		h := a.limitOperator(webapi.HandlerFunc(f))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.RequireKey("x-operator-key", httpx.StaticKey(a.Config.OperatorKey))(h).ServeHTTP(w, r)
		})
	}
	mux.Handle("GET /api/operator/tenants", guard(a.operatorTenants))
	mux.Handle("GET /api/operator/tenants/{tid}", guard(a.operatorTenant))
	mux.Handle("GET /api/operator/tenants/{tid}/audit", guard(a.operatorTenantAudit))
	mux.Handle("GET /api/operator/users", guard(a.operatorUser))
}

func (a *App) limitOperator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d := a.Limiter.Allow(r.Context(), "operator", operatorLimit); !d.Allowed {
			webapi.WriteErr(w, rateLimited(d.RetryAfterSec))
			return
		}
		next.ServeHTTP(w, r)
	})
}

type operatorTenantView struct {
	TenantID     string `json:"tenantId"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	CreatedAt    string `json:"createdAt"`
	MemberCount  int    `json:"memberCount"`
	CreatorEmail string `json:"creatorEmail"`
	SuspendedAt  string `json:"suspendedAt,omitempty"`
	DeletingAt   string `json:"deletingAt,omitempty"`
}

func (a *App) operatorTenantView(r *http.Request, t Tenant) (operatorTenantView, error) {
	v := operatorTenantView{TenantID: t.ID, Name: t.Name, Kind: t.Kind, CreatedAt: ts(t.CreatedAt), MemberCount: t.Counts.Members}
	if t.Kind != KindOrg {
		v.MemberCount = 1 // a personal workspace has its owner only and keeps no counter
	}
	// A creator who is gone leaves the address empty rather than failing the list.
	if u, err := a.Store.GetUser(r.Context(), t.CreatedBy); err == nil {
		v.CreatorEmail = u.Email
	} else if !errors.Is(err, ErrUserNotFound) {
		return v, err
	}
	if t.SuspendedAt != nil {
		v.SuspendedAt = ts(*t.SuspendedAt)
	}
	if t.DeletingAt != nil {
		v.DeletingAt = ts(*t.DeletingAt)
	}
	return v, nil
}

func (a *App) operatorTenants(w http.ResponseWriter, r *http.Request) error {
	ts_, next, err := a.Store.ListTenants(r.Context(), r.URL.Query().Get("cursor"))
	if errors.Is(err, ErrInvalidCursor) {
		return apiErr(http.StatusBadRequest, "invalid_cursor")
	}
	if err != nil {
		return err
	}
	out := make([]operatorTenantView, 0, len(ts_))
	for _, t := range ts_ {
		v, err := a.operatorTenantView(r, t)
		if err != nil {
			return err
		}
		out = append(out, v)
	}
	res := map[string]any{"tenants": out}
	if next != "" {
		res["nextCursor"] = next
	}
	httpx.WriteJSON(w, http.StatusOK, res)
	return nil
}

func (a *App) operatorGetTenant(r *http.Request) (Tenant, error) {
	t, err := a.Store.GetTenant(r.Context(), r.PathValue("tid"))
	if errors.Is(err, ErrTenantNotFound) {
		return Tenant{}, apiErr(http.StatusNotFound, "not_found")
	}
	return t, err
}

func (a *App) operatorTenant(w http.ResponseWriter, r *http.Request) error {
	t, err := a.operatorGetTenant(r)
	if err != nil {
		return err
	}
	v, err := a.operatorTenantView(r, t)
	if err != nil {
		return err
	}
	type memberView struct {
		UserID      string `json:"userId"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
		Role        string `json:"role"`
		JoinedAt    string `json:"joinedAt"`
	}
	members := []memberView{}
	ms, err := a.Store.ListMembers(r.Context(), t.ID)
	if err != nil {
		return err
	}
	for _, m := range ms {
		members = append(members, memberView{m.UserID, m.DisplayName, m.Email, m.Role, ts(m.JoinedAt)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tenant": v, "members": members, "pendingInvites": t.Counts.Invites})
	return nil
}

// operatorTenantAudit answers exactly what GET /api/t/{tid}/audit answers.
func (a *App) operatorTenantAudit(w http.ResponseWriter, r *http.Request) error {
	if _, err := a.operatorGetTenant(r); err != nil {
		return err
	}
	return a.writeAuditPage(w, r, r.PathValue("tid"))
}

func (a *App) operatorUser(w http.ResponseWriter, r *http.Request) error {
	u, err := a.Store.GetUserByEmail(r.Context(), r.URL.Query().Get("email"))
	if errors.Is(err, ErrUserNotFound) {
		return apiErr(http.StatusNotFound, "not_found")
	}
	if err != nil {
		return err
	}
	f, err := a.AuthGate.Factors(r.Context(), u.ID)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	count := len(f.WebAuthn)
	if f.PIN {
		count++
	}
	ms, err := a.Store.ListMemberships(r.Context(), u.ID)
	if err != nil {
		return err
	}
	type tenantRef struct {
		TenantID string `json:"tenantId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Role     string `json:"role"`
	}
	tenants := []tenantRef{}
	for _, m := range ms {
		t, err := a.Store.GetTenant(r.Context(), m.TenantID)
		if errors.Is(err, ErrTenantNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		tenants = append(tenants, tenantRef{t.ID, t.Name, t.Kind, m.Role})
	}
	out := map[string]any{
		"userId": u.ID, "email": u.Email, "displayName": u.DisplayName, "factorCount": count, "tenants": tenants,
	}
	for k, t := range map[string]*time.Time{"emailVerifiedAt": u.EmailVerifiedAt, "suspendedAt": u.SuspendedAt, "lastSignInAt": u.LastSignInAt} {
		if t != nil {
			out[k] = ts(*t)
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}
