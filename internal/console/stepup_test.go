package console_test

import (
	"context"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/authgate"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
)

const tokHeader = "x-step-up-token"

// fullAdmin returns a browser signed in at stage full with a PIN.
func fullAdmin(t *testing.T, h *consoletest.Harness, name string, role console.Role) (*consoletest.Browser, console.Admin) {
	t.Helper()
	a := h.CreateAdmin(name, pw, role, false)
	withPIN(t, h, a, pin1)
	b := h.NewBrowser()
	want(t, b.Login(name, pw), 200, "")
	want(t, b.SignInWithPIN(pin1), 200, "")
	return b, a
}

// newClient creates a client through the Console and returns its id and key.
func newClient(t *testing.T, b *consoletest.Browser, name string) (id, key string) {
	t.Helper()
	r := b.Do("POST", "/api/clients", map[string]any{"name": name, "rpId": "app.test", "origins": []string{"https://app.test"}})
	want(t, r, 201, "")
	return r.Str("clientId"), r.Str("accessKey")
}

func clientOf(t *testing.T, h *consoletest.Harness, id string) client.Client {
	t.Helper()
	c, err := h.Clients.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Why: step-up must be asked on the security-sensitive list of the spec and on nothing else.
func TestStepUpRequiredExactlyOnTheList(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	id, _ := newClient(t, b, "app")
	base := "/api/clients/" + id

	asks := []struct {
		action, method, path string
		body                 any
	}{
		{"rotate-key", "POST", base + "/keys", struct{}{}},
		{"revoke-previous-keys", "DELETE", base + "/keys/previous", nil},
		{"update-client", "PATCH", base, map[string]any{"origins": []string{"https://app.test", "https://b.app.test"}}},
		{"update-client", "PATCH", base, map[string]any{"tokenTtlSec": 60}},
		{"disable-client", "POST", base + "/disable", struct{}{}},
		{"purge-subject", "DELETE", base + "/subjects/s1", nil},
	}
	for _, c := range asks {
		want(t, b.Do(c.method, c.path, c.body), 403, "step_up_required")
		tok := b.StepUp(c.action)
		r := b.Do(c.method, c.path, c.body, tokHeader, tok)
		if r.Status/100 != 2 {
			t.Fatalf("%s %s with a token: %d %s", c.method, c.path, r.Status, r.Body)
		}
	}
	// A client that was disabled can be enabled again without a token.
	want(t, b.Do("POST", base+"/enable", struct{}{}), 200, "")

	// Own credentials: add a passkey, remove it, change the password.
	pk := h.NewPasskey()
	want(t, b.StartPasskey(), 403, "step_up_required")
	st := b.Do("POST", "/api/me/passkeys/start", struct{}{}, tokHeader, b.StepUp("add-factor"))
	want(t, st, 200, "")
	fin := b.FinishPasskey(st.Str("registrationId"), pk.Attest(h, st))
	want(t, fin, 200, "")
	pk.Authn.AddCredential(pk.Credential)
	del := "/api/me/passkeys/" + fin.Str("credentialId")
	want(t, b.Do("DELETE", del, nil), 403, "step_up_required")
	want(t, b.Do("DELETE", del, nil, tokHeader, b.StepUp("remove-factor")), 200, "")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2)), 403, "step_up_required")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2), tokHeader, b.StepUp("change-password")), 200, "")

	// Everything else asks for nothing.
	want(t, b.Do("GET", "/api/clients", nil), 200, "")
	want(t, b.Do("GET", base, nil), 200, "")
	want(t, b.Do("GET", "/api/audit", nil), 200, "")
	want(t, b.Do("POST", "/api/clients", map[string]any{"name": "two", "rpId": "app.test", "origins": []string{"https://app.test"}}), 201, "")
	want(t, b.Do("PATCH", base, map[string]any{"name": "renamed"}), 200, "")
	want(t, b.Do("POST", base+"/enable", struct{}{}), 200, "")
}

// Why: a token must work for one admin, one action, once, before it expires, and a sign-in token is not a step-up token.
func TestStepUpTokenRejections(t *testing.T) {
	h := consoletest.New(t)
	b, a := fullAdmin(t, h, "ada", console.RoleAdmin)
	other, _ := fullAdmin(t, h, "bob", console.RoleAdmin)
	id, _ := newClient(t, b, "app")
	path := "/api/clients/" + id + "/disable"
	before := clientOf(t, h, id)

	expired := b.StepUp("disable-client")
	h.Clock.Advance(6 * time.Minute) // past the 300 s token lifetime
	cases := []struct{ name, tok string }{
		{"another admin's token", other.StepUp("disable-client")},
		{"another action's purpose", b.StepUp("rotate-key")},
		{"expired", expired},
		{"sign-in token", h.StepUpToken(a.ID, "console-login", pin1)},
		{"not a token", "abc.def.ghi"},
	}
	for _, c := range cases {
		want(t, b.Do("POST", path, struct{}{}, tokHeader, c.tok), 403, "step_up_required")
		if got := clientOf(t, h, id); !reflect.DeepEqual(before, got) {
			t.Fatalf("%s: the client changed", c.name)
		}
	}

	// A token works once: after the disable, the same token cannot disable again.
	tok := b.StepUp("disable-client")
	want(t, b.Do("POST", path, struct{}{}, tokHeader, tok), 200, "")
	want(t, b.Do("POST", "/api/clients/"+id+"/enable", struct{}{}), 200, "")
	want(t, b.Do("POST", path, struct{}{}, tokHeader, tok), 403, "step_up_required")
	if c, err := h.Clients.Get(context.Background(), id); err != nil || c.DisabledAt != nil {
		t.Fatalf("a replayed token disabled the client: %v", err)
	}

	// A challenge started by another admin cannot be answered here.
	st := other.Do("POST", "/api/stepup/start", map[string]string{"action": "rotate-key", "factor": "pin"})
	want(t, st, 200, "")
	want(t, b.Do("POST", "/api/stepup/verify", map[string]string{"challengeId": st.Str("challengeId"), "pin": pin1}), 410, "challenge_gone")
}

type countingRT struct {
	base http.RoundTripper
	n    atomic.Int32
}

func (c *countingRT) RoundTrip(r *http.Request) (*http.Response, error) {
	c.n.Add(1)
	return c.base.RoundTrip(r)
}

// Why: a step-up for an action nobody defined must not reach AuthGate, or it would open challenges at will.
func TestStartRefusesUnknownAction(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	rt := &countingRT{base: h.AuthGate.HTTP.Transport}
	deps := h.Deps
	deps.AuthGate = &authgate.Client{BaseURL: h.AuthGateURL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey, HTTP: &http.Client{Transport: rt}}
	b := h.BrowserFor(console.New(deps))
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.SignInWithPIN(pin1), 200, "")

	calls := rt.n.Load()
	want(t, b.Do("POST", "/api/stepup/start", map[string]string{"action": "format-disk", "factor": "pin"}), 400, "invalid_action")
	want(t, b.Do("POST", "/api/stepup/start", map[string]string{"action": "", "factor": "pin"}), 400, "invalid_action")
	want(t, b.Do("POST", "/api/stepup/start", map[string]string{"action": "admin-create", "factor": "pin"}), 403, "forbidden")
	if rt.n.Load() != calls {
		t.Fatalf("AuthGate was called %d times", rt.n.Load()-calls)
	}
}

// Why: a full session is where the stage rules of spec section 7 meet every route group.
func TestSessionStageAgainstRouteGroups(t *testing.T) {
	h := consoletest.New(t)
	half := h.NewBrowser()
	hAdmin := h.CreateAdmin("hal", pw, console.RoleAdmin, false)
	withPIN(t, h, hAdmin, pin1)
	want(t, half.Login("hal", pw), 200, "")

	enroll := h.NewBrowser()
	h.CreateAdmin("eve", pw, console.RoleAdmin, true)
	want(t, enroll.Login("eve", pw), 200, "")

	full, _ := fullAdmin(t, h, "fay", console.RoleAdmin)

	type call struct{ method, path string }
	groups := []struct {
		name         string
		call         call
		body         any
		half, enroll string // "<status> <code>"
		full         string
	}{
		{"auth start", call{"POST", "/api/auth/second-factor/start"}, map[string]string{"factor": "pin"}, "200", "403 enrollment_required", "409 already_verified"},
		{"me", call{"GET", "/api/me"}, nil, "200", "200", "200"},
		{"me write", call{"DELETE", "/api/me/pin"}, nil, "401 second_factor_required", "409 factor_not_enrolled", "403 step_up_required"},
		{"stepup", call{"POST", "/api/stepup/start"}, map[string]string{"action": "rotate-key", "factor": "pin"}, "401 second_factor_required", "403 enrollment_required", "200"},
		{"clients", call{"GET", "/api/clients"}, nil, "401 second_factor_required", "403 enrollment_required", "200"},
		{"clients write", call{"POST", "/api/clients"}, map[string]any{}, "401 second_factor_required", "403 enrollment_required", "400 invalid_client"},
		{"audit", call{"GET", "/api/audit"}, nil, "401 second_factor_required", "403 enrollment_required", "200"},
		{"admins", call{"GET", "/api/admins"}, nil, "401 second_factor_required", "403 enrollment_required", ""},
	}
	check := func(t *testing.T, name string, b *consoletest.Browser, c call, body any, wantResp string) {
		t.Helper()
		if wantResp == "" {
			return
		}
		st, code, _ := strings.Cut(wantResp, " ")
		r := b.Do(c.method, c.path, body)
		if strconv.Itoa(r.Status) != st || (code != "" && r.Str("error") != code) {
			t.Errorf("%s: %s %s = %d %s, want %s", name, c.method, c.path, r.Status, r.Body, wantResp)
		}
	}
	for _, g := range groups {
		check(t, "half "+g.name, half, g.call, g.body, g.half)
		check(t, "enroll "+g.name, enroll, g.call, g.body, g.enroll)
		check(t, "full "+g.name, full, g.call, g.body, g.full)
	}
	// Signing out is open to every stage.
	for name, b := range map[string]*consoletest.Browser{"half": half, "enroll": enroll, "full": full} {
		if r := b.Do("POST", "/api/auth/logout", nil); r.Status != 204 {
			t.Errorf("%s logout = %d %s", name, r.Status, r.Body)
		}
	}
}

// Why: the access key, PIN, passwords and tokens must not end up in audit items or logs, however a request fails.
func TestSecretsAbsentFromAuditAndLogs(t *testing.T) {
	logs := captureLogs(t)
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	secrets := []string{pin1, pw, pw2}

	id, key := newClient(t, b, "app")
	secrets = append(secrets, key)
	tok := b.StepUp("rotate-key")
	secrets = append(secrets, tok, b.CookieValue())
	r := b.Do("POST", "/api/clients/"+id+"/keys", struct{}{}, tokHeader, tok)
	want(t, r, 201, "")
	secrets = append(secrets, r.Str("accessKey"))

	// Failures that carry secrets in their request: a wrong PIN, a wrong password, a bad token, a replay.
	bad := "000111"
	secrets = append(secrets, bad)
	st := b.Do("POST", "/api/stepup/start", map[string]string{"action": "rotate-key", "factor": "pin"})
	want(t, st, 200, "")
	want(t, b.Do("POST", "/api/stepup/verify", map[string]string{"challengeId": st.Str("challengeId"), "pin": bad}), 400, "verification_failed")
	want(t, b.Do("POST", "/api/clients/"+id+"/keys", struct{}{}, tokHeader, tok), 403, "step_up_required")
	want(t, b.Do("POST", "/api/clients/"+id+"/keys", struct{}{}, tokHeader, "eyJhbGciOiJub25lIn0.e30.x"), 403, "step_up_required")
	want(t, b.Do("POST", "/api/me/password", passwordBody("wrong-wrong-wrong", pw2), tokHeader, b.StepUp("change-password")), 400, "wrong_password")
	secrets = append(secrets, "wrong-wrong-wrong")
	want(t, h.NewBrowser().Login("ada", "wrong-wrong-wrong"), 401, "invalid_credentials")
	other := h.NewBrowser()
	want(t, other.Login("ada", pw), 200, "")
	st = other.StartSecondFactor("pin")
	want(t, other.Do("POST", "/api/auth/second-factor/verify", map[string]string{"challengeId": st.Str("challengeId"), "pin": bad}), 400, "verification_failed")

	items := scanTable(t, h)
	if !strings.Contains(items, "AUDIT#") {
		t.Fatal("scan found no audit item")
	}
	for _, sec := range secrets {
		if sec == "" {
			t.Fatal("empty secret in the list")
		}
		if strings.Contains(items, sec) {
			t.Errorf("a secret reached the table: %q", sec)
		}
		if strings.Contains(logs.String(), sec) {
			t.Errorf("a secret reached the logs: %q", sec)
		}
	}
}
