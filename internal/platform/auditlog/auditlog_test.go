package auditlog

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Why: a Console action whose AuthGate half succeeded must not be logged as a plain failure.
func TestWrapPrefersSetResult(t *testing.T) {
	var got Entry
	h := Wrap("tenant-suspend", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetResult(r.Context(), "partial")
		w.WriteHeader(http.StatusBadGateway)
	}), func(_ *http.Request, e Entry) { got = e })
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/", nil))
	if got.Result != "partial" {
		t.Fatalf("Result = %q, want partial", got.Result)
	}
}
