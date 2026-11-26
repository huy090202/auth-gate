package portal

import (
	"testing"

	"github.com/huy090202/auth-gate/internal/platform/id"
)

// Why: a suspended customer must not be able to create new keys or invite others while the operator investigates.
func TestSuspendedTenantRefusesWrites(t *testing.T) {
	e := newAuthEnv(t)
	withPortalAPI(e)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m := e.user(t, "m@x.com", goodPW)
	tid := e.orgWith(t, ob, o, orgMember{m, RoleOwner})
	want(t, e.opPost("/api/operator/tenants/"+tid+"/suspend", map[string]string{}), 200, "")
	base, cid := "/api/t/"+tid, id.New()
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", base, map[string]string{"name": "New"}},
		{"PATCH", base + "/members/" + m.ID, map[string]string{"role": "admin"}},
		{"DELETE", base + "/members/" + m.ID, nil},
		{"POST", base + "/invites", map[string]string{"email": "bob@x.com", "role": "member"}},
		{"POST", base + "/invites/" + id.New() + "/resend", nil},
		{"DELETE", base + "/invites/" + id.New(), nil},
		{"POST", base + "/clients", map[string]string{"name": "c"}},
		{"PATCH", base + "/clients/" + cid, map[string]string{"name": "c"}},
		{"POST", base + "/clients/" + cid + "/keys", nil},
		{"DELETE", base + "/clients/" + cid + "/keys/previous", nil},
		{"POST", base + "/clients/" + cid + "/disable", nil},
		{"POST", base + "/clients/" + cid + "/enable", nil},
		{"DELETE", base + "/clients/" + cid + "/subjects/s1", nil},
		{"DELETE", base + "/clients/" + cid, nil},
		{"DELETE", base, map[string]string{"name": "Acme"}},
	} {
		want(t, ob.do(c.method, c.path, c.body), 409, "tenant_suspended")
	}
	want(t, ob.do("GET", base+"/members", nil), 200, "")
	want(t, ob.do("POST", base+"/leave", nil), 204, "")
}

// Why: accepting an invite or deleting an account would otherwise change or erase a workspace the operator is
// still inspecting.
func TestSuspendedTenantRefusesInviteAcceptAndAccountDelete(t *testing.T) {
	e := newAuthEnv(t)
	_, ob := e.owner(t, "o@x.com", "203.0.113.1")
	r := ob.do("POST", "/api/tenants", map[string]string{"name": "Acme"})
	want(t, r, 201, "")
	tid := r.str("tenantId")
	want(t, ob.do("POST", "/api/t/"+tid+"/invites", map[string]string{"email": "bob@x.com", "role": "member"}), 201, "")
	token, _ := inviteMail(t, e, "bob@x.com")
	bob := e.user(t, "bob@x.com", goodPW)
	e.withPIN(t, bob, pin1)
	bb := e.fullSession(t, bob, "203.0.113.2")
	want(t, e.opPost("/api/operator/tenants/"+tid+"/suspend", map[string]string{}), 200, "")
	want(t, bb.do("POST", "/api/invites/"+tid+"/accept", map[string]string{"token": token}), 409, "tenant_suspended")

	// The owner is the only member of Acme: deleting the account would delete it. The step-up survives the refusal.
	e.a.Config.OperatorKey = opKey
	tok := ob.stepUp("delete-account", "")
	want(t, ob.do("DELETE", "/api/me", nil, stepHeader, tok), 409, "tenant_suspended")
	want(t, e.opPost("/api/operator/tenants/"+tid+"/unsuspend", map[string]string{}), 200, "")
	withTenantAPI(e, id.New(), id.New())
	want(t, ob.do("DELETE", "/api/me", nil, stepHeader, tok), 204, "")
}
