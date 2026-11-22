package authgate_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/huy090202/auth-gate/internal/auth/token"
	"github.com/huy090202/auth-gate/internal/platform/authgate"
)

const (
	aud     = "console-client"
	subject = "admin-1"
	purpose = "login"
)

// jwksStub serves the key set under the localhost role prefix and counts fetches.
type jwksStub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	signers []token.Signer
	fetches int
	fail    bool
}

func newJWKSStub(t *testing.T, signers ...token.Signer) *jwksStub {
	t.Helper()
	s := &jwksStub{signers: signers}
	mux := http.NewServeMux()
	mux.HandleFunc("/jwks/.well-known/jwks.json", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.fetches++
		if s.fail {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		set, err := token.JWKS(r.Context(), s.signers...)
		if err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(set)
	})
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

func (s *jwksStub) count() int { s.mu.Lock(); defer s.mu.Unlock(); return s.fetches }

func (s *jwksStub) set(fail bool, signers ...token.Signer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = fail
	if signers != nil {
		s.signers = signers
	}
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func signer(t *testing.T, kid string) *token.LocalSigner {
	t.Helper()
	s, err := token.NewLocalSigner(kid)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func issue(t *testing.T, s token.Signer, mod func(*token.Claims), iss string) string {
	t.Helper()
	c := token.Claims{
		Issuer: iss, Subject: subject, Audience: aud, Purpose: purpose, JTI: "j1",
		AMR: []string{"pin"}, IssuedAt: time.Now(), ExpiresAt: time.Now().Add(10 * time.Minute),
	}
	if mod != nil {
		mod(&c)
	}
	tok, err := token.Issue(context.Background(), s, c)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func verifier(s *jwksStub, c *clock) *authgate.Verifier {
	return &authgate.Verifier{BaseURL: s.srv.URL, Audience: aud, HTTP: s.srv.Client(), Now: c.now}
}

func TestVerifyAcceptsGoodToken(t *testing.T) {
	k := signer(t, "k1")
	stub := newJWKSStub(t, k)
	v := verifier(stub, &clock{time.Now()})
	got, err := v.Verify(context.Background(), issue(t, k, nil, stub.srv.URL), subject, purpose)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != subject || got.Purpose != purpose || got.JTI != "j1" {
		t.Fatalf("claims %+v", got)
	}
}

func TestVerifyRejects(t *testing.T) {
	k := signer(t, "k1")
	stub := newJWKSStub(t, k)
	other := signer(t, "k1") // same kid, different key
	b64 := base64.RawURLEncoding
	none := b64.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." +
		b64.EncodeToString([]byte(`{"iss":"`+stub.srv.URL+`","sub":"`+subject+`","aud":"`+aud+`","purpose":"`+purpose+`","exp":9999999999}`)) + "."
	cases := []struct{ name, tok string }{
		{"other key", issue(t, other, nil, stub.srv.URL)},
		{"wrong iss", issue(t, k, nil, "https://evil.example")},
		{"wrong aud", issue(t, k, func(c *token.Claims) { c.Audience = "someone-else" }, stub.srv.URL)},
		{"wrong sub", issue(t, k, func(c *token.Claims) { c.Subject = "admin-2" }, stub.srv.URL)},
		{"wrong purpose", issue(t, k, func(c *token.Claims) { c.Purpose = "delete-admin" }, stub.srv.URL)},
		{"expired", issue(t, k, func(c *token.Claims) { c.ExpiresAt = time.Now().Add(-time.Minute) }, stub.srv.URL)},
		{"malformed", "not-a-token"},
		{"alg none", none},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := verifier(stub, &clock{time.Now()})
			if _, err := v.Verify(context.Background(), tc.tok, subject, purpose); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestVerifyRefetchesForUnknownKid(t *testing.T) {
	k1, k2 := signer(t, "k1"), signer(t, "k2")
	stub := newJWKSStub(t, k1)
	c := &clock{time.Now()}
	v := verifier(stub, c)
	ctx := context.Background()
	if _, err := v.Verify(ctx, issue(t, k1, nil, stub.srv.URL), subject, purpose); err != nil {
		t.Fatal(err)
	}
	stub.set(false, k1, k2) // key rotation at AuthGate
	tok := issue(t, k2, nil, stub.srv.URL)
	// A cache younger than 30 s is not refetched on a failed check.
	if _, err := v.Verify(ctx, tok, subject, purpose); err == nil {
		t.Fatal("accepted an unknown kid from a fresh cache")
	}
	if n := stub.count(); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
	c.advance(31 * time.Second)
	if _, err := v.Verify(ctx, tok, subject, purpose); err != nil {
		t.Fatalf("after refetch: %v", err)
	}
	if n := stub.count(); n != 2 {
		t.Fatalf("fetches = %d, want 2", n)
	}
}

func TestJWKSCachedFor300Seconds(t *testing.T) {
	k := signer(t, "k1")
	stub := newJWKSStub(t, k)
	c := &clock{time.Now()}
	v := verifier(stub, c)
	tok := issue(t, k, nil, stub.srv.URL)
	check := func(wantFetches int) {
		t.Helper()
		if _, err := v.Verify(context.Background(), tok, subject, purpose); err != nil {
			t.Fatal(err)
		}
		if n := stub.count(); n != wantFetches {
			t.Fatalf("fetches = %d, want %d", n, wantFetches)
		}
	}
	check(1)
	check(1)
	c.advance(299 * time.Second)
	check(1)
	c.advance(2 * time.Second)
	check(2)
}

func TestKeepsStaleJWKSWhenRefetchFails(t *testing.T) {
	k := signer(t, "k1")
	stub := newJWKSStub(t, k)
	c := &clock{time.Now()}
	v := verifier(stub, c)
	ctx := context.Background()
	tok := issue(t, k, nil, stub.srv.URL)
	if _, err := v.Verify(ctx, tok, subject, purpose); err != nil {
		t.Fatal(err)
	}
	stub.set(true)
	c.advance(400 * time.Second)
	if _, err := v.Verify(ctx, tok, subject, purpose); err != nil {
		t.Fatalf("stale cache was dropped: %v", err)
	}
	if n := stub.count(); n != 2 {
		t.Fatalf("fetches = %d, want 2 (one failed refetch)", n)
	}
	// With no cache at all, a failed fetch is unavailability, not a bad token.
	cold := verifier(stub, c)
	if _, err := cold.Verify(ctx, tok, subject, purpose); !isUnavailable(err) {
		t.Fatalf("cold failure = %v, want ErrUnavailable", err)
	}
}

func TestVerifyExpiryFollowsInjectedClock(t *testing.T) {
	k := signer(t, "k1")
	stub := newJWKSStub(t, k)
	c := &clock{time.Now()}
	v := verifier(stub, c)
	tok := issue(t, k, nil, stub.srv.URL) // lives 10 minutes
	if _, err := v.Verify(context.Background(), tok, subject, purpose); err != nil {
		t.Fatal(err)
	}
	c.advance(11 * time.Minute)
	if _, err := v.Verify(context.Background(), tok, subject, purpose); err == nil {
		t.Fatal("accepted a token past exp on the injected clock")
	}
}

func TestFailedJWKSFetchesAreThrottled(t *testing.T) {
	k := signer(t, "k1")
	stub := newJWKSStub(t, k)
	c := &clock{time.Now()}
	v := verifier(stub, c)
	ctx := context.Background()
	good := issue(t, k, nil, stub.srv.URL)
	if _, err := v.Verify(ctx, good, subject, purpose); err != nil {
		t.Fatal(err)
	}
	stub.set(true)
	c.advance(400 * time.Second)
	forged := issue(t, signer(t, "k1"), nil, stub.srv.URL)
	for i := 0; i < 5; i++ {
		if _, err := v.Verify(ctx, forged, subject, purpose); err == nil {
			t.Fatal("accepted a forged token")
		}
		if _, err := v.Verify(ctx, good, subject, purpose); err != nil {
			t.Fatalf("stale cache dropped: %v", err)
		}
	}
	if n := stub.count(); n != 2 {
		t.Fatalf("fetches = %d, want 2 (the first plus one failed attempt)", n)
	}
	c.advance(31 * time.Second)
	_, _ = v.Verify(ctx, forged, subject, purpose)
	if n := stub.count(); n != 3 {
		t.Fatalf("fetches = %d, want 3 after 30 s", n)
	}
}
