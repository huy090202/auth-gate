package portal

import (
	"context"
	"strings"
	"testing"
	"time"
)

// lastLink is the token of the newest mail of the kind to the address.
func (e *signupEnv) lastLink(t *testing.T, kind string) string {
	t.Helper()
	var text string
	for _, m := range e.mails(t) {
		if m.Kind == kind {
			text = m.Text
		}
	}
	i := strings.Index(text, "#token=")
	if i < 0 {
		t.Fatalf("no %s mail with a link (mails: %s)", kind, kinds(e.mails(t)))
	}
	return strings.Fields(text[i+len("#token="):])[0]
}

func recoveryBody(email string) map[string]string {
	return map[string]string{"email": email, "turnstileToken": "tok"}
}

// Why: the forgotten-password answer must not reveal whether an address has an account.
func TestForgotAnswersSameForUnknown(t *testing.T) {
	e := newAuthEnv(t)
	e.user(t, "a@x.com", goodPW)
	known := e.post("/api/auth/forgot", recoveryBody(" A@x.com "))
	unknown := e.post("/api/auth/forgot", recoveryBody("nobody@x.com"))
	if known.Code != 202 || unknown.Code != 202 || known.Body.String() != unknown.Body.String() ||
		strings.TrimSpace(known.Body.String()) != `{"status":"check_email"}` {
		t.Fatalf("known %d %q, unknown %d %q", known.Code, known.Body, unknown.Code, unknown.Body)
	}
	if got := e.mails(t); kinds(got) != "reset" || got[0].To != "a@x.com" {
		t.Fatalf("mails = %v", got)
	}
	if !strings.Contains(e.mails(t)[0].Text, "https://portal.test/reset-password#token=") {
		t.Fatalf("link = %q", e.mails(t)[0].Text)
	}
	if len(e.slept) != 2 {
		t.Fatalf("floor waits = %v", e.slept)
	}
}

// Why: only a verified, active account may be mailed a recovery link; anything else gets nothing, silently.
func TestRecoveryRequestsMailOnlyVerifiedActiveAccounts(t *testing.T) {
	e := newAuthEnv(t)
	unverified := User{ID: "01UNVERIFIED", Email: "u@x.com", Lang: "en", PasswordHash: "x", CreatedAt: e.a.now()}
	if err := e.a.Store.CreateUser(context.Background(), unverified); err != nil {
		t.Fatal(err)
	}
	sus := e.user(t, "s@x.com", goodPW)
	if _, err := e.a.Store.DB.UpdateItem(context.Background(), suspendInput(e.a.Store, sus.ID, e.a.now())); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/auth/forgot", "/api/auth/factor-reset/request"} {
		for _, addr := range []string{"u@x.com", "s@x.com"} {
			if r := e.post(path, recoveryBody(addr)); r.Code != 202 {
				t.Fatalf("%s %s = %d", path, addr, r.Code)
			}
		}
	}
	if len(e.mails(t)) != 0 {
		t.Fatalf("mails = %s", kinds(e.mails(t)))
	}
}

// Why: both routes spend the sign-up checks in order: Turnstile, the per-IP scope and the shared address bucket.
func TestRecoveryRequestsAreGuarded(t *testing.T) {
	e := newAuthEnv(t)
	e.user(t, "a@x.com", goodPW)
	for _, path := range []string{"/api/auth/forgot", "/api/auth/factor-reset/request"} {
		if r := e.post(path, map[string]string{"email": "a@x.com"}); r.Code != 403 {
			t.Fatalf("%s without Turnstile = %d", path, r.Code)
		}
		if r := e.post(path, recoveryBody("not-an-email")); r.Code != 400 {
			t.Fatalf("%s malformed = %d", path, r.Code)
		}
	}
	// The shared per-address bucket: 3 per hour across both routes.
	for i := 0; i < 3; i++ {
		path := "/api/auth/forgot"
		if i == 1 {
			path = "/api/auth/factor-reset/request"
		}
		if r := e.post(path, recoveryBody("a@x.com")); r.Code != 202 {
			t.Fatalf("request %d = %d", i, r.Code)
		}
	}
	if r := e.post("/api/auth/forgot", recoveryBody("a@x.com")); r.Code != 429 {
		t.Fatalf("fourth for the address = %d", r.Code)
	}
}

// Why: a reset ends every old session but must not hand over a factor-free sign-in.
func TestResetPasswordEndsSessionsAndKeepsFactor(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	old := e.fullSession(t, u, "203.0.113.1")
	want(t, old.do("GET", "/api/me", nil), 200, "")
	if err := e.a.Store.ResetLoginFailures(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_ = e.a.Store.RecordLoginFailure(context.Background(), u.ID, e.a.now())
	}

	e.post("/api/auth/forgot", recoveryBody("a@x.com"))
	tok := e.lastLink(t, "reset")
	const newPW = "another-long-passphrase"
	if r := e.post("/api/auth/reset-password", map[string]string{"token": tok, "newPassword": "short"}); r.Code != 400 {
		t.Fatalf("weak = %d", r.Code)
	}
	if r := e.post("/api/auth/reset-password", map[string]string{"token": tok, "newPassword": newPW}); r.Code != 204 {
		t.Fatalf("reset = %d %s", r.Code, r.Body)
	}
	want(t, old.do("GET", "/api/me", nil), 401, "")
	if got := e.reload(t, u); got.SessionEpoch != u.SessionEpoch+1 || got.FailedCount != 0 {
		t.Fatalf("epoch %d -> %d, failed %d", u.SessionEpoch, got.SessionEpoch, got.FailedCount)
	}
	if got := kinds(e.mails(t)); !strings.HasSuffix(got, "password-changed") {
		t.Fatalf("mails = %s", got)
	}
	b := e.browser(t, "203.0.113.2")
	want(t, b.login("a@x.com", goodPW), 401, "invalid_credentials")
	r := b.login("a@x.com", newPW)
	want(t, r, 200, "")
	if r.str("next") != "second-factor" {
		t.Fatalf("login after reset = %s", r.Body)
	}
	if f, _ := e.ag.Factors(context.Background(), u.ID); !f.PIN {
		t.Fatal("the PIN did not survive the reset")
	}
	if acts := e.auditActions(t, u); !strings.Contains(strings.Join(acts, ","), "password-reset") {
		t.Fatalf("audit = %v", acts)
	}
}

// Why: a reset link used twice, with an unknown token, or for a breached password must not set anything.
func TestResetPasswordRefusals(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.post("/api/auth/forgot", recoveryBody("a@x.com"))
	tok := e.lastLink(t, "reset")
	body := func(tok, pw string) map[string]string { return map[string]string{"token": tok, "newPassword": pw} }
	const newPW = "another-long-passphrase"

	e.a.Passwords = fakeHIBP{breached: true}
	r := e.post("/api/auth/reset-password", body(tok, newPW))
	if r.Code != 400 || code(t, r) != "password_breached" {
		t.Fatalf("breached = %d %s", r.Code, r.Body)
	}
	e.a.Passwords = fakeHIBP{err: context.DeadlineExceeded} // fails open
	if r := e.post("/api/auth/reset-password", body(tok, newPW)); r.Code != 204 {
		t.Fatalf("hibp down = %d", r.Code)
	}
	again := e.post("/api/auth/reset-password", body(tok, "yet-another-passphrase"))
	unknown := e.post("/api/auth/reset-password", body("bogus", "yet-another-passphrase"))
	if again.Code != 400 || code(t, again) != "invalid_token" || again.Body.String() != unknown.Body.String() {
		t.Fatalf("reuse %d %s, unknown %d %s", again.Code, again.Body, unknown.Code, unknown.Body)
	}
	if got := e.reload(t, u); got.SessionEpoch != u.SessionEpoch+1 {
		t.Fatalf("epoch moved %d times", got.SessionEpoch-u.SessionEpoch)
	}
}

// Why: the link is good for one hour only.
func TestResetTokenExpiresAfterAnHour(t *testing.T) {
	e := newAuthEnv(t)
	e.user(t, "a@x.com", goodPW)
	body := func(tok string) map[string]string {
		return map[string]string{"token": tok, "newPassword": "another-long-passphrase"}
	}
	e.post("/api/auth/forgot", recoveryBody("a@x.com"))
	late := e.lastLink(t, "reset")
	e.advance(time.Hour + time.Second)
	if r := e.post("/api/auth/reset-password", body(late)); r.Code != 400 || code(t, r) != "invalid_token" {
		t.Fatalf("after an hour = %d %s", r.Code, r.Body)
	}
	e.advance(-time.Hour - time.Second)
	e.post("/api/auth/forgot", recoveryBody("a@x.com"))
	fresh := e.lastLink(t, "reset")
	e.advance(59 * time.Minute)
	if r := e.post("/api/auth/reset-password", body(fresh)); r.Code != 204 {
		t.Fatalf("within the hour = %d %s", r.Code, r.Body)
	}
}

// factorResetConfirmed is a user with a PIN whose factor reset was requested and confirmed now.
func (e *authEnv) factorResetConfirmed(t *testing.T) (User, resp2) {
	t.Helper()
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	e.post("/api/auth/factor-reset/request", recoveryBody("a@x.com"))
	r := e.post("/api/auth/factor-reset/confirm", map[string]string{"token": e.lastLink(t, "factor-reset-confirm")})
	return u, resp2{r.Code, r.Body.String()}
}

type resp2 struct {
	code int
	body string
}

// Why: the reset waits 24 hours; before that the old factor still works, after it the next sign-in enrols anew.
func TestFactorResetWaitsTwentyFourHours(t *testing.T) {
	e := newAuthEnv(t)
	u, r := e.factorResetConfirmed(t)
	due := e.a.now().Add(24 * time.Hour)
	if r.code != 200 || !strings.Contains(r.body, `"dueAt":"`+ts(due)+`"`) {
		t.Fatalf("confirm = %d %s", r.code, r.body)
	}
	started := e.mails(t)[len(e.mails(t))-1]
	if started.Kind != "factor-reset-started" || !strings.Contains(started.Text, due.UTC().Format(untilLayout)) {
		t.Fatalf("last mail = %+v", started)
	}
	if fr, _ := e.a.Store.GetFactorReset(context.Background(), u.ID); fr == nil || !fr.DueAt.Equal(due) {
		t.Fatalf("stored reset = %+v", fr)
	}

	e.advance(23 * time.Hour)
	b := e.browser(t, "203.0.113.1")
	res := b.login("a@x.com", goodPW)
	want(t, res, 200, "")
	if res.str("next") != "second-factor" {
		t.Fatalf("at +23h: %s", res.Body)
	}
	if f, _ := e.ag.Factors(context.Background(), u.ID); !f.PIN {
		t.Fatal("factor purged early")
	}

	e.advance(time.Hour + paceAuth)
	b2 := e.browser(t, "203.0.113.1")
	res = b2.login("a@x.com", goodPW)
	want(t, res, 200, "")
	if res.str("next") != "enroll" {
		t.Fatalf("at +24h: %s", res.Body)
	}
	if f, _ := e.ag.Factors(context.Background(), u.ID); f.PIN || len(f.WebAuthn) != 0 {
		t.Fatalf("factors left: %+v", f)
	}
	if acts := e.auditActions(t, u); !strings.Contains(strings.Join(acts, ","), "factor-reset-started") {
		t.Fatalf("audit = %v", acts)
	}
}

// Why: a second confirmation must not push the wait out, and must not mail the owner again.
func TestFactorResetConfirmKeepsEarlierDueTime(t *testing.T) {
	e := newAuthEnv(t)
	u, first := e.factorResetConfirmed(t)
	e.advance(2 * time.Hour)
	e.post("/api/auth/factor-reset/request", recoveryBody("a@x.com"))
	second := e.post("/api/auth/factor-reset/confirm", map[string]string{"token": e.lastLink(t, "factor-reset-confirm")})
	if second.Code != 200 || second.Body.String() != first.body {
		t.Fatalf("first %s, second %d %s", first.body, second.Code, second.Body)
	}
	started := 0
	for _, m := range e.mails(t) {
		if m.Kind == "factor-reset-started" {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("started mails = %d", started)
	}
	_ = u
}

// Why: a factor-reset link is single use, expires after an hour, and is not a password-reset link.
func TestFactorResetLinkRefusals(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	confirm := func(tok string) int {
		return e.post("/api/auth/factor-reset/confirm", map[string]string{"token": tok}).Code
	}
	e.post("/api/auth/forgot", recoveryBody("a@x.com"))
	if c := confirm(e.lastLink(t, "reset")); c != 400 {
		t.Fatalf("reset link on confirm = %d", c)
	}
	e.post("/api/auth/factor-reset/request", recoveryBody("a@x.com"))
	tok := e.lastLink(t, "factor-reset-confirm")
	e.advance(time.Hour + time.Second)
	if c := confirm(tok); c != 400 {
		t.Fatalf("expired = %d", c)
	}
	e.advance(-time.Hour - time.Second)
	e.post("/api/auth/factor-reset/request", recoveryBody("a@x.com"))
	tok = e.lastLink(t, "factor-reset-confirm")
	if c := confirm(tok); c != 200 {
		t.Fatalf("fresh = %d", c)
	}
	if c := confirm(tok); c != 400 {
		t.Fatalf("reuse = %d", c)
	}
	_ = u
}
