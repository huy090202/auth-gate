package console_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/authgate"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
)

var adminActions = []struct{ action, method, suffix string }{
	{"admin-reset-password", "POST", "/reset-password"},
	{"admin-clear-factors", "POST", "/clear-factors"},
	{"admin-disable", "POST", "/disable"},
	{"admin-enable", "POST", "/enable"},
	{"admin-role", "POST", "/role"},
}

// adminTargetOf is the step-up target of an /admins action on name: admin-role is bound to the role it sets too.
func adminTargetOf(action, name string) string {
	if action == "admin-role" {
		return name + "/admin"
	}
	return name
}

func actionBody(suffix string) any {
	if suffix == "/role" {
		return map[string]string{"role": "admin"}
	}
	return struct{}{}
}

func getAdmin(t *testing.T, h *consoletest.Harness, name string) console.Admin {
	t.Helper()
	a, err := h.Store().GetAdmin(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func factorsOf(t *testing.T, h *consoletest.Harness, a console.Admin) authgate.Factors {
	t.Helper()
	f, err := h.AuthGate.Factors(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// Why: only masters manage accounts, and a token or a role that has gone stale must not open the door.
func TestRoleChecksOnAdmins(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	h.CreateAdmin("bob", pw, console.RoleAdmin, false)
	before := getAdmin(t, h, "bob")

	want(t, b.Do("GET", "/api/admins", nil), 403, "forbidden")
	want(t, b.Do("POST", "/api/admins", map[string]string{"username": "newbie", "role": "admin"}), 403, "forbidden")
	for _, c := range adminActions {
		want(t, b.Do(c.method, "/api/admins/bob"+c.suffix, actionBody(c.suffix)), 403, "forbidden")
	}
	if got := getAdmin(t, h, "bob"); !reflect.DeepEqual(before, got) {
		t.Fatal("bob changed")
	}
	if _, err := h.Store().GetAdmin(context.Background(), "newbie"); err == nil {
		t.Fatal("an admin created newbie")
	}

	// A master whose token outlives a demotion is refused at the action.
	m, _ := fullAdmin(t, h, "mia", console.RoleMaster)
	h.CreateAdmin("max", pw, console.RoleMaster, false)
	tok := m.StepUp("admin-disable", "bob")
	if err := h.Store().SetRole(context.Background(), nil, "mia", console.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	want(t, m.Do("POST", "/api/admins/bob/disable", struct{}{}, tokHeader, tok), 403, "forbidden")
	if getAdmin(t, h, "bob").DisabledAt != nil {
		t.Fatal("bob was disabled by a demoted master")
	}
}

// Why: the temporary password is the only secret of this flow and must leave the server once.
func TestCreateAdminShowsTempPasswordOnce(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleMaster)
	body := map[string]string{"username": "Newbie", "role": "admin"}

	want(t, b.Do("POST", "/api/admins", body), 403, "step_up_required")
	r := b.Do("POST", "/api/admins", body, tokHeader, b.StepUp("admin-create", "newbie/admin"))
	want(t, r, 201, "")
	temp := r.Str("temporaryPassword")
	raw, err := base64.RawURLEncoding.DecodeString(temp)
	if err != nil || len(raw) != 20 {
		t.Fatalf("temporary password %q is not 20 bytes of base64url", temp)
	}
	if r.Str("username") != "newbie" || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("username %q, cache-control %q", r.Str("username"), r.Header.Get("Cache-Control"))
	}
	a := getAdmin(t, h, "newbie")
	if !a.MustChangePassword || a.Role != console.RoleAdmin || a.PasswordHash == temp || !console.VerifyPassword(temp, a.PasswordHash) {
		t.Fatalf("stored admin %+v", a)
	}

	list := b.Do("GET", "/api/admins", nil)
	want(t, list, 200, "")
	audit := b.Do("GET", "/api/audit", nil)
	for _, resp := range []consoletest.Response{list, audit} {
		s := string(resp.Body)
		if strings.Contains(s, temp) || strings.Contains(s, a.PasswordHash) || strings.Contains(strings.ToLower(s), "passwordhash") || strings.Contains(s, "argon2") {
			t.Fatalf("a later response carries a secret: %s", s)
		}
	}
	if !strings.Contains(string(list.Body), `"newbie"`) || !strings.Contains(string(audit.Body), "admin-create") {
		t.Fatalf("list %s audit %s", list.Body, audit.Body)
	}

	want(t, b.Do("POST", "/api/admins", body, tokHeader, b.StepUp("admin-create", "newbie/admin")), 409, "username_taken")
	want(t, b.Do("POST", "/api/admins", map[string]string{"username": "a b", "role": "admin"}, tokHeader, b.StepUp("admin-create", "carol/admin")), 400, "invalid_username")
	want(t, b.Do("POST", "/api/admins", map[string]string{"username": "carol", "role": "boss"}, tokHeader, b.StepUp("admin-create", "carol/admin")), 400, "invalid_role")

	n := h.NewBrowser()
	l := n.Login("newbie", temp)
	want(t, l, 200, "")
	if l.Str("next") != "enroll" {
		t.Fatalf("login = %s", l.Body)
	}
}

// Why: a reset hands out a password the master knows, so the old sessions, the lock and the old password must all be dead.
func TestResetEndsSessionsAndForcesChange(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleMaster)
	bb, bob := fullAdmin(t, h, "bob", console.RoleAdmin)
	ctx := context.Background()
	for range 5 {
		if err := h.Store().RecordLoginFailure(ctx, "bob", h.Clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if getAdmin(t, h, "bob").LockedUntil == nil {
		t.Fatal("bob is not locked")
	}

	r := b.Do("POST", "/api/admins/bob/reset-password", struct{}{}, tokHeader, b.StepUp("admin-reset-password", "bob"))
	want(t, r, 200, "")
	temp := r.Str("temporaryPassword")
	if temp == "" || r.Str("username") != "bob" || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("reset = %s", r.Body)
	}
	want(t, bb.Do("GET", "/api/me", nil), 401, "")
	a := getAdmin(t, h, "bob")
	if !a.MustChangePassword || a.LockedUntil != nil || a.FailedCount != 0 || a.SessionEpoch != bob.SessionEpoch+1 || !console.VerifyPassword(temp, a.PasswordHash) {
		t.Fatalf("bob after reset %+v", a)
	}
	n := h.NewBrowser()
	want(t, n.Login("bob", pw), 401, "invalid_credentials")
	l := n.Login("bob", temp)
	want(t, l, 200, "")
	want(t, n.SignInWithPIN(pin1), 200, "")
	if stageOf(t, n) != "enroll" {
		t.Fatal("a reset admin must change the password before full")
	}
}

// Why: clear-factors is only safe in its order (bump, purge, bump); the session must die and the admin must be able to enroll again.
func TestClearFactorsEndsSessionsAndReEnrolls(t *testing.T) {
	h := consoletest.New(t)
	var epochAtPurge = -1
	rec := *h.AuthGate
	base := rec.HTTP.Transport
	rec.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodDelete && strings.Contains(req.URL.Path, "/subjects/") {
			epochAtPurge = getAdmin(t, h, "bob").SessionEpoch
		}
		return base.RoundTrip(req)
	})}
	d := h.Deps
	d.AuthGate = &rec
	handler := console.New(d)

	b := h.BrowserFor(handler)
	h.CreateAdmin("ada", pw, console.RoleMaster, false)
	withPIN(t, h, getAdmin(t, h, "ada"), pin1)
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.SignInWithPIN(pin1), 200, "")
	bob := h.CreateAdmin("bob", pw, console.RoleAdmin, false)
	withPIN(t, h, bob, pin1)
	bb := h.BrowserFor(handler)
	want(t, bb.Login("bob", pw), 200, "")
	want(t, bb.SignInWithPIN(pin1), 200, "")

	want(t, b.Do("POST", "/api/admins/bob/clear-factors", struct{}{}, tokHeader, b.StepUp("admin-clear-factors", "bob")), 200, "")
	e0 := bob.SessionEpoch
	if epochAtPurge != e0+1 {
		t.Fatalf("epoch at the purge = %d, want %d (bump before purge)", epochAtPurge, e0+1)
	}
	if got := getAdmin(t, h, "bob").SessionEpoch; got != e0+2 {
		t.Fatalf("final epoch = %d, want %d (bump after purge)", got, e0+2)
	}
	want(t, bb.Do("GET", "/api/me", nil), 401, "")
	if f := factorsOf(t, h, bob); f.PIN || len(f.WebAuthn) != 0 {
		t.Fatalf("factors remain: %+v", f)
	}
	n := h.BrowserFor(handler)
	l := n.Login("bob", pw)
	want(t, l, 200, "")
	if l.Str("next") != "enroll" {
		t.Fatalf("login = %s", l.Body)
	}
	want(t, n.EnrollPIN(pin2), 200, "")
	if stageOf(t, n) != "full" {
		t.Fatal("bob did not reach full after enrolling")
	}
}

// Why: disabling must cut a live session at once, and enabling must give the account back.
func TestDisableEndsSessionsEnableRestores(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleMaster)
	bb, _ := fullAdmin(t, h, "bob", console.RoleAdmin)

	want(t, b.Do("POST", "/api/admins/bob/disable", struct{}{}, tokHeader, b.StepUp("admin-disable", "bob")), 200, "")
	want(t, bb.Do("GET", "/api/me", nil), 401, "")
	if getAdmin(t, h, "bob").DisabledAt == nil {
		t.Fatal("bob is not disabled")
	}
	want(t, h.NewBrowser().Login("bob", pw), 401, "invalid_credentials")

	want(t, b.Do("POST", "/api/admins/bob/enable", struct{}{}, tokHeader, b.StepUp("admin-enable", "bob")), 200, "")
	want(t, h.NewBrowser().Login("bob", pw), 200, "")

	want(t, b.Do("POST", "/api/admins/bob/role", map[string]string{"role": "master"}, tokHeader, b.StepUp("admin-role", "bob/master")), 200, "")
	if getAdmin(t, h, "bob").Role != console.RoleMaster {
		t.Fatal("bob is not a master")
	}
	want(t, b.Do("POST", "/api/admins/bob/role", map[string]string{"role": "root"}, tokHeader, b.StepUp("admin-role", "bob/admin")), 400, "invalid_role")
}

// Why: acting on yourself would bypass the last-factor and last-master rules.
func TestCannotActOnSelf(t *testing.T) {
	h := consoletest.New(t)
	b, ada := fullAdmin(t, h, "ada", console.RoleMaster)
	h.CreateAdmin("bob", pw, console.RoleMaster, false)
	before := getAdmin(t, h, "ada")

	for _, c := range adminActions {
		r := b.Do(c.method, "/api/admins/ADA"+c.suffix, actionBody(c.suffix), tokHeader, b.StepUp(c.action, adminTargetOf(c.action, "ada")))
		want(t, r, 409, "cannot_modify_self")
	}
	if got := getAdmin(t, h, "ada"); !reflect.DeepEqual(before, got) {
		t.Fatalf("ada changed: %+v", got)
	}
	if f := factorsOf(t, h, ada); !f.PIN {
		t.Fatal("ada lost her PIN")
	}
	// A refused guard does not spend the token. It is bound to ada, so it cannot be tried on bob any more:
	// the proof is that its jti is still unused.
	tok := b.StepUp("admin-disable", "ada")
	want(t, b.Do("POST", "/api/admins/ada/disable", struct{}{}, tokHeader, tok), 409, "cannot_modify_self")
	requireUnspent(t, h, tok)
}

// Why: the last active master is the only way back in; CLI and web must both refuse to remove it.
func TestLastMasterCannotBeRemoved(t *testing.T) {
	h := consoletest.New(t)
	ctx := context.Background()
	h.CreateAdmin("ada", pw, console.RoleMaster, false)
	deps := cliDeps(h)
	var out strings.Builder
	if err := console.RunAdmin(ctx, []string{"disable", "--username", "ada"}, strings.NewReader(""), &out, deps); err == nil {
		t.Fatal("the only master was disabled")
	}
	if getAdmin(t, h, "ada").DisabledAt != nil {
		t.Fatal("ada is disabled")
	}
	h.CreateAdmin("bob", pw, console.RoleMaster, false)
	if err := console.RunAdmin(ctx, []string{"disable", "--username", "bob"}, strings.NewReader(""), &out, deps); err != nil {
		t.Fatal(err)
	}
	if err := console.RunAdmin(ctx, []string{"disable", "--username", "ada"}, strings.NewReader(""), &out, deps); err == nil {
		t.Fatal("the last active master was disabled")
	}

	// Web: the actor is demoted between the read and the write.
	h2 := consoletest.New(t)
	b2h := func() {}
	d := h2.Deps
	handler := console.NewWithBeforeAdminWrite(d, func() { b2h() })
	b := h2.BrowserFor(handler)
	a := h2.CreateAdmin("ada", pw, console.RoleMaster, false)
	withPIN(t, h2, a, pin1)
	h2.CreateAdmin("bob", pw, console.RoleMaster, false)
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.SignInWithPIN(pin1), 200, "")
	b2h = func() {
		if err := h2.Store().SetRole(ctx, nil, "ada", console.RoleAdmin); err != nil {
			t.Error(err)
		}
	}
	want(t, b.Do("POST", "/api/admins/bob/disable", struct{}{}, tokHeader, b.StepUp("admin-disable", "bob")), 409, "last_master")
	if getAdmin(t, h2, "bob").DisabledAt != nil {
		t.Fatal("bob was disabled although ada was no master any more")
	}
}

// Why: a transient transaction clash is not the last-master guard and must not be reported as it.
func TestTransientClashIsNotLastMaster(t *testing.T) {
	st, code, retry := console.AdminWriteError(console.ErrBusy)
	if st != 503 || code != "busy" || retry < 1 {
		t.Fatalf("busy = %d %s %d", st, code, retry)
	}
	if st, code, _ := console.AdminWriteError(console.ErrConflict); st != 409 || code != "last_master" {
		t.Fatalf("conflict = %d %s", st, code)
	}
}

// Why: every account change must ask for a fresh, matching token and leave the account alone without one.
func TestOtherAdminActionsNeedStepUp(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleMaster)
	bob := h.CreateAdmin("bob", pw, console.RoleAdmin, false)
	withPIN(t, h, bob, pin1)
	before := getAdmin(t, h, "bob")

	for _, c := range adminActions {
		path := "/api/admins/bob" + c.suffix
		want(t, b.Do(c.method, path, actionBody(c.suffix)), 403, "step_up_required")
		other := "admin-disable"
		if c.action == other {
			other = "admin-enable"
		}
		want(t, b.Do(c.method, path, actionBody(c.suffix), tokHeader, b.StepUp(other, "bob")), 403, "step_up_required")
		if got := getAdmin(t, h, "bob"); !reflect.DeepEqual(before, got) {
			t.Fatalf("%s: bob changed without a valid token", c.action)
		}
	}
	if !factorsOf(t, h, bob).PIN {
		t.Fatal("bob lost his PIN")
	}
	tok := b.StepUp("admin-disable", "bob")
	want(t, b.Do("POST", "/api/admins/bob/disable", struct{}{}, tokHeader, tok), 200, "")
	want(t, b.Do("POST", "/api/admins/bob/disable", struct{}{}, tokHeader, tok), 403, "step_up_required")
}
