// Package authgate is the HTTP client the Console and the Portal use for
// AuthGate, and the offline check of the tokens AuthGate issues. It must not
// import internal/auth/service: both apps reach AuthGate only over HTTP.
package authgate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrUnavailable means AuthGate cannot be relied on right now: the network failed,
// it answered 5xx, or it answered 401 to our own key. The 401 case is our
// misconfiguration, never the caller's, so it is not passed on.
var ErrUnavailable = errors.New("authgate: unavailable")

// Error is a 4xx answer from AuthGate (including 429), passed through as is.
type Error struct {
	Status     int
	Code       string
	Extra      map[string]any // every body field except "error"
	RetryAfter int            // whole seconds from Retry-After; 0 when absent
}

func (e *Error) Error() string { return fmt.Sprintf("authgate: %d %s", e.Status, e.Code) }

// Credential is a passkey as AuthGate lists it.
type Credential struct {
	CredentialID string     `json:"credentialId"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastUsedAt   *time.Time `json:"lastUsedAt"`
}

// Factors is what a subject has enrolled.
type Factors struct {
	PIN      bool         `json:"pin"`
	WebAuthn []Credential `json:"webauthn"`
}

// Challenge is an open challenge. PublicKey is set for a passkey challenge.
type Challenge struct {
	ID        string          `json:"challengeId"`
	ExpiresAt time.Time       `json:"expiresAt"`
	PublicKey json.RawMessage `json:"publicKey,omitempty"`
}

// Client calls AuthGate. ClientKey signs the verify-role calls, AdminKey the
// admin-role calls and PortalKey the tenant-scoped /api/portal calls (see
// Tenant). A caller sets the keys it needs; none is ever logged or put in an
// error, and no call falls back to another key.
type Client struct {
	BaseURL, ClientKey, AdminKey, PortalKey string
	HTTP                                    *http.Client
}

const maxBody = 1 << 20

// Timeout is the Console's HTTP timeout for AuthGate calls. It equals AuthGate's
// own function timeout (Timeout: 10 in infra/template.yaml), which is what bounds
// how late a sent write can land.
const Timeout = 10 * time.Second

// rolePath adds the role prefix when base is a local address, as `make dev`
// serves the three roles on one port. It mirrors cmd/smoke.
func rolePath(base, path string) string {
	base = strings.TrimRight(base, "/")
	u, err := url.Parse(base)
	if err != nil {
		return base + path
	}
	if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" && h != "::1" {
		return base + path
	}
	switch {
	case strings.HasPrefix(path, "/api/clients"), strings.HasPrefix(path, "/api/portal"), strings.HasPrefix(path, "/api/owners"):
		return base + "/admin" + path
	case strings.HasPrefix(path, "/.well-known/"):
		return base + "/jwks" + path
	default:
		return base + "/verify" + path
	}
}

// noRedirect returns a copy of hc (or a default client) that never follows a
// redirect: Go would forward the key header to the target. A 3xx then reaches do
// as an unexpected status.
func noRedirect(hc *http.Client) *http.Client {
	var cp http.Client
	if hc != nil {
		cp = *hc
	} else {
		cp.Timeout = Timeout
	}
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

// ErrNoAnswer marks an ErrUnavailable where no status came back (the request
// failed, timed out or the body was cut off): a write that failed so may still land.
// An answered 5xx is not one; see ErrServerError.
var ErrNoAnswer = errors.New("authgate: no answer")

// ErrServerError marks an ErrUnavailable that was an answered 5xx, from AuthGate
// or from something in front of it: a write answered so may still have landed.
var ErrServerError = errors.New("authgate: server error")

func (c *Client) http() *http.Client { return noRedirect(c.HTTP) }

// do sends one request and returns the 2xx body. See ErrUnavailable and Error
// for how failures come back.
func (c *Client) do(ctx context.Context, method, path, key string, body any) (json.RawMessage, error) {
	return c.doTenant(ctx, method, path, key, "", body)
}

// doTenant is do plus the x-tenant-id header when tenant is not empty.
func (c *Client) doTenant(ctx context.Context, method, path, key, tenant string, body any) (json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("authgate: encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rolePath(c.BaseURL, path), rd)
	if err != nil {
		return nil, fmt.Errorf("authgate: build request: %w", err)
	}
	req.Header.Set("x-internal-access-key", key)
	if tenant != "" {
		req.Header.Set("x-tenant-id", tenant)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		// Only the kind of failure is kept; the URL error text is dropped.
		return nil, fmt.Errorf("%w: request failed: %w", ErrUnavailable, ErrNoAnswer)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("%w: read failed: %w", ErrUnavailable, ErrNoAnswer)
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return json.RawMessage(raw), nil
	case resp.StatusCode >= 500:
		return nil, fmt.Errorf("%w: status %d: %w", ErrUnavailable, resp.StatusCode, ErrServerError)
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	e := &Error{Status: resp.StatusCode}
	var fields map[string]any
	if json.Unmarshal(raw, &fields) == nil {
		e.Code, _ = fields["error"].(string)
		delete(fields, "error")
		if len(fields) > 0 {
			e.Extra = fields
		}
	}
	// Redirects and answers without an AuthGate error code come from something
	// in front of it (a CDN or WAF), not from AuthGate.
	if e.Code == "" {
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n > 0 {
		e.RetryAfter = n
	}
	return nil, e
}

func (c *Client) verify(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	return c.do(ctx, method, path, c.ClientKey, body)
}

func (c *Client) admin(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	return c.do(ctx, method, path, c.AdminKey, body)
}

func seg(s string) string { return url.PathEscape(s) }

func decode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: unreadable answer", ErrUnavailable)
	}
	return nil
}

// SetPIN enrolls or replaces the subject's PIN.
func (c *Client) SetPIN(ctx context.Context, subject, pin string) error {
	_, err := c.verify(ctx, http.MethodPost, "/api/factors/pin", map[string]string{"subjectId": subject, "pin": pin})
	return err
}

// DeletePIN removes the subject's PIN.
func (c *Client) DeletePIN(ctx context.Context, subject string) error {
	_, err := c.verify(ctx, http.MethodDelete, "/api/factors/pin/"+seg(subject), nil)
	return err
}

// Factors lists what the subject has enrolled.
func (c *Client) Factors(ctx context.Context, subject string) (Factors, error) {
	raw, err := c.verify(ctx, http.MethodGet, "/api/factors/"+seg(subject), nil)
	if err != nil {
		return Factors{}, err
	}
	var f Factors
	if err := decode(raw, &f); err != nil {
		return Factors{}, err
	}
	return f, nil
}

// StartRegistration begins a passkey registration and returns the creation options.
func (c *Client) StartRegistration(ctx context.Context, subject, displayName string) (string, json.RawMessage, error) {
	raw, err := c.verify(ctx, http.MethodPost, "/api/webauthn/registrations", map[string]string{"subjectId": subject, "displayName": displayName})
	if err != nil {
		return "", nil, err
	}
	var out struct {
		RegistrationID string          `json:"registrationId"`
		PublicKey      json.RawMessage `json:"publicKey"`
	}
	if err := decode(raw, &out); err != nil {
		return "", nil, err
	}
	return out.RegistrationID, out.PublicKey, nil
}

// FinishRegistration completes a registration with the browser's credential.
func (c *Client) FinishRegistration(ctx context.Context, id string, credential json.RawMessage) (string, error) {
	raw, err := c.verify(ctx, http.MethodPost, "/api/webauthn/registrations/"+seg(id), map[string]json.RawMessage{"credential": credential})
	if err != nil {
		return "", err
	}
	var out struct {
		CredentialID string `json:"credentialId"`
	}
	if err := decode(raw, &out); err != nil {
		return "", err
	}
	return out.CredentialID, nil
}

// DeleteCredential removes one of the subject's passkeys.
func (c *Client) DeleteCredential(ctx context.Context, subject, credentialID string) error {
	_, err := c.verify(ctx, http.MethodDelete, "/api/webauthn/credentials/"+seg(credentialID)+"?subjectId="+url.QueryEscape(subject), nil)
	return err
}

// CreateChallenge opens a challenge for a subject. factor is "pin" or "webauthn".
func (c *Client) CreateChallenge(ctx context.Context, subject, purpose, factor string) (Challenge, error) {
	raw, err := c.verify(ctx, http.MethodPost, "/api/challenges", map[string]string{"subjectId": subject, "purpose": purpose, "factor": factor})
	if err != nil {
		return Challenge{}, err
	}
	var ch Challenge
	if err := decode(raw, &ch); err != nil {
		return Challenge{}, err
	}
	return ch, nil
}

// VerifyChallenge answers a challenge with a pin or a credential (exactly one)
// and returns the assertion token.
func (c *Client) VerifyChallenge(ctx context.Context, id string, pin *string, credential json.RawMessage) (string, error) {
	body := map[string]any{}
	if pin != nil {
		body["pin"] = *pin
	}
	if len(credential) > 0 {
		body["credential"] = credential
	}
	raw, err := c.verify(ctx, http.MethodPost, "/api/challenges/"+seg(id)+"/verify", body)
	if err != nil {
		return "", err
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := decode(raw, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// ListClients returns one page of client summaries; cursor is empty for the first.
func (c *Client) ListClients(ctx context.Context, cursor string) (json.RawMessage, error) {
	path := "/api/clients"
	if cursor != "" {
		path += "?cursor=" + url.QueryEscape(cursor)
	}
	return c.admin(ctx, http.MethodGet, path, nil)
}

// ListClientsOwned returns one page of a tenant's client summaries.
func (c *Client) ListClientsOwned(ctx context.Context, tenantID, cursor string) (json.RawMessage, error) {
	q := url.Values{"owner": {tenantID}}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	return c.admin(ctx, http.MethodGet, "/api/clients?"+q.Encode(), nil)
}

// GetOwner returns a tenant's counters; a tenant that never created a client has the defaults.
func (c *Client) GetOwner(ctx context.Context, tenantID string) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodGet, "/api/owners/"+seg(tenantID), nil)
}

// SetClientQuota sets how many clients a tenant may own.
func (c *Client) SetClientQuota(ctx context.Context, tenantID string, quota int) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodPatch, "/api/owners/"+seg(tenantID), map[string]int{"clientQuota": quota})
}

// SuspendOwner refuses the tenant's writes and, soon after, its keys.
func (c *Client) SuspendOwner(ctx context.Context, tenantID string) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodPost, "/api/owners/"+seg(tenantID)+"/suspend", struct{}{})
}

// UnsuspendOwner lifts a suspension.
func (c *Client) UnsuspendOwner(ctx context.Context, tenantID string) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodPost, "/api/owners/"+seg(tenantID)+"/unsuspend", struct{}{})
}

// GetClient returns one client's view.
func (c *Client) GetClient(ctx context.Context, id string) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodGet, "/api/clients/"+seg(id), nil)
}

// CreateClient registers a client. The answer carries the new key once.
func (c *Client) CreateClient(ctx context.Context, body json.RawMessage) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodPost, "/api/clients", body)
}

// UpdateClient patches a client.
func (c *Client) UpdateClient(ctx context.Context, id string, body json.RawMessage) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodPatch, "/api/clients/"+seg(id), body)
}

// RotateKey issues a new key. A concurrent rotation answers 409 rotate_conflict.
func (c *Client) RotateKey(ctx context.Context, id string) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodPost, "/api/clients/"+seg(id)+"/keys", struct{}{})
}

// RevokePreviousKeys ends the grace period of every previous key at once.
func (c *Client) RevokePreviousKeys(ctx context.Context, id string) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodDelete, "/api/clients/"+seg(id)+"/keys/previous", nil)
}

// DisableClient disables a client.
func (c *Client) DisableClient(ctx context.Context, id string) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodPost, "/api/clients/"+seg(id)+"/disable", struct{}{})
}

// EnableClient enables a client.
func (c *Client) EnableClient(ctx context.Context, id string) (json.RawMessage, error) {
	return c.admin(ctx, http.MethodPost, "/api/clients/"+seg(id)+"/enable", struct{}{})
}

// DeleteClient deletes a client: its keys stop at once and its id answers 404 from then on.
func (c *Client) DeleteClient(ctx context.Context, id string) error {
	_, err := c.admin(ctx, http.MethodDelete, "/api/clients/"+seg(id), nil)
	return err
}

// PurgeSubject deletes every factor a subject has under a client.
func (c *Client) PurgeSubject(ctx context.Context, clientID, subject string) error {
	_, err := c.admin(ctx, http.MethodDelete, "/api/clients/"+seg(clientID)+"/subjects/"+seg(subject), nil)
	return err
}
