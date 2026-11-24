package portal

import (
	"errors"
	"net/http"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// orgErr maps the store's organisation errors to the API's. Errors it does not
// know pass through unchanged. A tenant being deleted answers like one that is gone.
func orgErr(err error) error {
	switch {
	case errors.Is(err, ErrOrgQuota):
		return apiErr(http.StatusConflict, "org_quota_exceeded")
	case errors.Is(err, ErrLastOwner):
		return apiErr(http.StatusConflict, "last_owner")
	case errors.Is(err, ErrPersonal):
		return apiErr(http.StatusBadRequest, "personal_workspace")
	case errors.Is(err, ErrTenantDeleting), errors.Is(err, ErrTenantNotFound), errors.Is(err, ErrNotMember):
		return apiErr(http.StatusNotFound, "not_found")
	case errors.Is(err, ErrMembersFull):
		return apiErr(http.StatusConflict, "members_full")
	case errors.Is(err, ErrInvitesFull):
		return apiErr(http.StatusConflict, "invites_full")
	case errors.Is(err, ErrAlreadyMember):
		return apiErr(http.StatusConflict, "already_member")
	case errors.Is(err, ErrAlreadyInvited):
		return apiErr(http.StatusConflict, "already_invited")
	case errors.Is(err, ErrInviteNotFound):
		return apiErr(http.StatusNotFound, "invite_not_found")
	case errors.Is(err, ErrConflict):
		return apiErr(http.StatusConflict, "conflict")
	}
	return err
}

var errForbidden = apiErr(http.StatusForbidden, "forbidden")

// mountOrgs serves organisation creation, renaming and member management.
func (a *App) mountOrgs(mux *http.ServeMux) {
	full := a.session(StageFull)
	const base = "/api/t/{tenantId}"
	all := a.member()
	owners := a.member(RoleOwner)
	admins := a.member(RoleOwner, RoleAdmin)
	// A tenant guard sets the audit scope itself; creation names the new tenant in its handler.
	mux.Handle("POST /api/tenants", a.audited("org-create", full(webapi.HandlerFunc(a.createOrg))))
	mux.Handle("PATCH "+base, a.audited("org-rename", full(owners(webapi.HandlerFunc(a.renameOrg)))))
	mux.Handle("GET "+base+"/members", full(all(webapi.HandlerFunc(a.listOrgMembers))))
	mux.Handle("PATCH "+base+"/members/{userId}", a.audited("member-role-change", full(admins(webapi.HandlerFunc(a.changeRole)))))
	mux.Handle("DELETE "+base+"/members/{userId}", a.audited("member-remove", full(admins(webapi.HandlerFunc(a.removeMember)))))
	mux.Handle("POST "+base+"/leave", a.audited("member-leave", full(all(webapi.HandlerFunc(a.leaveOrg)))))
}

func (a *App) createOrg(w http.ResponseWriter, r *http.Request) error {
	_, u := sessionFrom(r.Context())
	var in struct{ Name string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	name, ok := validOrgName(in.Name)
	if !ok {
		return apiErr(http.StatusBadRequest, "invalid_name")
	}
	t, err := a.Store.CreateOrg(r.Context(), u, id.New(), name, a.now())
	if err != nil {
		return orgErr(err)
	}
	setAuditTenant(r.Context(), t.ID)
	setAuditTarget(r.Context(), t.ID)
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"tenantId": t.ID, "kind": t.Kind, "name": t.Name, "role": RoleOwner})
	return nil
}

func (a *App) renameOrg(w http.ResponseWriter, r *http.Request) error {
	t := tenantFrom(r.Context())
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	var in struct{ Name string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	name, ok := validOrgName(in.Name)
	if !ok {
		return apiErr(http.StatusBadRequest, "invalid_name")
	}
	setAuditTarget(r.Context(), t.ID)
	if err := a.Store.RenameOrg(r.Context(), t.ID, name); err != nil {
		return orgErr(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"name": name})
	return nil
}

func (a *App) listOrgMembers(w http.ResponseWriter, r *http.Request) error {
	t := tenantFrom(r.Context())
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	ms, err := a.Store.ListMembers(r.Context(), t.ID)
	if err != nil {
		return err
	}
	type view struct {
		UserID      string `json:"userId"`
		DisplayName string `json:"displayName"`
		Email       string `json:"email"`
		Role        string `json:"role"`
		JoinedAt    string `json:"joinedAt"`
	}
	out := make([]view, 0, len(ms))
	for _, m := range ms {
		out = append(out, view{m.UserID, m.DisplayName, m.Email, m.Role, ts(m.JoinedAt)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"members": out})
	return nil
}

// memberTargetID is the {userId} of the route. One that cannot be a user's is
// 404, like a member who is not there.
func memberTargetID(r *http.Request) (string, error) {
	v := r.PathValue("userId")
	if id.ValidateULID(v) != nil {
		return "", apiErr(http.StatusNotFound, "not_found")
	}
	return v, nil
}

// canChangeRole says whether the actor may move a member from old to role.
// An owner may do anything; an admin only moves a non-owner between admin and member.
func canChangeRole(actor, old, role string) bool {
	switch actor {
	case RoleOwner:
		return true
	case RoleAdmin:
		return old != RoleOwner && role != RoleOwner
	}
	return false
}

// canRemove says whether the actor may remove a member with the given role.
func canRemove(actor, actorID, target, targetID string) bool {
	switch actor {
	case RoleOwner:
		return actorID != targetID
	case RoleAdmin:
		return target != RoleOwner
	}
	return false
}

func (a *App) changeRole(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	ctx := r.Context()
	t, actor := tenantFrom(ctx), memberFrom(ctx)
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	uid, err := memberTargetID(r)
	if err != nil {
		return err
	}
	tok, err := a.checkStepUp(r, x, "change-role", t.ID+"/"+uid)
	if err != nil {
		return err
	}
	var in struct{ Role string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if in.Role != RoleOwner && in.Role != RoleAdmin && in.Role != RoleMember {
		return apiErr(http.StatusBadRequest, "invalid_role")
	}
	setAuditTarget(ctx, uid)
	setAuditDetail(ctx, "to", in.Role)
	target, err := a.Store.GetMembership(ctx, t.ID, uid)
	if err != nil {
		return orgErr(err)
	}
	setAuditDetail(ctx, "from", target.Role)
	if !canChangeRole(actor.Role, target.Role, in.Role) {
		return errForbidden
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	old, err := a.Store.SetRole(ctx, t.ID, actor, target, in.Role)
	if err != nil {
		return orgErr(err)
	}
	setAuditDetail(ctx, "from", old)
	if old != in.Role && uid != u.ID {
		a.mailMember(r, uid, "org-role-changed", map[string]string{"org": t.Name, "role": in.Role})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"role": in.Role})
	return nil
}

func (a *App) removeMember(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	ctx := r.Context()
	t, actor := tenantFrom(ctx), memberFrom(ctx)
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	uid, err := memberTargetID(r)
	if err != nil {
		return err
	}
	tok, err := a.checkStepUp(r, x, "remove-member", t.ID+"/"+uid)
	if err != nil {
		return err
	}
	target, err := a.Store.GetMembership(ctx, t.ID, uid)
	if err != nil {
		return orgErr(err)
	}
	setAuditTarget(ctx, uid)
	if !canRemove(actor.Role, actor.UserID, target.Role, uid) {
		return errForbidden
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	removed, err := a.Store.RemoveMember(ctx, t.ID, actor, target)
	if err != nil {
		return orgErr(err)
	}
	setAuditDetail(ctx, "role", removed.Role)
	if uid != u.ID {
		a.mailMember(r, uid, "org-removed", map[string]string{"org": t.Name})
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *App) leaveOrg(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	t, actor := tenantFrom(ctx), memberFrom(ctx)
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	setAuditTarget(ctx, actor.UserID)
	if _, err := a.Store.RemoveMember(ctx, t.ID, actor, actor); err != nil {
		return orgErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// mailMember sends an organisation notice to a member in their own language (see mailUser).
func (a *App) mailMember(r *http.Request, userID, kind string, vars map[string]string) {
	a.mailUser(r.Context(), userID, kind, vars)
}
