// Package portalapi is the Console's client for the Portal's operator API
// (/api/operator/*). It reaches the Portal only over HTTP with the operator key,
// so the Console never imports internal/portal.
package portalapi

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

// ErrUnavailable means the Portal cannot be relied on right now: the network
// failed, it answered 5xx, or it answered 401 to our own key (our
// misconfiguration, never the caller's) or without an error code (a CDN or WAF
// in front of it).
var ErrUnavailable = errors.New("portalapi: unavailable")

// ErrNoAnswer marks an ErrUnavailable where no status came back: a write that
// failed so may still have landed.
var ErrNoAnswer = errors.New("portalapi: no answer")

// ErrServerError marks an ErrUnavailable that was an answered 5xx: a write
// answered so may still have landed.
var ErrServerError = errors.New("portalapi: server error")

// Error is a 4xx answer from the Portal (including 429), passed through as is.
type Error struct {
	Status     int
	Code       string
	Extra      map[string]any // every body field except "error"
	RetryAfter int            // whole seconds from Retry-After; 0 when absent
}

func (e *Error) Error() string { return fmt.Sprintf("portalapi: %d %s", e.Status, e.Code) }

// Timeout is the default HTTP timeout when the caller gives no client.
const Timeout = 10 * time.Second

const maxBody = 1 << 20

// Client calls the Portal's operator API. The key is never logged or put in an
// error.
type Client struct {
	BaseURL, OperatorKey string
	HTTP                 *http.Client
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

// do sends one request and returns the 2xx body. A POST without a body sends {}
// because the Portal refuses other content types with 415.
func (c *Client) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var rd io.Reader
	if method == http.MethodPost {
		if body == nil {
			body = struct{}{}
		}
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("portalapi: encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, rd)
	if err != nil {
		return nil, fmt.Errorf("portalapi: build request: %w", err)
	}
	req.Header.Set("x-operator-key", c.OperatorKey)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := noRedirect(c.HTTP).Do(req)
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
	// Redirects and answers without an error code come from something in front
	// of the Portal, not from it.
	if e.Code == "" {
		return nil, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n > 0 {
		e.RetryAfter = n
	}
	return nil, e
}

func seg(s string) string { return url.PathEscape(s) }

func paged(path, cursor string) string {
	if cursor == "" {
		return path
	}
	return path + "?cursor=" + url.QueryEscape(cursor)
}

// ListTenants returns one page of tenants, newest first; cursor is empty for the first.
func (c *Client) ListTenants(ctx context.Context, cursor string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, paged("/api/operator/tenants", cursor), nil)
}

// GetTenant returns one tenant with its members and pending invite count.
func (c *Client) GetTenant(ctx context.Context, tid string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/api/operator/tenants/"+seg(tid), nil)
}

// TenantAudit returns one page of a tenant's own audit.
func (c *Client) TenantAudit(ctx context.Context, tid, cursor string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, paged("/api/operator/tenants/"+seg(tid)+"/audit", cursor), nil)
}

// SuspendTenant makes a workspace read-only. Repeating it is a quiet 200.
func (c *Client) SuspendTenant(ctx context.Context, tid string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, "/api/operator/tenants/"+seg(tid)+"/suspend", nil)
}

// UnsuspendTenant lifts a workspace suspension.
func (c *Client) UnsuspendTenant(ctx context.Context, tid string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, "/api/operator/tenants/"+seg(tid)+"/unsuspend", nil)
}

// TenantQuotaAudit records in the tenant's audit that its client quota went from
// one number to another. The Portal writes the audit only.
func (c *Client) TenantQuotaAudit(ctx context.Context, tid string, from, to int) error {
	_, err := c.do(ctx, http.MethodPost, "/api/operator/tenants/"+seg(tid)+"/audit",
		map[string]any{"action": "tenant-quota", "from": from, "to": to})
	return err
}

// FindUser looks a user up by exact email.
func (c *Client) FindUser(ctx context.Context, email string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodGet, "/api/operator/users?email="+url.QueryEscape(email), nil)
}

// SuspendUser suspends a user and signs them out everywhere.
func (c *Client) SuspendUser(ctx context.Context, uid string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, "/api/operator/users/"+seg(uid)+"/suspend", nil)
}

// UnsuspendUser lifts a user's suspension.
func (c *Client) UnsuspendUser(ctx context.Context, uid string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, "/api/operator/users/"+seg(uid)+"/unsuspend", nil)
}

// ResendVerification mails a fresh verify link to an unverified user.
func (c *Client) ResendVerification(ctx context.Context, uid string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, "/api/operator/users/"+seg(uid)+"/resend-verification", nil)
}

// ForceFactorReset starts the lost-factor wait for a user and answers {dueAt}.
func (c *Client) ForceFactorReset(ctx context.Context, uid string) (json.RawMessage, error) {
	return c.do(ctx, http.MethodPost, "/api/operator/users/"+seg(uid)+"/factor-reset", nil)
}
