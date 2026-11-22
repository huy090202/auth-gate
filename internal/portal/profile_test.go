package portal

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/portal/avatar"
)

const stepHeader = "x-step-up-token"

// emailChange asks for a change to newEmail from a full session with a fresh step-up.
func emailChange(b *browser, newEmail string) resp {
	return b.do("POST", "/api/me/email", map[string]string{"newEmail": newEmail}, stepHeader, b.stepUp("change-email", ""))
}

// Why: until the new address is confirmed nothing moves, so a mistyped or hostile address cannot lock the owner out.
func TestChangeEmailNeedsConfirmation(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")

	if r := b.do("POST", "/api/me/email", map[string]string{"newEmail": "n@x.com"}); r.Status != 403 {
		t.Fatalf("without step-up = %d", r.Status)
	}
	if r := emailChange(b, " A@X.com "); r.Status != 400 || r.str("error") != "invalid_email" {
		t.Fatalf("same address = %d %s", r.Status, r.Body)
	}
	want(t, emailChange(b, " New@X.com "), 202, "")
	var to string
	for _, m := range e.mails(t) {
		if m.Kind == "email-change-confirm" {
			to = m.To
		}
	}
	if to != "new@x.com" {
		t.Fatalf("confirm mail to %q (mails: %s)", to, kinds(e.mails(t)))
	}
	tok := e.lastLink(t, "email-change-confirm")
	if !strings.Contains(e.mails(t)[len(e.mails(t))-1].Text, "https://portal.test/confirm-email#token=") {
		t.Fatal("link is not /confirm-email#token=")
	}

	// Before the link is opened the old address still signs in and the new one does not.
	want(t, e.browser(t, "203.0.113.2").login("a@x.com", goodPW), 200, "")
	want(t, e.browser(t, "203.0.113.3").login("new@x.com", goodPW), 401, "invalid_credentials")

	r := e.post("/api/auth/confirm-email", map[string]string{"token": tok})
	if r.Code != 204 {
		t.Fatalf("confirm = %d %s", r.Code, r.Body)
	}
	if r := e.post("/api/auth/confirm-email", map[string]string{"token": tok}); r.Code != 400 {
		t.Fatalf("second use = %d", r.Code)
	}
	got := e.reload(t, u)
	if got.Email != "new@x.com" {
		t.Fatalf("email = %q", got.Email)
	}
	if _, err := e.a.Store.GetUserByEmail(context.Background(), "a@x.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("old EMAIL item: %v", err)
	}
	last := e.mails(t)
	if m := last[len(last)-1]; m.Kind != "email-changed" || m.To != "a@x.com" {
		t.Fatalf("last mail = %+v", m)
	}
	want(t, e.browser(t, "203.0.113.4").login("new@x.com", goodPW), 200, "")
	if acts := e.auditActions(t, u); !slices.Contains(acts, "email-change-requested:success") || !slices.Contains(acts, "email-changed:success") {
		t.Fatalf("audit = %v", acts)
	}
}

// Why: two accounts can race for one address; the loser must get 409 and keep its old address, and a request for
// an address already taken must look like any other.
func TestChangeEmailRaceTaken(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")

	want(t, emailChange(b, "n@x.com"), 202, "")
	tok := e.lastLink(t, "email-change-confirm")
	e.user(t, "n@x.com", goodPW) // another account takes it first
	r := e.post("/api/auth/confirm-email", map[string]string{"token": tok})
	if r.Code != 409 || !strings.Contains(r.Body.String(), "email_taken") {
		t.Fatalf("confirm = %d %s", r.Code, r.Body)
	}
	if got := e.reload(t, u); got.Email != "a@x.com" {
		t.Fatalf("email moved to %q", got.Email)
	}
	if _, err := e.a.Store.GetUserByEmail(context.Background(), "a@x.com"); err != nil {
		t.Fatalf("old EMAIL item lost: %v", err)
	}

	// Asking for an address that is taken answers 202 and sends nothing.
	before := len(e.mails(t))
	want(t, emailChange(b, "n@x.com"), 202, "")
	if len(e.mails(t)) != before {
		t.Fatalf("mail sent for a taken address: %s", kinds(e.mails(t)))
	}
}

// Why: a link issued before a password change must not survive it, as with recovery links.
func TestEmailChangeLinkVoidedByPasswordChange(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")
	want(t, emailChange(b, "n@x.com"), 202, "")
	tok := e.lastLink(t, "email-change-confirm")
	want(t, b.do("POST", "/api/me/password", map[string]string{"currentPassword": goodPW, "newPassword": "another-long-passphrase"},
		stepHeader, b.stepUp("change-password", "")), 200, "")
	if r := e.post("/api/auth/confirm-email", map[string]string{"token": tok}); r.Code != 400 {
		t.Fatalf("confirm after epoch bump = %d", r.Code)
	}
	if got := e.reload(t, u); got.Email != "a@x.com" {
		t.Fatalf("email = %q", got.Email)
	}
}

// Why: changing the password must end every other session yet keep this one, and a wrong current password must not pass.
func TestChangePasswordKeepsThisSessionEndsOthers(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b1 := e.fullSession(t, u, "203.0.113.1")
	b2 := e.fullSession(t, u, "203.0.113.2")
	const newPW = "another-long-passphrase"
	change := func(cur, next string) resp {
		return b1.do("POST", "/api/me/password", map[string]string{"currentPassword": cur, "newPassword": next},
			stepHeader, b1.stepUp("change-password", ""))
	}
	want(t, change("not-the-password", newPW), 400, "wrong_password")
	want(t, change(goodPW, "short"), 400, "invalid_password")
	want(t, change(goodPW, goodPW), 400, "invalid_password")
	want(t, change(goodPW, newPW), 200, "")
	want(t, b1.do("GET", "/api/me", nil), 200, "")
	want(t, b2.do("GET", "/api/me", nil), 401, "")
	want(t, e.browser(t, "203.0.113.3").login("a@x.com", goodPW), 401, "invalid_credentials")
	want(t, e.browser(t, "203.0.113.4").login("a@x.com", newPW), 200, "")
	if got := kinds(e.mails(t)); !strings.HasSuffix(got, "password-changed") {
		t.Fatalf("mails = %s", got)
	}
	if acts := e.auditActions(t, u); !slices.Contains(acts, "password-change:success") {
		t.Fatalf("audit = %v", acts)
	}
}

// Why: the name is shown to others, so it follows sign-up's rules.
func TestDisplayName(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")
	want(t, b.do("POST", "/api/me/display-name", map[string]string{"displayName": "  Ann Lee "}), 200, "")
	for _, bad := range []string{"", "   ", "a\x00b", strings.Repeat("x", 65)} {
		want(t, b.do("POST", "/api/me/display-name", map[string]string{"displayName": bad}), 400, "invalid_display_name")
	}
	if got := e.reload(t, u); got.DisplayName != "Ann Lee" {
		t.Fatalf("name = %q", got.DisplayName)
	}
}

// tenantRT answers the portal-key client routes of a fake AuthGate and sends every other call to the real one.
type tenantRT struct {
	next    http.RoundTripper
	mu      sync.Mutex
	pages   map[string]string // cursor -> body of the list answer
	deleted []string          // "<tenant> <client>"
	failOn  string            // client id whose delete answers 500
}

func (f *tenantRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasPrefix(req.URL.Path, "/admin/api/portal/clients") {
		return f.next.RoundTrip(req)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := httptest.NewRecorder()
	switch req.Method {
	case http.MethodGet:
		rec.WriteHeader(200)
		_, _ = rec.WriteString(f.pages[req.URL.Query().Get("cursor")])
	case http.MethodDelete:
		cid := strings.TrimPrefix(req.URL.Path, "/admin/api/portal/clients/")
		if cid == f.failOn {
			rec.WriteHeader(500)
		} else {
			f.deleted = append(f.deleted, req.Header.Get("x-tenant-id")+" "+cid)
			rec.WriteHeader(200)
			_, _ = rec.WriteString("{}")
		}
	default:
		rec.WriteHeader(405)
	}
	return rec.Result(), nil
}

func withTenantAPI(e *authEnv, c1, c2 string) *tenantRT {
	rt := &tenantRT{next: e.ag.HTTP.Transport, pages: map[string]string{
		"":   `{"clients":[{"clientId":"` + c1 + `"}],"nextCursor":"p2"}`,
		"p2": `{"clients":[{"clientId":"` + c2 + `"}]}`,
	}}
	ag := *e.ag
	ag.PortalKey = "portal-key"
	ag.HTTP = &http.Client{Transport: rt}
	e.a.AuthGate = &ag
	return rt
}

// Why: deleting an account must delete its workspace's clients at AuthGate (every page), its factors and its
// Portal items, end the session, and free the email.
func TestDeleteAccountDeletesWorkspaceClients(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")
	c1, c2 := id.New(), id.New()
	rt := withTenantAPI(e, c1, c2)
	if _, err := e.a.Store.GetTenant(context.Background(), u.PersonalTenantID); err != nil {
		t.Fatal(err)
	}

	tok := b.stepUp("delete-account", "")
	if r := b.do("DELETE", "/api/me", nil); r.Status != 403 {
		t.Fatalf("without step-up = %d", r.Status)
	}
	r := b.do("DELETE", "/api/me", nil, stepHeader, tok)
	if r.Status != 204 {
		t.Fatalf("delete = %d %s", r.Status, r.Body)
	}
	wantDeleted := []string{u.PersonalTenantID + " " + c1, u.PersonalTenantID + " " + c2}
	if !slices.Equal(rt.deleted, wantDeleted) {
		t.Fatalf("AuthGate deletes = %v, want %v", rt.deleted, wantDeleted)
	}
	want(t, b.do("GET", "/api/me", nil), 401, "")
	ctx := context.Background()
	if _, err := e.a.Store.GetUser(ctx, u.ID); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("user: %v", err)
	}
	if _, err := e.a.Store.GetUserByEmail(ctx, "a@x.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("email item: %v", err)
	}
	if _, err := e.a.Store.GetTenant(ctx, u.PersonalTenantID); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("tenant: %v", err)
	}
	if _, err := e.a.Store.GetMembership(ctx, u.PersonalTenantID, u.ID); !errors.Is(err, ErrNotMember) {
		t.Fatalf("membership: %v", err)
	}
	if ms, _ := e.a.Store.ListMemberships(ctx, u.ID); len(ms) != 0 {
		t.Fatalf("user-side memberships: %v", ms)
	}
	if es, _, _ := e.a.Store.ListAudit(ctx, u.PersonalTenantID, ""); len(es) != 0 {
		t.Fatalf("audit entries left: %d", len(es))
	}
	if f, err := e.ag.Factors(ctx, u.ID); err != nil || f.PIN {
		t.Fatalf("factors after: %+v %v", f, err)
	}
	if ms := e.mails(t); len(ms) == 0 || ms[len(ms)-1].Kind != "account-deleted" || ms[len(ms)-1].To != "a@x.com" {
		t.Fatalf("mails = %s", kinds(e.mails(t)))
	}
	// The address can sign up again.
	if r := e.post("/api/auth/signup", signupBody("a@x.com")); r.Code != 202 {
		t.Fatalf("sign up again = %d", r.Code)
	}
	if got := e.mails(t); got[len(got)-1].Kind != "verify" {
		t.Fatalf("sign-up after deletion sent %s", got[len(got)-1].Kind)
	}
}

// Why: if AuthGate fails midway the account must stay whole, so the person can run the deletion again.
func TestDeleteAccountStopsWhenAuthGateFails(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")
	c1, c2 := id.New(), id.New()
	rt := withTenantAPI(e, c1, c2)
	rt.failOn = c2
	r := b.do("DELETE", "/api/me", nil, stepHeader, b.stepUp("delete-account", ""))
	if r.Status != 502 || r.str("error") != "authgate_unavailable" {
		t.Fatalf("delete = %d %s", r.Status, r.Body)
	}
	want(t, b.do("GET", "/api/me", nil), 200, "")
	if _, err := e.a.Store.GetTenant(context.Background(), u.PersonalTenantID); err != nil {
		t.Fatalf("tenant gone: %v", err)
	}
	if f, _ := e.ag.Factors(context.Background(), u.ID); !f.PIN {
		t.Fatal("PIN deleted before the clients were")
	}
	// Run again once AuthGate is back: the client already deleted answers nothing wrong.
	rt.failOn = ""
	r = b.do("DELETE", "/api/me", nil, stepHeader, b.stepUp("delete-account", ""))
	if r.Status != 204 {
		t.Fatalf("second run = %d %s", r.Status, r.Body)
	}
}

// memAvatars is an in-memory avatar store.
type memAvatars struct {
	objs    map[string]memObj
	deleted []string
}
type memObj struct {
	size int64
	ct   string
}

func (m *memAvatars) PresignPost(_ context.Context, key, ct string) (avatar.PostForm, error) {
	return avatar.PostForm{URL: "https://s3.test/up", Fields: map[string]string{"key": key, "Content-Type": ct}}, nil
}
func (m *memAvatars) Head(_ context.Context, key string) (int64, string, error) {
	o, ok := m.objs[key]
	if !ok {
		return 0, "", errors.New("no such object")
	}
	return o.size, o.ct, nil
}
func (m *memAvatars) Delete(_ context.Context, key string) error {
	delete(m.objs, key)
	m.deleted = append(m.deleted, key)
	return nil
}

// Why: a confirm names an object key; it must be the caller's own, within the size and type the form allowed,
// or one person could claim or destroy another's picture, or park a huge file.
func TestAvatarConfirmRejectsOthersKeyAndOversize(t *testing.T) {
	e := newAuthEnv(t)
	u := e.user(t, "a@x.com", goodPW)
	e.withPIN(t, u, pin1)
	b := e.fullSession(t, u, "203.0.113.1")
	mem := &memAvatars{objs: map[string]memObj{}}
	e.a.Avatars = mem
	confirm := func(key string) resp { return b.do("POST", "/api/me/avatar/confirm", map[string]string{"key": key}) }
	form := func(ct string) resp { return b.do("POST", "/api/me/avatar", map[string]string{"contentType": ct}) }
	put := func(ct string, size int64) string {
		r := form(ct)
		want(t, r, 200, "")
		key := r.str("key")
		if !strings.HasPrefix(key, "avatars/"+u.ID+"/") {
			t.Fatalf("key = %q", key)
		}
		mem.objs[key] = memObj{size, ct}
		return key
	}

	want(t, form("image/gif"), 400, "invalid_avatar")
	want(t, form("text/html"), 400, "invalid_avatar")

	other := "avatars/" + id.New() + "/" + id.New() + ".png"
	mem.objs[other] = memObj{10, "image/png"}
	want(t, confirm(other), 400, "invalid_avatar")
	want(t, confirm("avatars/"+u.ID+"/../"+id.New()+".png"), 400, "invalid_avatar")
	want(t, confirm("avatars/"+u.ID+"/nope.png"), 400, "invalid_avatar")
	if _, ok := mem.objs[other]; !ok || len(mem.deleted) != 0 {
		t.Fatalf("a refused key was touched: deleted %v", mem.deleted)
	}

	big := put("image/png", avatar.MaxBytes+1)
	want(t, confirm(big), 400, "invalid_avatar")
	empty := put("image/webp", 0)
	want(t, confirm(empty), 400, "invalid_avatar")
	wrong := put("image/jpeg", 100)
	mem.objs[wrong] = memObj{100, "image/png"} // the object is not what the key says
	want(t, confirm(wrong), 400, "invalid_avatar")
	if !slices.Equal(slices.Sorted(slices.Values(mem.deleted)), slices.Sorted(slices.Values([]string{big, empty, wrong}))) {
		t.Fatalf("deleted = %v", mem.deleted)
	}
	if got := e.reload(t, u); got.AvatarKey != "" {
		t.Fatalf("avatar set by a refused confirm: %q", got.AvatarKey)
	}

	first := put("image/png", 1000)
	r := confirm(first)
	want(t, r, 200, "")
	if r.str("avatarUrl") != "/"+first {
		t.Fatalf("avatarUrl = %q", r.str("avatarUrl"))
	}
	if me := b.do("GET", "/api/me", nil); me.str("avatarUrl") != "/"+first {
		t.Fatalf("/api/me avatarUrl = %q", me.str("avatarUrl"))
	}
	mem.deleted = nil
	second := put("image/webp", 2000)
	want(t, confirm(second), 200, "")
	if !slices.Equal(mem.deleted, []string{first}) {
		t.Fatalf("previous avatar not deleted: %v", mem.deleted)
	}
	want(t, confirm(second), 200, "") // confirming again keeps the object
	if _, ok := mem.objs[second]; !ok {
		t.Fatal("re-confirm deleted the current avatar")
	}
	if r := b.do("DELETE", "/api/me/avatar", nil); r.Status != 204 {
		t.Fatalf("remove = %d", r.Status)
	}
	if _, ok := mem.objs[second]; ok {
		t.Fatal("removed avatar object kept")
	}
	if me := b.do("GET", "/api/me", nil); me.m()["avatarUrl"] != nil {
		t.Fatalf("avatarUrl after remove: %s", me.Body)
	}
	if acts := e.auditActions(t, u); !slices.Contains(acts, "avatar-set:success") || !slices.Contains(acts, "avatar-removed:success") {
		t.Fatalf("audit = %v", acts)
	}
}
