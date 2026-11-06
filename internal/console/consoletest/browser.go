package consoletest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/descope/virtualwebauthn"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

// Pace is how far the clock moves before each /api/auth request, so the
// Console's login bucket (one token per 6 s) refills between calls.
const Pace = 7 * time.Second

const cookieName = "__Host-console_session"

// Response is what the Console answered.
type Response struct {
	Status int
	Body   []byte
	Header http.Header
}

// Map decodes the body as a JSON object; a body that is not one gives an empty map.
func (r Response) Map() map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal(r.Body, &m)
	return m
}

// Str returns one string field of the body, or "".
func (r Response) Str(key string) string {
	s, _ := r.Map()[key].(string)
	return s
}

// Browser is one signed-in-or-not caller with its own cookie jar.
type Browser struct {
	H    *Harness
	Base string
	HTTP *http.Client
}

// NewBrowser returns a caller of the harness's Console.
func (h *Harness) NewBrowser() *Browser { return h.browser(h.ConsoleURL, h.ConsoleHTTP) }

// BrowserFor serves another handler (for example one built with different
// Deps) over TLS and returns a caller of it.
func (h *Harness) BrowserFor(handler http.Handler) *Browser {
	srv := httptest.NewTLSServer(handler)
	h.T.Cleanup(srv.Close)
	return h.browser(srv.URL, srv.Client())
}

func (h *Harness) browser(base string, base0 *http.Client) *Browser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		h.T.Fatal(err)
	}
	c := *base0
	c.Jar = jar
	return &Browser{H: h, Base: base, HTTP: &c}
}

// CookieValue returns the session cookie the browser holds, or "".
func (b *Browser) CookieValue() string {
	u, _ := url.Parse(b.Base)
	for _, c := range b.HTTP.Jar.Cookies(u) {
		if c.Name == cookieName {
			return c.Value
		}
	}
	return ""
}

// SetCookie makes the browser present the given session cookie.
func (b *Browser) SetCookie(value string) {
	u, _ := url.Parse(b.Base)
	b.HTTP.Jar.SetCookies(u, []*http.Cookie{{Name: cookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}})
}

// Do sends one request. A body that is not nil is sent as JSON. Calls to
// /api/auth/ and /api/me/password (both behind the login limiter) move the clock by Pace first. Header pairs follow as name, value.
func (b *Browser) Do(method, path string, body any, headers ...string) Response {
	b.H.T.Helper()
	if strings.HasPrefix(path, "/api/auth/") || path == "/api/me/password" {
		b.H.Clock.Advance(Pace)
	}
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			b.H.T.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, b.Base+path, rd)
	if err != nil {
		b.H.T.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := b.HTTP.Do(req)
	if err != nil {
		b.H.T.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		b.H.T.Fatal(err)
	}
	return Response{Status: resp.StatusCode, Body: raw, Header: resp.Header}
}

// CreateAdmin stores an admin with the given password.
func (h *Harness) CreateAdmin(username, password string, role console.Role, mustChange bool) console.Admin {
	h.T.Helper()
	hash, err := console.HashPassword(password)
	if err != nil {
		h.T.Fatal(err)
	}
	a := console.Admin{ID: id.New(), Username: username, Role: role, PasswordHash: hash,
		MustChangePassword: mustChange, CreatedAt: h.Clock.Now().UTC()}
	if err := h.Store().CreateAdmin(context.Background(), a); err != nil {
		h.T.Fatalf("create admin %s: %v", username, err)
	}
	return a
}

// Store is the Console's table.
func (h *Harness) Store() *console.Store {
	return &console.Store{DB: h.Deps.DB, Table: h.Deps.Config.Table}
}

// Login posts the credentials.
func (b *Browser) Login(username, password string) Response {
	b.H.T.Helper()
	return b.Do("POST", "/api/auth/login", map[string]string{"username": username, "password": password})
}

// EnrollPIN sets the signed-in admin's PIN (no step-up token).
func (b *Browser) EnrollPIN(pin string) Response {
	b.H.T.Helper()
	return b.Do("POST", "/api/me/pin", map[string]string{"pin": pin})
}

// StartSecondFactor opens a login challenge for "pin" or "webauthn".
func (b *Browser) StartSecondFactor(factor string) Response {
	b.H.T.Helper()
	return b.Do("POST", "/api/auth/second-factor/start", map[string]string{"factor": factor})
}

// SignInWithPIN runs start and verify with a PIN and returns the verify answer.
func (b *Browser) SignInWithPIN(pin string) Response {
	b.H.T.Helper()
	st := b.StartSecondFactor("pin")
	if st.Status != http.StatusOK {
		b.H.T.Fatalf("start second factor: %d %s", st.Status, st.Body)
	}
	return b.Do("POST", "/api/auth/second-factor/verify", map[string]string{"challengeId": st.Str("challengeId"), "pin": pin})
}

// NewPasskey returns a fresh virtual authenticator with one credential.
func (h *Harness) NewPasskey() *Passkey {
	return &Passkey{
		RP:         h.Passkey.RP,
		Authn:      virtualwebauthn.NewAuthenticator(),
		Credential: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

// StartPasskey begins a registration and returns the Console's answer.
func (b *Browser) StartPasskey() Response {
	b.H.T.Helper()
	return b.Do("POST", "/api/me/passkeys/start", struct{}{})
}

// Attest answers a registration start with this passkey's credential.
func (p *Passkey) Attest(h *Harness, start Response) json.RawMessage {
	h.T.Helper()
	pk, _ := json.Marshal(start.Map()["publicKey"])
	ao, err := virtualwebauthn.ParseAttestationOptions(string(pk))
	if err != nil {
		h.T.Fatalf("parse creation options: %v", err)
	}
	return json.RawMessage(virtualwebauthn.CreateAttestationResponse(p.RP, p.Authn, p.Credential, *ao))
}

// FinishPasskey completes a registration.
func (b *Browser) FinishPasskey(registrationID string, credential json.RawMessage) Response {
	b.H.T.Helper()
	return b.Do("POST", "/api/me/passkeys/finish", map[string]any{"registrationId": registrationID, "credential": credential})
}

// EnrollPasskey runs start and finish and returns the finish answer. On
// success the authenticator holds the credential, so it can sign in.
func (b *Browser) EnrollPasskey(p *Passkey) Response {
	b.H.T.Helper()
	st := b.StartPasskey()
	if st.Status != http.StatusOK {
		b.H.T.Fatalf("start passkey: %d %s", st.Status, st.Body)
	}
	resp := b.FinishPasskey(st.Str("registrationId"), p.Attest(b.H, st))
	if resp.Status == http.StatusOK {
		p.Authn.AddCredential(p.Credential)
	}
	return resp
}

// SignInWithPasskey runs the second factor with a passkey and returns the verify answer.
func (b *Browser) SignInWithPasskey(p *Passkey) Response {
	b.H.T.Helper()
	st := b.StartSecondFactor("webauthn")
	if st.Status != http.StatusOK {
		b.H.T.Fatalf("start second factor: %d %s", st.Status, st.Body)
	}
	pk, _ := json.Marshal(st.Map()["publicKey"])
	ao, err := virtualwebauthn.ParseAssertionOptions(string(pk))
	if err != nil {
		b.H.T.Fatalf("parse request options: %v", err)
	}
	// virtualwebauthn does not bump the sign counter; a real authenticator does.
	p.Credential.Counter++
	assertion := virtualwebauthn.CreateAssertionResponse(p.RP, p.Authn, p.Credential, *ao)
	return b.Do("POST", "/api/auth/second-factor/verify",
		map[string]any{"challengeId": st.Str("challengeId"), "credential": json.RawMessage(assertion)})
}

// StepUpToken mints, straight at AuthGate, the token a step-up would give: a PIN
// challenge for the admin with the given purpose, answered with pin.
func (h *Harness) StepUpToken(adminID, purpose, pin string) string {
	h.T.Helper()
	ctx := context.Background()
	ch, err := h.AuthGate.CreateChallenge(ctx, adminID, purpose, "pin")
	if err != nil {
		h.T.Fatal(err)
	}
	tok, err := h.AuthGate.VerifyChallenge(ctx, ch.ID, &pin, nil)
	if err != nil {
		h.T.Fatal(err)
	}
	return tok
}
