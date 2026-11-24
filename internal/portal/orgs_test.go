package portal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/huy090202/auth-gate/internal/platform/id"
)

type orgMember struct {
	U    User
	Role string
}

// orgWith creates an organisation as ob (the owner) through the API and adds the
// members through the store, as an accepted invite would. It returns the tenant id.
func (e *authEnv) orgWith(t *testing.T, ob *browser, owner User, members ...orgMember) string {
	t.Helper()
	r := ob.do("POST", "/api/tenants", map[string]string{"name": "Acme"})
	want(t, r, 201, "")
	tid := r.str("tenantId")
	ctx := context.Background()
	for i, m := range members {
		sum := sha256.Sum256([]byte("token-" + m.U.ID))
		inv := Invite{ID: id.New(), TenantID: tid, Email: m.U.Email, Role: m.Role, InvitedBy: owner.ID, TokenHash: hex.EncodeToString(sum[:])}
		if err := e.a.Store.CreateInvite(ctx, inv, e.a.now()); err != nil {
			t.Fatalf("invite %d: %v", i, err)
		}
		got, err := e.a.Store.PeekInvite(ctx, tid, inv.TokenHash, e.a.now())
		if err != nil {
			t.Fatal(err)
		}
		if err := e.a.Store.AcceptInvite(ctx, got, m.U, e.a.now()); err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
	}
	return tid
}

// Why: the quota and the list order are what the workspace switcher shows; a fourth org must not slip through.
func TestCreateOrgOverHTTP(t *testing.T) {
	e := newAuthEnv(t)
	_, b := e.owner(t, "o@x.com", "203.0.113.1")
	r := b.do("POST", "/api/tenants", map[string]string{"name": "  Acme  "})
	want(t, r, 201, "")
	if r.str("kind") != "org" || r.str("name") != "Acme" || r.str("role") != "owner" || id.ValidateULID(r.str("tenantId")) != nil {
		t.Fatalf("body = %s", r.Body)
	}
	want(t, b.do("POST", "/api/tenants", map[string]string{"name": "x"}), 400, "invalid_name")
	want(t, b.do("POST", "/api/tenants", map[string]string{"name": "see example.com"}), 400, "invalid_name")

	list := b.do("GET", "/api/tenants", nil)
	var out struct{ Tenants []tenantView }
	if err := json.Unmarshal(list.Body, &out); err != nil || len(out.Tenants) != 2 ||
		out.Tenants[0].Kind != "personal" || out.Tenants[1].Name != "Acme" || out.Tenants[1].Role != "owner" {
		t.Fatalf("tenants = %s", list.Body)
	}
	want(t, b.do("POST", "/api/tenants", map[string]string{"name": "Two"}), 201, "")
	want(t, b.do("POST", "/api/tenants", map[string]string{"name": "Three"}), 201, "")
	want(t, b.do("POST", "/api/tenants", map[string]string{"name": "Four"}), 409, "org_quota_exceeded")
}

// Why: a member who could rename the org or manage people would turn a read role into control of the org.
func TestMemberRoutesForbiddenToMembers(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m, mb := e.owner(t, "m@x.com", "203.0.113.2")
	tid := e.orgWith(t, ob, o, orgMember{m, RoleMember})
	base := "/api/t/" + tid
	want(t, mb.do("PATCH", base, map[string]string{"name": "Mine"}), 403, "forbidden")
	want(t, mb.do("PATCH", base+"/members/"+o.ID, map[string]string{"role": "member"}), 403, "forbidden")
	want(t, mb.do("DELETE", base+"/members/"+o.ID, nil), 403, "forbidden")
	want(t, mb.do("GET", base+"/members", nil), 200, "")
}

// Why: if an admin could demote, remove or out-rank an owner, the owner role would protect nothing.
func TestAdminCannotTouchOwner(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	a, ab := e.owner(t, "a@x.com", "203.0.113.2")
	m, _ := e.owner(t, "m@x.com", "203.0.113.3")
	tid := e.orgWith(t, ob, o, orgMember{a, RoleAdmin}, orgMember{m, RoleMember})
	base := "/api/t/" + tid + "/members/"
	patch := func(uid, role string) resp {
		return ab.do("PATCH", base+uid, map[string]string{"role": role}, stepHeader, ab.stepUp("change-role", tid+"/"+uid))
	}
	want(t, patch(o.ID, "member"), 403, "forbidden")
	want(t, ab.do("DELETE", base+o.ID, nil, stepHeader, ab.stepUp("remove-member", tid+"/"+o.ID)), 403, "forbidden")
	want(t, patch(m.ID, "owner"), 403, "forbidden")
	want(t, patch(m.ID, "admin"), 200, "")
}

// Why: a removed person must lose access on their next request, and must be told why they lost it.
func TestRemovedMemberRefusedNextRequest(t *testing.T) {
	e := newAuthEnv(t)
	withPortalAPI(e)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m, mb := e.owner(t, "m@x.com", "203.0.113.2")
	tid := e.orgWith(t, ob, o, orgMember{m, RoleMember})
	want(t, mb.do("GET", "/api/t/"+tid+"/clients", nil), 200, "")
	want(t, ob.do("DELETE", "/api/t/"+tid+"/members/"+m.ID, nil, stepHeader, ob.stepUp("remove-member", tid+"/"+m.ID)), 204, "")
	want(t, mb.do("GET", "/api/t/"+tid+"/clients", nil), 404, "not_found")
	found := false
	for _, x := range e.mails(t) {
		if x.Kind == "org-removed" && x.To == "m@x.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no org-removed mail: %s", kinds(e.mails(t)))
	}
}

// Why: a role change without a fresh, matching step-up would let a stolen session hand out ownership.
func TestRoleChangeNeedsStepUp(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m, _ := e.owner(t, "m@x.com", "203.0.113.2")
	x, _ := e.owner(t, "x@x.com", "203.0.113.3")
	tid := e.orgWith(t, ob, o, orgMember{m, RoleMember}, orgMember{x, RoleMember})
	path := "/api/t/" + tid + "/members/" + m.ID
	body := map[string]string{"role": "admin"}
	want(t, ob.do("PATCH", path, body), 403, "step_up_required")
	other := ob.stepUp("change-role", tid+"/"+x.ID)
	want(t, ob.do("PATCH", path, body, stepHeader, other), 403, "step_up_required")
	want(t, ob.do("PATCH", path, map[string]string{"role": "boss"}, stepHeader, ob.stepUp("change-role", tid+"/"+m.ID)), 400, "invalid_role")
	want(t, ob.do("PATCH", path, body, stepHeader, ob.stepUp("change-role", tid+"/"+m.ID)), 200, "")
}

// Why: a personal workspace has no members; the answer must be the explicit code, not a 403 or an empty list.
func TestPersonalWorkspaceRoutes(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	base := "/api/t/" + o.PersonalTenantID
	want(t, ob.do("POST", base+"/leave", nil), 400, "personal_workspace")
	want(t, ob.do("GET", base+"/members", nil), 400, "personal_workspace")
	want(t, ob.do("PATCH", base, map[string]string{"name": "Acme"}), 400, "personal_workspace")
}

// Why: the last owner leaving would orphan the organisation; any other member leaving must work.
func TestLeaveOrg(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m, mb := e.owner(t, "m@x.com", "203.0.113.2")
	tid := e.orgWith(t, ob, o, orgMember{m, RoleMember})
	want(t, ob.do("POST", "/api/t/"+tid+"/leave", nil), 409, "last_owner")
	want(t, mb.do("POST", "/api/t/"+tid+"/leave", nil), 204, "")
	want(t, mb.do("GET", "/api/t/"+tid+"/members", nil), 404, "not_found")
}
