package hibp

import (
	"context"
	"crypto/sha1" //nolint:gosec // the range API is defined over SHA-1
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func sum(p string) string {
	h := sha1.Sum([]byte(p)) //nolint:gosec // as above
	return strings.ToUpper(hex.EncodeToString(h[:]))
}

func TestHIBPSendsOnlyThePrefix(t *testing.T) {
	const pw = "correct horse battery"
	full := sum(pw)
	var gotPath, gotPad, gotRaw string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotPad, gotRaw = r.URL.Path, r.Header.Get("Add-Padding"), r.URL.RequestURI()
		// lower-case suffix proves the comparison is case-insensitive; the padded line has count 0
		_, _ = w.Write([]byte("0000000000000000000000000000000000A:0\r\n" + strings.ToLower(full[5:]) + ":42\r\n"))
	}))
	defer srv.Close()
	c := NewClient()
	c.URL = srv.URL + "/range"
	bad, err := c.Breached(context.Background(), pw)
	if err != nil || !bad {
		t.Fatalf("bad=%v err=%v", bad, err)
	}
	if gotPath != "/range/"+full[:5] {
		t.Errorf("path %q", gotPath)
	}
	if strings.Contains(gotRaw, full[5:]) || strings.Contains(strings.ToLower(gotRaw), strings.ToLower(pw)) {
		t.Errorf("more than the prefix was sent: %q", gotRaw)
	}
	if gotPad != "true" {
		t.Errorf("Add-Padding %q", gotPad)
	}
}

func TestHIBPNoMatchPaddingAndErrors(t *testing.T) {
	full := sum("pw")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(full[5:] + ":0\r\nFFFFF:3\r\n"))
	}))
	c := NewClient()
	c.URL = srv.URL
	if bad, err := c.Breached(context.Background(), "pw"); bad || err != nil {
		t.Errorf("a padded zero-count line is not a breach: bad=%v err=%v", bad, err)
	}
	srv.Close()

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
	c.URL = srv.URL
	if _, err := c.Breached(context.Background(), "pw"); err == nil {
		t.Error("non-200 must be an error")
	}
	srv.Close()
}

func TestHIBPTimeoutIsAnError(t *testing.T) {
	if NewClient().HTTP.Timeout != 2*time.Second {
		t.Fatal("default timeout must be 2 s")
	}
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-done }))
	defer srv.Close()
	defer close(done)
	c := NewClient()
	c.URL = srv.URL
	c.HTTP.Timeout = 50 * time.Millisecond
	if _, err := c.Breached(context.Background(), "pw"); err == nil {
		t.Fatal("a timeout must be an error")
	}
}
