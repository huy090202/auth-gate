package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huy090202/auth-gate/internal/portal/mail"
)

type fakeTurnstile struct {
	ok  bool
	err error
}

func (f fakeTurnstile) Verify(context.Context, string, string) (bool, error) { return f.ok, f.err }

type fakeHIBP struct {
	breached bool
	err      error
}

func (f fakeHIBP) Breached(context.Context, string) (bool, error) { return f.breached, f.err }

type signupEnv struct {
	*env
	mailbox string
	slept   []time.Duration // what the answer floor asked to wait
	h       http.Handler
}

func newSignupEnv(t *testing.T) *signupEnv {
	t.Helper()
	e := newEnv(t)
	dir := t.TempDir()
	box, err := mail.NewDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	e.a.Mail, e.a.Turnstile, e.a.Passwords = box, fakeTurnstile{ok: true}, fakeHIBP{}
	e.a.Config.PublicBaseURL = "https://portal.test"
	se := &signupEnv{env: e, mailbox: dir, h: e.a.handler()}
	e.a.Sleep = func(_ context.Context, d time.Duration) { se.slept = append(se.slept, d) }
	return se
}

func (e *signupEnv) post(path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func signupBody(email string) map[string]string {
	return map[string]string{"email": email, "displayName": "Ann", "password": "correct horse battery", "lang": "en", "turnstileToken": "tok"}
}

type sent struct{ To, Kind, Text string }

func (e *signupEnv) mails(t *testing.T) []sent {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(e.mailbox, "*.json"))
	var out []sent
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // paths come from Glob of the test's own temp dir
		if err != nil {
			t.Fatal(err)
		}
		var m struct{ To, Kind, Text string }
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		out = append(out, sent(m))
	}
	return out
}

func (e *signupEnv) token(t *testing.T) string {
	t.Helper()
	for _, m := range e.mails(t) {
		if m.Kind == "verify" {
			i := strings.Index(m.Text, "#token=")
			if i < 0 {
				t.Fatalf("no token in %q", m.Text)
			}
			return strings.Fields(m.Text[i+len("#token="):])[0]
		}
	}
	t.Fatal("no verify mail")
	return ""
}

func kinds(ms []sent) string {
	var k []string
	for _, m := range ms {
		k = append(k, m.Kind)
	}
	return strings.Join(k, ",")
}

// Why: the answer must not reveal whether an address has an account.
func TestSignupAnswersSameForNewAndKnownEmail(t *testing.T) {
	e := newSignupEnv(t)
	e.verified(t, "v@x.com")
	first := e.post("/api/auth/signup", signupBody("a@x.com"))
	if kinds(e.mails(t)) != "verify" {
		t.Fatalf("mails = %v", kinds(e.mails(t)))
	}
	second := e.post("/api/auth/signup", signupBody("v@x.com"))
	if first.Code != 202 || second.Code != 202 || first.Body.String() != second.Body.String() ||
		strings.TrimSpace(first.Body.String()) != `{"status":"check_email"}` {
		t.Fatalf("new %d %q, known %d %q", first.Code, first.Body, second.Code, second.Body)
	}
	if got := kinds(e.mails(t)); got != "verify,signup-exists" && got != "signup-exists,verify" {
		t.Fatalf("mails = %s", got)
	}
}

// Why: case or spaces must not create a second account for the same address.
func TestSignupNormalisesEmail(t *testing.T) {
	e := newSignupEnv(t)
	e.post("/api/auth/signup", signupBody(" A@X.com "))
	first, _ := e.a.Store.GetUserByEmail(context.Background(), "a@x.com")
	e.post("/api/auth/signup", signupBody("a@x.com"))
	if again, err := e.a.Store.GetUserByEmail(context.Background(), "a@x.com"); err != nil || again.ID != first.ID {
		t.Fatalf("a second account: %+v %v (first %s)", again, err, first.ID)
	}
	for _, m := range e.mails(t) {
		if m.To != "a@x.com" || m.Kind != "verify" {
			t.Fatalf("mails = %+v", e.mails(t))
		}
	}
}

// Why: a known-breached password must not become a Portal password.
func TestSignupRefusesBreachedPassword(t *testing.T) {
	e := newSignupEnv(t)
	e.a.Passwords = fakeHIBP{breached: true}
	rec := e.post("/api/auth/signup", signupBody("a@x.com"))
	if rec.Code != 400 || code(t, rec) != "password_breached" || len(e.mails(t)) != 0 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// Why: HIBP being down must not stop sign-ups.
func TestSignupFailsOpenWhenHIBPDown(t *testing.T) {
	e := newSignupEnv(t)
	e.a.Passwords = fakeHIBP{err: errors.New("down")}
	if rec := e.post("/api/auth/signup", signupBody("a@x.com")); rec.Code != 202 {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// Why: sign-up is the bot target; no token, a refused token or a Cloudflare outage must write nothing.
func TestSignupRequiresTurnstile(t *testing.T) {
	e := newSignupEnv(t)
	b := signupBody("a@x.com")
	b["turnstileToken"] = ""
	if rec := e.post("/api/auth/signup", b); rec.Code != 403 || code(t, rec) != "turnstile_failed" {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body)
	}
	e.a.Turnstile = fakeTurnstile{ok: false}
	if rec := e.post("/api/auth/signup", signupBody("a@x.com")); rec.Code != 403 || code(t, rec) != "turnstile_failed" {
		t.Fatalf("refused: %d %s", rec.Code, rec.Body)
	}
	e.a.Turnstile = fakeTurnstile{err: errors.New("down")}
	if rec := e.post("/api/auth/signup", signupBody("a@x.com")); rec.Code != 503 || code(t, rec) != "turnstile_unavailable" {
		t.Fatalf("down: %d %s", rec.Code, rec.Body)
	}
	if _, err := e.a.Store.GetUserByEmail(context.Background(), "a@x.com"); !errors.Is(err, ErrUserNotFound) || len(e.mails(t)) != 0 {
		t.Fatalf("something was written: %v %v", err, kinds(e.mails(t)))
	}
}

// Why: malformed input is the only thing that may answer other than 202.
func TestSignupRejectsMalformedInput(t *testing.T) {
	e := newSignupEnv(t)
	cases := []struct{ field, val, want string }{
		{"email", "not-an-email", "invalid_email"},
		{"email", "Ann <a@x.com>", "invalid_email"},
		{"email", "a@x.com, b@x.com", "invalid_email"},
		{"displayName", "   ", "invalid_name"},
		{"displayName", "a\x00b", "invalid_name"},
		{"displayName", strings.Repeat("é", 65), "invalid_name"},
		{"password", "short", "invalid_password"},
		{"lang", "fr", "invalid_lang"},
	}
	for _, c := range cases {
		b := signupBody("a@x.com")
		b[c.field] = c.val
		if rec := e.post("/api/auth/signup", b); rec.Code != 400 || code(t, rec) != c.want {
			t.Errorf("%s=%q: %d %s, want %s", c.field, c.val, rec.Code, rec.Body, c.want)
		}
	}
}

// Why: the per-IP bucket is the brake on mass sign-up; one address is also capped so it cannot be mail-bombed, known or not.
func TestSignupRateLimits(t *testing.T) {
	e := newSignupEnv(t)
	for i := 0; i < 5; i++ {
		if rec := e.post("/api/auth/signup", signupBody("u"+string(rune('a'+i))+"@x.com")); rec.Code != 202 {
			t.Fatalf("sign-up %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if rec := e.post("/api/auth/signup", signupBody("z@x.com")); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("6th: %d %s", rec.Code, rec.Body)
	}
	e2 := newSignupEnv(t)
	for i := 0; i < 3; i++ {
		if rec := e2.post("/api/auth/signup", signupBody("a@x.com")); rec.Code != 202 {
			t.Fatalf("same address %d: %d", i, rec.Code)
		}
	}
	if rec := e2.post("/api/auth/signup", signupBody("a@x.com")); rec.Code != 429 {
		t.Fatalf("4th for one address: %d", rec.Code)
	}
	if n := len(e2.mails(t)); n != 3 {
		t.Fatalf("mails = %d", n)
	}
}

// Why: verification is what creates the workspace and signs the person in to enrol factors.
func TestVerifyEmailCreatesWorkspaceAndEnrollSession(t *testing.T) {
	e := newSignupEnv(t)
	e.post("/api/auth/signup", signupBody("a@x.com"))
	rec := e.post("/api/auth/verify-email", verifyBody(e.token(t), signupPW))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"next":"enroll"}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	u, err := e.a.Store.GetUserByEmail(context.Background(), "a@x.com")
	if err != nil || u.EmailVerifiedAt == nil || u.PersonalTenantID == "" {
		t.Fatalf("user = %+v %v", u, err)
	}
	if m, err := e.a.Store.GetMembership(context.Background(), u.PersonalTenantID, u.ID); err != nil || m.Role != RoleOwner {
		t.Fatalf("membership = %+v %v", m, err)
	}
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies = %v", cs)
	}
	s, err := e.a.Store.GetSession(context.Background(), hashToken(cs[0].Value))
	if err != nil || s.Stage != StageEnroll || s.UserID != u.ID {
		t.Fatalf("session = %+v %v", s, err)
	}
	if es, _, _ := e.a.Store.ListAudit(context.Background(), u.PersonalTenantID, ""); len(es) != 1 || es[0].Action != "email-verified" || es[0].ActorID != u.ID {
		t.Fatalf("audit = %+v", es)
	}
}

// Why: a link used twice must do nothing the second time and look like any unknown token.
func TestVerifyTokenSingleUse(t *testing.T) {
	e := newSignupEnv(t)
	e.post("/api/auth/signup", signupBody("a@x.com"))
	tok := e.token(t)
	if rec := e.post("/api/auth/verify-email", verifyBody(tok, signupPW)); rec.Code != 200 {
		t.Fatalf("first: %d", rec.Code)
	}
	u, _ := e.a.Store.GetUserByEmail(context.Background(), "a@x.com")
	rec := e.post("/api/auth/verify-email", verifyBody(tok, signupPW))
	if rec.Code != 400 || code(t, rec) != "invalid_token" || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("second: %d %s", rec.Code, rec.Body)
	}
	if u2, _ := e.a.Store.GetUserByEmail(context.Background(), "a@x.com"); u2.PersonalTenantID != u.PersonalTenantID {
		t.Fatal("a second workspace was made")
	}
	if rec := e.post("/api/auth/verify-email", verifyBody("nope", signupPW)); rec.Code != 400 || code(t, rec) != "invalid_token" {
		t.Fatalf("unknown: %d %s", rec.Code, rec.Body)
	}
}

// Why: a lapsed link must answer like an unknown one and leave the account unverified.
func TestVerifyTokenExpires(t *testing.T) {
	e := newSignupEnv(t)
	e.post("/api/auth/signup", signupBody("a@x.com"))
	tok := e.token(t)
	*e.clock = e.clock.Add(25 * 60 * 60 * 1e9)
	if rec := e.post("/api/auth/verify-email", verifyBody(tok, signupPW)); rec.Code != 400 || code(t, rec) != "invalid_token" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// Why: resend must not reveal which addresses exist or are verified, and must work for the unverified one.
func TestResendVerification(t *testing.T) {
	e := newSignupEnv(t)
	e.post("/api/auth/signup", signupBody("a@x.com"))
	e.verified(t, "v@x.com")
	before := len(e.mails(t))
	var bodies []string
	for _, addr := range []string{"nobody@x.com", "v@x.com"} {
		rec := e.post("/api/auth/resend-verification", map[string]string{"email": addr, "turnstileToken": "t"})
		bodies = append(bodies, rec.Body.String())
		if rec.Code != 202 {
			t.Fatalf("%s: %d", addr, rec.Code)
		}
	}
	if len(e.mails(t)) != before {
		t.Fatal("mail sent for an unknown or verified address")
	}
	rec := e.post("/api/auth/resend-verification", map[string]string{"email": " A@x.com", "turnstileToken": "t"})
	if rec.Code != 202 || rec.Body.String() != bodies[0] || len(e.mails(t)) != before+1 {
		t.Fatalf("unverified: %d %s mails=%d", rec.Code, rec.Body, len(e.mails(t)))
	}
	e.a.Turnstile = fakeTurnstile{ok: false}
	if rec := e.post("/api/auth/resend-verification", map[string]string{"email": "a@x.com", "turnstileToken": "t"}); rec.Code != 403 {
		t.Fatalf("turnstile: %d", rec.Code)
	}
}

// Why: the sign-up page needs its site key without a session.
func TestPublicConfig(t *testing.T) {
	e := newSignupEnv(t)
	e.a.Config.TurnstileSiteKey = "site"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"turnstileSiteKey":"site"}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

// Why: a new and a known address, an unknown and a pending one, must take the same time to answer, or timing reveals the account.
func TestSignupAndResendWaitTheSameFloor(t *testing.T) {
	e := newSignupEnv(t)
	e.post("/api/auth/signup", signupBody("a@x.com")) // new
	e.post("/api/auth/signup", signupBody("a@x.com")) // known
	e.post("/api/auth/resend-verification", map[string]string{"email": "nobody@x.com", "turnstileToken": "t"})
	e.post("/api/auth/resend-verification", map[string]string{"email": "a@x.com", "turnstileToken": "t"}) // pending
	if len(e.slept) != 4 {
		t.Fatalf("waits = %v", e.slept)
	}
	for _, d := range e.slept {
		if d != answerFloor {
			t.Fatalf("waits = %v, want every one %v", e.slept, answerFloor)
		}
	}
	// A refusal that does not depend on the account answers at once.
	n := len(e.slept)
	b := signupBody("a@x.com")
	b["lang"] = "fr"
	if rec := e.post("/api/auth/signup", b); rec.Code != 400 || len(e.slept) != n {
		t.Fatalf("malformed waited or passed: %d %v", rec.Code, e.slept)
	}
}

// Why: a failing token write must not make the pending address answer 5xx while an unknown one answers 202.
func TestResendFailureStillAnswers202(t *testing.T) {
	e := newSignupEnv(t)
	e.post("/api/auth/signup", signupBody("a@x.com"))
	// A generator that repeats itself makes the second PutToken collide.
	e.a.newToken = func() (string, string, error) { return "fixed", hashToken("fixed"), nil }
	for i := 0; i < 2; i++ {
		rec := e.post("/api/auth/resend-verification", map[string]string{"email": "a@x.com", "turnstileToken": "t"})
		if rec.Code != 202 || strings.TrimSpace(rec.Body.String()) != `{"status":"check_email"}` {
			t.Fatalf("resend %d: %d %s", i, rec.Code, rec.Body)
		}
	}
}

const signupPW = "correct horse battery" // signupBody's password

func verifyBody(tok, pw string) map[string]string {
	return map[string]string{"token": tok, "password": pw}
}

// Why: a stranger who signs up first with the owner's address must not win the account when the owner clicks the
// link: the owner's own sign-up replaces the pending account, and only the owner's password then verifies it.
func TestSignupReplacesPendingSquatter(t *testing.T) {
	e := newSignupEnv(t)
	const attackerPW, ownerPW = "attacker long password", "owner long passphrase"
	b := signupBody("a@x.com")
	b["password"], b["displayName"], b["lang"] = attackerPW, "Mallory", "vi"
	e.post("/api/auth/signup", b)
	squat := e.lastLink(t, "verify")
	squatter, err := e.a.Store.GetUserByEmail(context.Background(), "a@x.com")
	if err != nil {
		t.Fatal(err)
	}

	b = signupBody(" A@x.com ")
	b["password"] = ownerPW
	if rec := e.post("/api/auth/signup", b); rec.Code != 202 {
		t.Fatalf("owner sign-up = %d %s", rec.Code, rec.Body)
	}
	if got := kinds(e.mails(t)); got != "verify,verify" {
		t.Fatalf("mails = %s, want a fresh verify link for the owner", got)
	}
	owner := e.lastLink(t, "verify")
	u, err := e.a.Store.GetUserByEmail(context.Background(), "a@x.com")
	if err != nil || u.ID != squatter.ID || u.DisplayName != "Ann" || u.Lang != "en" || u.SessionEpoch != squatter.SessionEpoch+1 {
		t.Fatalf("pending account not replaced: %+v %v", u, err)
	}
	// The squatter's link died with the old epoch, and the squatter's password opens nothing.
	if rec := e.post("/api/auth/verify-email", verifyBody(squat, ownerPW)); rec.Code != 400 || code(t, rec) != "invalid_token" {
		t.Fatalf("old link = %d %s", rec.Code, rec.Body)
	}
	if rec := e.post("/api/auth/verify-email", verifyBody(owner, attackerPW)); rec.Code != 400 || code(t, rec) != "invalid_token" {
		t.Fatalf("attacker password = %d %s", rec.Code, rec.Body)
	}
	if rec := e.post("/api/auth/verify-email", verifyBody(owner, ownerPW)); rec.Code != 200 {
		t.Fatalf("owner password = %d %s", rec.Code, rec.Body)
	}
	if u, _ := e.a.Store.GetUserByEmail(context.Background(), "a@x.com"); u.EmailVerifiedAt == nil {
		t.Fatal("not verified")
	}
}

// Why: a wrong password at the link must look like a bad link, count like a failed sign-in, and leave the link
// usable so the owner can retype.
func TestVerifyEmailWrongPasswordKeepsToken(t *testing.T) {
	e := newSignupEnv(t)
	e.post("/api/auth/signup", signupBody("a@x.com"))
	tok := e.lastLink(t, "verify")
	bad := e.post("/api/auth/verify-email", verifyBody(tok, "not the password at all"))
	unknown := e.post("/api/auth/verify-email", verifyBody("nope", signupPW))
	if bad.Code != 400 || bad.Body.String() != unknown.Body.String() || len(bad.Result().Cookies()) != 0 {
		t.Fatalf("wrong password = %d %s, unknown token = %s", bad.Code, bad.Body, unknown.Body)
	}
	u, _ := e.a.Store.GetUserByEmail(context.Background(), "a@x.com")
	if u.FailedCount != 1 || u.EmailVerifiedAt != nil {
		t.Fatalf("after a wrong password: %+v", u)
	}
	if n, _ := e.a.Store.FailCount(context.Background(), failAcct, emailHash("a@x.com"), e.a.now()); n != 1 {
		t.Fatalf("account window = %d", n)
	}
	if rec := e.post("/api/auth/verify-email", verifyBody(tok, signupPW)); rec.Code != 200 {
		t.Fatalf("right password after a wrong one = %d %s", rec.Code, rec.Body)
	}
}

// Why: sign-up for a new, a pending and a verified address must answer the same bytes after the same floor.
func TestSignupAnswersSameForNewPendingAndVerified(t *testing.T) {
	e := newSignupEnv(t)
	e.verified(t, "v@x.com")
	e.post("/api/auth/signup", signupBody("p@x.com"))
	e.slept = nil
	var bodies []string
	for _, addr := range []string{"n@x.com", "p@x.com", "v@x.com"} {
		rec := e.post("/api/auth/signup", signupBody(addr))
		if rec.Code != 202 {
			t.Fatalf("%s: %d %s", addr, rec.Code, rec.Body)
		}
		bodies = append(bodies, rec.Body.String())
	}
	if bodies[0] != bodies[1] || bodies[1] != bodies[2] || len(e.slept) != 3 || e.slept[0] != answerFloor ||
		e.slept[1] != answerFloor || e.slept[2] != answerFloor {
		t.Fatalf("bodies %q, waits %v", bodies, e.slept)
	}
	got := map[string]string{}
	for _, m := range e.mails(t) {
		got[m.To] += m.Kind + ";"
	}
	if got["n@x.com"] != "verify;" || got["p@x.com"] != "verify;verify;" || got["v@x.com"] != "signup-exists;" {
		t.Fatalf("mails = %v", got)
	}
}
