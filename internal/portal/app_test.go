package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// Why: a service that boots without its wiring fails on the first request, not at start.
func TestLoadConfigRefusesMissing(t *testing.T) {
	full := map[string]string{
		"TABLE_NAME": "t", "AWS_REGION": "r", "AUTHGATE_BASE_URL": "https://a", "AUTHGATE_CLIENT_ID": "c",
		"PUBLIC_BASE_URL": "https://p", "EMAIL_FROM": "no-reply@p", "AVATAR_BUCKET": "b", "TURNSTILE_SITE_KEY": "k",
		"CLIENT_KEY_PARAM": "/c", "PORTAL_KEY_PARAM": "/p", "TURNSTILE_SECRET_PARAM": "/t", "ORIGIN_VERIFY_PARAM": "/o",
	}
	dev := map[string]string{"PORTAL_DEV": "1", "TABLE_NAME": "t", "AUTHGATE_BASE_URL": "http://a", "AUTHGATE_CLIENT_ID": "c", "CLIENT_KEY": "k"}
	without := func(base map[string]string, drop string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			if k != drop {
				m[k] = v
			}
		}
		return m
	}
	cases := []struct {
		name string
		env  map[string]string
		ok   bool
	}{{"stage complete", full, true}, {"dev complete", dev, true}, {"dev without client key", without(dev, "CLIENT_KEY"), false},
		{"dev without table", without(dev, "TABLE_NAME"), false}}
	for k := range full {
		cases = append(cases, struct {
			name string
			env  map[string]string
			ok   bool
		}{"stage without " + k, without(full, k), false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(func(k string) string { return tc.env[k] })
			if (err == nil) != tc.ok {
				t.Fatalf("LoadConfig err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
	c, _ := LoadConfig(func(k string) string { return dev[k] })
	if !c.Dev || c.Port != "8789" || c.PortalKey != "dev-portal" || c.TurnstileSiteKey != TurnstileTestSiteKey ||
		c.TurnstileSecret != TurnstileTestSecretKey || c.MailboxDir != ".portal-mailbox" || c.PublicBaseURL != "http://localhost:8789" {
		t.Fatalf("dev defaults = %+v", c)
	}
}

type env struct {
	h     http.Handler
	a     *App
	clock *time.Time
}

// newEnv builds an App on a fresh table and a mux with one guarded probe route
// per guard, the way later tasks register theirs.
func newEnv(t *testing.T) *env {
	t.Helper()
	db, table := dynamotest.NewTable(t)
	// Anchored to real time: a stored ttl in the past lets DynamoDB's reaper delete the item mid-test.
	clock := time.Now()
	e := &env{clock: &clock}
	now := func() time.Time { return *e.clock }
	e.a = newApp(Deps{
		Config:  Config{Table: table},
		DB:      db,
		Limiter: &ratelimit.Limiter{DB: db, Table: table, TTL: time.Hour, Now: now},
		Now:     now,
	})
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setAuditTarget(r.Context(), "target-1")
		setAuditDetail(r.Context(), "fact", "yes")
		m := memberFrom(r.Context())
		_, _ = w.Write([]byte(`{"role":"` + m.Role + `"}`))
	})
	mux := http.NewServeMux()
	mux.Handle("GET /api/me", e.a.session(StageFull, StageEnroll)(ok))
	mux.Handle("GET /api/full", e.a.session(StageFull)(ok))
	mux.Handle("GET /api/t/{tenantId}/any", e.a.audited("probe", e.a.session(StageFull)(e.a.member()(ok))))
	mux.Handle("GET /api/t/{tenantId}/owner", e.a.audited("probe-owner", e.a.session(StageFull)(e.a.member(RoleOwner, RoleAdmin)(ok))))
	mux.Handle("GET /api/ip", e.a.limitIP("probe", ratelimit.Settings{Burst: 2, PerSecond: 0})(ok))
	mux.Handle("POST /api/promote", e.a.session(StageHalf)(webapi.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		x, _ := sessionFrom(r.Context())
		return e.a.promote(r.Context(), w, x, StageFull)
	})))
	mux.Handle("POST /api/signout", e.a.session(StageFull)(webapi.HandlerFunc(func(w http.ResponseWriter, r *http.Request) error {
		x, _ := sessionFrom(r.Context())
		return e.a.endSession(w, r, x)
	})))
	mux.HandleFunc("/api/", notFound)
	e.h = mux
	return e
}

// verified makes a verified user with a personal workspace.
func (e *env) verified(t *testing.T, email string) (User, string) {
	t.Helper()
	ctx := context.Background()
	u := User{ID: id.New(), Email: email, PasswordHash: "h", CreatedAt: e.a.now()}
	if err := e.a.Store.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	tid := id.New()
	if err := e.a.Store.VerifyEmail(ctx, u.ID, Tenant{ID: tid}, e.a.now()); err != nil {
		t.Fatal(err)
	}
	u, err := e.a.Store.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u, tid
}

// login starts a session and returns its cookie.
func (e *env) login(t *testing.T, u User, st Stage) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := e.a.startSession(context.Background(), rec, u, st); err != nil {
		t.Fatal(err)
	}
	return rec.Result().Cookies()[0]
}

func (e *env) get(path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func code(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var b struct{ Error string }
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	return b.Error
}

// Why: a client that probes for routes must get the one JSON not_found, and API answers must never be cached.
func TestUnknownAPIRouteIs404JSON(t *testing.T) {
	h := New(Deps{Config: Config{Table: "t"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/nope", nil))
	if rec.Code != 404 || strings.TrimSpace(rec.Body.String()) != `{"error":"not_found"}` || rec.Header().Get("Cache-Control") == "" {
		t.Fatalf("got %d %q cache=%q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
}

// Why: a password reset in another tab bumps the epoch; the old session must die on its very next request.
func TestSessionEpochMismatchIsRefused(t *testing.T) {
	e := newEnv(t)
	u, tid := e.verified(t, "a@x.com")
	c := e.login(t, u, StageFull)
	if rec := e.get("/api/t/"+tid+"/any", c); rec.Code != 200 {
		t.Fatalf("before: %d %s", rec.Code, rec.Body)
	}
	if err := e.a.Store.EndSessions(context.Background(), u.ID); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/api/t/"+tid+"/any", c)
	if rec.Code != 401 || code(t, rec) != "unauthorized" {
		t.Fatalf("after: %d %s", rec.Code, rec.Body)
	}
	if _, err := e.a.Store.GetSession(context.Background(), hashToken(c.Value)); err == nil {
		t.Fatal("the refused session item was kept")
	}
}

// Why: a suspended or deleted account must lose its sessions at once, on tenant routes too.
func TestSessionRefusedForSuspendedOrMissingUser(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, tid := e.verified(t, "a@x.com")
	c := e.login(t, u, StageFull)
	if _, err := e.a.Store.DB.UpdateItem(ctx, suspendInput(e.a.Store, u.ID, e.a.now())); err != nil {
		t.Fatal(err)
	}
	if rec := e.get("/api/t/"+tid+"/any", c); rec.Code != 401 {
		t.Fatalf("suspended: %d", rec.Code)
	}
	ghost := e.login(t, User{ID: id.New()}, StageFull)
	if rec := e.get("/api/me", ghost); rec.Code != 401 {
		t.Fatalf("missing user: %d", rec.Code)
	}
	// A user who never verified has no business holding a session, even a valid one.
	unverified := User{ID: id.New(), Email: "u@x.com", PasswordHash: "h", CreatedAt: e.a.now()}
	if err := e.a.Store.CreateUser(ctx, unverified); err != nil {
		t.Fatal(err)
	}
	if rec := e.get("/api/me", e.login(t, unverified, StageFull)); rec.Code != 401 {
		t.Fatalf("unverified user: %d", rec.Code)
	}
}

// Why: a session without a cookie, with an unknown one, or past its time limit is a plain 401, and stages gate routes.
func TestSessionStagesAndExpiry(t *testing.T) {
	e := newEnv(t)
	u, _ := e.verified(t, "a@x.com")
	if rec := e.get("/api/me", nil); rec.Code != 401 {
		t.Fatalf("no cookie: %d", rec.Code)
	}
	if rec := e.get("/api/me", &http.Cookie{Name: e.a.cookieName(), Value: "token-without-a-session", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode}); rec.Code != 401 {
		t.Fatalf("unknown cookie: %d", rec.Code)
	}
	half := e.login(t, u, StageHalf)
	if rec := e.get("/api/me", half); rec.Code != 401 || code(t, rec) != "second_factor_required" {
		t.Fatalf("half: %d %s", rec.Code, rec.Body)
	}
	enroll := e.login(t, u, StageEnroll)
	if rec := e.get("/api/full", enroll); rec.Code != 403 || code(t, rec) != "enrollment_required" {
		t.Fatalf("enroll on a full route: %d %s", rec.Code, rec.Body)
	}
	full := e.login(t, u, StageFull)
	if rec := e.get("/api/full", full); rec.Code != 200 {
		t.Fatalf("full: %d %s", rec.Code, rec.Body)
	}
	*e.clock = e.clock.Add(31 * time.Minute) // past the 30 minute idle limit
	if rec := e.get("/api/full", full); rec.Code != 401 {
		t.Fatalf("idle: %d", rec.Code)
	}
	if rec := e.get("/api/me", half); rec.Code != 401 || code(t, rec) != "unauthorized" {
		t.Fatalf("half after 10 minutes: %d %s", rec.Code, rec.Body)
	}
}

// Why: the URL's tenant decides access; another user's tenant must look exactly like one that does not exist.
func TestMemberGuard(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, mine := e.verified(t, "a@x.com")
	_, theirs := e.verified(t, "b@x.com")
	c := e.login(t, u, StageFull)

	rec := e.get("/api/t/"+mine+"/owner", c)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"owner"`) {
		t.Fatalf("own tenant: %d %s", rec.Code, rec.Body)
	}
	unknown := e.get("/api/t/"+id.New()+"/any", c)
	other := e.get("/api/t/"+theirs+"/any", c)
	bad := e.get("/api/t/not-an-id/any", c)
	lower := e.get("/api/t/"+strings.ToLower(mine)+"/any", c)
	for name, r := range map[string]*httptest.ResponseRecorder{"unknown": unknown, "other user's": other, "malformed": bad, "lower-case": lower} {
		if r.Code != 404 || code(t, r) != "not_found" {
			t.Fatalf("%s tenant: %d %s", name, r.Code, r.Body)
		}
	}
	if unknown.Body.String() != other.Body.String() {
		t.Fatal("a tenant that exists answers differently from one that does not")
	}

	// A role too low is 403, and the role is read on every request.
	if _, err := e.a.Store.DB.PutItem(ctx, memberPut(e.a.Store, theirs, u.ID, RoleMember, e.a.now())); err != nil {
		t.Fatal(err)
	}
	if rec := e.get("/api/t/"+theirs+"/any", c); rec.Code != 200 {
		t.Fatalf("member on any: %d %s", rec.Code, rec.Body)
	}
	if rec := e.get("/api/t/"+theirs+"/owner", c); rec.Code != 403 || code(t, rec) != "forbidden" {
		t.Fatalf("member on owner route: %d %s", rec.Code, rec.Body)
	}
	if _, err := e.a.Store.DB.PutItem(ctx, memberPut(e.a.Store, theirs, u.ID, RoleAdmin, e.a.now())); err != nil {
		t.Fatal(err)
	}
	if rec := e.get("/api/t/"+theirs+"/owner", c); rec.Code != 200 {
		t.Fatalf("a promotion did not show on the next request: %d", rec.Code)
	}
}

// Why: the per-user bucket must cap a signed-in caller at 60 calls in a burst on every full-session route, without a route opting in.
func TestSessionGuardSpendsTheUserBucketOnFullSessions(t *testing.T) {
	e := newEnv(t)
	u, _ := e.verified(t, "a@x.com")
	c := e.login(t, u, StageFull)
	for i := 0; i < 60; i++ {
		if rec := e.get("/api/full", c); rec.Code != 200 {
			t.Fatalf("call %d: %d", i+1, rec.Code)
		}
	}
	rec := e.get("/api/full", c)
	if rec.Code != 429 || code(t, rec) != "rate_limited" || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("call 61: %d %s retry-after %q", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
	enroll := e.login(t, u, StageEnroll)
	if rec := e.get("/api/me", enroll); rec.Code != 200 {
		t.Fatalf("enroll session is not metered by the user bucket: %d", rec.Code)
	}
}

// Why: the public auth routes are limited per address, with no header the caller controls picking the bucket.
func TestLimitIPByViewerAddress(t *testing.T) {
	e := newEnv(t)
	call := func(ip string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/ip", nil)
		req.RemoteAddr = "9.9.9.9:1"
		req.Header.Set("x-forwarded-for", "7.7.7.7")
		if ip != "" {
			req.Header.Set("cloudfront-viewer-address", ip+":4000")
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec.Code
	}
	for i, want := range []int{200, 200, 429} {
		if got := call("1.1.1.1"); got != want {
			t.Fatalf("call %d for one address = %d, want %d (burst of 2)", i+1, got, want)
		}
	}
	if call("2.2.2.2") != 200 {
		t.Fatal("another address shares the bucket")
	}
	for i, want := range []int{200, 200, 429} {
		if got := call(""); got != want {
			t.Fatalf("call %d with no viewer address = %d, want %d (one shared bucket)", i+1, got, want)
		}
	}
}

// Why: an action in a workspace must leave its mark in that workspace's log, with who, what and how it ended.
func TestAuditedWritesToTheTenantLog(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	u, mine := e.verified(t, "a@x.com")
	c := e.login(t, u, StageFull)
	e.get("/api/t/"+mine+"/owner", c)
	e.get("/api/t/"+id.New()+"/any", c) // not a member: the actor is known, the tenant is not, so nothing is stored
	got, _, err := e.a.Store.ListAudit(ctx, mine, "")
	if err != nil || len(got) != 1 {
		t.Fatalf("log = %+v, %v", got, err)
	}
	if got[0].ActorID != u.ID || got[0].Action != "probe-owner" || got[0].Result != "success" ||
		got[0].Target != "target-1" || got[0].Detail["fact"] != "yes" {
		t.Fatalf("entry = %+v", got[0])
	}
	// A refused request is logged with its error code.
	if _, err := e.a.Store.DB.PutItem(ctx, memberPut(e.a.Store, mine, u.ID, RoleMember, e.a.now())); err != nil {
		t.Fatal(err)
	}
	e.get("/api/t/"+mine+"/owner", c)
	got, _, _ = e.a.Store.ListAudit(ctx, mine, "")
	if len(got) != 2 || got[0].Result != "failed:forbidden" {
		t.Fatalf("log = %+v", got)
	}
}

func (e *env) post(path string, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// Why: a token seen at one stage must be worthless at the next, and sign-out must end the session, not only the cookie.
func TestPromoteRotatesTheCookieAndEndSessionEndsIt(t *testing.T) {
	e := newEnv(t)
	u, tid := e.verified(t, "a@x.com")
	half := e.login(t, u, StageHalf)
	rec := e.post("/api/promote", half)
	if rec.Code != 200 {
		t.Fatalf("promote: %d %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Value == half.Value || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies = %+v", cookies)
	}
	if rec := e.get("/api/me", half); rec.Code != 401 {
		t.Fatalf("old token still works: %d", rec.Code)
	}
	full := cookies[0]
	x, err := e.a.Store.GetSession(context.Background(), hashToken(full.Value))
	if err != nil || x.Stage != StageFull || x.ActiveTenantID != tid {
		t.Fatalf("promoted session = %+v, %v", x, err)
	}
	rec = e.post("/api/signout", full)
	if rec.Code != 401 {
		t.Fatalf("signout: %d", rec.Code)
	}
	if rec := e.get("/api/full", full); rec.Code != 401 {
		t.Fatalf("session survived sign-out: %d", rec.Code)
	}
}
