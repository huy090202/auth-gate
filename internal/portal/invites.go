package portal

import (
	"context"
	"errors"
	"net/http"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
)

// inviteDayLimit caps the invites one organisation can send (and re-send) in a
// day. Revoking an invite does not give one back.
var inviteDayLimit = ratelimit.Settings{Burst: 20, PerSecond: 20.0 / 86400}

// mountInvites serves the invites of an organisation (for its admins) and the
// three routes an invitee uses with the token from the link's fragment.
func (a *App) mountInvites(mux *http.ServeMux) {
	full := a.session(StageFull)
	const base = "/api/t/{tenantId}/invites"
	admins := a.member(RoleOwner, RoleAdmin)
	mux.Handle("GET "+base, full(admins(webapi.HandlerFunc(a.listInvites))))
	mux.Handle("POST "+base, a.audited("invite-create", full(admins(webapi.HandlerFunc(a.createInvite)))))
	mux.Handle("POST "+base+"/{inviteId}/resend", a.audited("invite-resend", full(admins(webapi.HandlerFunc(a.resendInvite)))))
	mux.Handle("DELETE "+base+"/{inviteId}", a.audited("invite-revoke", full(admins(webapi.HandlerFunc(a.revokeInvite)))))
	// The invitee's routes name the tenant in the path but are not member routes: the caller is not a member yet.
	mux.Handle("POST /api/invites/{tenantId}/lookup", webapi.HandlerFunc(a.lookupInvite))
	mux.Handle("POST /api/invites/{tenantId}/accept", a.audited("invite-accept", full(webapi.HandlerFunc(a.acceptInvite))))
	mux.Handle("POST /api/invites/{tenantId}/decline", a.audited("invite-decline", full(webapi.HandlerFunc(a.declineInvite))))
}

var errInviteNotFound = apiErr(http.StatusNotFound, "invite_not_found")

func validRole(r string) bool { return r == RoleOwner || r == RoleAdmin || r == RoleMember }

func (a *App) listInvites(w http.ResponseWriter, r *http.Request) error {
	t := tenantFrom(r.Context())
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	invs, err := a.Store.ListInvites(r.Context(), t.ID, a.now())
	if err != nil {
		return orgErr(err)
	}
	type inviter struct {
		UserID      string `json:"userId"`
		DisplayName string `json:"displayName"`
	}
	type view struct {
		InviteID  string  `json:"inviteId"`
		Email     string  `json:"email"`
		Role      string  `json:"role"`
		InvitedBy inviter `json:"invitedBy"`
		ExpiresAt string  `json:"expiresAt"`
	}
	names := map[string]string{}
	out := make([]view, 0, len(invs))
	for _, inv := range invs {
		name, seen := names[inv.InvitedBy]
		if !seen {
			if u, err := a.Store.GetUser(r.Context(), inv.InvitedBy); err == nil {
				name = u.DisplayName
			} else if !errors.Is(err, ErrUserNotFound) {
				return err
			}
			names[inv.InvitedBy] = name
		}
		out = append(out, view{inv.ID, inv.Email, inv.Role, inviter{inv.InvitedBy, name}, ts(inv.ExpiresAt)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"invites": out})
	return nil
}

// spendInviteDay spends one of the organisation's invites for the day.
func (a *App) spendInviteDay(r *http.Request, tenantID string) error {
	if d := a.Limiter.Allow(r.Context(), "invite#"+tenantID, inviteDayLimit); !d.Allowed {
		return rateLimited(d.RetryAfterSec)
	}
	return nil
}

// mailInvite sends the invite with its link. The mail is in the inviter's language.
func (a *App) mailInvite(r *http.Request, inviter User, orgName string, inv Invite, token string) {
	a.mailBestEffort(r.Context(), pmail.Message{To: inv.Email, Lang: inviter.Lang, Kind: "org-invite", Vars: map[string]string{
		"link":    a.Config.PublicBaseURL + "/invite/" + inv.TenantID + "#token=" + token,
		"org":     orgName,
		"inviter": inviterLabel(inviter.DisplayName),
		"role":    inv.Role,
	}})
}

func (a *App) createInvite(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	_, u := sessionFrom(ctx)
	t, actor := tenantFrom(ctx), memberFrom(ctx)
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	var in struct{ Email, Role string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	email, ok := parseEmail(in.Email)
	if !ok {
		return apiErr(http.StatusBadRequest, "invalid_email")
	}
	if !validRole(in.Role) {
		return apiErr(http.StatusBadRequest, "invalid_role")
	}
	setAuditTarget(ctx, email)
	setAuditDetail(ctx, "role", in.Role)
	if in.Role == RoleOwner && actor.Role != RoleOwner {
		return errForbidden
	}
	existing, err := a.Store.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
		if _, err := a.Store.GetMembership(ctx, t.ID, existing.ID); err == nil {
			return orgErr(ErrAlreadyMember)
		} else if !errors.Is(err, ErrNotMember) {
			return err
		}
	case !errors.Is(err, ErrUserNotFound):
		return err
	}
	if err := a.spendInviteDay(r, t.ID); err != nil {
		return err
	}
	token, hash, err := a.newToken()
	if err != nil {
		return err
	}
	inv := Invite{ID: id.New(), TenantID: t.ID, Email: email, Role: in.Role, InvitedBy: u.ID, TokenHash: hash}
	if err := a.Store.CreateInvite(ctx, inv, a.now()); err != nil {
		return orgErr(err)
	}
	inv.ExpiresAt = a.now().Add(inviteTTL)
	a.mailInvite(r, u, t.Name, inv, token)
	httpx.WriteJSON(w, http.StatusCreated, map[string]string{"inviteId": inv.ID, "email": email, "role": inv.Role, "expiresAt": ts(inv.ExpiresAt)})
	return nil
}

func inviteIDOf(r *http.Request) (string, error) {
	v := r.PathValue("inviteId")
	if id.ValidateULID(v) != nil {
		return "", errInviteNotFound
	}
	return v, nil
}

func (a *App) resendInvite(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	_, u := sessionFrom(ctx)
	t := tenantFrom(ctx)
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	inviteID, err := inviteIDOf(r)
	if err != nil {
		return err
	}
	if err := a.spendInviteDay(r, t.ID); err != nil {
		return err
	}
	token, hash, err := a.newToken()
	if err != nil {
		return err
	}
	inv, err := a.Store.ResendInvite(ctx, t.ID, inviteID, hash, a.now())
	if err != nil {
		return orgErr(err)
	}
	setAuditTarget(ctx, inv.Email)
	a.mailInvite(r, u, t.Name, inv, token)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"inviteId": inv.ID, "email": inv.Email, "role": inv.Role, "expiresAt": ts(inv.ExpiresAt)})
	return nil
}

func (a *App) revokeInvite(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	t := tenantFrom(ctx)
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	inviteID, err := inviteIDOf(r)
	if err != nil {
		return err
	}
	inv, err := a.Store.RevokeInvite(ctx, t.ID, inviteID)
	if err != nil {
		return orgErr(err)
	}
	setAuditTarget(ctx, inv.Email)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// optionalFullUser returns the signed-in user when the request carries a valid
// full session, read as the session guard reads it: the session is unexpired
// and the user is verified, not suspended and on the session's epoch. Anything
// else is simply "no user"; it never fails the request.
func (a *App) optionalFullUser(r *http.Request) (User, bool) {
	c, err := r.Cookie(a.cookieName())
	if err != nil || c.Value == "" {
		return User{}, false
	}
	x, err := a.Store.GetSession(r.Context(), hashToken(c.Value))
	if err != nil || x.Stage != StageFull || !a.now().Before(x.web().ExpiresAt()) {
		return User{}, false
	}
	u, err := a.Store.GetUser(r.Context(), x.UserID)
	if err != nil || u.EmailVerifiedAt == nil || u.SuspendedAt != nil || u.SessionEpoch != x.Epoch {
		return User{}, false
	}
	return u, true
}

// inviteBody reads {token} and the tenant of the route. A tenant id that cannot
// be one is the same 404 as a token that matches nothing.
func inviteBody(r *http.Request) (tenantID, tokenHash string, err error) {
	var in struct{ Token string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return "", "", err
	}
	tenantID = r.PathValue("tenantId")
	if id.ValidateULID(tenantID) != nil {
		return "", "", errInviteNotFound
	}
	return tenantID, hashToken(in.Token), nil
}

// peekInvite finds the live invite for a token in an organisation that is not
// being deleted. Every way of not finding it is the same 404.
func (a *App) peekInvite(ctx context.Context, tenantID, tokenHash string) (Tenant, Invite, error) {
	t, err := a.Store.GetTenant(ctx, tenantID)
	if errors.Is(err, ErrTenantNotFound) || (err == nil && (t.Kind != KindOrg || t.DeletingAt != nil)) {
		return Tenant{}, Invite{}, errInviteNotFound
	}
	if err != nil {
		return Tenant{}, Invite{}, err
	}
	inv, err := a.Store.PeekInvite(ctx, tenantID, tokenHash, a.now())
	if err != nil {
		return Tenant{}, Invite{}, orgErr(err)
	}
	return t, inv, nil
}

func (a *App) lookupInvite(w http.ResponseWriter, r *http.Request) error {
	tenantID, hash, err := inviteBody(r)
	if err != nil {
		return err
	}
	if err := a.spendIP(r, "invite-lookup", loginIPLimit); err != nil {
		return err
	}
	ctx := r.Context()
	t, inv, err := a.peekInvite(ctx, tenantID, hash)
	if err != nil {
		return err
	}
	inviterName := ""
	if by, err := a.Store.GetUser(ctx, inv.InvitedBy); err == nil {
		inviterName = inviterLabel(by.DisplayName)
	} else if !errors.Is(err, ErrUserNotFound) {
		return err
	}
	out := map[string]any{"orgName": t.Name, "inviterName": inviterName, "role": inv.Role, "email": inv.Email}
	if u, ok := a.optionalFullUser(r); ok {
		out["emailMatches"] = NormalizeEmail(u.Email) == inv.Email
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// ownInvite reads the invite for a signed-in caller and checks that it is theirs.
func (a *App) ownInvite(r *http.Request) (Invite, User, error) {
	_, u := sessionFrom(r.Context())
	tenantID, hash, err := inviteBody(r)
	if err != nil {
		return Invite{}, u, err
	}
	_, inv, err := a.peekInvite(r.Context(), tenantID, hash)
	if err != nil {
		return Invite{}, u, err
	}
	setAuditTenant(r.Context(), tenantID)
	setAuditTarget(r.Context(), inv.Email)
	if NormalizeEmail(u.Email) != inv.Email {
		return Invite{}, u, apiErr(http.StatusForbidden, "invite_wrong_email")
	}
	return inv, u, nil
}

func (a *App) acceptInvite(w http.ResponseWriter, r *http.Request) error {
	inv, u, err := a.ownInvite(r)
	if err != nil {
		return err
	}
	setAuditDetail(r.Context(), "role", inv.Role)
	if err := a.Store.AcceptInvite(r.Context(), inv, u, a.now()); err != nil {
		return orgErr(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"tenantId": inv.TenantID})
	return nil
}

func (a *App) declineInvite(w http.ResponseWriter, r *http.Request) error {
	inv, _, err := a.ownInvite(r)
	if err != nil {
		return err
	}
	if err := a.Store.DeclineInvite(r.Context(), inv); err != nil {
		return orgErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
