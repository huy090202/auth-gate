package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/id"
)

type agCall struct{ Method, Path, Key, Tenant, Body string }

// portalRT stands in for AuthGate's /api/portal routes: it records each call and
// answers with the scripted status and body (200 {} by default). Every other
// call goes to the real in-process AuthGate.
type portalRT struct {
	next   http.RoundTripper
	mu     sync.Mutex
	calls  []agCall
	status int
	body   string
	// answer, when set, scripts the reply to one call; a 0 status falls back to status and body.
	answer func(c agCall) (status int, body string)
}

func (f *portalRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(req.URL.Path, "/admin/api/portal/clients") {
		return f.next.RoundTrip(req)
	}
	var b strings.Builder
	if req.Body != nil {
		buf := make([]byte, 4096)
		for {
			n, err := req.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c := agCall{req.Method, req.URL.RequestURI(), req.Header.Get("x-internal-access-key"), req.Header.Get("x-tenant-id"), b.String()}
	f.calls = append(f.calls, c)
	rec := httptest.NewRecorder()
	rec.Header().Set("Content-Type", "application/json")
	status, body := f.status, f.body
	if f.answer != nil {
		if st, bd := f.answer(c); st != 0 {
			status, body = st, bd
		}
	}
	if status == 0 {
		status = 200
	}
	if body == "" {
		body = "{}"
	}
	rec.WriteHeader(status)
	_, _ = rec.WriteString(body)
	return rec.Result(), nil
}

func (f *portalRT) reset(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.status, f.body = nil, status, body
}

// script sets the per-call answer (see answer).
func (f *portalRT) script(fn func(c agCall) (status int, body string)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer = fn
}

func (f *portalRT) seen() []agCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func withPortalAPI(e *authEnv) *portalRT {
	rt := &portalRT{next: e.ag.HTTP.Transport}
	ag := *e.ag
	ag.PortalKey = "portal-key"
	ag.HTTP = &http.Client{Transport: rt}
	e.a.AuthGate = &ag
	return rt
}

// owner is a signed-in user with a PIN and a personal workspace.
func (e *authEnv) owner(t *testing.T, email, ip string) (User, *browser) {
	t.Helper()
	u := e.user(t, email, goodPW)
	e.withPIN(t, u, pin1)
	return u, e.fullSession(t, u, ip)
}

func (e *authEnv) setDisplayName(t *testing.T, u User, name string) {
	t.Helper()
	if err := e.a.Store.update(context.Background(), u.ID, "SET displayName = :n", "attribute_exists(pk)", nil, item{":n": s(name)}); err != nil {
		t.Fatal(err)
	}
}

// Why: the Portal's isolation is its own check, before AuthGate's: another user's workspace must look exactly
// like one that does not exist, and must cost no AuthGate call.
func TestOtherUsersWorkspaceIs404(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	a, _ := e.owner(t, "a@x.com", "203.0.113.1")
	_, bb := e.owner(t, "b@x.com", "203.0.113.2")
	cid := id.New()

	var bodies []string
	for _, tid := range []string{a.PersonalTenantID, id.New()} {
		base := "/api/t/" + tid + "/clients"
		for _, c := range []struct{ method, path string }{
			{"GET", base}, {"POST", base}, {"GET", base + "/" + cid}, {"PATCH", base + "/" + cid},
			{"POST", base + "/" + cid + "/disable"}, {"POST", base + "/" + cid + "/enable"}, {"POST", base + "/" + cid + "/keys"},
			{"DELETE", base + "/" + cid + "/keys/previous"}, {"DELETE", base + "/" + cid + "/subjects/s1"}, {"DELETE", base + "/" + cid},
			{"GET", "/api/t/" + tid + "/audit"},
		} {
			var body any
			if c.method == "POST" || c.method == "PATCH" {
				body = map[string]string{"name": "x"}
			}
			r := bb.do(c.method, c.path, body)
			want(t, r, 404, "not_found")
			bodies = append(bodies, string(r.Body))
		}
	}
	for _, b := range bodies {
		if b != bodies[0] {
			t.Fatalf("answers differ: %q vs %q", b, bodies[0])
		}
	}
	if n := len(rt.seen()); n != 0 {
		t.Fatalf("%d AuthGate calls for a workspace that is not the caller's", n)
	}
}

// Why: AuthGate confines a call by the portal key and x-tenant-id; the Portal must send the workspace of the
// URL (not a body field) with the portal key, and must not let a user set the owner or the rate limit.
func TestClientCallsCarryTheWorkspaceTenant(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	u, b := e.owner(t, "a@x.com", "203.0.113.1")
	base := "/api/t/" + u.PersonalTenantID + "/clients"
	cid := id.New()

	want(t, b.do("GET", base+"?cursor=c1", nil), 200, "")
	want(t, b.do("POST", base, map[string]any{
		"name": "app", "rpId": "app.test", "origins": []string{"https://app.test"}, "tokenTtlSec": 300,
		"owner": "T#someone-else", "tenantId": "someone-else", "rateLimit": map[string]any{"burst": 9999, "perSecond": 9999},
	}), 201, "")
	want(t, b.do("GET", base+"/"+cid, nil), 200, "")
	want(t, b.do("PATCH", base+"/"+cid, map[string]any{"name": "new", "owner": "x"}), 200, "")

	calls := rt.seen()
	if len(calls) != 4 {
		t.Fatalf("calls = %+v", calls)
	}
	for _, c := range calls {
		if c.Key != "portal-key" || c.Tenant != u.PersonalTenantID {
			t.Fatalf("call %+v: want the portal key and tenant %s", c, u.PersonalTenantID)
		}
	}
	if calls[0].Path != "/admin/api/portal/clients?cursor=c1" || calls[2].Path != "/admin/api/portal/clients/"+cid {
		t.Fatalf("paths = %q, %q", calls[0].Path, calls[2].Path)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(calls[1].Body), &sent); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"owner", "tenantId", "rateLimit"} {
		if _, ok := sent[k]; ok {
			t.Fatalf("create passed %q on: %s", k, calls[1].Body)
		}
	}
	if sent["name"] != "app" || sent["rpId"] != "app.test" || sent["tokenTtlSec"] != float64(300) {
		t.Fatalf("create body = %s", calls[1].Body)
	}
	if strings.Contains(calls[3].Body, "owner") || !strings.Contains(calls[3].Body, `"name":"new"`) {
		t.Fatalf("patch body = %s", calls[3].Body)
	}
}

// Why: a quota, a suspension or an operator lock is the user's to see; the Portal must pass AuthGate's code and
// status through, and keep the user's own refusals (rateLimit in a patch) as AuthGate gave them.
func TestQuotaAndSuspensionPassThrough(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	u, b := e.owner(t, "a@x.com", "203.0.113.1")
	base := "/api/t/" + u.PersonalTenantID + "/clients"
	cid := id.New()
	for _, c := range []struct {
		status int
		code   string
		do     func() resp
	}{
		{409, "quota_exceeded", func() resp { return b.do("POST", base, map[string]string{"name": "n"}) }},
		{409, "tenant_suspended", func() resp { return b.do("POST", base+"/"+cid+"/enable", nil) }},
		{409, "disabled_by_operator", func() resp { return b.do("POST", base+"/"+cid+"/enable", nil) }},
		{400, "invalid_client", func() resp {
			return b.do("PATCH", base+"/"+cid, map[string]any{"rateLimit": map[string]any{"burst": 1}})
		}},
		{404, "client_not_found", func() resp { return b.do("GET", base+"/"+cid, nil) }},
	} {
		rt.reset(c.status, `{"error":"`+c.code+`"}`)
		want(t, c.do(), c.status, c.code)
		// A patch with rateLimit keeps it on the wire, so AuthGate is the one that refuses it.
		if c.code == "invalid_client" {
			if last := rt.seen(); len(last) != 1 || !strings.Contains(last[0].Body, "rateLimit") {
				t.Fatalf("calls = %+v", last)
			}
		}
	}
}

// Why: every write must leave a line in the workspace's log with who did it, from where, to what, and how it ended.
func TestClientWritesAreAuditedWithIP(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	u, b := e.owner(t, "a@x.com", "203.0.113.1")
	e.setDisplayName(t, u, "Ann")
	base := "/api/t/" + u.PersonalTenantID + "/clients"
	cid := id.New()
	step := func(action, target string) []string { return []string{stepHeader, b.stepUp(action, target)} }

	rt.reset(200, `{"clientId":"`+cid+`","key":"k"}`)
	want(t, b.do("POST", base, map[string]any{"name": "app", "rpId": "app.test", "origins": []string{"https://app.test"}}), 201, "")
	rt.reset(200, "")
	want(t, b.do("PATCH", base+"/"+cid, map[string]any{"name": "n2"}), 200, "")
	want(t, b.do("POST", base+"/"+cid+"/keys", nil, step("rotate-key", cid)...), 201, "")
	rt.reset(200, `{"revoked":2}`)
	want(t, b.do("DELETE", base+"/"+cid+"/keys/previous", nil, step("revoke-previous-keys", cid)...), 200, "")
	rt.reset(200, "")
	want(t, b.do("POST", base+"/"+cid+"/disable", nil, step("disable-client", cid)...), 200, "")
	want(t, b.do("POST", base+"/"+cid+"/enable", nil), 200, "")
	want(t, b.do("DELETE", base+"/"+cid+"/subjects/user-1", nil, step("purge-subject", cid+"/user-1")...), 204, "")
	rt.reset(409, `{"error":"tenant_suspended"}`)
	want(t, b.do("DELETE", base+"/"+cid, nil, step("delete-client", cid)...), 409, "tenant_suspended")

	got, _, err := e.a.Store.ListAudit(context.Background(), u.PersonalTenantID, "")
	if err != nil {
		t.Fatal(err)
	}
	type line struct{ action, target, result string }
	var lines []line
	for _, x := range got {
		if !strings.HasPrefix(x.Action, "client-") {
			continue
		}
		if x.ActorID != u.ID || x.ActorName != "Ann" || x.IP != "203.0.113.1" {
			t.Fatalf("entry %+v: want actor %s/Ann from 203.0.113.1", x, u.ID)
		}
		lines = append(lines, line{x.Action, x.Target, x.Result})
	}
	slices.Reverse(lines) // oldest first
	wantLines := []line{
		{"client-create", cid, "success"}, {"client-update", cid, "success"}, {"client-rotate-key", cid, "success"},
		{"client-revoke-previous-keys", cid, "success"}, {"client-disable", cid, "success"}, {"client-enable", cid, "success"},
		{"client-purge-subject", cid + "/user-1", "success"}, {"client-delete", cid, "failed:tenant_suspended"},
	}
	if !slices.Equal(lines, wantLines) {
		t.Fatalf("audit = %+v\nwant %+v", lines, wantLines)
	}
	for _, x := range got {
		if x.Action == "client-revoke-previous-keys" && x.Detail["revoked"] != "2" {
			t.Fatalf("revoke detail = %v", x.Detail)
		}
		if strings.Contains(x.Target+x.Detail["name"], `"key"`) {
			t.Fatal("the key reached the log")
		}
	}
}

// Why: rotating a key is the action an attacker with a stolen session wants; it must need a token made for this
// client, spent once, and a refusal must not cost an AuthGate call.
func TestRotateNeedsStepUp(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	u, b := e.owner(t, "a@x.com", "203.0.113.1")
	cid, other := id.New(), id.New()
	path := "/api/t/" + u.PersonalTenantID + "/clients/" + cid + "/keys"

	want(t, b.do("POST", path, nil), 403, "step_up_required")
	want(t, b.do("POST", path, nil, stepHeader, b.stepUp("rotate-key", other)), 403, "step_up_required")
	want(t, b.do("POST", path, nil, stepHeader, b.stepUp("disable-client", cid)), 403, "step_up_required")
	if n := len(rt.seen()); n != 0 {
		t.Fatalf("%d AuthGate calls before a valid token", n)
	}
	tok := b.stepUp("rotate-key", cid)
	want(t, b.do("POST", path, nil, stepHeader, tok), 201, "")
	want(t, b.do("POST", path, nil, stepHeader, tok), 403, "step_up_required")
	if n := len(rt.seen()); n != 1 {
		t.Fatalf("AuthGate calls = %d, want 1", n)
	}
	// A refused guard before the call (bad id) spends nothing.
	tok = b.stepUp("rotate-key", cid)
	want(t, b.do("POST", "/api/t/"+u.PersonalTenantID+"/clients/not-an-id/keys", nil, stepHeader, tok), 404, "not_found")
	want(t, b.do("POST", path, nil, stepHeader, tok), 201, "")
}

// Why: roles are wired now so that organisations only add members; a member must not disable, enable, purge or
// delete, nor read the audit log, but may do the rest.
func TestRolesGateTheRoutes(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	owner, _ := e.owner(t, "a@x.com", "203.0.113.1")
	m, bm := e.owner(t, "m@x.com", "203.0.113.2")
	ctx := context.Background()
	for _, in := range []*dynamodb.PutItemInput{memberPut(e.a.Store, owner.PersonalTenantID, m.ID, RoleMember, e.a.now())} {
		if _, err := e.a.Store.DB.PutItem(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	cid := id.New()
	base := "/api/t/" + owner.PersonalTenantID + "/clients"
	tok := func(a, tg string) []string { return []string{stepHeader, bm.stepUp(a, tg)} }

	want(t, bm.do("GET", base, nil), 200, "")
	want(t, bm.do("POST", base, map[string]string{"name": "n"}), 201, "")
	want(t, bm.do("PATCH", base+"/"+cid, map[string]string{"name": "n"}), 200, "")
	want(t, bm.do("POST", base+"/"+cid+"/keys", nil, tok("rotate-key", cid)...), 201, "")
	want(t, bm.do("DELETE", base+"/"+cid+"/keys/previous", nil, tok("revoke-previous-keys", cid)...), 200, "")
	n := len(rt.seen())
	for _, c := range []struct{ method, path, action, target string }{
		{"POST", base + "/" + cid + "/disable", "disable-client", cid},
		{"POST", base + "/" + cid + "/enable", "", ""},
		{"DELETE", base + "/" + cid + "/subjects/s1", "purge-subject", cid + "/s1"},
		{"DELETE", base + "/" + cid, "delete-client", cid},
		{"GET", "/api/t/" + owner.PersonalTenantID + "/audit", "", ""},
	} {
		var h []string
		if c.action != "" {
			h = tok(c.action, c.target)
		}
		want(t, bm.do(c.method, c.path, nil, h...), 403, "forbidden")
	}
	if len(rt.seen()) != n {
		t.Fatal("a forbidden role reached AuthGate")
	}
}

// Why: account deletion can race a create; a workspace marked as being deleted must take no new client.
func TestCreateRefusedWhileTenantIsDeleting(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	u, b := e.owner(t, "a@x.com", "203.0.113.1")
	_, err := e.a.Store.DB.UpdateItem(context.Background(), &dynamodb.UpdateItemInput{
		TableName: aws.String(e.a.Store.Table), Key: tenantKey(u.PersonalTenantID),
		UpdateExpression:          aws.String("SET deletingAt = :t"),
		ExpressionAttributeValues: item{":t": s(ts(e.a.now()))},
	})
	if err != nil {
		t.Fatal(err)
	}
	want(t, b.do("POST", "/api/t/"+u.PersonalTenantID+"/clients", map[string]string{"name": "n"}), 409, "tenant_deleting")
	if len(rt.seen()) != 0 {
		t.Fatal("create reached AuthGate")
	}
}

// Why: a session that is no longer valid (password changed elsewhere, account suspended or gone) must be refused
// on the workspace routes too, not only on /api/me.
func TestWorkspaceRoutesRefuseStaleSessions(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	ctx := context.Background()
	u1, b1 := e.owner(t, "a@x.com", "203.0.113.1")
	u2, b2 := e.owner(t, "b@x.com", "203.0.113.2")
	u3, b3 := e.owner(t, "c@x.com", "203.0.113.3")
	list := func(u User) string { return "/api/t/" + u.PersonalTenantID + "/clients" }
	for _, p := range []struct {
		u User
		b *browser
	}{{u1, b1}, {u2, b2}, {u3, b3}} {
		want(t, p.b.do("GET", list(p.u), nil), 200, "")
	}
	if err := e.a.Store.EndSessions(ctx, u1.ID); err != nil { // bumps the epoch
		t.Fatal(err)
	}
	if _, err := e.a.Store.DB.UpdateItem(ctx, suspendInput(e.a.Store, u2.ID, e.a.now())); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.Store.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(e.a.Store.Table), Key: userKey(u3.ID)}); err != nil {
		t.Fatal(err)
	}
	before := len(rt.seen())
	for name, p := range map[string]struct {
		u User
		b *browser
	}{"epoch moved": {u1, b1}, "suspended": {u2, b2}, "user gone": {u3, b3}} {
		if r := p.b.do("GET", list(p.u), nil); r.Status != 401 {
			t.Fatalf("%s: %d %s", name, r.Status, r.Body)
		}
	}
	if len(rt.seen()) != before {
		t.Fatal("a refused session reached AuthGate")
	}
}

// Why: the audit route must page without a gap, and a cursor the route did not produce must be a 400, never a 500
// and never a read of another key range.
func TestAuditRoutePagesAndRejectsForeignCursors(t *testing.T) {
	e := newAuthEnv(t)
	u, b := e.owner(t, "a@x.com", "203.0.113.1")
	ctx := context.Background()
	base := e.a.now()
	for i := 0; i < 55; i++ {
		err := e.a.Store.PutAudit(ctx, AuditEntry{TenantID: u.PersonalTenantID, ActorID: u.ID, ActorName: "Ann", Action: "client-create",
			Target: "t", Result: "success", IP: "203.0.113.1", At: base.Add(time.Duration(i+1) * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/t/" + u.PersonalTenantID + "/audit"
	r := b.do("GET", path, nil)
	want(t, r, 200, "")
	var p1 struct {
		Entries    []map[string]any `json:"entries"`
		NextCursor string           `json:"nextCursor"`
	}
	_ = json.Unmarshal(r.Body, &p1)
	if len(p1.Entries) != 50 || p1.NextCursor == "" || p1.Entries[0]["actorName"] != "Ann" || p1.Entries[0]["ip"] != "203.0.113.1" {
		t.Fatalf("page 1 = %d entries, next %q, first %v", len(p1.Entries), p1.NextCursor, p1.Entries[0])
	}
	r = b.do("GET", path+"?cursor="+url.QueryEscape(p1.NextCursor), nil)
	want(t, r, 200, "")
	var p2 struct {
		Entries    []map[string]any `json:"entries"`
		NextCursor string           `json:"nextCursor"`
	}
	_ = json.Unmarshal(r.Body, &p2)
	// 55 written plus the sign-in and step-up lines of the session itself.
	if len(p2.Entries) == 0 || p2.NextCursor != "" {
		t.Fatalf("page 2 = %d entries, next %q", len(p2.Entries), p2.NextCursor)
	}
	for _, bad := range []string{"x", "META", "AUDIT#", "MEMBER%23" + u.ID, "AUDIT%23zzz%23" + id.New(), "TENANT%23" + id.New(), strings.Repeat("A", 5000)} {
		want(t, b.do("GET", path+"?cursor="+bad, nil), 400, "invalid_cursor")
	}
}

// Why: the tenant list is what the SPA opens on; it must hold only the user's own workspaces, personal first.
func TestTenantsListPersonalFirst(t *testing.T) {
	e := newAuthEnv(t)
	u, b := e.owner(t, "a@x.com", "203.0.113.1")
	_, _ = e.owner(t, "b@x.com", "203.0.113.2") // someone else's workspace must not show
	ctx := context.Background()
	org := id.New()
	ti := tenantKey(org)
	ti["kind"], ti["name"], ti["createdBy"], ti["createdAt"] = s("org"), s("Acme"), s(u.ID), s(ts(e.a.now()))
	um := userTenantKey(u.ID, org)
	um["role"], um["joinedAt"] = s(RoleAdmin), s(ts(e.a.now()))
	for _, it := range []item{ti, um, memberPutItem(e.a.Store, org, u.ID, RoleAdmin, e.a.now())} {
		if _, err := e.a.Store.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(e.a.Store.Table), Item: it}); err != nil {
			t.Fatal(err)
		}
	}
	r := b.do("GET", "/api/tenants", nil)
	want(t, r, 200, "")
	var out struct{ Tenants []tenantView }
	_ = json.Unmarshal(r.Body, &out)
	wantList := []tenantView{{u.PersonalTenantID, "personal", "", "owner"}, {org, "org", "Acme", "admin"}}
	if !slices.Equal(out.Tenants, wantList) {
		t.Fatalf("tenants = %+v, want %+v", out.Tenants, wantList)
	}
}

func memberPutItem(st *Store, tenantID, userID, role string, at time.Time) item {
	return memberPut(st, tenantID, userID, role, at).Item
}

// Why: clearing a client's origins is a real edit; `{"origins": []}` must reach AuthGate as an empty list (not be
// dropped as if absent), and the audit must say the origins were changed.
func TestPatchWithEmptyOriginsIsForwardedAndAudited(t *testing.T) {
	e := newAuthEnv(t)
	rt := withPortalAPI(e)
	u, b := e.owner(t, "a@x.com", "203.0.113.1")
	cid := id.New()
	want(t, b.do("PATCH", "/api/t/"+u.PersonalTenantID+"/clients/"+cid, map[string]any{"origins": []string{}}), 200, "")
	calls := rt.seen()
	if len(calls) != 1 || !strings.Contains(calls[0].Body, `"origins":[]`) {
		t.Fatalf("calls = %+v", calls)
	}
	got, _, err := e.a.Store.ListAudit(context.Background(), u.PersonalTenantID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range got {
		if x.Action == "client-update" {
			if x.Detail["fields"] != "origins" {
				t.Fatalf("audited fields = %q", x.Detail["fields"])
			}
			return
		}
	}
	t.Fatal("no client-update entry")
}
