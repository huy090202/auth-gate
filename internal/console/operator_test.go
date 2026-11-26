package console_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

// Why: the operator must see both halves separately, or a half-done suspension looks finished.
func TestTenantPageCombinesBothSides(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	tid := h.Portal.AddTenant(consoletest.FakeTenant{Name: "Acme"})
	if _, err := h.AuthGate.SuspendOwner(context.Background(), tid); err != nil {
		t.Fatal(err)
	}
	r := b.Do("GET", "/api/tenants/"+tid, nil)
	want(t, r, 200, "")
	var page struct {
		Tenant struct {
			Name        string `json:"name"`
			SuspendedAt string `json:"suspendedAt"`
		} `json:"tenant"`
		Owner struct {
			SuspendedAt *string `json:"suspendedAt"`
			ClientQuota int     `json:"clientQuota"`
		} `json:"owner"`
		Clients json.RawMessage `json:"clients"`
	}
	if err := json.Unmarshal(r.Body, &page); err != nil {
		t.Fatal(err)
	}
	if page.Tenant.Name != "Acme" || page.Tenant.SuspendedAt != "" {
		t.Fatalf("the Portal half: %s", r.Body)
	}
	if page.Owner.SuspendedAt == nil || page.Owner.ClientQuota == 0 || len(page.Clients) == 0 {
		t.Fatalf("the AuthGate half: %s", r.Body)
	}
	// The tenants list is enriched from the same owner view.
	r = b.Do("GET", "/api/tenants", nil)
	want(t, r, 200, "")
	if !strings.Contains(string(r.Body), `"clientCount":0`) || !strings.Contains(string(r.Body), `"clientQuota":`) {
		t.Fatalf("counts missing: %s", r.Body)
	}
}

// Why: the Console's tenant and user pages are for signed-in admins only; the Portal's operator key must not be reachable without a session.
func TestOperatorRoutesNeedSession(t *testing.T) {
	h := consoletest.New(t)
	tid := h.Portal.AddTenant(consoletest.FakeTenant{Name: "Acme"})
	b := h.NewBrowser()
	for _, p := range []string{"/api/tenants", "/api/tenants/" + tid, "/api/tenants/" + tid + "/audit", "/api/users?email=a@b.test"} {
		want(t, b.Do("GET", p, nil), 401, "")
	}
	if n := len(h.Portal.Calls()); n != 0 {
		t.Fatalf("the Portal was called %d times without a session", n)
	}
}

// failOwners fails every AuthGate owner call, so the list's enrichment has nothing to add.
type failOwners struct{ next http.RoundTripper }

func (f failOwners) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.Contains(r.URL.Path, "/api/owners/") {
		return nil, errors.New("down")
	}
	return f.next.RoundTrip(r)
}

// Why: a list that dies with one AuthGate hiccup would hide every tenant from the operator, and a Portal outage must read as 502, not as an empty list.
func TestTenantsListSurvivesOwnerFailure(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	tid := h.Portal.AddTenant(consoletest.FakeTenant{Name: "Acme"})

	d := h.Deps
	hc := *h.AuthGate.HTTP
	hc.Transport = failOwners{hc.Transport}
	d.AuthGate = &authgate.Client{BaseURL: h.AuthGateURL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey, HTTP: &hc}
	b2 := h.BrowserFor(console.New(d))
	b2.SetCookie(b.CookieValue())
	r := b2.Do("GET", "/api/tenants", nil)
	want(t, r, 200, "")
	if !strings.Contains(string(r.Body), tid) || strings.Contains(string(r.Body), "clientCount") {
		t.Fatalf("want the row without counts: %s", r.Body)
	}

	h.Portal.SetDown(true)
	want(t, b.Do("GET", "/api/tenants", nil), 502, "portal_unavailable")
	want(t, b.Do("GET", "/api/tenants/"+tid, nil), 502, "portal_unavailable")
}

// Why: the operator needs the reason of the last suspension on the page, and a refused attempt is not a suspension.
func TestLastSuspensionFromConsoleAudit(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	tid := h.Portal.AddTenant(consoletest.FakeTenant{Name: "Acme"})
	uid := h.Portal.AddUser(consoletest.FakeUser{Email: "u@x.test", DisplayName: "U"})
	put := func(action, target, result, reason string) {
		h.Clock.Advance(1) // entries sort by time
		e := console.AuditEntry{Username: "ada", Action: action, Target: target, Result: result, At: h.Clock.Now(), Detail: map[string]string{"reason": reason}}
		if err := h.Store().PutAudit(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	put("tenant-suspend", tid, "success", "old")
	put("tenant-suspend", tid, "success", "chargeback")
	put("tenant-suspend", tid, "failure", "refused")
	put("tenant-suspend", id.New(), "success", "other tenant")
	put("user-suspend", uid, "success", "spam")

	r := b.Do("GET", "/api/tenants/"+tid, nil)
	want(t, r, 200, "")
	var tp struct {
		Last struct{ Reason, Username string } `json:"lastSuspension"`
	}
	if err := json.Unmarshal(r.Body, &tp); err != nil || tp.Last.Reason != "chargeback" || tp.Last.Username != "ada" {
		t.Fatalf("tenant: %s %v", r.Body, err)
	}
	r = b.Do("GET", "/api/users?email=u@x.test", nil)
	want(t, r, 200, "")
	if err := json.Unmarshal(r.Body, &tp); err != nil || tp.Last.Reason != "spam" {
		t.Fatalf("user: %s %v", r.Body, err)
	}
	want(t, b.Do("GET", "/api/users?email=nobody@x.test", nil), 404, "not_found")
}

func lastAudit(t *testing.T, h *consoletest.Harness, action string) console.AuditEntry {
	t.Helper()
	es, _, err := h.Store().ListAudit(context.Background(), h.Clock.Now().UTC().Format("2006-01"), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.Action == action {
			return e
		}
	}
	t.Fatalf("no %s audit entry", action)
	return console.AuditEntry{}
}

// Why: a half-done suspension must be visible and repeatable; AuthGate goes first so a Portal outage never leaves the Portal saying suspended while the keys still work.
func TestTenantSuspendOrderAndPartial(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	tid := h.Portal.AddTenant(consoletest.FakeTenant{Name: "Acme"})
	path := "/api/tenants/" + tid + "/suspend"
	body := map[string]string{"reason": "chargeback"}

	h.Portal.SetDown(true)
	r := b.Do("POST", path, body, tokHeader, b.StepUp("tenant-suspend", tid))
	if r.Status != 502 && r.Status != 503 {
		t.Fatalf("want 502 or 503, got %d %s", r.Status, r.Body)
	}
	raw, err := h.AuthGate.GetOwner(context.Background(), tid)
	if err != nil || !strings.Contains(string(raw), `"suspendedAt":"`) {
		t.Fatalf("AuthGate's owner is not suspended: %s %v", raw, err)
	}
	if e := lastAudit(t, h, "tenant-suspend"); e.Result != "partial" || e.Detail["reason"] != "chargeback" || e.Target != tid {
		t.Fatalf("audit: %+v", e)
	}

	h.Portal.SetDown(false)
	want(t, b.Do("POST", path, body, tokHeader, b.StepUp("tenant-suspend", tid)), 200, "")
	if tn, _ := h.Portal.Tenant(tid); tn.SuspendedAt == "" {
		t.Fatal("the Portal was not called on the repeat")
	}
	if e := lastAudit(t, h, "tenant-suspend"); e.Result != "success" {
		t.Fatalf("audit: %+v", e)
	}
}

// Why: a bad reason must not burn the one-use token, and no write may run without step-up.
func TestSuspendNeedsReasonAndStepUp(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	tid := h.Portal.AddTenant(consoletest.FakeTenant{Name: "Acme"})
	uid := h.Portal.AddUser(consoletest.FakeUser{Email: "u@x.test"})
	for _, c := range []struct{ action, target, path string }{
		{"tenant-suspend", tid, "/api/tenants/" + tid + "/suspend"},
		{"user-suspend", uid, "/api/users/" + uid + "/suspend"},
	} {
		tok := b.StepUp(c.action, c.target)
		for _, reason := range []string{"", "   ", strings.Repeat("a", 201), strings.Repeat("é", 201)} {
			want(t, b.Do("POST", c.path, map[string]string{"reason": reason}, tokHeader, tok), 400, "invalid_reason")
		}
		requireUnspent(t, h, tok) // a refusal costs no token (this also spends it)
		// 200 multi-byte runes are 400 bytes and still fine.
		tok = b.StepUp(c.action, c.target)
		want(t, b.Do("POST", c.path, map[string]string{"reason": " " + strings.Repeat("é", 200) + " "}, tokHeader, tok), 200, "")
		want(t, b.Do("POST", c.path, map[string]string{"reason": "x"}), 403, "step_up_required")
	}
	if u, _ := h.Portal.User(uid); u.SuspendedAt == "" {
		t.Fatal("user not suspended")
	}
}

// Why: the quota audit must carry the real old value, read from AuthGate before the write.
func TestTenantQuotaAndUserActions(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	tid := h.Portal.AddTenant(consoletest.FakeTenant{Name: "Acme"})
	uid := h.Portal.AddUser(consoletest.FakeUser{Email: "u@x.test"})
	raw, _ := h.AuthGate.GetOwner(context.Background(), tid)
	var o struct{ ClientQuota int }
	_ = json.Unmarshal(raw, &o)

	want(t, b.Do("PATCH", "/api/tenants/"+tid+"/quota", map[string]int{"clientQuota": 7}, tokHeader, b.StepUp("tenant-quota", tid)), 200, "")
	es := h.Portal.TenantAuditEntries(tid)
	if len(es) != 1 || es[0]["action"] != "tenant-quota" {
		t.Fatalf("portal audit: %v", es)
	}
	if e := lastAudit(t, h, "tenant-quota"); e.Detail["from"] != strconv.Itoa(o.ClientQuota) || e.Detail["to"] != "7" {
		t.Fatalf("audit: %+v", e)
	}
	want(t, b.Do("PATCH", "/api/tenants/"+tid+"/quota", map[string]int{"clientQuota": -1}, tokHeader, b.StepUp("tenant-quota", tid)), 400, "invalid_quota")

	for _, c := range []struct{ action, path string }{
		{"user-unsuspend", "/unsuspend"}, {"user-resend-verification", "/resend-verification"}, {"user-factor-reset", "/factor-reset"},
	} {
		want(t, b.Do("POST", "/api/users/"+uid+c.path, nil, tokHeader, b.StepUp(c.action, uid)), 200, "")
	}
}

// Why: a tenant suspension of an unknown or malformed id must not reach either service.
func TestTenantUnsuspendRoute(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	tid := h.Portal.AddTenant(consoletest.FakeTenant{Name: "Acme", SuspendedAt: "2026-11-01T00:00:00Z"})
	want(t, b.Do("POST", "/api/tenants/bad/unsuspend", nil), 404, "not_found")
	want(t, b.Do("POST", "/api/tenants/"+tid+"/unsuspend", nil, tokHeader, b.StepUp("tenant-unsuspend", tid)), 200, "")
	if tn, _ := h.Portal.Tenant(tid); tn.SuspendedAt != "" {
		t.Fatal("still suspended")
	}
}
