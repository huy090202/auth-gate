package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type rig struct {
	app *App
	clk *clock
	mux http.Handler
	st  *Store
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, table := dynamotest.NewTable(t)
	clk := &clock{t: time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC)}
	st := &Store{DB: db, Table: table}
	app := &App{Deps: Deps{Now: clk.now}, Store: st}
	mux := http.NewServeMux()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		x, adm := sessionFrom(r.Context())
		if x.Username != adm.Username {
			t.Errorf("context mismatch %q %q", x.Username, adm.Username)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/half", app.session(StageHalf)(ok))
	mux.Handle("/enroll", app.session(StageEnroll)(ok))
	mux.Handle("/full", app.session(StageFull)(ok))
	mux.Handle("/enroll-or-full", app.session(StageEnroll, StageFull)(ok))
	return &rig{app: app, clk: clk, mux: mux, st: st}
}

func (g *rig) admin(t *testing.T, name string) Admin {
	t.Helper()
	return mkAdmin(t, g.st, name, RoleAdmin)
}

// open starts a session and returns its raw token.
func (g *rig) open(t *testing.T, a Admin, stage Stage) string {
	t.Helper()
	w := httptest.NewRecorder()
	if err := g.app.startSession(context.Background(), w, a, stage); err != nil {
		t.Fatal(err)
	}
	return w.Result().Cookies()[0].Value
}

func (g *rig) call(path, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", path, nil)
	if token != "" {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: token}) //nolint:gosec // a request cookie; the attributes are not sent by browsers
	}
	w := httptest.NewRecorder()
	g.mux.ServeHTTP(w, r)
	return w
}

func (g *rig) expect(t *testing.T, path, token string, status int, code string) {
	t.Helper()
	w := g.call(path, token)
	if w.Code != status || (code != "" && !strings.Contains(w.Body.String(), `"`+code+`"`)) {
		t.Fatalf("%s = %d %s, want %d %s", path, w.Code, w.Body, status, code)
	}
}

// Why: each row of the stage table is a different promise to the browser.
func TestStageMatrix(t *testing.T) {
	g := newRig(t)
	a := g.admin(t, "huy")
	half, enroll, full := g.open(t, a, StageHalf), g.open(t, a, StageEnroll), g.open(t, a, StageFull)

	g.expect(t, "/full", "", 401, "unauthorized")
	g.expect(t, "/full", "not-a-token", 401, "unauthorized")

	g.expect(t, "/half", half, 204, "")
	g.expect(t, "/full", half, 401, "second_factor_required")
	g.expect(t, "/enroll", half, 401, "second_factor_required")
	g.expect(t, "/enroll-or-full", half, 401, "second_factor_required")

	g.expect(t, "/enroll", enroll, 204, "")
	g.expect(t, "/enroll-or-full", enroll, 204, "")
	g.expect(t, "/half", enroll, 403, "enrollment_required")
	g.expect(t, "/full", enroll, 403, "enrollment_required")

	g.expect(t, "/full", full, 204, "")
	g.expect(t, "/enroll-or-full", full, 204, "")
	g.expect(t, "/half", full, 409, "already_verified")
	g.expect(t, "/enroll", full, 409, "already_verified")

	// An unauthorized answer clears the cookie.
	w := g.call("/full", "not-a-token")
	if c := w.Result().Cookies(); len(c) != 1 || c[0].MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %v", c)
	}
}

// Why: DynamoDB TTL is lazy, so the half-session limit has to be enforced in code.
func TestHalfExpiresAfterTenMinutes(t *testing.T) {
	g := newRig(t)
	tok := g.open(t, g.admin(t, "huy"), StageHalf)
	g.clk.t = g.clk.t.Add(9*time.Minute + 59*time.Second)
	g.expect(t, "/half", tok, 204, "")
	g.clk.t = g.clk.t.Add(2 * time.Second)
	g.expect(t, "/half", tok, 401, "unauthorized")
	if _, err := g.st.GetSession(context.Background(), hashToken(tok)); err != ErrSessionNotFound {
		t.Fatalf("expired item not deleted: %v", err)
	}
}

// Why: activity may keep a session alive, but never past the absolute limit.
func TestFullIdleThirtyAndAbsoluteEightHours(t *testing.T) {
	g := newRig(t)
	a := g.admin(t, "huy")

	idle := g.open(t, a, StageFull)
	g.clk.t = g.clk.t.Add(29 * time.Minute)
	g.expect(t, "/full", idle, 204, "")
	g.clk.t = g.clk.t.Add(29 * time.Minute) // touched 29 min ago, so still alive
	g.expect(t, "/full", idle, 204, "")
	g.clk.t = g.clk.t.Add(31 * time.Minute)
	g.expect(t, "/full", idle, 401, "unauthorized")

	g.clk.t = time.Date(2026, 11, 7, 9, 0, 0, 0, time.UTC)
	long := g.open(t, a, StageFull)
	start := g.clk.t
	for g.clk.t.Add(20 * time.Minute).Before(start.Add(8 * time.Hour)) {
		g.clk.t = g.clk.t.Add(20 * time.Minute)
		g.expect(t, "/full", long, 204, "")
	}
	g.clk.t = start.Add(8*time.Hour - time.Second)
	g.expect(t, "/full", long, 204, "")
	g.clk.t = start.Add(8 * time.Hour)
	g.expect(t, "/full", long, 401, "unauthorized")
}

// Why: Review Focus 3 - a tab left open must lose access when a master resets the admin.
func TestEndSessionsEndsEveryOpenSession(t *testing.T) {
	g := newRig(t)
	a := g.admin(t, "huy")
	t1, t2 := g.open(t, a, StageFull), g.open(t, a, StageFull)
	other := g.open(t, g.admin(t, "lan"), StageFull)
	g.expect(t, "/full", t1, 204, "")
	g.expect(t, "/full", t2, 204, "")
	if err := g.st.EndSessions(context.Background(), "huy"); err != nil {
		t.Fatal(err)
	}
	g.expect(t, "/full", t1, 401, "unauthorized")
	g.expect(t, "/full", t2, 401, "unauthorized")
	g.expect(t, "/full", other, 204, "")
	// A session opened after the reset carries the new epoch and works.
	fresh, _ := g.st.GetAdmin(context.Background(), "huy")
	g.expect(t, "/full", g.open(t, fresh, StageFull), 204, "")
}

// Why: disabling an admin has to cut them off now, and enabling must not revive old sessions.
func TestDisabledAdminEndsSessionAtOnce(t *testing.T) {
	g := newRig(t)
	ctx := context.Background()
	a := g.admin(t, "huy")
	master := mkAdmin(t, g.st, "boss", RoleMaster)
	tok := g.open(t, a, StageFull)
	g.expect(t, "/full", tok, 204, "")
	at := g.clk.t
	if err := g.st.SetDisabled(ctx, &master, "huy", &at); err != nil {
		t.Fatal(err)
	}
	g.expect(t, "/full", tok, 401, "unauthorized")
	if err := g.st.SetDisabled(ctx, &master, "huy", nil); err != nil {
		t.Fatal(err)
	}
	g.expect(t, "/full", tok, 401, "unauthorized")
}

// Why: the cookie attributes are what keep the session out of scripts, other sites and subdomains.
func TestCookieAttributes(t *testing.T) {
	g := newRig(t)
	a := g.admin(t, "huy")
	w := httptest.NewRecorder()
	if err := g.app.startSession(context.Background(), w, a, StageHalf); err != nil {
		t.Fatal(err)
	}
	h := w.Header().Get("Set-Cookie")
	for _, want := range []string{"__Host-console_session=", "HttpOnly", "Secure", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(h, want) {
			t.Errorf("Set-Cookie %q lacks %q", h, want)
		}
	}
	if strings.Contains(h, "Domain") {
		t.Errorf("Set-Cookie %q has a Domain", h)
	}
	g.app.Config.Dev = true
	w = httptest.NewRecorder()
	if err := g.app.startSession(context.Background(), w, a, StageHalf); err != nil {
		t.Fatal(err)
	}
	h = w.Header().Get("Set-Cookie")
	if !strings.HasPrefix(h, "console_session=") || strings.Contains(h, "Secure") {
		t.Errorf("dev cookie = %q", h)
	}
}

// Why: a leaked table must not hand out live sessions, so the raw token may never be stored.
func TestStoredKeyIsTheHashNotTheToken(t *testing.T) {
	g := newRig(t)
	a := g.admin(t, "huy")
	tok := g.open(t, a, StageHalf)
	// Promote, so the old and new items are both exercised.
	x, err := g.st.GetSession(context.Background(), hashToken(tok))
	if err != nil {
		t.Fatalf("session not under sha256(token): %v", err)
	}
	pw := httptest.NewRecorder()
	if err := g.app.promote(context.Background(), pw, x, StageFull); err != nil {
		t.Fatal(err)
	}
	tok2 := pw.Result().Cookies()[0].Value
	if _, err := g.st.GetSession(context.Background(), hashToken(tok)); err != ErrSessionNotFound {
		t.Fatalf("old session survives promote: %v", err)
	}
	g.expect(t, "/full", tok2, 204, "")

	var start map[string]types.AttributeValue
	for {
		out, err := g.st.DB.Scan(context.Background(), &dynamodb.ScanInput{TableName: aws.String(g.st.Table), ExclusiveStartKey: start})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range out.Items {
			for k, v := range it {
				if sv, ok := v.(*types.AttributeValueMemberS); ok && (strings.Contains(sv.Value, tok) || strings.Contains(sv.Value, tok2)) {
					t.Fatalf("raw token found in attribute %s", k)
				}
			}
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
}
