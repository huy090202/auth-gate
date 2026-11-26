package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const opKey = "operator-test-key"

// op calls an operator route in process, with the given key header ("" sends none).
func (e *authEnv) op(path, key string) resp {
	e.a.Config.OperatorKey = opKey
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if key != "" {
		req.Header.Set("x-operator-key", key)
	}
	rec := httptest.NewRecorder()
	e.a.handler().ServeHTTP(rec, req)
	return resp{Status: rec.Code, Body: rec.Body.Bytes()}
}

// opPost calls an operator write route in process.
func (e *authEnv) opPost(path string, body any) resp {
	e.a.Config.OperatorKey = opKey
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Header.Set("x-operator-key", opKey)
	req.Header.Set("content-type", "application/json")
	rec := httptest.NewRecorder()
	e.a.handler().ServeHTTP(rec, req)
	return resp{Status: rec.Code, Body: rec.Body.Bytes()}
}

// Why: a route added without the guard would hand every customer's data or a suspend button to the
// internet; this walks every operator route, reads and writes alike.
func TestOperatorEveryRouteNeedsTheKey(t *testing.T) {
	e := newAuthEnv(t)
	e.a.Config.OperatorKey = opKey
	const tid, uid = "01ARZ3NDEKTSV4RRFFQ69G5FAV", "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	routes := []string{
		"GET /api/operator/tenants", "GET /api/operator/tenants/" + tid, "GET /api/operator/tenants/" + tid + "/audit",
		"GET /api/operator/users?email=a@x.com",
		"POST /api/operator/tenants/" + tid + "/suspend", "POST /api/operator/tenants/" + tid + "/unsuspend",
		"POST /api/operator/tenants/" + tid + "/audit",
		"POST /api/operator/users/" + uid + "/suspend", "POST /api/operator/users/" + uid + "/unsuspend",
		"POST /api/operator/users/" + uid + "/resend-verification", "POST /api/operator/users/" + uid + "/factor-reset",
	}
	for _, route := range routes {
		for _, key := range []string{"", "wrong"} {
			method, path, _ := cutSpace(route)
			req := httptest.NewRequest(method, path, bytes.NewReader([]byte("{}")))
			req.Header.Set("content-type", "application/json")
			if key != "" {
				req.Header.Set("x-operator-key", key)
			}
			rec := httptest.NewRecorder()
			e.a.handler().ServeHTTP(rec, req)
			want(t, resp{Status: rec.Code, Body: rec.Body.Bytes()}, 401, "unauthorized")
		}
	}
	want(t, e.op("/api/operator/tenants?cursor=garbage", opKey), 400, "invalid_cursor")
}

func cutSpace(s string) (string, string, bool) {
	for i := range s {
		if s[i] == ' ' {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}

// Why: the key is the only thing between the internet and every customer's data.
func TestOperatorKeyRequired(t *testing.T) {
	e := newAuthEnv(t)
	want(t, e.op("/api/operator/tenants", ""), 401, "unauthorized")
	want(t, e.op("/api/operator/tenants", "wrong"), 401, "unauthorized")
	want(t, e.op("/api/operator/tenants", opKey), 200, "")
}

// Why: support picks the right workspace from this list; a wrong order or a personal workspace
// shown as empty would send them to the wrong customer.
func TestOperatorListsTenantsNewestFirst(t *testing.T) {
	e := newAuthEnv(t)
	u, b := e.owner(t, "o@x.com", "203.0.113.1")
	tid := e.orgWith(t, b, u)
	var out struct {
		Tenants []operatorTenantView
	}
	if err := json.Unmarshal(e.op("/api/operator/tenants", opKey).Body, &out); err != nil {
		t.Fatal(err)
	}
	// e.user's personal tenant exists for every user; the org was made last.
	if len(out.Tenants) < 2 || out.Tenants[0].TenantID != tid || out.Tenants[0].Name != "Acme" || out.Tenants[0].MemberCount != 1 {
		t.Fatalf("tenants = %+v", out.Tenants)
	}
	if out.Tenants[0].CreatorEmail != "o@x.com" {
		t.Fatalf("creatorEmail = %q", out.Tenants[0].CreatorEmail)
	}
	p := out.Tenants[len(out.Tenants)-1]
	if p.Kind != "personal" || p.MemberCount != 1 || p.Name != "" || p.CreatorEmail != "o@x.com" {
		t.Fatalf("personal = %+v", p)
	}
	detail := e.op("/api/operator/tenants/"+tid, opKey)
	want(t, detail, 200, "")
	want(t, e.op("/api/operator/tenants/"+tid+"/audit", opKey), 200, "")
	want(t, e.op("/api/operator/tenants/01ARZ3NDEKTSV4RRFFQ69G5FAV", opKey), 404, "not_found")
}

// Why: the search must find people however support typed the address.
func TestOperatorUserLookupNormalises(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "ann@x.com", goodPW)
	e.withPIN(t, u, pin1)
	r := e.op("/api/operator/users?email=%20Ann@X.com%20", opKey)
	want(t, r, 200, "")
	if r.str("userId") != u.ID || r.str("email") != "ann@x.com" || r.m()["factorCount"] != float64(1) {
		t.Fatalf("body = %s", r.Body)
	}
	want(t, e.op("/api/operator/users?email=nobody@x.com", opKey), 404, "not_found")
}

// Why: "last sign-in" helps support tell a live account from an abandoned one.
func TestSignInRecordsLastSignIn(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "ann@x.com", goodPW)
	e.withPIN(t, u, pin1)
	e.fullSession(t, u, "203.0.113.1")
	got, err := e.a.Store.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSignInAt == nil || !got.LastSignInAt.Equal(e.a.now()) {
		t.Fatalf("lastSignInAt = %v, want %v", got.LastSignInAt, e.a.now())
	}
}

func (e *authEnv) tenantAudit(t *testing.T, tid string) []AuditEntry {
	t.Helper()
	es, _, err := e.a.Store.ListAudit(context.Background(), tid, "")
	if err != nil {
		t.Fatal(err)
	}
	return es
}

// Why: repeated clicks must not spam a customer, nor fill the audit with entries for changes that never happened.
func TestOperatorSuspendTenantOnce(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m := e.user(t, "m@x.com", goodPW)
	tid := e.orgWith(t, ob, o, orgMember{m, RoleOwner})
	path := "/api/operator/tenants/" + tid
	for range 2 {
		r := e.opPost(path+"/suspend", map[string]string{})
		want(t, r, 200, "")
		if r.m()["suspendedAt"] == nil {
			t.Fatalf("body = %s", r.Body)
		}
	}
	for _, to := range []string{"o@x.com", "m@x.com"} {
		if n := mailedTo(e, t, "tenant-suspended", to); n != 1 {
			t.Fatalf("%s got %d tenant-suspended mails", to, n)
		}
	}
	var got []AuditEntry
	for _, x := range e.tenantAudit(t, tid) {
		if x.Action == "tenant-suspend" {
			got = append(got, x)
		}
	}
	if len(got) != 1 || got[0].ActorID != "operator" || got[0].ActorName != "AuthGate administrator" || got[0].Result != "success" {
		t.Fatalf("audit = %+v", got)
	}
	for range 2 {
		r := e.opPost(path+"/unsuspend", map[string]string{})
		want(t, r, 200, "")
		if r.m()["suspendedAt"] != nil {
			t.Fatalf("still suspended: %s", r.Body)
		}
	}
	if n := mailedTo(e, t, "tenant-unsuspended", "o@x.com"); n != 1 {
		t.Fatalf("%d tenant-unsuspended mails", n)
	}
}

// Why: a personal workspace has no name; its owner must still be told, in their language, what was suspended.
func TestOperatorSuspendPersonalTenant(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "p@x.com", goodPW)
	want(t, e.opPost("/api/operator/tenants/"+u.PersonalTenantID+"/suspend", map[string]string{}), 200, "")
	if n := mailedTo(e, t, "tenant-suspended", "p@x.com"); n != 1 {
		t.Fatalf("%d mails", n)
	}
}

// Why: a workspace being deleted must not be brought back by a click, and a gone one must not look like success.
func TestOperatorSuspendDeletingTenant(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	tid := e.orgWith(t, ob, o)
	if _, err := e.a.Store.MarkDeleting(context.Background(), tid, e.a.now()); err != nil {
		t.Fatal(err)
	}
	want(t, e.opPost("/api/operator/tenants/"+tid+"/suspend", map[string]string{}), 409, "tenant_deleting")
	want(t, e.opPost("/api/operator/tenants/"+tid+"/unsuspend", map[string]string{}), 409, "tenant_deleting")
	want(t, e.opPost("/api/operator/tenants/01ARZ3NDEKTSV4RRFFQ69G5FAV/suspend", map[string]string{}), 404, "not_found")
	if n := mailedTo(e, t, "tenant-suspended", "o@x.com"); n != 0 {
		t.Fatalf("%d mails", n)
	}
}

// Why: a suspended user must lose access at once, not at session expiry; and an organisation they alone
// owned must keep serving its other members.
func TestOperatorSuspendUserEndsSession(t *testing.T) {
	e := newAuthEnv(t)
	o, ob := e.owner(t, "o@x.com", "203.0.113.1")
	m := e.user(t, "m@x.com", goodPW)
	e.withPIN(t, m, pin1)
	tid := e.orgWith(t, ob, o, orgMember{m, RoleAdmin})
	mb := e.fullSession(t, m, "203.0.113.2")
	want(t, ob.do("GET", "/api/tenants", nil), 200, "")

	path := "/api/operator/users/" + o.ID
	for range 2 {
		r := e.opPost(path+"/suspend", map[string]string{})
		want(t, r, 200, "")
		if r.str("userId") != o.ID || r.m()["suspendedAt"] == nil {
			t.Fatalf("body = %s", r.Body)
		}
	}
	want(t, ob.do("GET", "/api/tenants", nil), 401, "")
	if !listed(t, mb, tid) {
		t.Fatal("the organisation stopped serving its other member")
	}
	if n := mailedTo(e, t, "account-suspended", "o@x.com"); n != 1 {
		t.Fatalf("%d account-suspended mails", n)
	}
	for range 2 {
		r := e.opPost(path+"/unsuspend", map[string]string{})
		want(t, r, 200, "")
		if r.m()["suspendedAt"] != nil {
			t.Fatalf("body = %s", r.Body)
		}
	}
	if n := mailedTo(e, t, "account-unsuspended", "o@x.com"); n != 1 {
		t.Fatalf("%d account-unsuspended mails", n)
	}
	want(t, e.opPost("/api/operator/users/01ARZ3NDEKTSV4RRFFQ69G5FAV/suspend", map[string]string{}), 404, "not_found")
}

// Why: resending to a verified address is noise and would let the operator button probe who is verified
// by mail; only an unverified user gets one.
func TestOperatorResendOnlyUnverified(t *testing.T) {
	e := newAuthEnv(t)
	v := e.user(t, "v@x.com", goodPW)
	want(t, e.opPost("/api/operator/users/"+v.ID+"/resend-verification", map[string]string{}), 409, "already_verified")
	u := User{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", Email: "new@x.com", PasswordHash: "x", Lang: "en", CreatedAt: e.a.now()}
	if err := e.a.Store.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	want(t, e.opPost("/api/operator/users/"+u.ID+"/resend-verification", map[string]string{}), 200, "")
	if n := mailedTo(e, t, "verify", "new@x.com"); n != 1 {
		t.Fatalf("%d verify mails", n)
	}
	if n := mailedTo(e, t, "verify", "v@x.com"); n != 0 {
		t.Fatalf("%d verify mails to the verified user", n)
	}
}

// Why: the operator starts the 24-hour wait for someone who lost their factors; a second click must not
// restart it or mail again, and an account that cannot receive mail must not start one.
func TestOperatorFactorResetStartsWait(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "f@x.com", goodPW)
	path := "/api/operator/users/" + u.ID + "/factor-reset"
	r := e.opPost(path, map[string]string{})
	want(t, r, 200, "")
	due, err := time.Parse(time.RFC3339, r.str("dueAt"))
	if err != nil || !due.Equal(e.a.now().Add(24*time.Hour)) {
		t.Fatalf("dueAt = %q (%v)", r.str("dueAt"), err)
	}
	e.advance(time.Hour)
	r2 := e.opPost(path, map[string]string{})
	want(t, r2, 200, "")
	if r2.str("dueAt") != r.str("dueAt") {
		t.Fatalf("second click moved the wait: %s vs %s", r2.Body, r.Body)
	}
	if n := mailedTo(e, t, "factor-reset-started", "f@x.com"); n != 1 {
		t.Fatalf("%d mails", n)
	}
	s := e.user(t, "s@x.com", goodPW)
	if _, err := e.a.Store.DB.UpdateItem(context.Background(), suspendInput(e.a.Store, s.ID, e.a.now())); err != nil {
		t.Fatal(err)
	}
	want(t, e.opPost("/api/operator/users/"+s.ID+"/factor-reset", map[string]string{}), 409, "not_mailable")
}

// Why: a quota change is the one operator action AuthGate owns; the Portal only records it, and nothing
// else may be written to a customer's audit through this door.
func TestOperatorQuotaAudit(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "q@x.com", goodPW)
	path := "/api/operator/tenants/" + u.PersonalTenantID + "/audit"
	want(t, e.opPost(path, map[string]any{"action": "tenant-quota", "from": 5, "to": 8}), 204, "")
	want(t, e.opPost(path, map[string]any{"action": "client-delete", "from": 5, "to": 8}), 400, "invalid_action")
	want(t, e.opPost("/api/operator/tenants/01ARZ3NDEKTSV4RRFFQ69G5FAV/audit", map[string]any{"action": "tenant-quota", "from": 1, "to": 2}), 404, "not_found")
	var got []AuditEntry
	for _, x := range e.tenantAudit(t, u.PersonalTenantID) {
		if x.Action == "tenant-quota" {
			got = append(got, x)
		}
	}
	if len(got) != 1 || got[0].ActorID != "operator" || got[0].Detail["from"] != "5" || got[0].Detail["to"] != "8" {
		t.Fatalf("audit = %+v", got)
	}
}
