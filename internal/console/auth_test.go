package console_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/authgate"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

const (
	// AuthGate's policy refuses sequential and repeated PINs, so these are neither.
	pin1 = "582913"
	pin2 = "749306"
	pw   = "correct-horse-battery"
	pw2  = "another-long-passphrase"
)

func want(t *testing.T, r consoletest.Response, status int, code string) {
	t.Helper()
	if r.Status != status || (code != "" && r.Str("error") != code) {
		t.Fatalf("got %d %s, want %d %s", r.Status, r.Body, status, code)
	}
}

func passwordBody(current, next string) map[string]string {
	return map[string]string{"currentPassword": current, "newPassword": next}
}

func stageOf(t *testing.T, b *consoletest.Browser) string {
	t.Helper()
	r := b.Do("GET", "/api/me", nil)
	want(t, r, 200, "")
	return r.Str("stage")
}

// withPIN gives an admin a PIN at AuthGate.
func withPIN(t *testing.T, h *consoletest.Harness, a console.Admin, pin string) {
	t.Helper()
	if err := h.AuthGate.SetPIN(context.Background(), a.ID, pin); err != nil {
		t.Fatal(err)
	}
}

// Why: a sign-in that answers or costs differently for an unknown name, a wrong
// password, a locked or a disabled account tells an attacker which names exist.
func TestLoginDoesNotEnumerate(t *testing.T) {
	h := consoletest.New(t)
	ctx := context.Background()
	live := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	locked := h.CreateAdmin("lock", pw, console.RoleAdmin, false)
	disabled := h.CreateAdmin("off", pw, console.RoleAdmin, false)
	for range 5 {
		if err := h.Store().RecordLoginFailure(ctx, locked.Username, h.Clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	now := h.Clock.Now()
	if err := h.Store().SetDisabled(ctx, nil, disabled.Username, &now); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	calls := 0
	b := h.BrowserFor(console.NewWithPasswordCheck(h.Deps, func(p, stored string) bool {
		mu.Lock()
		calls++
		mu.Unlock()
		return console.VerifyPassword(p, stored)
	}))

	cases := []struct{ name, user, pass string }{
		{"unknown", "nobody", pw},
		{"wrong", "ada", "wrong-password-here"},
		{"locked", "lock", pw},
		{"disabled", "off", pw},
		{"malformed name", "x!", pw},
		{"overlong password", "ada", string(bytes.Repeat([]byte("a"), 300))},
	}
	var first consoletest.Response
	for i, c := range cases {
		before := calls
		r := b.Login(c.user, c.pass)
		want(t, r, 401, "invalid_credentials")
		if calls != before+1 {
			t.Errorf("%s: %d hash compares, want 1", c.name, calls-before)
		}
		if i == 0 {
			first = r
		} else if !bytes.Equal(r.Body, first.Body) {
			t.Errorf("%s: body %s differs from %s", c.name, r.Body, first.Body)
		}
		if len(r.Header.Values("Set-Cookie")) != 0 {
			t.Errorf("%s: set a cookie", c.name)
		}
	}
	// Only the wrong password on a live account counted (the overlong one is a wrong password too).
	for name, n := range map[string]int{live.Username: 2, locked.Username: 0, disabled.Username: 0} {
		a, err := h.Store().GetAdmin(ctx, name)
		if err != nil || a.FailedCount != n {
			t.Errorf("%s: failedCount = %d (%v), want %d", name, a.FailedCount, err, n)
		}
	}
}

// Why: five wrong passwords lock the account for 15 minutes, even against the right one.
func TestLoginLockoutAfterFive(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	b := h.NewBrowser()
	for range 5 {
		want(t, b.Login("ada", "wrong-password-here"), 401, "invalid_credentials")
	}
	want(t, b.Login("ada", pw), 401, "invalid_credentials")
	h.Clock.Advance(14 * time.Minute)
	want(t, b.Login("ada", pw), 401, "invalid_credentials")
	h.Clock.Advance(2 * time.Minute)
	r := b.Login("ada", pw)
	want(t, r, 200, "")
	if r.Str("next") != "enroll" {
		t.Fatalf("got %s", r.Body)
	}
}

// Why: a new admin has a temporary password and no factor; the only way on is to enroll.
func TestFirstSignInGoesToEnroll(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	b := h.NewBrowser()
	r := b.Login("ada", pw)
	want(t, r, 200, "")
	if r.Str("next") != "enroll" || r.Map()["factors"] != nil {
		t.Fatalf("login = %s", r.Body)
	}
	me := b.Do("GET", "/api/me", nil)
	m := me.Map()
	if m["stage"] != "enroll" || m["username"] != "ada" || m["role"] != "admin" || m["mustChangePassword"] != true {
		t.Fatalf("me = %s", me.Body)
	}
}

// Why: an enrolling admin may reach nothing but the way out of enrolling.
func TestEnrollAllowsOnlyItsRoutes(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")

	// Allowed: each is refused by its own handler's rules, not by the stage.
	want(t, b.Do("GET", "/api/me", nil), 200, "")
	want(t, b.Do("POST", "/api/me/password", passwordBody("nope-nope-nope", pw2)), 400, "wrong_password")
	want(t, b.Do("POST", "/api/me/pin", map[string]string{"pin": "123456"}), 400, "pin_rejected")
	want(t, b.Do("DELETE", "/api/me/pin", nil), 409, "factor_not_enrolled")
	want(t, b.StartPasskey(), 200, "")
	// Refused by the stage.
	want(t, b.Do("GET", "/api/clients", nil), 403, "enrollment_required")
	want(t, b.Do("GET", "/api/audit", nil), 403, "enrollment_required")
	want(t, b.Do("POST", "/api/auth/second-factor/start", map[string]string{"factor": "pin"}), 403, "enrollment_required")
	want(t, b.Do("POST", "/api/auth/logout", struct{}{}), 204, "")
	want(t, b.Do("GET", "/api/me", nil), 401, "unauthorized")
}

// Why: the admin must have changed the temporary password and hold a factor; either
// alone is not enough, and the order does not matter.
func TestEnrollNeedsBothPasswordAndFactor(t *testing.T) {
	changePassword := func(t *testing.T, b *consoletest.Browser) consoletest.Response {
		return b.Do("POST", "/api/me/password", passwordBody(pw, pw2))
	}
	t.Run("password then factor", func(t *testing.T) {
		h := consoletest.New(t)
		h.CreateAdmin("ada", pw, console.RoleAdmin, true)
		b := h.NewBrowser()
		want(t, b.Login("ada", pw), 200, "")
		r := changePassword(t, b)
		want(t, r, 200, "")
		if r.Str("stage") != "enroll" || stageOf(t, b) != "enroll" {
			t.Fatalf("after the password alone: %s", r.Body)
		}
		r = b.EnrollPIN(pin1)
		want(t, r, 200, "")
		if r.Str("stage") != "full" || stageOf(t, b) != "full" {
			t.Fatalf("after both: %s", r.Body)
		}
	})
	t.Run("factor then password", func(t *testing.T) {
		h := consoletest.New(t)
		h.CreateAdmin("ada", pw, console.RoleAdmin, true)
		b := h.NewBrowser()
		want(t, b.Login("ada", pw), 200, "")
		r := b.EnrollPIN(pin1)
		want(t, r, 200, "")
		if r.Str("stage") != "enroll" || stageOf(t, b) != "enroll" {
			t.Fatalf("after the factor alone: %s", r.Body)
		}
		r = changePassword(t, b)
		want(t, r, 200, "")
		if r.Str("stage") != "full" || stageOf(t, b) != "full" {
			t.Fatalf("after both: %s", r.Body)
		}
		// The new password is the one that works now.
		want(t, h.NewBrowser().Login("ada", pw), 401, "invalid_credentials")
		want(t, h.NewBrowser().Login("ada", pw2), 200, "")
	})
}

// Why: Spec conflict 6. A reset admin who still has factors must pass the second
// factor before enrolling, or the master who issued the temporary password could
// sign in as them with no factor.
func TestResetAdminWithFactorsGoesSecondFactorFirst(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	withPIN(t, h, a, pin1)
	b := h.NewBrowser()
	r := b.Login("ada", pw)
	want(t, r, 200, "")
	if r.Str("next") != "second-factor" {
		t.Fatalf("login = %s", r.Body)
	}
	f, _ := r.Map()["factors"].(map[string]any)
	if f["pin"] != true || f["webauthn"] != false {
		t.Fatalf("factors = %v", f)
	}
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2)), 401, "second_factor_required")
	r = b.SignInWithPIN(pin1)
	want(t, r, 200, "")
	if r.Str("stage") != "enroll" || stageOf(t, b) != "enroll" {
		t.Fatalf("after the second factor: %s", r.Body)
	}
	r = b.Do("POST", "/api/me/password", passwordBody(pw, pw2))
	want(t, r, 200, "")
	if r.Str("stage") != "full" {
		t.Fatalf("after the password: %s", r.Body)
	}
}

// Why: the second factor is what makes the session full, and the half-session cookie must not outlive it.
func TestSecondFactorThenFull(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	if stageOf(t, b) != "half" {
		t.Fatal("not half after the password")
	}
	half := b.CookieValue()
	r := b.SignInWithPIN(pin1)
	want(t, r, 200, "")
	if r.Str("stage") != "full" || stageOf(t, b) != "full" {
		t.Fatalf("verify = %s", r.Body)
	}
	if b.CookieValue() == half {
		t.Fatal("the cookie did not change at promotion")
	}
	old := h.NewBrowser()
	old.SetCookie(half)
	want(t, old.Do("GET", "/api/me", nil), 401, "unauthorized")
}

// replay answers the next challenge verify with the recorded answer of an earlier one.
type replay struct {
	base   http.RoundTripper
	mu     sync.Mutex
	last   []byte
	replay bool
}

func (rp *replay) RoundTrip(r *http.Request) (*http.Response, error) {
	rp.mu.Lock()
	replaying, saved := rp.replay, rp.last
	rp.mu.Unlock()
	isVerify := r.Method == http.MethodPost && len(r.URL.Path) > 7 && r.URL.Path[len(r.URL.Path)-7:] == "/verify"
	if isVerify && replaying {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}},
			Body: io.NopCloser(bytes.NewReader(saved)), Request: r}, nil
	}
	resp, err := rp.base.RoundTrip(r)
	if err == nil && isVerify && resp.StatusCode == 200 {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		rp.mu.Lock()
		rp.last = raw
		rp.mu.Unlock()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
	}
	return resp, err
}

// Why: a login token is single use; seeing the same one twice must not open a second session.
func TestReplayedLoginTokenRefused(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	rp := &replay{base: h.AuthGate.HTTP.Transport}
	deps := h.Deps
	deps.AuthGate = &authgate.Client{BaseURL: h.AuthGateURL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey,
		HTTP: &http.Client{Transport: rp}}
	handler := console.New(deps)

	first := h.BrowserFor(handler)
	want(t, first.Login("ada", pw), 200, "")
	want(t, first.SignInWithPIN(pin1), 200, "")

	rp.mu.Lock()
	rp.replay = true
	rp.mu.Unlock()
	second := h.BrowserFor(handler)
	want(t, second.Login("ada", pw), 200, "")
	want(t, second.SignInWithPIN(pin1), 401, "invalid_token")
	if stageOf(t, second) != "half" {
		t.Fatal("the replayed token promoted the session")
	}
}

// Why: Review Focus 5. A half session that submits someone else's challenge must
// neither reach AuthGate nor spend that admin's PIN attempts.
func TestForeignChallengeDoesNotSpendAttempts(t *testing.T) {
	h := consoletest.New(t)
	for _, n := range []string{"ada", "bob"} {
		withPIN(t, h, h.CreateAdmin(n, pw, console.RoleAdmin, false), pin1)
	}
	ada, bob := h.NewBrowser(), h.NewBrowser()
	want(t, ada.Login("ada", pw), 200, "")
	want(t, bob.Login("bob", pw), 200, "")
	chA := ada.StartSecondFactor("pin")
	want(t, chA, 200, "")
	chB := bob.StartSecondFactor("pin")
	want(t, chB, 200, "")

	verify := func(b *consoletest.Browser, challengeID, pin string) consoletest.Response {
		return b.Do("POST", "/api/auth/second-factor/verify", map[string]string{"challengeId": challengeID, "pin": pin})
	}
	for range 5 {
		want(t, verify(bob, chA.Str("challengeId"), pin2), 410, "challenge_gone")
	}
	want(t, verify(bob, "", pin2), 410, "challenge_gone")
	// Ada's first wrong PIN still finds the whole allowance but one (the default policy allows 5).
	r := verify(ada, chA.Str("challengeId"), pin2)
	want(t, r, 400, "verification_failed")
	if r.Map()["attemptsRemaining"] != float64(4) {
		t.Fatalf("attemptsRemaining = %v, want 4: Bob's tries were spent", r.Map()["attemptsRemaining"])
	}
	// Bob's own challenge is untouched too.
	want(t, verify(bob, chB.Str("challengeId"), pin1), 200, "")
}

// Why: Review Focus 5. A passkey registration started in another session must not finish in this one.
func TestForeignRegistrationCannotFinish(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	h.CreateAdmin("bob", pw, console.RoleAdmin, true)
	ada, bob := h.NewBrowser(), h.NewBrowser()
	want(t, ada.Login("ada", pw), 200, "")
	want(t, bob.Login("bob", pw), 200, "")
	pa, pb := h.NewPasskey(), h.NewPasskey()
	startA, startB := ada.StartPasskey(), bob.StartPasskey()
	want(t, startA, 200, "")
	want(t, startB, 200, "")

	// Bob offers Ada's registration id with a credential made for it.
	want(t, bob.FinishPasskey(startA.Str("registrationId"), pa.Attest(h, startA)), 410, "challenge_gone")
	// And with no registration of his own started yet, nothing finishes either.
	carol := h.NewBrowser()
	h.CreateAdmin("carol", pw, console.RoleAdmin, true)
	want(t, carol.Login("carol", pw), 200, "")
	want(t, carol.FinishPasskey(startA.Str("registrationId"), pa.Attest(h, startA)), 410, "challenge_gone")

	// Ada's registration was not spent by those tries, and Bob's own still works.
	want(t, ada.FinishPasskey(startA.Str("registrationId"), pa.Attest(h, startA)), 200, "")
	want(t, bob.FinishPasskey(startB.Str("registrationId"), pb.Attest(h, startB)), 200, "")
}

// Why: signing out must end the session at the server, not just forget the cookie.
func TestLogoutDeletesSession(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	cookie := b.CookieValue()
	want(t, b.Do("POST", "/api/auth/logout", struct{}{}), 204, "")
	again := h.NewBrowser()
	again.SetCookie(cookie)
	want(t, again.Do("GET", "/api/me", nil), 401, "unauthorized")
}

// Why: when AuthGate throttles the Console's client, the admin needs to hear "wait", not "AuthGate is broken".
func TestRateLimitedFromAuthGateIs429(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	burst := 1
	if _, err := h.Clients.Update(context.Background(), h.ClientID,
		client.Patch{RateLimit: &ratelimit.Settings{Burst: burst, PerSecond: 0.01}}); err != nil {
		t.Fatal(err)
	}
	b := h.NewBrowser()
	var r consoletest.Response
	for range 3 {
		if r = b.Login("ada", pw); r.Status == http.StatusTooManyRequests {
			break
		}
	}
	want(t, r, 429, "rate_limited")
	if r.Header.Get("Retry-After") == "" {
		t.Fatal("no Retry-After")
	}
	if len(r.Header.Values("Set-Cookie")) != 0 {
		t.Fatal("a session was opened")
	}
}

// Why: the whole passkey path (enroll, then sign in with it) has to work against the real WebAuthn code.
func TestPasskeyEnrollAndSignIn(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	pk := h.NewPasskey()
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	r := b.EnrollPasskey(pk)
	want(t, r, 200, "")
	if r.Str("credentialId") == "" || r.Str("stage") != "enroll" {
		t.Fatalf("finish = %s", r.Body)
	}
	r = b.Do("POST", "/api/me/password", passwordBody(pw, pw2))
	want(t, r, 200, "")
	if r.Str("stage") != "full" {
		t.Fatalf("password = %s", r.Body)
	}
	want(t, b.Do("POST", "/api/auth/logout", struct{}{}), 204, "")

	again := h.NewBrowser()
	r = again.Login("ada", pw2)
	want(t, r, 200, "")
	f, _ := r.Map()["factors"].(map[string]any)
	if r.Str("next") != "second-factor" || f["webauthn"] != true || f["pin"] != false {
		t.Fatalf("login = %s", r.Body)
	}
	r = again.SignInWithPasskey(pk)
	want(t, r, 200, "")
	if r.Str("stage") != "full" || stageOf(t, again) != "full" {
		t.Fatalf("verify = %s", r.Body)
	}
}
