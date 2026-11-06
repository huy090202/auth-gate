package console

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

// Why: a state-changing request that is not JSON is how a cross-site form post looks.
func TestRequireJSON(t *testing.T) {
	h := requireJSON(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
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

// Why: a service that boots without its wiring fails on the first request, not at start.
func TestLoadConfigRefusesMissing(t *testing.T) {
	full := map[string]string{
		"TABLE_NAME": "t", "AUTHGATE_BASE_URL": "https://a", "AUTHGATE_CLIENT_ID": "c",
		"ADMIN_KEY_PARAM": "/a", "CLIENT_KEY_PARAM": "/c", "ORIGIN_VERIFY_PARAM": "/o",
	}
	dev := map[string]string{
		"CONSOLE_DEV": "1", "TABLE_NAME": "t", "AUTHGATE_BASE_URL": "http://a", "AUTHGATE_CLIENT_ID": "c",
		"ADMIN_KEY": "k", "CLIENT_KEY": "k",
	}
	without := func(base map[string]string, drop string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			if k != drop {
				m[k] = v
			}
		}
		return m
	}
	cases := []struct {
		name string
		env  map[string]string
		ok   bool
	}{
		{"stage complete", full, true},
		{"stage without table", without(full, "TABLE_NAME"), false},
		{"stage without authgate url", without(full, "AUTHGATE_BASE_URL"), false},
		{"stage without client id", without(full, "AUTHGATE_CLIENT_ID"), false},
		{"stage without admin key param", without(full, "ADMIN_KEY_PARAM"), false},
		{"stage without client key param", without(full, "CLIENT_KEY_PARAM"), false},
		{"stage without origin param", without(full, "ORIGIN_VERIFY_PARAM"), false},
		{"dev complete", dev, true},
		{"dev without admin key", without(dev, "ADMIN_KEY"), false},
		{"dev without client key", without(dev, "CLIENT_KEY"), false},
		{"dev without table", without(dev, "TABLE_NAME"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := LoadConfig(func(k string) string { return tc.env[k] })
			if (err == nil) != tc.ok {
				t.Fatalf("LoadConfig err = %v, want ok=%v", err, tc.ok)
			}
			if tc.ok && c.Dev != (tc.env["CONSOLE_DEV"] == "1") {
				t.Fatalf("Dev = %v", c.Dev)
			}
		})
	}
}

func newTestApp(t *testing.T) http.Handler {
	t.Helper()
	db, table := dynamotest.NewTable(t)
	now := func() time.Time { return time.Unix(1_800_000_000, 0) }
	return New(Deps{
		Config:  Config{Table: table},
		DB:      db,
		Limiter: &ratelimit.Limiter{DB: db, Table: table, TTL: time.Hour, Now: now},
		Now:     now,
	})
}

func authReq(h http.Handler, viewer, xff string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/api/auth/session", nil)
	if viewer != "" {
		r.Header.Set("CloudFront-Viewer-Address", viewer)
	}
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Why: the sign-in routes need a brake per visitor, and one visitor must not spend another's.
func TestAuthRoutesRateLimitedPerIP(t *testing.T) {
	h := newTestApp(t)
	for i := 0; i < 10; i++ {
		if w := authReq(h, "198.51.100.10:1000", ""); w.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d limited", i+1)
		}
	}
	w := authReq(h, "198.51.100.10:2000", "")
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"rate_limited"`) {
		t.Fatalf("11th = %d %s", w.Code, w.Body)
	}
	if ra, _ := strconv.Atoi(w.Header().Get("Retry-After")); ra != 6 {
		t.Fatalf("Retry-After = %q, want 6", w.Header().Get("Retry-After"))
	}
	if w := authReq(h, "198.51.100.11:1000", ""); w.Code == http.StatusTooManyRequests {
		t.Fatal("other IP was limited")
	}
}

// Why: X-Forwarded-For is caller-controlled; if it chose the bucket, anyone could skip the limit.
func TestForgedXForwardedForDoesNotChangeBucket(t *testing.T) {
	h := newTestApp(t)
	for i := 0; i < 10; i++ {
		authReq(h, "198.51.100.10:1000", "10.0.0."+strconv.Itoa(i))
	}
	if w := authReq(h, "198.51.100.10:1000", "10.9.9.9"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("forged header escaped the limit: %d", w.Code)
	}
}

// Why: a missing header must neither disable the limit nor fall back to a forgeable key.
func TestMissingViewerAddressSharesOneBucket(t *testing.T) {
	h := newTestApp(t)
	for i := 0; i < 10; i++ {
		authReq(h, "", "10.0.0."+strconv.Itoa(i))
	}
	if w := authReq(h, "", "10.9.9.9"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("no-header request escaped the limit: %d", w.Code)
	}
}

// Why: the SPA reads every API answer as JSON, so an unknown path must not return HTML or text.
func TestUnknownAPIPathIsJSON404(t *testing.T) {
	h := newTestApp(t)
	r := httptest.NewRequest("GET", "/api/nope", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || !strings.Contains(w.Body.String(), `"not_found"`) || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("got %d %q %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
}
