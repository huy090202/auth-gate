package portalapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/huy090202/auth-gate/internal/platform/portalapi"
)

// Why: a redirect must never carry the operator key to another host, our own 401 must not reach the caller as theirs, and a POST without a body must still be JSON or the Portal answers 415.
func TestClientHardening(t *testing.T) {
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer other.Close()
	var ct string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/operator/tenants":
			http.Redirect(w, r, other.URL, http.StatusFound)
		case "/api/operator/tenants/T/suspend":
			ct = r.Header.Get("Content-Type")
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"not_found"}`))
		}
	}))
	defer srv.Close()
	c := &portalapi.Client{BaseURL: srv.URL, OperatorKey: "k"}

	if _, err := c.ListTenants(context.Background(), ""); !errors.Is(err, portalapi.ErrUnavailable) || leaked {
		t.Fatalf("redirect: err=%v leaked=%v", err, leaked)
	}
	if _, err := c.SuspendTenant(context.Background(), "T"); !errors.Is(err, portalapi.ErrUnavailable) || ct != "application/json" {
		t.Fatalf("401: err=%v content-type=%q", err, ct)
	}
	var pe *portalapi.Error
	if _, err := c.GetTenant(context.Background(), "X"); !errors.As(err, &pe) || pe.Status != 404 || pe.Code != "not_found" {
		t.Fatalf("4xx: %v", err)
	}
}
