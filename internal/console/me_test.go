package console_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// check is want for code that runs on a server goroutine: it reports with
// t.Errorf, because t.Fatalf may only be called from the test goroutine.
func check(t *testing.T, r consoletest.Response, status int, code string) {
	t.Helper()
	if r.Status != status || (code != "" && r.Str("error") != code) {
		t.Errorf("got %d %s, want %d %s", r.Status, r.Body, status, code)
	}
}

// afterFactors runs a callback right after AuthGate has answered one factor
// listing, once armed: the moment between a handler's read and its write.
type afterFactors struct {
	base  http.RoundTripper
	mu    sync.Mutex
	armed func()
	early func() // runs before AuthGate is asked, once
}

func (t *afterFactors) arm(f func()) { t.mu.Lock(); t.armed = f; t.mu.Unlock() }

func (t *afterFactors) before(f func()) { t.mu.Lock(); t.early = f; t.mu.Unlock() }

func (t *afterFactors) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/api/factors/") {
		t.mu.Lock()
		e := t.early
		t.early = nil
		t.mu.Unlock()
		if e != nil {
			e()
		}
	}
	resp, err := t.base.RoundTrip(r)
	if err != nil || r.Method != http.MethodGet || !strings.Contains(r.URL.Path, "/api/factors/") {
		return resp, err
	}
	t.mu.Lock()
	f := t.armed
	t.armed = nil
	t.mu.Unlock()
	if f != nil {
		raw, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		f()
		resp.Body = io.NopCloser(bytes.NewReader(raw))
	}
	return resp, err
}

// raceRig: the owner uses the normal Console; the attacker's session goes through
// one whose AuthGate client can be held after a factor listing.
func raceRig(t *testing.T) (h *consoletest.Harness, a console.Admin, owner, attacker *consoletest.Browser, hook *afterFactors) {
	h = consoletest.New(t)
	a = h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	hook = &afterFactors{base: h.AuthGate.HTTP.Transport}
	deps := h.Deps
	deps.AuthGate = &authgate.Client{BaseURL: h.AuthGateURL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey,
		HTTP: &http.Client{Transport: hook}}
	owner, attacker = h.NewBrowser(), h.BrowserFor(console.New(deps))
	want(t, attacker.Login("ada", pw), 200, "")
	want(t, owner.Login("ada", pw), 200, "")
	return
}

// Why: while one session holds the first-factor lease and has read "no factor yet",
// another cannot add its own factor beside the first one's passkey.
func TestFirstPasskeyHoldsOffOtherSessions(t *testing.T) {
	h, a, owner, attacker, hook := raceRig(t)
	pk := h.NewPasskey()
	start := attacker.StartPasskey()
	want(t, start, 200, "")
	hook.arm(func() { check(t, owner.EnrollPIN(pin1), 409, "enrollment_in_progress") })
	want(t, attacker.FinishPasskey(start.Str("registrationId"), pk.Attest(h, start)), 200, "")
	f, err := h.AuthGate.Factors(context.Background(), a.ID)
	if err != nil || f.PIN || len(f.WebAuthn) != 1 {
		t.Fatalf("factors = %+v (%v): want only the first session's passkey", f, err)
	}
	// The refused session was not ended, but the winner's epoch bump made its epoch
	// stale: the 401 comes from the middleware, not from a factor read.
	want(t, owner.EnrollPIN(pin1), 401, "unauthorized")
}

// Why: the same with a PIN: a second session's passkey must not land beside it.
func TestFirstPINHoldsOffOtherSessions(t *testing.T) {
	h, a, owner, attacker, hook := raceRig(t)
	pk := h.NewPasskey()
	hook.arm(func() { check(t, owner.EnrollPasskey(pk), 409, "enrollment_in_progress") })
	want(t, attacker.EnrollPIN(pin2), 200, "")
	f, err := h.AuthGate.Factors(context.Background(), a.ID)
	if err != nil || !f.PIN || len(f.WebAuthn) != 0 {
		t.Fatalf("factors = %+v (%v): want only the first session's PIN", f, err)
	}
	want(t, owner.EnrollPIN(pin1), 401, "unauthorized")
}

// Why: an enrolling session that has not proved a factor must not be shown the passkey list,
// even when a factor appears between its session check and its factor read.
func TestUnprovedEnrollSessionSeesFactorFlagsOnly(t *testing.T) {
	h, _, owner, attacker, hook := raceRig(t)
	pk := h.NewPasskey()
	hook.before(func() { check(t, owner.EnrollPasskey(pk), 200, "") })
	f, _ := attacker.Do("GET", "/api/me", nil).Map()["factors"].(map[string]any)
	if f["webauthn"] != true || f["pin"] != false {
		t.Fatalf("factors = %v, want flags only", f)
	}
}

// gate sits between a Console and AuthGate. It counts the factor writes that get
// through and can hold the first PIN write open until the test lets it go.
type gate struct {
	base    http.RoundTripper
	writes  atomic.Int32
	entered chan struct{} // receives once when a held write arrives
	release chan struct{}
	hold    atomic.Bool
	once    sync.Once
}

func newGate(t *testing.T, base http.RoundTripper) *gate {
	g := &gate{base: base, entered: make(chan struct{}, 1), release: make(chan struct{})}
	return g
}

// let releases the held write (and any later one); it is safe to call twice.
func (g *gate) let() { g.once.Do(func() { close(g.release) }) }

func (g *gate) RoundTrip(r *http.Request) (*http.Response, error) {
	isPIN := r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/factors/pin")
	isFinish := r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/api/webauthn/registrations/")
	if isPIN || isFinish {
		g.writes.Add(1)
	}
	if isPIN && g.hold.CompareAndSwap(true, false) {
		g.entered <- struct{}{}
		select {
		case <-g.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	return g.base.RoundTrip(r)
}

// gated returns a Console whose AuthGate client goes through g.
func gated(h *consoletest.Harness, g *gate) *consoletest.Browser {
	deps := h.Deps
	deps.AuthGate = &authgate.Client{BaseURL: h.AuthGateURL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey,
		HTTP: &http.Client{Transport: g}}
	b := h.BrowserFor(console.New(deps))
	// Cleanups run last in, first out: register after the server so a held write
	// is let go before the server's Close waits for it.
	h.T.Cleanup(g.let)
	return b
}

// Why: a login during the owner's AuthGate write reads the new epoch and no factor;
// without the lease its write would land beside the owner's (a silent second factor
// or a replaced PIN while the owner got 200).
func TestFreshLoginDuringOwnersWriteCannotAddFactor(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	g := newGate(t, h.AuthGate.HTTP.Transport)
	g.hold.Store(true)
	owner := gated(h, g)
	want(t, owner.Login("ada", pw), 200, "")
	done := make(chan consoletest.Response, 1)
	go func() { done <- owner.EnrollPIN(pin1) }()
	<-g.entered // the owner has bumped the epoch and is inside the AuthGate write

	// Past the old 15 s lease: the read and the write each have up to 10 s, so a
	// write held this long can still land and the lease must still be held.
	h.Clock.Advance(16 * time.Second)
	other := gated(h, g)
	want(t, other.Login("ada", pw), 200, "")
	pk := h.NewPasskey()
	start := other.StartPasskey()
	want(t, start, 200, "")
	before := g.writes.Load()

	r := other.EnrollPIN(pin2)
	want(t, r, 409, "enrollment_in_progress")
	if ra, err := strconv.Atoi(r.Header.Get("Retry-After")); err != nil || ra < 1 {
		t.Fatalf("Retry-After = %q", r.Header.Get("Retry-After"))
	}
	want(t, other.FinishPasskey(start.Str("registrationId"), pk.Attest(h, start)), 409, "enrollment_in_progress")
	if g.writes.Load() != before {
		t.Fatal("the refused session reached AuthGate")
	}

	g.let()
	want(t, <-done, 200, "")
	f, err := h.AuthGate.Factors(context.Background(), a.ID)
	if err != nil || !f.PIN || len(f.WebAuthn) != 0 || !pinWorks(t, h, a, pin1) || pinWorks(t, h, a, pin2) {
		t.Fatalf("factors = %+v (%v): more than the owner's PIN", f, err)
	}
	// The refused session was not ended; its next try finds the owner's factor.
	want(t, other.EnrollPIN(pin2), 401, "unauthorized")
}

// Why: a holder that crashed, or was refused, must not lock the admin out of
// enrolling for the whole lease: a lease nobody released is waited out, not ignored.
func TestLeaseExpiresAfterACrash(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	now := h.Clock.Now()
	if _, err := h.Store().ClaimFirstFactorLease(context.Background(), "ada", "gone", now, now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	r := b.EnrollPIN(pin1)
	want(t, r, 409, "enrollment_in_progress")
	if ra, err := strconv.Atoi(r.Header.Get("Retry-After")); err != nil || ra < 1 || ra > 15 {
		t.Fatalf("Retry-After = %q", r.Header.Get("Retry-After"))
	}
	h.Clock.Advance(16 * time.Second)
	want(t, b.EnrollPIN(pin1), 200, "")
}

// Why: a claimer that waited out an expired, unreleased lease must read the factors
// only after it holds the lease, so it sees what the earlier holder wrote.
func TestLeaseExpiredClaimerSeesEarlierFactor(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	g := newGate(t, h.AuthGate.HTTP.Transport)
	late := gated(h, g)
	want(t, late.Login("ada", pw), 200, "")
	now := h.Clock.Now()
	if _, err := h.Store().ClaimFirstFactorLease(context.Background(), "ada", "slow", now, now.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	withPIN(t, h, a, pin1) // the holder's write landed, then it never released
	h.Clock.Advance(16 * time.Second)
	want(t, late.EnrollPIN(pin2), 401, "unauthorized")
	if g.writes.Load() != 0 || !pinWorks(t, h, a, pin1) || pinWorks(t, h, a, pin2) {
		t.Fatal("the late session wrote, or the earlier factor was replaced")
	}
}

// failFinish refuses a passkey registration finish with an AuthGate 400.
type failFinish struct{ base http.RoundTripper }

func (f failFinish) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/api/webauthn/registrations/") {
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"registration_failed"}`)), Request: r}, nil
	}
	return f.base.RoundTrip(r)
}

// Why: when AuthGate refuses the write, the lease must go with it, or the
// admin's own retry is locked out for the lease's length.
func TestFailedWriteReleasesLease(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.EnrollPIN("12"), 400, "")
	want(t, b.EnrollPIN(pin1), 200, "")

	h2 := consoletest.New(t)
	h2.CreateAdmin("bob", pw, console.RoleAdmin, true)
	deps := h2.Deps
	deps.AuthGate = &authgate.Client{BaseURL: h2.AuthGateURL, ClientKey: h2.ClientKey, AdminKey: consoletest.AdminKey,
		HTTP: &http.Client{Transport: failFinish{base: h2.AuthGate.HTTP.Transport}}}
	flaky := h2.BrowserFor(console.New(deps))
	want(t, flaky.Login("bob", pw), 200, "")
	pk := h2.NewPasskey()
	start := flaky.StartPasskey()
	want(t, start, 200, "")
	want(t, flaky.FinishPasskey(start.Str("registrationId"), pk.Attest(h2, start)), 400, "registration_failed")
	// The same browser, on the Console that reaches AuthGate again: a fresh start and finish.
	ok := h2.BrowserFor(console.New(h2.Deps))
	ok.SetCookie(flaky.CookieValue())
	want(t, ok.EnrollPasskey(pk), 200, "")
}

// failWrite fails a PIN write with no answer at all (status 0), as a client
// timeout does, or with the given 5xx: either way the request may have reached
// AuthGate and the write may land.
type failWrite struct {
	base   http.RoundTripper
	status int
}

func (f failWrite) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/api/factors/pin") {
		if f.status == 0 {
			return nil, errors.New("timeout")
		}
		return &http.Response{StatusCode: f.status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	}
	return f.base.RoundTrip(r)
}

// Why: a write that got no answer, or an answered 5xx, may still land later, so
// its lease must stay until it runs out; an AuthGate refusal
// (TestFailedWriteReleasesLease) releases.
func TestLeaseKeptWhenWriteGotNoAnswer(t *testing.T) {
	for name, status := range map[string]int{"no answer": 0, "answered 502": 502} {
		t.Run(name, func(t *testing.T) {
			h := consoletest.New(t)
			h.CreateAdmin("ada", pw, console.RoleAdmin, true)
			deps := h.Deps
			deps.AuthGate = &authgate.Client{BaseURL: h.AuthGateURL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey,
				HTTP: &http.Client{Transport: failWrite{base: h.AuthGate.HTTP.Transport, status: status}}}
			owner := h.BrowserFor(console.New(deps))
			other := h.NewBrowser()
			want(t, owner.Login("ada", pw), 200, "")
			want(t, owner.EnrollPIN(pin1), 502, "authgate_unavailable")
			want(t, other.Login("ada", pw), 200, "") // opened after the owner's epoch bump
			want(t, other.EnrollPIN(pin2), 409, "enrollment_in_progress")
			h.Clock.Advance(26 * time.Second)
			want(t, other.EnrollPIN(pin2), 200, "")
		})
	}
}

// Why: the lease holds only while a write lands before it runs out, and a write
// can land up to AuthGate's 10 s timeout after it is sent. One that could only be
// sent too late must not be sent at all, and then cannot land, so the lease goes.
func TestFirstFactorWriteNotSentPastItsWindow(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	g := newGate(t, h.AuthGate.HTTP.Transport)
	hook := &afterFactors{base: g}
	deps := h.Deps
	deps.AuthGate = &authgate.Client{BaseURL: h.AuthGateURL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey,
		HTTP: &http.Client{Transport: hook}}
	b := h.BrowserFor(console.New(deps))
	want(t, b.Login("ada", pw), 200, "")
	// Between the claim and the write, time passes the last moment the write may go out.
	hook.arm(func() { h.Clock.Advance(console.FirstFactorSendWindow + time.Second) })
	want(t, b.EnrollPIN(pin1), 502, "authgate_unavailable")
	if n := g.writes.Load(); n != 0 {
		t.Fatalf("%d write(s) sent past the window", n)
	}
	// Nothing was sent, so nothing can land: the lease is gone and a retry goes through.
	want(t, b.EnrollPIN(pin1), 200, "")
	if !pinWorks(t, h, a, pin1) {
		t.Fatal("the retry's PIN did not land")
	}
}

// Why: on the normal first run (password, then the first factor) one response
// both reissues and promotes the session. Two cookies of one name in one answer
// rely on every hop keeping their order; exactly one, the live one, must go out.
func TestFirstFactorAnswersOneSessionCookie(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2)), 200, "")
	r := b.EnrollPIN(pin1)
	want(t, r, 200, "")
	if r.Str("stage") != "full" {
		t.Fatalf("stage = %q, want full", r.Str("stage"))
	}
	var values []string
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		if c.Name == "__Host-console_session" {
			values = append(values, c.Value)
		}
	}
	if len(values) != 1 {
		t.Fatalf("%d session cookies in one answer, want 1", len(values))
	}
	fresh := h.NewBrowser()
	fresh.SetCookie(values[0])
	if st := stageOf(t, fresh); st != "full" {
		t.Fatalf("the cookie sent is at stage %q, want the live full session", st)
	}
}

// Why: a holder whose lease ran out must not remove the lease of the session that
// claimed after it, or a third session could slip in beside the second one's write.
func TestExpiredHoldersReleaseKeepsNewerLease(t *testing.T) {
	h := consoletest.New(t)
	h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	st, ctx := h.Store(), context.Background()
	t0 := h.Clock.Now()
	if _, err := st.ClaimFirstFactorLease(ctx, "ada", "first", t0, t0.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	t1 := t0.Add(16 * time.Second)
	if _, err := st.ClaimFirstFactorLease(ctx, "ada", "second", t1, t1.Add(15*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := st.ReleaseFirstFactorLease(ctx, "ada", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimFirstFactorLease(ctx, "ada", "third", t1, t1.Add(15*time.Second)); !errors.Is(err, console.ErrLeaseHeld) {
		t.Fatalf("claim after the stale release = %v, want ErrLeaseHeld", err)
	}
	if err := st.ReleaseFirstFactorLease(ctx, "ada", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimFirstFactorLease(ctx, "ada", "third", t1, t1.Add(15*time.Second)); err != nil {
		t.Fatalf("claim after the holder's own release = %v", err)
	}
}

// Why: a session that passed the middleware at the old epoch and then outlasts its
// lease must still lose to the session that claimed after it: only the epoch claim
// stops its write.
func TestOldEpochSessionLosesAfterItsLeaseRanOut(t *testing.T) {
	h, a, owner, attacker, hook := raceRig(t)
	hook.arm(func() {
		h.Clock.Advance(26 * time.Second) // the attacker's lease runs out while it is held here
		check(t, owner.EnrollPIN(pin1), 200, "")
	})
	want(t, attacker.EnrollPIN(pin2), 401, "unauthorized")
	if !pinWorks(t, h, a, pin1) || pinWorks(t, h, a, pin2) {
		t.Fatal("the late session's PIN replaced the owner's")
	}
}
