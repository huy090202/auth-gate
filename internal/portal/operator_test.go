package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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
