package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Why: on the admin function this split is all that keeps the portal key
// check and the admin key check apart; each path must reach exactly one.
func TestAdminRoleSplitsPortalFromAdmin(t *testing.T) {
	named := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(name)) })
	}
	h := adminRole(named("admin"), named("portal"))
	for path, want := range map[string]string{
		"/api/portal/clients":     "portal",
		"/api/portal/clients/x/y": "portal",
		"/api/portal":             "admin",
		"/api/portalx/clients":    "admin",
		"/api/clients":            "admin",
		"/api/owners/x":           "admin",
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if got := w.Body.String(); got != want {
			t.Errorf("%s reached %s, want %s", path, got, want)
		}
	}
}
