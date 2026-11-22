package webapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Why: a state-changing request that is not JSON is how a cross-site form post looks.
func TestRequireJSON(t *testing.T) {
	h := RequireJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	cases := []struct {
		method, ctype string
		want          int
	}{
		{"POST", "text/plain", 415},
		{"POST", "", 415},
		{"POST", "application/json; charset=utf-8", 204},
		{"POST", "application/json", 204},
		{"GET", "", 204},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, "/api/x", strings.NewReader("{}"))
		if c.ctype != "" {
			r.Header.Set("Content-Type", c.ctype)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s %q = %d, want %d", c.method, c.ctype, w.Code, c.want)
		}
		if c.want == 415 && !strings.Contains(w.Body.String(), "unsupported_media_type") {
			t.Errorf("415 body = %s", w.Body)
		}
	}
}
