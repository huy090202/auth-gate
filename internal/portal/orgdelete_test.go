package portal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/id"
)

// orgClients scripts AuthGate's portal routes: tenant tid lists ids and every other tenant lists none. failDelete,
// when set, is told the 1-based count of client deletes so far and says whether this one answers 500.
func orgClients(t *testing.T, rt *portalRT, tid string, ids []string, failDelete func(n int) bool) {
	t.Helper()
	type c struct {
		ClientID string `json:"clientId"`
	}
	list := struct {
		Clients []c `json:"clients"`
	}{Clients: []c{}}
	for _, x := range ids {
		list.Clients = append(list.Clients, c{x})
	}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	deletes := 0
	rt.script(func(call agCall) (int, string) {
		switch {
		case call.Method == "GET" && call.Tenant == tid:
			return 200, string(raw)
		case call.Method == "GET":
			return 200, `{"clients":[]}`
		case call.Method == "DELETE":
			deletes++
			if failDelete != nil && failDelete(deletes) {
				return 500, `{"error":"internal"}`
			}
		}
		return 0, ""
	})
}

// clientDeletes is the paths of the client deletes AuthGate saw for tenant tid.
func clientDeletes(rt *portalRT, tid string) []string {
	var out []string
	for _, c := range rt.seen() {
		if c.Method == "DELETE" && c.Tenant == tid {
			out = append(out, c.Path)
		}
	}
	return out
}

// tenantItems is the ttl (0 when absent) of every item left in the tenant's partition, by sort key.
func (e *authEnv) tenantItems(t *testing.T, tid string) map[string]int64 {
	t.Helper()
	out, err := e.a.Store.DB.Query(context.Background(), &dynamodb.QueryInput{
		TableName: aws.String(e.a.Store.Table), ConsistentRead: aws.Bool(true),
		KeyConditionExpression:    aws.String("pk = :p"),
		ExpressionAttributeValues: item{":p": s("TENANT#" + tid)},
	})
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]int64{}
	for _, it := range out.Items {
		items[getS(it, "sk")] = getN64(it, "ttl")
	}
	return items
}

// wantFinished checks that only items with a ttl within 30 days are left of the tenant.
func (e *authEnv) wantFinished(t *testing.T, tid string) {
	t.Helper()
	now := e.a.now()
	items := e.tenantItems(t, tid)
	if _, ok := items["META"]; !ok {
		t.Fatalf("tenant item gone too early: %v", items)
	}
	for sk, ttl := range items {
		if ttl <= now.Unix() || ttl > now.Add(30*24*time.Hour).Unix() {
			t.Fatalf("item %s has ttl %d (now %d): %v", sk, ttl, now.Unix(), items)
		}
	}
	idx, err := e.a.Store.DB.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(e.a.Store.Table), Key: tenantIndexKey(tid), ConsistentRead: aws.Bool(true),
	})
	if err != nil || idx.Item != nil {
		t.Fatalf("TENANTS index item: %v %v", idx.Item, err)
	}
}

func listed(t *testing.T, b *browser, tid string) bool {
	t.Helper()
	r := b.do("GET", "/api/tenants", nil)
	want(t, r, 200, "")
	var out struct{ Tenants []tenantView }
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(out.Tenants, func(v tenantView) bool { return v.TenantID == tid })
}

func mailedTo(e *authEnv, t *testing.T, kind, to string) int {
	t.Helper()
	n := 0
	for _, x := range e.mails(t) {
		if x.Kind == kind && x.To == to {
			n++
		}
	}
	return n
}

func (e *authEnv) deleteOrg(t *testing.T, b *browser, tid, name string) resp {
	t.Helper()
	return b.do("DELETE", "/api/t/"+tid, map[string]string{"name": name}, stepHeader, b.stepUp("delete-org", tid))
}

// Why: deleting an organisation must take its clients at AuthGate, its people and its invites with it, give
// the creator their quota back, and leave nothing that outlives it (spec section 4).
func TestDeleteOrgRound(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m, mb := e.owner(t, "m@x.com", "203.0.113.2")
	tid := e.orgWith(t, ob, o, orgMember{m, RoleMember})
	sum := sha256.Sum256([]byte("pending"))
	inv := Invite{ID: id.New(), TenantID: tid, Email: "p@x.com", Role: RoleMember, InvitedBy: o.ID, TokenHash: hex.EncodeToString(sum[:])}
	if err := e.a.Store.CreateInvite(context.Background(), inv, e.a.now()); err != nil {
		t.Fatal(err)
	}
	c1, c2 := id.New(), id.New()
	orgClients(t, rt, tid, []string{c1, c2}, nil)

	want(t, e.deleteOrg(t, ob, tid, " Acme "), 204, "")

	got := clientDeletes(rt, tid)
	if wantDel := []string{"/admin/api/portal/clients/" + c1, "/admin/api/portal/clients/" + c2}; !slices.Equal(got, wantDel) {
		t.Fatalf("client deletes = %v, want %v", got, wantDel)
	}
	if listed(t, ob, tid) || listed(t, mb, tid) {
		t.Fatal("a deleted organisation is still listed")
	}
	if mailedTo(e, t, "org-deleted", "m@x.com") != 1 {
		t.Fatalf("mails = %s", kinds(e.mails(t)))
	}
	if n := e.reload(t, o).OrgsCreated; n != 0 {
		t.Fatalf("orgsCreated = %d", n)
	}
	e.wantFinished(t, tid)
}

// Why: AuthGate failing midway must leave a deletion that any later visit finishes, exactly once: a second
// decrement of orgsCreated would hand out a fourth organisation.
func TestDeleteOrgResumes(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m, mb := e.owner(t, "m@x.com", "203.0.113.2")
	tid := e.orgWith(t, ob, o, orgMember{m, RoleMember})
	orgClients(t, rt, tid, []string{id.New(), id.New()}, func(n int) bool { return n == 2 })

	r := e.deleteOrg(t, ob, tid, "Acme")
	want(t, r, 502, "authgate_unavailable")
	if tn, err := e.a.Store.GetTenant(context.Background(), tid); err != nil || tn.DeletingAt == nil {
		t.Fatalf("tenant after the failure: %+v %v", tn, err)
	}

	if listed(t, mb, tid) {
		t.Fatal("a deleting organisation is listed")
	}
	e.wantFinished(t, tid)
	if n := e.reload(t, o).OrgsCreated; n != 0 {
		t.Fatalf("orgsCreated after the resume = %d", n)
	}

	if r := e.deleteOrg(t, ob, tid, "Acme"); r.Status != 204 && r.Status != 404 {
		t.Fatalf("second delete = %d %s", r.Status, r.Body)
	}
	if n := e.reload(t, o).OrgsCreated; n != 0 {
		t.Fatalf("orgsCreated after the second delete = %d", n)
	}
	want(t, ob.do("POST", "/api/tenants", map[string]string{"name": "Next"}), 201, "")
	// Any later run of the store's last step must not give the quota back again.
	tn, err := e.a.Store.GetTenant(context.Background(), tid)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.a.Store.FinishDeletion(context.Background(), tn, e.a.now()); err != nil {
		t.Fatal(err)
	}
	if n := e.reload(t, o).OrgsCreated; n != 1 {
		t.Fatalf("orgsCreated after a new org and a rerun = %d", n)
	}
}

// Why: a member's open tab must stop working once deletion starts, and create nothing at AuthGate (Review Focus 3).
func TestDeletingTenantRefusesWork(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m, mb := e.owner(t, "m@x.com", "203.0.113.2")
	tid := e.orgWith(t, ob, o, orgMember{m, RoleMember})
	if _, err := e.a.Store.MarkDeleting(context.Background(), tid, e.a.now()); err != nil {
		t.Fatal(err)
	}
	want(t, mb.do("POST", "/api/t/"+tid+"/clients", map[string]string{"name": "app"}), 404, "not_found")
	want(t, mb.do("GET", "/api/t/"+tid+"/members", nil), 404, "not_found")
	for _, c := range rt.seen() {
		if c.Method == "POST" {
			t.Fatalf("AuthGate saw a create: %+v", c)
		}
	}
}

// Why: the typed name is the guard against deleting the wrong organisation; it must match exactly.
func TestDeleteOrgNameMismatch(t *testing.T) {
	e := newAuthEnv(t)
	withPortalAPI(e)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	tid := e.orgWith(t, ob, o)
	want(t, e.deleteOrg(t, ob, tid, "acme"), 400, "name_mismatch")
	if tn, err := e.a.Store.GetTenant(context.Background(), tid); err != nil || tn.DeletingAt != nil {
		t.Fatalf("tenant after a mismatch: %+v %v", tn, err)
	}
	want(t, ob.do("DELETE", "/api/t/"+o.PersonalTenantID, map[string]string{"name": ""}), 400, "personal_workspace")
}

// Why: deleting the account of an organisation's only owner would orphan its members; the refusal must name
// the organisations and keep the step-up, so the person can transfer ownership and retry.
func TestDeleteAccountSoleOwner(t *testing.T) {
	e := newAuthEnv(t)
	withPortalAPI(e)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m, mb := e.owner(t, "m@x.com", "203.0.113.2")
	tid := e.orgWith(t, ob, o, orgMember{m, RoleMember})
	tok := ob.stepUp("delete-account", "")
	r := ob.do("DELETE", "/api/me", nil, stepHeader, tok)
	want(t, r, 409, "sole_owner")
	var out struct{ Organisations []string }
	if err := json.Unmarshal(r.Body, &out); err != nil || !slices.Equal(out.Organisations, []string{"Acme"}) {
		t.Fatalf("body = %s", r.Body)
	}
	want(t, ob.do("PATCH", "/api/t/"+tid+"/members/"+m.ID, map[string]string{"role": "owner"},
		stepHeader, ob.stepUp("change-role", tid+"/"+m.ID)), 200, "")
	want(t, ob.do("DELETE", "/api/me", nil, stepHeader, tok), 204, "")
	if !listed(t, mb, tid) {
		t.Fatal("the organisation went with its former owner")
	}
}

// Why: one of two owners leaving through account deletion must keep the counters right, or the last-owner
// guard and the member quota would read wrong (Review Focus 4).
func TestDeleteAccountCoOwner(t *testing.T) {
	e := newAuthEnv(t)
	withPortalAPI(e)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	o2, o2b := e.owner(t, "o2@x.com", "203.0.113.2")
	m, _ := e.owner(t, "m@x.com", "203.0.113.3")
	tid := e.orgWith(t, ob, o, orgMember{o2, RoleOwner}, orgMember{m, RoleMember})
	want(t, o2b.do("DELETE", "/api/me", nil, stepHeader, o2b.stepUp("delete-account", "")), 204, "")
	tn, err := e.a.Store.GetTenant(context.Background(), tid)
	if err != nil || tn.DeletingAt != nil || tn.Counts != (OrgCounts{Members: 2, Owners: 1}) {
		t.Fatalf("tenant = %+v %v", tn, err)
	}
	if _, err := e.a.Store.GetMembership(context.Background(), tid, o2.ID); err == nil {
		t.Fatal("the deleted owner is still a member")
	}
}

// Why: an organisation whose only member deletes their account has no one left to delete it; it must go
// with the account, clients included.
func TestDeleteAccountOnlyMember(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	tid := e.orgWith(t, ob, o)
	c1, c2 := id.New(), id.New()
	orgClients(t, rt, tid, []string{c1, c2}, nil)
	want(t, ob.do("DELETE", "/api/me", nil, stepHeader, ob.stepUp("delete-account", "")), 204, "")
	got := clientDeletes(rt, tid)
	if wantDel := []string{"/admin/api/portal/clients/" + c1, "/admin/api/portal/clients/" + c2}; !slices.Equal(got, wantDel) {
		t.Fatalf("client deletes = %v, want %v", got, wantDel)
	}
	e.wantFinished(t, tid)
	if _, err := e.a.Store.GetUser(context.Background(), o.ID); err == nil {
		t.Fatal("the account is still there")
	}
}
