package authgate

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huy090202/auth-gate/internal/auth/token"
)

const (
	jwksTTL    = 300 * time.Second // a cached key set is used this long
	jwksRetry  = 30 * time.Second  // a failed check may refetch once the cache is older than this
	jwksMaxLen = 1 << 20
)

// ErrInvalidToken means the token failed a check. It never says which, and a
// caller must treat every cause the same.
var ErrInvalidToken = errors.New("authgate: invalid token")

// Verifier checks assertion tokens offline against AuthGate's published keys.
// BaseURL is also the expected issuer; Audience is the Console's client id.
// The key set is cached for 300 s. A failed check with a cache older than 30 s
// refetches once and retries, so a rotated key is picked up without a restart;
// a failed refetch keeps the cache.
type Verifier struct {
	BaseURL, Audience string
	HTTP              *http.Client
	Now               func() time.Time // nil means time.Now

	mu      sync.Mutex
	jwks    map[string]any
	fetched time.Time // last successful fetch
	lastTry time.Time // last attempt, successful or not
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify checks alg, kid, signature and expiry, then iss, aud, sub and purpose.
func (v *Verifier) Verify(ctx context.Context, tok, subject, purpose string) (token.Claims, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.jwks == nil || (v.now().Sub(v.fetched) >= jwksTTL && v.mayTry()) {
		// A failed refetch with a cache keeps the cache; without one it is fatal.
		if err := v.fetch(ctx); err != nil && v.jwks == nil {
			return token.Claims{}, err
		}
	}
	c, err := token.VerifyWithJWKS(tok, v.jwks)
	if err != nil && v.now().Sub(v.fetched) > jwksRetry && v.mayTry() {
		if v.fetch(ctx) == nil {
			c, err = token.VerifyWithJWKS(tok, v.jwks)
		}
	}
	if err != nil {
		return token.Claims{}, ErrInvalidToken
	}
	if !v.now().Before(c.ExpiresAt) {
		return token.Claims{}, ErrInvalidToken
	}
	if c.Issuer != v.BaseURL || c.Audience != v.Audience || c.Subject != subject || c.Purpose != purpose {
		return token.Claims{}, ErrInvalidToken
	}
	return c, nil
}

// mayTry reports whether a fetch is allowed: at most one attempt per 30 s, so
// an outage costs one request per interval, not one per Verify.
func (v *Verifier) mayTry() bool { return v.now().Sub(v.lastTry) > jwksRetry }

// fetch replaces the cache. On failure the cache is left as it was.
func (v *Verifier) fetch(ctx context.Context) error {
	v.lastTry = v.now()
	client := noRedirect(v.HTTP)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rolePath(v.BaseURL, "/.well-known/jwks.json"), nil)
	if err != nil {
		return fmt.Errorf("%w: build jwks request", ErrUnavailable)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: jwks request failed", ErrUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: jwks status %d", ErrUnavailable, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, jwksMaxLen))
	if err != nil {
		return fmt.Errorf("%w: jwks read failed", ErrUnavailable)
	}
	var set map[string]any
	if err := json.Unmarshal(raw, &set); err != nil || set["keys"] == nil {
		return fmt.Errorf("%w: jwks unreadable", ErrUnavailable)
	}
	v.jwks, v.fetched = set, v.now()
	return nil
}

// UnverifiedExp reads the exp claim of a token for a browser to show. It checks
// nothing else: the token came from AuthGate in answer to our own call and is
// fully checked by Verify when an action uses it.
func UnverifiedExp(tok string) (time.Time, bool) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var c struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &c) != nil || c.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(c.Exp, 0).UTC(), true
}
