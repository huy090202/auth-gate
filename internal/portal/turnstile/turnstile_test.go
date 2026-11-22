package turnstile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTurnstileClientPostsSecretAndToken(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost {
			t.Errorf("method %s", r.Method)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("secret or token leaked to the query: %q", r.URL.RawQuery)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.PostForm.Get("secret") != "s3cret" || r.PostForm.Get("response") != "tok" || r.PostForm.Get("remoteip") != "203.0.113.9" {
			t.Errorf("form %v", r.PostForm)
		}
		if _, ok := r.Header["Authorization"]; ok {
			t.Error("unexpected Authorization header")
		}
		_, _ = w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := NewClient("s3cret")
	c.URL = srv.URL
	ok, err := c.Verify(context.Background(), "tok", "203.0.113.9")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if ok, err := c.Verify(context.Background(), "", "203.0.113.9"); ok || err != nil || calls != 1 {
		t.Errorf("empty token: ok=%v err=%v calls=%d", ok, err, calls)
	}
}

func TestTurnstileFailureModes(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"rejected":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"success":false}`)) },
		"http error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"bad json":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
	} {
		srv := httptest.NewServer(h)
		c := NewClient("s")
		c.URL = srv.URL
		ok, err := c.Verify(context.Background(), "tok", "")
		srv.Close()
		if ok {
			t.Errorf("%s: must not verify", name)
		}
		if (name == "rejected") != (err == nil) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if got := NewClient("s").HTTP.Timeout; got != 5*time.Second {
		t.Errorf("timeout %v", got)
	}
}
