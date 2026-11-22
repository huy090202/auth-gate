// Package hibp checks a password against Have I Been Pwned's range API. Only the
// first 5 hex characters of the SHA-1 leave the process.
package hibp

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // the range API is defined over SHA-1
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const rangeURL = "https://api.pwnedpasswords.com/range"

// Checker reports whether a password appears in known breaches.
type Checker interface {
	Breached(ctx context.Context, password string) (bool, error)
}

// Client calls the range API. URL is the base, without the prefix.
type Client struct {
	URL  string
	HTTP *http.Client
}

func NewClient() *Client {
	return &Client{URL: rangeURL, HTTP: &http.Client{Timeout: 2 * time.Second}}
}

func (c *Client) Breached(ctx context.Context, password string) (bool, error) {
	sum := sha1.Sum([]byte(password)) //nolint:gosec
	full := strings.ToUpper(hex.EncodeToString(sum[:]))
	prefix, suffix := full[:5], full[5:]
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/"+prefix, nil)
	if err != nil {
		return false, fmt.Errorf("hibp: request")
	}
	req.Header.Set("Add-Padding", "true")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return false, fmt.Errorf("hibp: range unreachable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("hibp: range status %d", resp.StatusCode)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		s, count, ok := strings.Cut(strings.TrimSpace(sc.Text()), ":")
		if !ok || !strings.EqualFold(s, suffix) {
			continue
		}
		return strings.TrimSpace(count) != "0", nil
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("hibp: reading range")
	}
	return false, nil
}
