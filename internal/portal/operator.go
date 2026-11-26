package portal

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
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
	mux.Handle("POST /api/operator/tenants/{tid}/suspend", guard(a.operatorSetTenantSuspended(true)))
	mux.Handle("POST /api/operator/tenants/{tid}/unsuspend", guard(a.operatorSetTenantSuspended(false)))
	mux.Handle("POST /api/operator/tenants/{tid}/audit", guard(a.operatorQuotaAudit))
	mux.Handle("POST /api/operator/users/{uid}/suspend", guard(a.operatorSetUserSuspended(true)))
	mux.Handle("POST /api/operator/users/{uid}/unsuspend", guard(a.operatorSetUserSuspended(false)))
	mux.Handle("POST /api/operator/users/{uid}/resend-verification", guard(a.operatorResendVerification))
	mux.Handle("POST /api/operator/users/{uid}/factor-reset", guard(a.operatorFactorReset))
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
	return a.writeOperatorUser(w, r, u)
}

// writeOperatorUser answers the operator's view of one user.
func (a *App) writeOperatorUser(w http.ResponseWriter, r *http.Request, u User) error {
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

// operatorGetUser reads the user in the path, or answers 404.
func (a *App) operatorGetUser(r *http.Request) (User, error) {
	u, err := a.Store.GetUser(r.Context(), r.PathValue("uid"))
	if errors.Is(err, ErrUserNotFound) {
		return User{}, apiErr(http.StatusNotFound, "not_found")
	}
	return u, err
}

const operatorActorName = "AuthGate administrator"

// auditOperator writes one entry of the operator's action in a tenant's log.
func (a *App) auditOperator(r *http.Request, tid, action string, detail map[string]string) {
	a.writeAudit(r, AuditEntry{
		TenantID: tid, ActorID: "operator", ActorName: operatorActorName, Action: action,
		Target: tid, Result: "success", IP: a.auditIP(r), Detail: detail, At: a.now(),
	})
}

// operatorSetTenantSuspended suspends or restores a workspace. Only a call that changed the state
// writes the audit entry and mails the owners, so a repeated click is a quiet 200.
func (a *App) operatorSetTenantSuspended(suspend bool) func(http.ResponseWriter, *http.Request) error {
	action, kind := "tenant-unsuspend", "tenant-unsuspended"
	if suspend {
		action, kind = "tenant-suspend", "tenant-suspended"
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		t, changed, err := a.Store.SetTenantSuspended(r.Context(), r.PathValue("tid"), suspend, a.now())
		switch {
		case errors.Is(err, ErrTenantNotFound):
			return apiErr(http.StatusNotFound, "not_found")
		case errors.Is(err, ErrTenantDeleting):
			return apiErr(http.StatusConflict, "tenant_deleting")
		case err != nil:
			return err
		}
		if changed {
			a.auditOperator(r, t.ID, action, nil)
			// Best effort, like every other notice: the state change is done, and a retry
			// (changed=false) would never send it.
			if err := a.mailTenantOwners(r, t, kind); err != nil {
				slog.Warn("portal: tenant owner notice not sent", "kind", kind, "err", err)
			}
		}
		v, err := a.operatorTenantView(r, t)
		if err != nil {
			return err
		}
		httpx.WriteJSON(w, http.StatusOK, v)
		return nil
	}
}

// mailTenantOwners sends the kind to every owner of t, naming the workspace in the owner's language.
func (a *App) mailTenantOwners(r *http.Request, t Tenant, kind string) error {
	ms, err := a.Store.tenantMemberships(r.Context(), t.ID)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if m.Role != RoleOwner {
			continue
		}
		o, err := a.Store.GetUser(r.Context(), m.UserID)
		if errors.Is(err, ErrUserNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		org := t.Name
		if t.Kind != KindOrg {
			org = pmail.PersonalLabel[o.Lang]
		}
		a.mailBestEffort(r.Context(), pmail.Message{To: o.Email, Lang: o.Lang, Kind: kind, Vars: map[string]string{"org": org}})
	}
	return nil
}

// operatorQuotaAudit records a client quota change AuthGate made. It writes the audit only.
func (a *App) operatorQuotaAudit(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Action   string `json:"action"`
		From, To *int
	}
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if in.Action != "tenant-quota" || in.From == nil || in.To == nil || *in.From < 0 || *in.To < 0 {
		return apiErr(http.StatusBadRequest, "invalid_action")
	}
	t, err := a.operatorGetTenant(r)
	if err != nil {
		return err
	}
	a.auditOperator(r, t.ID, in.Action, map[string]string{"from": strconv.Itoa(*in.From), "to": strconv.Itoa(*in.To)})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// operatorSetUserSuspended suspends a user and signs them out everywhere (the epoch bump), or restores
// them. Only a call that changed the state mails, so a repeated click is a quiet 200.
func (a *App) operatorSetUserSuspended(suspend bool) func(http.ResponseWriter, *http.Request) error {
	kind, expr, cond := "account-unsuspended", "REMOVE suspendedAt", "attribute_exists(suspendedAt)"
	if suspend {
		kind, expr, cond = "account-suspended", "SET suspendedAt = :t ADD sessionEpoch :one", "attribute_not_exists(suspendedAt)"
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx := r.Context()
		u, err := a.operatorGetUser(r)
		if err != nil {
			return err
		}
		var vals item
		if suspend {
			vals = item{":t": s(ts(a.now())), ":one": n(1)}
		}
		err = a.Store.update(ctx, u.ID, expr, cond, nil, vals)
		changed := err == nil
		if err != nil && !dynamo.IsConditionFailed(err) {
			return err
		}
		// Re-read: the answer shows the stored state, whoever changed it.
		if u, err = a.Store.GetUser(ctx, u.ID); err != nil {
			return err
		}
		// An unverified address was never confirmed, so it gets no mail.
		if changed && u.EmailVerifiedAt != nil {
			a.mailBestEffort(ctx, pmail.Message{To: u.Email, Lang: u.Lang, Kind: kind})
		}
		return a.writeOperatorUser(w, r, u)
	}
}

// operatorResendVerification mails a fresh verify link to an unverified user.
func (a *App) operatorResendVerification(w http.ResponseWriter, r *http.Request) error {
	u, err := a.operatorGetUser(r)
	if err != nil {
		return err
	}
	if u.EmailVerifiedAt != nil {
		return apiErr(http.StatusConflict, "already_verified")
	}
	if err := a.sendVerify(r.Context(), u); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "sent"})
	return nil
}

// operatorFactorReset starts the lost-factor wait directly: the operator has confirmed the person, so
// there is no confirm link. A reset already pending is kept and not mailed again.
func (a *App) operatorFactorReset(w http.ResponseWriter, r *http.Request) error {
	u, err := a.operatorGetUser(r)
	if err != nil {
		return err
	}
	if !mailable(u) {
		return apiErr(http.StatusConflict, "not_mailable")
	}
	now := a.now()
	fr, created, err := a.Store.PutFactorReset(r.Context(), u.ID, now, now.Add(factorResetWait))
	if err != nil {
		return err
	}
	if created {
		a.mailBestEffort(r.Context(), pmail.Message{To: u.Email, Lang: u.Lang, Kind: "factor-reset-started",
			Vars: map[string]string{"until": fr.DueAt.UTC().Format(untilLayout)}})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"dueAt": ts(fr.DueAt)})
	return nil
}
