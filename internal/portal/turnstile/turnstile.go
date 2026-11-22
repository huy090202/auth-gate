// Package turnstile verifies Cloudflare Turnstile tokens.
package turnstile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const siteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Verifier checks a token the browser got from the Turnstile widget.
type Verifier interface {
	Verify(ctx context.Context, token, ip string) (bool, error)
}

// Client calls Cloudflare's siteverify endpoint.
type Client struct {
	Secret string
	URL    string
	HTTP   *http.Client
}

func NewClient(secret string) *Client {
	return &Client{Secret: secret, URL: siteverifyURL, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

// Verify reports whether Cloudflare accepted the token. An empty token is false
// without a call; a transport, status or decoding failure is an error.
func (c *Client) Verify(ctx context.Context, token, ip string) (bool, error) {
	if token == "" {
		return false, nil
	}
	form := url.Values{"secret": {c.Secret}, "response": {token}, "remoteip": {ip}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, fmt.Errorf("turnstile: request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("turnstile: siteverify unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("turnstile: siteverify status %d", resp.StatusCode)
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out); err != nil {
		return false, fmt.Errorf("turnstile: bad siteverify response")
	}
	return out.Success, nil
}
