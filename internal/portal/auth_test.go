package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/descope/virtualwebauthn"

	"github.com/huy090202/auth-gate/internal/auth/challenge"
	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/auth/factor"
	"github.com/huy090202/auth-gate/internal/auth/handler"
	"github.com/huy090202/auth-gate/internal/auth/service"
	"github.com/huy090202/auth-gate/internal/auth/token"
	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/password"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

const (
	rpID     = "portal.test"
	origin   = "https://portal.test"
	pin1     = "582913" // AuthGate's policy refuses sequential and repeated PINs
	pin2     = "749306"
	goodPW   = "correct-horse-battery"
	codeRE   = `^[0-9A-HJKMNP-TV-Z]{5}-[0-9A-HJKMNP-TV-Z]{5}$`
	paceAuth = 7 * time.Second // the login bucket refills one token per 6 s
)

// authEnv is a Portal over a real AuthGate (in process, on its own table) and a
// TLS front for browsers with cookie jars.
type authEnv struct {
	*signupEnv
	ag       *authgate.Client
	clientID string
	url      string
	hc       *http.Client
	logins   atomic.Int32 // password comparisons
}

func newAuthEnv(t *testing.T) *authEnv {
	t.Helper()
	se := newSignupEnv(t)
	now := func() time.Time { return *se.clock }
	db, agTable := dynamotest.NewTable(t)
	signer, err := token.NewLocalSigner("harness")
	if err != nil {
		t.Fatal(err)
	}
	clients := &client.Service{Store: client.NewDynamoStore(db, agTable), Now: now}
	svc := &service.Service{
		Clients: clients, Factors: factor.NewDynamoStore(db, agTable), Challenges: challenge.NewDynamoStore(db, agTable),
		Signer: signer, Now: now,
	}
	mux := http.NewServeMux()
	mux.Handle("/verify/", http.StripPrefix("/verify", handler.NewVerify(svc, clients, &ratelimit.Limiter{DB: db, Table: agTable, Now: now})))
	mux.Handle("/jwks/", http.StripPrefix("/jwks", handler.NewJWKS(signer)))
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	svc.Issuer = srv.URL
	c, key, err := clients.Create(context.Background(), client.CreateInput{Name: "portal", RPID: rpID, Origins: []string{origin}})
	if err != nil {
		t.Fatal(err)
	}
	e := &authEnv{signupEnv: se, clientID: c.ID}
	e.ag = &authgate.Client{BaseURL: srv.URL, ClientKey: key, HTTP: srv.Client()}
	e.a.AuthGate = e.ag
	e.a.Tokens = &authgate.Verifier{BaseURL: srv.URL, Audience: c.ID, HTTP: srv.Client(), Now: now}
	e.a.verifyPassword = func(p, stored string) bool { e.logins.Add(1); return password.Verify(p, stored) }
	front := httptest.NewTLSServer(e.h)
	t.Cleanup(front.Close)
	e.url, e.hc = front.URL, front.Client()
	return e
}

// user stores a verified account with a real password hash and a personal workspace.
func (e *authEnv) user(t *testing.T, email, pw string) User {
	t.Helper()
	hash, err := password.Hash(pw)
	if err != nil {
		t.Fatal(err)
	}
	u := User{ID: id.New(), Email: email, PasswordHash: hash, Lang: "en", CreatedAt: e.a.now()}
	if err := e.a.Store.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if err := e.a.Store.VerifyEmail(context.Background(), u.ID, Tenant{ID: id.New()}, e.a.now()); err != nil {
		t.Fatal(err)
	}
	u, err = e.a.Store.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *authEnv) withPIN(t *testing.T, u User, pin string) {
	t.Helper()
	if err := e.ag.SetPIN(context.Background(), u.ID, pin); err != nil {
		t.Fatal(err)
	}
}

func (e *authEnv) advance(d time.Duration) { *e.clock = e.clock.Add(d) }

type resp struct {
	Status int
	Body   []byte
}

func (r resp) m() map[string]any   { m := map[string]any{}; _ = json.Unmarshal(r.Body, &m); return m }
func (r resp) str(k string) string { s, _ := r.m()[k].(string); return s }

func want(t *testing.T, r resp, status int, code string) {
	t.Helper()
	if r.Status != status || (code != "" && r.str("error") != code) {
		t.Fatalf("got %d %s, want %d %s", r.Status, r.Body, status, code)
	}
}

type browser struct {
	t   *testing.T
	e   *authEnv
	hc  *http.Client
	ip  string
	pin string
}

// browser is a caller with its own cookie jar and viewer address.
func (e *authEnv) browser(t *testing.T, ip string) *browser {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := *e.hc
	c.Jar = jar
	return &browser{t: t, e: e, hc: &c, ip: ip}
}

func (b *browser) do(method, path string, body any, headers ...string) resp {
	b.t.Helper()
	if strings.HasPrefix(path, "/api/auth/") {
		b.e.advance(paceAuth)
	}
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, b.e.url+path, rd)
	if err != nil {
		b.t.Fatal(err)
	}
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	if b.ip != "" {
		req.Header.Set("CloudFront-Viewer-Address", b.ip+":4000")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := b.hc.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	raw, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, raw}
}

func (b *browser) login(email, pw string) resp {
	b.t.Helper()
	return b.do("POST", "/api/auth/login", map[string]string{"email": email, "password": pw, "turnstileToken": "t"})
}

func (b *browser) cookie() string {
	u, _ := url.Parse(b.e.url)
	for _, c := range b.hc.Jar.Cookies(u) {
		if c.Name == cookieName {
			return c.Value
		}
	}
	return ""
}

func (b *browser) signInWithPIN(pin string) resp {
	b.t.Helper()
	st := b.do("POST", "/api/auth/second-factor/start", map[string]string{"factor": "pin"})
	want(b.t, st, 200, "")
	r := b.do("POST", "/api/auth/second-factor/verify", map[string]string{"challengeId": st.str("challengeId"), "pin": pin})
	if r.Status == 200 {
		b.pin = pin
	}
	return r
}

// fullSession signs the user in with their PIN.
func (e *authEnv) fullSession(t *testing.T, u User, ip string) *browser {
	t.Helper()
	b := e.browser(t, ip)
	want(t, b.login(u.Email, goodPW), 200, "")
	want(t, b.signInWithPIN(pin1), 200, "")
	return b
}

func (b *browser) stepUp(action, target string) string {
	b.t.Helper()
	body := map[string]string{"action": action, "factor": "pin"}
	if target != "" {
		body["target"] = target
	}
	st := b.do("POST", "/api/stepup/start", body)
	want(b.t, st, 200, "")
	v := b.do("POST", "/api/stepup/verify", map[string]string{"challengeId": st.str("challengeId"), "pin": b.pin})
	want(b.t, v, 200, "")
	return v.str("stepUpToken")
}

func codesOf(t *testing.T, r resp) []string {
	t.Helper()
	var out struct{ RecoveryCodes []string }
	_ = json.Unmarshal(r.Body, &out)
	return out.RecoveryCodes
}

func (e *authEnv) auditActions(t *testing.T, u User) []string {
	t.Helper()
	es, _, err := e.a.Store.ListAudit(context.Background(), u.PersonalTenantID, "")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range es {
		out = append(out, x.Action+":"+x.Result)
	}
	return out
}

// Why: a sign-in that answers or costs differently for an unknown address, a wrong password or a suspended
// account tells an attacker which addresses have accounts.
func TestLoginFailuresLookAlike(t *testing.T) {
	e := newAuthEnv(t)
	e.user(t, "live@x.com", goodPW)
	sus := e.user(t, "sus@x.com", goodPW)
	if _, err := e.a.Store.DB.UpdateItem(context.Background(), suspendInput(e.a.Store, sus.ID, e.a.now())); err != nil {
		t.Fatal(err)
	}
	b := e.browser(t, "203.0.113.1")
	var bodies []string
	for _, c := range [][2]string{{"nobody@x.com", goodPW}, {"live@x.com", "wrong-password-here"}, {"sus@x.com", goodPW}, {"sus@x.com", "wrong-password-here"}} {
		r := b.login(c[0], c[1])
		want(t, r, 401, "invalid_credentials")
		if len(r.Body) == 0 || len(r.m()) != 1 {
			t.Fatalf("body = %s", r.Body)
		}
		bodies = append(bodies, string(r.Body))
	}
	for _, x := range bodies[1:] {
		if x != bodies[0] {
			t.Fatalf("bodies differ: %q", bodies)
		}
	}
	if n := e.logins.Load(); n != 4 {
		t.Fatalf("password comparisons = %d, want 4 (one per attempt)", n)
	}
	// A suspended account's failures do not move its lockout counter.
	if u, _ := e.a.Store.GetUser(context.Background(), sus.ID); u.FailedCount != 0 {
		t.Fatalf("suspended failedCount = %d", u.FailedCount)
	}
	if kinds(e.mails(t)) != "" {
		t.Fatalf("mails = %s", kinds(e.mails(t)))
	}
}

// Why: telling "unverified" before the password is right would reveal who has signed up.
func TestLoginUnverifiedOnlyAfterRightPassword(t *testing.T) {
	e := newAuthEnv(t)
	hash, _ := password.Hash(goodPW)
	if err := e.a.Store.CreateUser(context.Background(), User{ID: id.New(), Email: "p@x.com", PasswordHash: hash, Lang: "en", CreatedAt: e.a.now()}); err != nil {
		t.Fatal(err)
	}
	b := e.browser(t, "203.0.113.1")
	want(t, b.login("p@x.com", "wrong-password-here"), 401, "invalid_credentials")
	want(t, b.login("p@x.com", goodPW), 403, "email_unverified")
	if b.cookie() != "" {
		t.Fatal("a session was opened for an unverified address")
	}
}

// Why: five wrong passwords must stop guessing, and the owner must hear of it once.
func TestLoginLocksAfterFiveFailures(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	b := e.browser(t, "203.0.113.1")
	for i := 0; i < 5; i++ {
		want(t, b.login("a@x.com", "wrong-password-here"), 401, "invalid_credentials")
	}
	if kinds(e.mails(t)) != "account-locked" {
		t.Fatalf("mails after the lock = %q", kinds(e.mails(t)))
	}
	// The right password is refused the same way while locked, and sends nothing more.
	want(t, b.login("a@x.com", goodPW), 401, "invalid_credentials")
	want(t, b.login("a@x.com", "wrong-password-here"), 401, "invalid_credentials")
	if kinds(e.mails(t)) != "account-locked" {
		t.Fatalf("mails = %q", kinds(e.mails(t)))
	}
	e.advance(lockFor + time.Minute)
	e.advance(failWindow) // and the Turnstile windows have lapsed
	r := b.login(" A@X.com ", goodPW)
	want(t, r, 200, "")
	if r.str("next") != "enroll" {
		t.Fatalf("after the lock: %s", r.Body)
	}
	if cur, _ := e.a.Store.GetUser(context.Background(), u.ID); cur.FailedCount != 0 || cur.LockedUntil != nil {
		t.Fatalf("counters not reset: %+v", cur)
	}
	// Account events land in the user's personal workspace.
	got := strings.Join(e.auditActions(t, u), " ")
	if !strings.Contains(got, "login:success") || !strings.Contains(got, "login:failed:invalid_credentials") {
		t.Fatalf("audit = %s", got)
	}
}

// Why: Turnstile after two failures slows a guesser from one address and one from many addresses at one account,
// and an unknown address must not look different from a known one.
func TestLoginNeedsTurnstileAfterTwoFailures(t *testing.T) {
	e := newAuthEnv(t)
	e.user(t, "a@x.com", goodPW)
	e.user(t, "b@x.com", goodPW)
	noToken := func(b *browser, email, pw string) resp {
		return b.do("POST", "/api/auth/login", map[string]string{"email": email, "password": pw})
	}

	// Per IP: two failures (on addresses that do not exist) and the third try needs the widget.
	ipA := e.browser(t, "203.0.113.1")
	want(t, noToken(ipA, "ghost1@x.com", "x-wrong-password"), 401, "invalid_credentials")
	want(t, noToken(ipA, "ghost2@x.com", "x-wrong-password"), 401, "invalid_credentials")
	want(t, noToken(ipA, "b@x.com", goodPW), 403, "turnstile_required")
	e.a.Turnstile = fakeTurnstile{ok: false}
	want(t, ipA.login("b@x.com", goodPW), 403, "turnstile_required")
	e.a.Turnstile = fakeTurnstile{ok: true}
	want(t, ipA.login("b@x.com", goodPW), 200, "") // a solved widget lets a right password through
	// Another address is not slowed by it.
	ipB := e.browser(t, "203.0.113.2")
	want(t, noToken(ipB, "b@x.com", goodPW), 200, "")

	// Per account: one failure from each of two addresses, then a third address is asked.
	want(t, noToken(e.browser(t, "198.51.100.1"), "a@x.com", "x-wrong-password"), 401, "invalid_credentials")
	want(t, noToken(e.browser(t, "198.51.100.2"), "a@x.com", "x-wrong-password"), 401, "invalid_credentials")
	fresh := e.browser(t, "198.51.100.3")
	want(t, noToken(fresh, "a@x.com", goodPW), 403, "turnstile_required")
	// The same holds for an address with no account, so the answer says nothing about it.
	want(t, noToken(e.browser(t, "198.51.100.4"), "ghost3@x.com", "x-wrong-password"), 401, "invalid_credentials")
	want(t, noToken(e.browser(t, "198.51.100.5"), "ghost3@x.com", "x-wrong-password"), 401, "invalid_credentials")
	want(t, noToken(e.browser(t, "198.51.100.6"), "ghost3@x.com", "x-wrong-password"), 403, "turnstile_required")

	// The window is 15 minutes.
	e.advance(failWindow + time.Second)
	want(t, noToken(fresh, "a@x.com", goodPW), 200, "")
}

// Why: the recovery codes are the way back in; they must be shown with the first factor and only then.
func TestFirstFactorIssuesTenRecoveryCodesOnce(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	b := e.browser(t, "203.0.113.1")
	want(t, b.login("a@x.com", goodPW), 200, "")
	r := b.do("POST", "/api/me/pin", map[string]string{"pin": pin1})
	want(t, r, 200, "")
	b.pin = pin1
	codes := codesOf(t, r)
	seen := map[string]bool{}
	for _, c := range codes {
		if !regexp.MustCompile(codeRE).MatchString(c) || seen[c] {
			t.Fatalf("bad or repeated code %q in %v", c, codes)
		}
		seen[c] = true
	}
	if len(codes) != 10 || r.str("stage") != "full" {
		t.Fatalf("got %d codes, stage %s", len(codes), r.str("stage"))
	}
	me := b.do("GET", "/api/me", nil)
	want(t, me, 200, "")
	if me.m()["recoveryCodesLeft"] != float64(10) || me.str("email") != "a@x.com" || me.str("userId") != u.ID ||
		me.str("personalTenantId") != u.PersonalTenantID || me.str("stage") != "full" {
		t.Fatalf("me = %s", me.Body)
	}
	// Only hashes are stored.
	out, err := e.a.Store.DB.Query(context.Background(), &dynamodb.QueryInput{
		TableName: aws.String(e.a.Store.Table), KeyConditionExpression: aws.String("pk = :p AND begins_with(sk, :r)"),
		ExpressionAttributeValues: item{":p": s("USER#" + u.ID), ":r": s("RECOVERY#")}})
	if err != nil || len(out.Items) != 10 {
		t.Fatalf("items = %d %v", len(out.Items), err)
	}
	for _, it := range out.Items {
		for c := range seen {
			if strings.Contains(getS(it, "sk"), strings.ReplaceAll(c, "-", "")) {
				t.Fatalf("a code is stored in the clear: %v", it)
			}
		}
	}
	// A second factor later does not show codes again, and the first factor sent no email.
	tok := b.stepUp("add-factor", "")
	r2 := b.do("POST", "/api/me/pin", map[string]string{"pin": pin2}, "x-step-up-token", tok)
	want(t, r2, 200, "")
	if _, has := r2.m()["recoveryCodes"]; has {
		t.Fatalf("codes shown again: %s", r2.Body)
	}
	if kinds(e.mails(t)) != "factor-added" {
		t.Fatalf("mails = %q", kinds(e.mails(t)))
	}
	// Regenerate needs a step-up bound to the action, and voids the old set.
	want(t, b.do("POST", "/api/me/recovery-codes", struct{}{}), 403, "step_up_required")
	b.pin = pin2
	wrong := b.stepUp("add-factor", "")
	want(t, b.do("POST", "/api/me/recovery-codes", struct{}{}, "x-step-up-token", wrong), 403, "step_up_required")
	reg := b.do("POST", "/api/me/recovery-codes", struct{}{}, "x-step-up-token", b.stepUp("regenerate-recovery-codes", ""))
	want(t, reg, 200, "")
	fresh := codesOf(t, reg)
	if len(fresh) != 10 || seen[fresh[0]] {
		t.Fatalf("regenerated = %v", fresh)
	}
	if n, _ := e.a.Store.CountRecoveryCodes(context.Background(), u.ID); n != 10 {
		t.Fatalf("codes after regenerate = %d", n)
	}
	if ok, _ := e.a.Store.UseRecoveryCode(context.Background(), u.ID, codes[0]); ok {
		t.Fatal("an old code still works after regenerate")
	}
	if ok, _ := e.a.Store.UseRecoveryCode(context.Background(), u.ID, fresh[0]); !ok {
		t.Fatal("a new code does not work")
	}
}

func TestNormaliseCode(t *testing.T) {
	for in, want := range map[string]string{"abcde-fghij": "ABCDE-FGHIJ", "ABCDEFGHIJ": "ABCDE-FGHIJ", " abcde fghij ": "ABCDE-FGHIJ",
		"ab-cd-ef-gh-ij": "ABCDE-FGHIJ", "short": "SHORT"} {
		if got := normaliseCode(in); got != want {
			t.Errorf("normaliseCode(%q) = %q, want %q", in, got, want)
		}
	}
}

// recoverable is a user with a PIN, three known recovery codes and a half session.
func (e *authEnv) recoverable(t *testing.T, ip string) (User, *browser) {
	t.Helper()
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	if err := e.a.Store.ReplaceRecoveryCodes(context.Background(), u.ID, []string{"ABCDE-FGHIJ", "KMNPQ-RSTVW", "XYZ01-23456"}); err != nil {
		t.Fatal(err)
	}
	b := e.browser(t, ip)
	want(t, b.login("a@x.com", goodPW), 200, "")
	return u, b
}

// Why: a code typed in lower case, with a space or without the dash must still work, once.
func TestRecoveryCodeNormalisedAndSingleUse(t *testing.T) {
	e := newAuthEnv(t)
	u, b := e.recoverable(t, "203.0.113.1")
	want(t, b.do("POST", "/api/auth/recovery", map[string]string{"code": "abcde-fghij"}), 200, "")
	for _, c := range []string{"abcde-fghij", "ABCDEFGHIJ", "abcde fghij"} { // second use of the first code
		b2 := e.browser(t, "203.0.113.1")
		want(t, b2.login("a@x.com", goodPW), 200, "")
		want(t, b2.do("POST", "/api/auth/recovery", map[string]string{"code": c}), 401, "invalid_credentials")
	}
	b3 := e.browser(t, "203.0.113.1")
	want(t, b3.login("a@x.com", goodPW), 200, "") // the count so far (3 wrong codes) is not reset by a right password; this recovery completes the sign-in
	want(t, b3.do("POST", "/api/auth/recovery", map[string]string{"code": "kmnpq rstvw"}), 200, "")
	b4 := e.browser(t, "203.0.113.1")
	want(t, b4.login("a@x.com", goodPW), 200, "")
	want(t, b4.do("POST", "/api/auth/recovery", map[string]string{"code": "XYZ0123456"}), 200, "")
	if n, _ := e.a.Store.CountRecoveryCodes(context.Background(), u.ID); n != 0 {
		t.Fatalf("codes left = %d", n)
	}
}

// Why: a recovery code replaces the lost factor, not the password: it must lead to enrolling a new one.
func TestRecoveryCodeLandsInEnroll(t *testing.T) {
	e := newAuthEnv(t)
	u, b := e.recoverable(t, "203.0.113.1")
	// A half session is not a way to use the account.
	want(t, b.do("POST", "/api/me/pin", map[string]string{"pin": pin2}), 401, "second_factor_required")
	r := b.do("POST", "/api/auth/recovery", map[string]string{"code": "ABCDE-FGHIJ"})
	want(t, r, 200, "")
	if r.str("next") != "enroll" || r.m()["recoveryCodesLeft"] != float64(2) {
		t.Fatalf("recovery = %s", r.Body)
	}
	x, err := e.a.Store.GetSession(context.Background(), hashToken(b.cookie()))
	if err != nil || x.Stage != StageEnroll || !x.FactorProved {
		t.Fatalf("session = %+v %v", x, err)
	}
	if kinds(e.mails(t)) != "recovery-code-used" {
		t.Fatalf("mails = %q", kinds(e.mails(t)))
	}
	// Full-only routes stay closed; the enrol routes are open without a step-up.
	want(t, b.do("POST", "/api/stepup/start", map[string]string{"action": "add-factor", "factor": "pin"}), 403, "enrollment_required")
	want(t, b.do("POST", "/api/me/recovery-codes", struct{}{}), 403, "enrollment_required")
	add := b.do("POST", "/api/me/pin", map[string]string{"pin": pin2})
	want(t, add, 200, "")
	if add.str("stage") != "full" {
		t.Fatalf("add = %s", add.Body)
	}
	if _, has := add.m()["recoveryCodes"]; has {
		t.Fatal("codes shown for a second factor")
	}
	want(t, b.do("GET", "/api/me", nil), 200, "")
	got := strings.Join(e.auditActions(t, u), " ")
	if !strings.Contains(got, "recovery:success") {
		t.Fatalf("audit = %s", got)
	}
}

// Why: a wrong recovery code is a password-style guess at a second factor and must lock like one.
func TestWrongRecoveryCodesLockTheAccount(t *testing.T) {
	e := newAuthEnv(t)
	_, b := e.recoverable(t, "203.0.113.1")
	for i := 0; i < 4; i++ {
		want(t, b.do("POST", "/api/auth/recovery", map[string]string{"code": "AAAAA-AAAAA"}), 401, "invalid_credentials")
	}
	if len(e.mails(t)) != 0 {
		t.Fatal("mail before the lock")
	}
	want(t, b.do("POST", "/api/auth/recovery", map[string]string{"code": "AAAAA-AAAAA"}), 401, "invalid_credentials")
	if kinds(e.mails(t)) != "account-locked" {
		t.Fatalf("mails = %q", kinds(e.mails(t)))
	}
	if b.cookie() != "" {
		t.Fatal("the half session survived the lock")
	}
	want(t, e.browser(t, "203.0.113.9").login("a@x.com", goodPW), 401, "invalid_credentials")
}

// Why: a password reset in another tab, or a suspension, must end a signed-in session on its next call.
func TestSessionRefusedAfterEpochBump(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")
	want(t, b.do("GET", "/api/me", nil), 200, "")
	if err := e.a.Store.EndSessions(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	want(t, b.do("GET", "/api/me", nil), 401, "")
	want(t, b.do("POST", "/api/stepup/start", map[string]string{"action": "add-factor", "factor": "pin"}), 401, "")

	b2 := e.fullSession(t, e.reload(t, u), "203.0.113.1")
	want(t, b2.do("GET", "/api/me", nil), 200, "")
	if _, err := e.a.Store.DB.UpdateItem(context.Background(), suspendInput(e.a.Store, u.ID, e.a.now())); err != nil {
		t.Fatal(err)
	}
	want(t, b2.do("GET", "/api/me", nil), 401, "")
}

func (e *authEnv) reload(t *testing.T, u User) User {
	t.Helper()
	u, err := e.a.Store.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// passkey is a virtual authenticator registered with the Portal's relying party.
type passkey struct {
	rp    virtualwebauthn.RelyingParty
	authn virtualwebauthn.Authenticator
	cred  virtualwebauthn.Credential
}

func newPasskey() *passkey {
	return &passkey{
		rp:    virtualwebauthn.RelyingParty{Name: "Portal", ID: rpID, Origin: origin},
		authn: virtualwebauthn.NewAuthenticator(), cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

// enroll runs passkey start and finish on the browser and returns the finish answer.
func (p *passkey) enroll(t *testing.T, b *browser, headers ...string) resp {
	t.Helper()
	st := b.do("POST", "/api/me/passkeys/start", struct{}{}, headers...)
	want(t, st, 200, "")
	pk, _ := json.Marshal(st.m()["publicKey"])
	ao, err := virtualwebauthn.ParseAttestationOptions(string(pk))
	if err != nil {
		t.Fatal(err)
	}
	cred := virtualwebauthn.CreateAttestationResponse(p.rp, p.authn, p.cred, *ao)
	r := b.do("POST", "/api/me/passkeys/finish", map[string]any{"registrationId": st.str("registrationId"), "credential": json.RawMessage(cred)})
	if r.Status == 200 {
		p.authn.AddCredential(p.cred)
	}
	return r
}

func (p *passkey) signIn(t *testing.T, b *browser) resp {
	t.Helper()
	st := b.do("POST", "/api/auth/second-factor/start", map[string]string{"factor": "webauthn"})
	want(t, st, 200, "")
	pk, _ := json.Marshal(st.m()["publicKey"])
	ao, err := virtualwebauthn.ParseAssertionOptions(string(pk))
	if err != nil {
		t.Fatal(err)
	}
	p.cred.Counter++
	a := virtualwebauthn.CreateAssertionResponse(p.rp, p.authn, p.cred, *ao)
	return b.do("POST", "/api/auth/second-factor/verify", map[string]any{"challengeId": st.str("challengeId"), "credential": json.RawMessage(a)})
}

// Why: a passkey can be the first factor and carries the same recovery codes; a person must never be left without a factor.
func TestLastFactorCannotBeRemoved(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	pk := newPasskey()
	b := e.browser(t, "203.0.113.1")
	want(t, b.login("a@x.com", goodPW), 200, "")
	fin := pk.enroll(t, b)
	want(t, fin, 200, "")
	credID := fin.str("credentialId")
	if len(codesOf(t, fin)) != 10 || fin.str("stage") != "full" {
		t.Fatalf("finish = %s", fin.Body)
	}
	// Sign in again with the passkey, and try to remove it: the last factor.
	b2 := e.browser(t, "203.0.113.1")
	want(t, b2.login("a@x.com", goodPW), 200, "")
	r := pk.signIn(t, b2)
	want(t, r, 200, "")
	want(t, b2.do("DELETE", "/api/me/passkeys/"+credID, nil), 403, "step_up_required")
	want(t, b2.do("DELETE", "/api/me/passkeys/"+credID, nil, "x-step-up-token", b2.stepUpPasskey("remove-factor", pk)), 409, "last_factor")
	want(t, b2.do("DELETE", "/api/me/pin", nil, "x-step-up-token", b2.stepUpPasskey("remove-factor", pk)), 409, "factor_not_enrolled")
	// With a PIN added, the passkey can go, and then the PIN is the last factor.
	b2.pin = pin1
	add := b2.do("POST", "/api/me/pin", map[string]string{"pin": pin1}, "x-step-up-token", b2.stepUpPasskey("add-factor", pk))
	want(t, add, 200, "")
	want(t, b2.do("DELETE", "/api/me/passkeys/"+credID, nil, "x-step-up-token", b2.stepUpPasskey("remove-factor", pk)), 200, "")
	want(t, b2.do("DELETE", "/api/me/pin", nil, "x-step-up-token", b2.stepUp("remove-factor", "")), 409, "last_factor")
	if f, _ := e.ag.Factors(context.Background(), u.ID); !f.PIN || len(f.WebAuthn) != 0 {
		t.Fatalf("factors = %+v", f)
	}
	if got := kinds(e.mails(t)); got != "factor-added,factor-removed" {
		t.Fatalf("mails = %q", got)
	}
}

// stepUpPasskey answers a step-up with a passkey the account holds.
func (b *browser) stepUpPasskey(action string, pk *passkey) string {
	b.t.Helper()
	st := b.do("POST", "/api/stepup/start", map[string]string{"action": action, "factor": "webauthn"})
	want(b.t, st, 200, "")
	pj, _ := json.Marshal(st.m()["publicKey"])
	ao, _ := virtualwebauthn.ParseAssertionOptions(string(pj))
	pk.cred.Counter++
	a := virtualwebauthn.CreateAssertionResponse(pk.rp, pk.authn, pk.cred, *ao)
	v := b.do("POST", "/api/stepup/verify", map[string]any{"challengeId": st.str("challengeId"), "credential": json.RawMessage(a)})
	want(b.t, v, 200, "")
	return v.str("stepUpToken")
}

// Why: a token approved for one client must not authorise the same action on another, or on someone else's account.
func TestStepUpBoundToTarget(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")
	clientA, clientB := id.New(), id.New()
	tokA := b.stepUp("rotate-key", clientA)

	x, _ := e.a.Store.GetSession(context.Background(), hashToken(b.cookie()))
	check := func(tok, action, target string) error {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.Header.Set(stepUpHeader, tok)
		_, err := e.a.checkStepUp(r, x, action, target)
		return err
	}
	if err := check(tokA, "rotate-key", clientB); err != errStepUp {
		t.Fatalf("token for A on B: %v", err)
	}
	if err := check(tokA, "disable-client", clientA); err != errStepUp {
		t.Fatalf("rotate-key token for disable-client: %v", err)
	}
	if err := check(tokA, "rotate-key", clientA); err != nil {
		t.Fatalf("token for A on A: %v", err)
	}
	// Shape of the target is checked before AuthGate is called.
	for _, c := range []struct {
		action, target, code string
	}{
		{"rotate-key", "", "invalid_target"}, {"rotate-key", "not-an-id", "invalid_target"},
		{"purge-subject", clientA, "invalid_target"}, {"add-factor", id.New(), "invalid_target"},
		{"drop-database", "", "invalid_action"},
	} {
		r := b.do("POST", "/api/stepup/start", map[string]string{"action": c.action, "factor": "pin", "target": c.target})
		want(t, r, 400, c.code)
	}
	want(t, b.do("POST", "/api/stepup/start", map[string]string{"action": "purge-subject", "factor": "pin", "target": clientA + "/end-user-1"}), 200, "")
	// An account action is bound to the session's own user, whether named or not.
	own := b.stepUp("add-factor", u.ID)
	if err := check(own, "add-factor", ""); err != nil {
		t.Fatalf("account token: %v", err)
	}
}

// Why: a factor reset past its wait must be applied at the next sign-in with the right password, without any factor.
func TestDueFactorResetAppliedAtLogin(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	pk := newPasskey()
	b := e.fullSession(t, u, "203.0.113.1")
	want(t, pk.enroll(t, b, "x-step-up-token", b.stepUp("add-factor", "")), 200, "")
	if err := e.a.Store.ReplaceRecoveryCodes(context.Background(), u.ID, []string{"ABCDE-FGHIJ"}); err != nil {
		t.Fatal(err)
	}
	e.putFactorReset(t, u, e.a.now().Add(-time.Hour))

	b2 := e.browser(t, "203.0.113.1")
	r := b2.login("a@x.com", goodPW)
	want(t, r, 200, "")
	if r.str("next") != "enroll" {
		t.Fatalf("login = %s", r.Body)
	}
	if f, _ := e.ag.Factors(context.Background(), u.ID); f.PIN || len(f.WebAuthn) != 0 {
		t.Fatalf("factors not purged: %+v", f)
	}
	if fr, err := e.a.Store.GetFactorReset(context.Background(), u.ID); fr != nil || err != nil {
		t.Fatalf("factor reset = %+v %v", fr, err)
	}
	if n, _ := e.a.Store.CountRecoveryCodes(context.Background(), u.ID); n != 0 {
		t.Fatalf("recovery codes left = %d", n)
	}
	// The old full session ended with the purge; the new one enrols a first factor and gets new codes.
	want(t, b.do("GET", "/api/me", nil), 401, "")
	add := b2.do("POST", "/api/me/pin", map[string]string{"pin": pin2})
	want(t, add, 200, "")
	if len(codesOf(t, add)) != 10 {
		t.Fatalf("add = %s", add.Body)
	}
}

func (e *authEnv) putFactorReset(t *testing.T, u User, due time.Time) {
	t.Helper()
	it := factorResetKey(u.ID)
	it["requestedAt"], it["dueAt"] = s(ts(due.Add(-24*time.Hour))), s(ts(due))
	if _, err := e.a.Store.DB.PutItem(context.Background(), &dynamodb.PutItemInput{TableName: aws.String(e.a.Store.Table), Item: it}); err != nil {
		t.Fatal(err)
	}
}

// Why: a person who still holds a factor and signs in during the wait has shown the reset was not wanted.
func TestFactorResetCancelledBySecondFactor(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	e.putFactorReset(t, u, e.a.now().Add(time.Hour))
	b := e.browser(t, "203.0.113.1")
	r := b.login("a@x.com", goodPW)
	want(t, r, 200, "")
	if r.str("next") != "second-factor" {
		t.Fatalf("login = %s", r.Body)
	}
	if fr, _ := e.a.Store.GetFactorReset(context.Background(), u.ID); fr == nil {
		t.Fatal("the reset went away before the factor was proved")
	}
	want(t, b.signInWithPIN(pin1), 200, "")
	if fr, _ := e.a.Store.GetFactorReset(context.Background(), u.ID); fr != nil {
		t.Fatal("the reset survived a verified factor")
	}
	if f, _ := e.ag.Factors(context.Background(), u.ID); !f.PIN {
		t.Fatal("factor lost")
	}
	if len(e.mails(t)) != 0 {
		t.Fatalf("mails = %q", kinds(e.mails(t)))
	}
}

// Why: sign-out must end the session on the server, not only drop the cookie.
func TestLogoutEndsSession(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")
	old := b.cookie()
	want(t, b.do("POST", "/api/auth/logout", struct{}{}), 204, "")
	if b.cookie() != "" {
		t.Fatal("cookie kept")
	}
	if _, err := e.a.Store.GetSession(context.Background(), hashToken(old)); err == nil {
		t.Fatal("session item kept")
	}
	if got := strings.Join(e.auditActions(t, u), " "); !strings.Contains(got, "logout:success") || !strings.Contains(got, "second-factor-verify:success") {
		t.Fatalf("audit = %s", got)
	}
}

// Why: a right password must not reset what wrong recovery codes count, or whoever holds the password
// can guess codes four at a time forever.
func TestRightPasswordDoesNotResetRecoveryFailures(t *testing.T) {
	e := newAuthEnv(t)
	u, b := e.recoverable(t, "203.0.113.1")
	wrong := func(b *browser) resp {
		return b.do("POST", "/api/auth/recovery", map[string]string{"code": "AAAAA-AAAAA"})
	}
	for i := 0; i < 3; i++ {
		want(t, wrong(b), 401, "invalid_credentials")
	}
	b2 := e.browser(t, "203.0.113.1")
	want(t, b2.login("a@x.com", goodPW), 200, "") // the password is right again
	if cur, _ := e.a.Store.GetUser(context.Background(), u.ID); cur.FailedCount != 3 {
		t.Fatalf("failedCount after a password-only sign-in = %d, want 3", cur.FailedCount)
	}
	want(t, wrong(b2), 401, "invalid_credentials")
	want(t, wrong(b2), 401, "invalid_credentials")
	if kinds(e.mails(t)) != "account-locked" {
		t.Fatalf("mails = %q", kinds(e.mails(t)))
	}
	want(t, e.browser(t, "203.0.113.9").login("a@x.com", goodPW), 401, "invalid_credentials")
}

// Why: if the recovery codes cannot be stored after the first factor went in, the person is still enrolled
// and told honestly that no codes are left.
func TestFirstFactorCodesStoredOnlyAfterTheWrite(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	b := e.browser(t, "203.0.113.1")
	want(t, b.login("a@x.com", goodPW), 200, "")
	// A PIN the policy refuses: the write fails and no codes may be left behind.
	want(t, b.do("POST", "/api/me/pin", map[string]string{"pin": "123456"}), 400, "")
	if n, _ := e.a.Store.CountRecoveryCodes(context.Background(), u.ID); n != 0 {
		t.Fatalf("codes after a failed write = %d", n)
	}
	want(t, b.do("POST", "/api/me/pin", map[string]string{"pin": pin1}), 200, "")
	if n, _ := e.a.Store.CountRecoveryCodes(context.Background(), u.ID); n != 10 {
		t.Fatalf("codes after the write = %d", n)
	}
}

// Why: anyone can type an owner's address; wrong passwords must not write into the owner's audit log, or a stranger
// could pad it until the real entries scroll away. The right password (even while locked) is the owner, and is logged.
func TestLoginFailuresDoNotPadTheOwnersAudit(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	for i := 0; i < 5; i++ {
		want(t, e.browser(t, "203.0.113.1").login("a@x.com", "wrong-password-here"), 401, "invalid_credentials")
	}
	if acts := e.auditActions(t, u); len(acts) != 0 {
		t.Fatalf("a stranger's failures are in the owner's log: %v", acts)
	}
	want(t, e.browser(t, "203.0.113.2").login("a@x.com", goodPW), 401, "invalid_credentials") // locked
	if acts := e.auditActions(t, u); len(acts) != 1 || acts[0] != "login:failed:invalid_credentials" {
		t.Fatalf("audit = %v", acts)
	}
}

// Why: a session that proved a factor (a second factor or a recovery code) and adds one is the person, not someone
// who lost them; a reset left waiting would purge the factor just added at the next sign-in after the wait.
func TestFactorAddedByProvedSessionCancelsPendingReset(t *testing.T) {
	e := newAuthEnv(t)
	u, b := e.recoverable(t, "203.0.113.1")
	e.putFactorReset(t, u, e.a.now().Add(time.Hour))
	want(t, b.do("POST", "/api/auth/recovery", map[string]string{"code": "ABCDE-FGHIJ"}), 200, "")
	if fr, _ := e.a.Store.GetFactorReset(context.Background(), u.ID); fr == nil {
		t.Fatal("a recovery code alone cancelled the reset") // only the new factor does
	}
	want(t, b.do("POST", "/api/me/pin", map[string]string{"pin": pin2}), 200, "")
	if fr, _ := e.a.Store.GetFactorReset(context.Background(), u.ID); fr != nil {
		t.Fatalf("reset still pending after a factor was added by a recovery session: %+v", fr)
	}

	// The same from a full session, which proved its factor at sign-in.
	b.pin = pin2
	e.putFactorReset(t, u, e.a.now().Add(time.Hour))
	want(t, b.do("POST", "/api/me/pin", map[string]string{"pin": pin1}, stepHeader, b.stepUp("add-factor", "")), 200, "")
	if fr, _ := e.a.Store.GetFactorReset(context.Background(), u.ID); fr != nil {
		t.Fatalf("reset still pending after a factor was added by a full session: %+v", fr)
	}
}

// Why: the sign-in steps after the password are separate buckets per IP, so a burst of logins from a shared
// address does not stop the second factor of the people behind it, and code guessing has its own brake.
func TestSecondFactorAndRecoveryHaveTheirOwnIPBuckets(t *testing.T) {
	e := newAuthEnv(t)
	const ip = "203.0.113.1"
	_, b := e.recoverable(t, ip)
	raw := func(path string, body any) int {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("CloudFront-Viewer-Address", ip+":4000")
		req.AddCookie(&http.Cookie{Name: cookieName, Value: b.cookie(), Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req) // no clock advance: the buckets do not refill between calls
		return rec.Code
	}
	drain := func(scope string) {
		for range 10 {
			e.a.Limiter.Allow(context.Background(), scope+"#"+ip, loginIPLimit)
		}
	}
	drain("login-ip")
	if c := raw("/api/auth/second-factor/start", map[string]string{"factor": "pin"}); c != 200 {
		t.Fatalf("second factor after the login bucket ran dry = %d", c)
	}
	if c := raw("/api/auth/recovery", map[string]string{"code": "00000-00000"}); c == 429 {
		t.Fatal("recovery shares the login bucket")
	}
	drain("factor-ip")
	if c := raw("/api/auth/second-factor/start", map[string]string{"factor": "pin"}); c != 429 {
		t.Fatalf("second factor with its bucket dry = %d", c)
	}
	if c := raw("/api/auth/second-factor/verify", map[string]string{"challengeId": "x", "pin": pin1}); c != 429 {
		t.Fatalf("second-factor verify with the factor bucket dry = %d", c)
	}
	if c := raw("/api/auth/recovery", map[string]string{"code": "00000-00000"}); c == 429 {
		t.Fatal("recovery shares the factor bucket")
	}
	drain("recovery-ip")
	if c := raw("/api/auth/recovery", map[string]string{"code": "00000-00000"}); c != 429 {
		t.Fatalf("recovery with its bucket dry = %d", c)
	}
}
