package authgate_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
)

const portalKey = "portal-secret"

type seen struct{ method, path, key, tenant, body string }

// Why: the Portal's tenant isolation rests on every call carrying the portal key
// and the workspace's tenant id, never the admin or client key.
func TestTenantClientSendsPortalKeyAndTenant(t *testing.T) {
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.RequestURI(), r.Header.Get("x-internal-access-key"), r.Header.Get("x-tenant-id"), string(b)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &authgate.Client{BaseURL: srv.URL, ClientKey: clientKey, AdminKey: adminKey, PortalKey: portalKey, HTTP: srv.Client()}
	tc := c.Tenant("tn1")
	ctx := context.Background()

	steps := []struct {
		method, path, body string
		call               func() error
	}{
		{"GET", "/api/portal/clients", "", func() error { _, err := tc.List(ctx, ""); return err }},
		{"GET", "/api/portal/clients?cursor=a+b", "", func() error { _, err := tc.List(ctx, "a b"); return err }},
		{"GET", "/api/portal/clients/c1", "", func() error { _, err := tc.Get(ctx, "c1"); return err }},
		{"POST", "/api/portal/clients", `{"name":"x"}`, func() error { _, err := tc.Create(ctx, map[string]string{"name": "x"}); return err }},
		{"PATCH", "/api/portal/clients/c1", `{"name":"y"}`, func() error { _, err := tc.Update(ctx, "c1", json.RawMessage(`{"name":"y"}`)); return err }},
		{"POST", "/api/portal/clients/c1/disable", `{}`, func() error { _, err := tc.Disable(ctx, "c1"); return err }},
		{"POST", "/api/portal/clients/c1/enable", `{}`, func() error { _, err := tc.Enable(ctx, "c1"); return err }},
		{"POST", "/api/portal/clients/c1/keys", `{}`, func() error { _, err := tc.RotateKey(ctx, "c1"); return err }},
		{"DELETE", "/api/portal/clients/c1/keys/previous", "", func() error { _, err := tc.RevokePreviousKeys(ctx, "c1"); return err }},
		{"DELETE", "/api/portal/clients/c1/subjects/u%2F1", "", func() error { return tc.PurgeSubject(ctx, "c1", "u/1") }},
		{"DELETE", "/api/portal/clients/c1", "", func() error { return tc.Delete(ctx, "c1") }},
	}
	for _, s := range steps {
		got = nil
		if err := s.call(); err != nil {
			t.Fatalf("%s %s: %v", s.method, s.path, err)
		}
		if len(got) != 1 {
			t.Fatalf("%s %s: %d requests", s.method, s.path, len(got))
		}
		// The test server is local, so the role prefix /admin is added.
		want := seen{s.method, "/admin" + s.path, portalKey, "tn1", s.body}
		if got[0] != want {
			t.Errorf("got %+v, want %+v", got[0], want)
		}
	}
}

// Why: an unset portal key must fail closed, not fall back to the admin or client key.
func TestTenantClientWithoutPortalKeySendsNothing(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	c := &authgate.Client{BaseURL: srv.URL, ClientKey: clientKey, AdminKey: adminKey, HTTP: srv.Client()}
	if _, err := c.Tenant("tn1").List(context.Background(), ""); !isUnavailable(err) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if _, err := (&authgate.Client{BaseURL: srv.URL, PortalKey: portalKey, HTTP: srv.Client()}).Tenant("").List(context.Background(), ""); !isUnavailable(err) {
		t.Fatalf("empty tenant: err = %v, want ErrUnavailable", err)
	}
	if hit {
		t.Fatal("a request was sent")
	}
}

func TestTenantClientNotFoundComesThrough(t *testing.T) {
	s := newStub(t, http.StatusNotFound, `{"error":"client_not_found"}`)
	c := s.client()
	c.PortalKey = portalKey
	_, err := c.Tenant("tn1").Get(context.Background(), "nope")
	var e *authgate.Error
	if !errors.As(err, &e) || e.Status != 404 || e.Code != "client_not_found" {
		t.Fatalf("err = %v, want 404 client_not_found", err)
	}
}
