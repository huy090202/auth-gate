package console_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/authgate"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
)

// Why: the last factor is the only way back in; deleting it would lock the admin out.
func TestLastFactorRefused(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	pk := h.NewPasskey()
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.EnrollPIN(pin1), 200, "")
	r := b.EnrollPasskey(pk)
	want(t, r, 200, "")

	// With two factors, one can go.
	want(t, b.Do("DELETE", "/api/me/passkeys/"+r.Str("credentialId"), nil), 200, "")
	// The PIN is now the only one.
	want(t, b.Do("DELETE", "/api/me/pin", nil), 409, "last_factor")
	f, err := h.AuthGate.Factors(context.Background(), a.ID)
	if err != nil || !f.PIN || len(f.WebAuthn) != 0 {
		t.Fatalf("factors = %+v (%v)", f, err)
	}
	// A passkey that is the only factor is protected the same way.
	pk2 := h.NewPasskey()
	r = b.EnrollPasskey(pk2)
	want(t, r, 200, "")
	want(t, b.Do("DELETE", "/api/me/pin", nil), 200, "")
	want(t, b.Do("DELETE", "/api/me/passkeys/"+r.Str("credentialId"), nil), 409, "last_factor")
}

// Why: in a full session every change to one's own credentials asks for a step-up token.
// (Tests holding a token come with the routes that mint one.)
func TestFullSelfServiceWithoutTokenIsRefused(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.SignInWithPIN(pin1), 200, "")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2)), 403, "step_up_required")
	want(t, b.EnrollPIN(pin2), 403, "step_up_required")
	want(t, b.Do("DELETE", "/api/me/pin", nil), 403, "step_up_required")
	want(t, b.StartPasskey(), 403, "step_up_required")
	want(t, b.Do("DELETE", "/api/me/passkeys/abc", nil), 403, "step_up_required")
	// Nothing changed.
	want(t, h.NewBrowser().Login("ada", pw), 200, "")
}

func pinWorks(t *testing.T, h *consoletest.Harness, a console.Admin, pin string) bool {
	t.Helper()
	ch, err := h.AuthGate.CreateChallenge(context.Background(), a.ID, "probe", "pin")
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.AuthGate.VerifyChallenge(context.Background(), ch.ID, &pin, nil)
	return err == nil
}

// twoSessions opens two enrolling sessions for one account: the attacker's who
// holds the temporary password, and the owner's.
func twoSessions(t *testing.T) (h *consoletest.Harness, a console.Admin, stale, owner *consoletest.Browser) {
	h = consoletest.New(t)
	a = h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	stale, owner = h.NewBrowser(), h.NewBrowser()
	want(t, stale.Login("ada", pw), 200, "")
	want(t, owner.Login("ada", pw), 200, "")
	return
}

// Why: a session that never proved a factor must not ride on a factor another
// session added; it would reach "full" without a second factor and could replace the owner's PIN.
func TestStaleEnrollSessionCannotTakeOver(t *testing.T) {
	h, a, stale, owner := twoSessions(t)
	want(t, owner.EnrollPIN(pin1), 200, "")
	want(t, owner.Do("POST", "/api/me/password", passwordBody(pw, pw2)), 200, "")
	if stageOf(t, owner) != "full" {
		t.Fatal("owner is not full")
	}
	want(t, stale.EnrollPIN(pin2), 401, "unauthorized")
	if !pinWorks(t, h, a, pin1) || pinWorks(t, h, a, pin2) {
		t.Fatal("the owner's PIN was replaced")
	}
}

// Why: with a factor already enrolled, the temporary password alone must not be able to set a new password.
func TestStaleEnrollSessionCannotChangePassword(t *testing.T) {
	h, _, stale, owner := twoSessions(t)
	want(t, owner.EnrollPIN(pin1), 200, "")
	want(t, stale.Do("POST", "/api/me/password", passwordBody(pw, pw2)), 401, "unauthorized")
	want(t, stale.Do("GET", "/api/me", nil), 401, "unauthorized")
	want(t, h.NewBrowser().Login("ada", pw2), 401, "invalid_credentials")
	want(t, h.NewBrowser().Login("ada", pw), 200, "")
}

// Why: a stale enrolling session must not remove the owner's factors without a token.
func TestStaleEnrollSessionCannotDeleteFactors(t *testing.T) {
	h, a, stale, owner := twoSessions(t)
	want(t, owner.EnrollPIN(pin1), 200, "")
	want(t, owner.EnrollPasskey(h.NewPasskey()), 200, "")
	want(t, stale.Do("DELETE", "/api/me/pin", nil), 401, "unauthorized")
	f, err := h.AuthGate.Factors(context.Background(), a.ID)
	if err != nil || !f.PIN || len(f.WebAuthn) != 1 {
		t.Fatalf("factors = %+v (%v)", f, err)
	}
}

// Why: once the owner has set a new password, every older session of the account is dead,
// or the temporary-password holder could still add a factor to an account with none.
func TestPasswordChangeEndsOtherSessions(t *testing.T) {
	h, a, stale, owner := twoSessions(t)
	want(t, owner.Do("POST", "/api/me/password", passwordBody(pw, pw2)), 200, "")
	if stageOf(t, owner) != "enroll" {
		t.Fatal("the owner's own session did not survive its password change")
	}
	want(t, stale.Do("GET", "/api/me", nil), 401, "unauthorized")
	want(t, stale.EnrollPIN(pin2), 401, "unauthorized")
	f, err := h.AuthGate.Factors(context.Background(), a.ID)
	if err != nil || f.PIN {
		t.Fatalf("factors = %+v (%v)", f, err)
	}
}

// Why: the same holds for a full session: changing the password ends the admin's other sessions.
func TestFullPasswordChangeEndsOtherSessions(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	one, two := h.NewBrowser(), h.NewBrowser()
	for _, b := range []*consoletest.Browser{one, two} {
		want(t, b.Login("ada", pw), 200, "")
		want(t, b.SignInWithPIN(pin1), 200, "")
	}
	tok := h.StepUpToken(a.ID, "console-change-password", pin1)
	before := one.CookieValue()
	want(t, one.Do("POST", "/api/me/password", passwordBody(pw, pw2), "x-step-up-token", tok), 200, "")
	if one.CookieValue() == before || stageOf(t, one) != "full" {
		t.Fatal("the changing session was not reissued as full")
	}
	want(t, two.Do("GET", "/api/me", nil), 401, "unauthorized")
}

// Why: a typo in the current password must not cost the admin their one-use step-up token.
func TestTypoInCurrentPasswordKeepsStepUpToken(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.SignInWithPIN(pin1), 200, "")
	tok := h.StepUpToken(a.ID, "console-change-password", pin1)
	hdr := []string{"x-step-up-token", tok}
	want(t, b.Do("POST", "/api/me/password", passwordBody("typo-typo-typo", pw2), hdr...), 400, "wrong_password")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, "short"), hdr...), 400, "invalid_password")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw), hdr...), 400, "invalid_password")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2), hdr...), 200, "")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw2, pw), hdr...), 403, "step_up_required")
}

// Why: in enroll the route has no step-up, so it must not be a free oracle for the current password.
func TestWrongPasswordsInEnrollLockAndEndSession(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	for range 4 {
		want(t, b.Do("POST", "/api/me/password", passwordBody("wrong-wrong-wrong", pw2)), 400, "wrong_password")
	}
	want(t, b.Do("POST", "/api/me/password", passwordBody("wrong-wrong-wrong", pw2)), 401, "unauthorized")
	want(t, b.Do("GET", "/api/me", nil), 401, "unauthorized")
	want(t, h.NewBrowser().Login("ada", pw), 401, "invalid_credentials")
}

// Why: a password-only session sees whether it must pass a factor, not which passkeys exist.
func TestHalfSessionSeesFactorFlagsOnly(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	f, _ := b.Do("GET", "/api/me", nil).Map()["factors"].(map[string]any)
	if f["pin"] != true || f["webauthn"] != false {
		t.Fatalf("factors = %v", f)
	}
}

// Why: the password route must say invalid_password for a weak or unchanged new password.
func TestInvalidPasswordAtTheRoute(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, "short")), 400, "invalid_password")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw)), 400, "invalid_password")
}

// Why: when AuthGate is down nobody may get a session.
func TestAuthGateDownOpensNoSession(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	dead := httptest.NewTLSServer(http.NotFoundHandler())
	dead.Close()
	deps := h.Deps
	deps.AuthGate = &authgate.Client{BaseURL: dead.URL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey}
	b := h.BrowserFor(console.New(deps))
	r := b.Login("ada", pw)
	want(t, r, 502, "authgate_unavailable")
	if len(r.Header.Values("Set-Cookie")) != 0 || b.CookieValue() != "" {
		t.Fatal("a session was opened")
	}
}

type stubVerify struct {
	base http.RoundTripper
	body string
}

func (s stubVerify) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasSuffix(r.URL.Path, "/verify") {
		return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(strings.NewReader(s.body)), Request: r}, nil
	}
	return s.base.RoundTrip(r)
}

// Why: whatever else AuthGate puts in an error body must not reach the browser.
func TestUnlistedAuthGateFieldsAreDropped(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	deps := h.Deps
	deps.AuthGate = &authgate.Client{BaseURL: h.AuthGateURL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey,
		HTTP: &http.Client{Transport: stubVerify{base: h.AuthGate.HTTP.Transport,
			body: `{"error":"verification_failed","attemptsRemaining":3,"lockedUntil":"2030-01-01T00:00:00Z","retryAfter":5,"internalNote":"x"}`}}}
	b := h.BrowserFor(console.New(deps))
	want(t, b.Login("ada", pw), 200, "")
	r := b.SignInWithPIN(pin1)
	want(t, r, 400, "verification_failed")
	m := r.Map()
	if m["attemptsRemaining"] != float64(3) || m["lockedUntil"] == nil || m["retryAfter"] != float64(5) {
		t.Fatalf("allowed fields lost: %s", r.Body)
	}
	if _, ok := m["internalNote"]; ok || len(m) != 4 {
		t.Fatalf("unlisted field passed through: %s", r.Body)
	}
}
