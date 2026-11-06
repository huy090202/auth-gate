package authgate_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huy090202/auth-gate/internal/console/authgate"
)

const (
	clientKey = "ag_client_secret"
	adminKey  = "admin-secret"
)

func isUnavailable(err error) bool { return errors.Is(err, authgate.ErrUnavailable) }

// stub answers every request with one canned response and records the last one.
type stub struct {
	srv        *httptest.Server
	path, key  string
	status     int
	retryAfter string
	body       string
}

func newStub(t *testing.T, status int, body string) *stub {
	t.Helper()
	s := &stub{status: status, body: body}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.path, s.key = r.URL.Path, r.Header.Get("x-internal-access-key")
		w.Header().Set("Content-Type", "application/json")
		if s.retryAfter != "" {
			w.Header().Set("Retry-After", s.retryAfter)
		}
		w.WriteHeader(s.status)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stub) client() *authgate.Client {
	return &authgate.Client{BaseURL: s.srv.URL, ClientKey: clientKey, AdminKey: adminKey, HTTP: s.srv.Client()}
}

func TestRateLimitedComesThroughWithRetryAfter(t *testing.T) {
	s := newStub(t, http.StatusTooManyRequests, `{"error":"rate_limited"}`)
	s.retryAfter = "7"
	_, err := s.client().Factors(context.Background(), "u1")
	var e *authgate.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if e.Status != 429 || e.Code != "rate_limited" || e.RetryAfter != 7 {
		t.Fatalf("error %+v", e)
	}
	if isUnavailable(err) {
		t.Fatal("429 must not read as unavailable")
	}
	// The localhost role prefix and the client key header.
	if s.path != "/verify/api/factors/u1" || s.key != clientKey {
		t.Fatalf("request path %q key-ok=%v", s.path, s.key == clientKey)
	}
}

func TestServerErrorAndOwnKey401AreUnavailable(t *testing.T) {
	ctx := context.Background()
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusUnauthorized} {
		s := newStub(t, status, `{"error":"unauthorized","hint":"`+clientKey+`"}`)
		c := s.client()
		calls := map[string]error{
			"client key": func() error { _, err := c.Factors(ctx, "u1"); return err }(),
			"admin key":  func() error { _, err := c.GetClient(ctx, "c1"); return err }(),
		}
		for name, err := range calls {
			if !isUnavailable(err) {
				t.Fatalf("%d via %s: err = %v, want ErrUnavailable", status, name, err)
			}
			if strings.Contains(err.Error(), clientKey) || strings.Contains(err.Error(), adminKey) {
				t.Fatalf("error text carries a key: %v", err)
			}
		}
	}
	// A 4xx that is not AuthGate's (a CDN or WAF page) has no error code.
	html := newStub(t, http.StatusForbidden, "<html>blocked</html>")
	if _, err := html.client().Factors(ctx, "u1"); !isUnavailable(err) {
		t.Fatalf("foreign 403 = %v, want ErrUnavailable", err)
	}
	// A dead server is unavailable too.
	s := newStub(t, 200, `{}`)
	c := s.client()
	s.srv.Close()
	if _, err := c.Factors(ctx, "u1"); !isUnavailable(err) {
		t.Fatalf("network failure = %v, want ErrUnavailable", err)
	}
}

func TestPassThroughCodes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		status int
		body   string
		code   string
		extra  string
	}{
		{"verification_failed", 400, `{"error":"verification_failed","attemptsRemaining":2}`, "verification_failed", "attemptsRemaining"},
		{"locked", 423, `{"error":"locked","lockedUntil":"2026-11-06T10:00:00Z"}`, "locked", "lockedUntil"},
		{"challenge_gone", 410, `{"error":"challenge_gone"}`, "challenge_gone", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStub(t, tc.status, tc.body)
			pin := "123456"
			_, err := s.client().VerifyChallenge(ctx, "ch1", &pin, nil)
			var e *authgate.Error
			if !errors.As(err, &e) || isUnavailable(err) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if e.Status != tc.status || e.Code != tc.code {
				t.Fatalf("error %+v", e)
			}
			if tc.extra != "" && e.Extra[tc.extra] == nil {
				t.Fatalf("extra %q missing: %+v", tc.extra, e.Extra)
			}
			if tc.extra == "" && len(e.Extra) != 0 {
				t.Fatalf("unexpected extra %+v", e.Extra)
			}
		})
	}
}

func TestRedirectDoesNotCarryKey(t *testing.T) {
	var seen string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("x-internal-access-key")
	}))
	defer target.Close()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+r.URL.Path)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer front.Close()
	// HTTP is nil, so the client builds its own, which must not follow redirects.
	c := &authgate.Client{BaseURL: front.URL, ClientKey: clientKey, AdminKey: adminKey}
	if _, err := c.Factors(context.Background(), "u1"); !isUnavailable(err) {
		t.Fatalf("redirect = %v, want ErrUnavailable", err)
	}
	if seen != "" {
		t.Fatal("the redirect target received the key")
	}
}
