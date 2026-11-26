package console_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
