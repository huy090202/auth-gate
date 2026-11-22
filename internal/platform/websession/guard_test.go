package websession

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// fakeAccounts is the account loader: one account per session's "acct" attribute.
type fakeAccounts struct {
	epoch   map[string]int
	refused map[string]bool
	limited bool     // Limit refuses every request
	spent   []string // the accounts Limit was called for, in order
}

type guardEnv struct {
	g    Guard
	acc  *fakeAccounts
	now  time.Time
	hits int // requests that reached the handler
}

func newGuardEnv(t *testing.T) *guardEnv {
	t.Helper()
	db, table := dynamotest.NewTable(t)
	e := &guardEnv{acc: &fakeAccounts{epoch: map[string]int{}, refused: map[string]bool{}}, now: time.Now()}
	e.g = Guard{
		Store: Store{DB: db, Table: table, Prefix: "SES#"}, CookieName: "s", Now: func() time.Time { return e.now },
		Load: func(_ context.Context, x Session) (any, int, bool, error) {
			id := x.Extra["acct"]
			ep, ok := e.acc.epoch[id]
			return id, ep, ok && !e.acc.refused[id], nil
		},
		Limit: func(_ context.Context, _ Session, acct any) error {
			e.acc.spent = append(e.acc.spent, acct.(string))
			if e.acc.limited {
				return &webapi.Error{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: 1}
			}
			return nil
		},
		Attach: func(ctx context.Context, _ Session, _ any) context.Context { return ctx },
	}
	return e
}

// start opens a session of the account at the stage and returns its cookie value.
func (e *guardEnv) start(t *testing.T, acct string, st Stage) string {
	t.Helper()
	if _, ok := e.acc.epoch[acct]; !ok {
		e.acc.epoch[acct] = 0
	}
	rec := httptest.NewRecorder()
	if err := e.g.Start(context.Background(), rec, Session{Stage: st, Epoch: e.acc.epoch[acct], Extra: map[string]string{"acct": acct}}); err != nil {
		t.Fatal(err)
	}
	return rec.Result().Cookies()[0].Value
}

// call runs one request with the cookie through a route that accepts the stages.
func (e *guardEnv) call(cookie string, stages ...Stage) (int, string) {
	h := e.g.Middleware([]string{"acct"}, stages...)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		e.hits++
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "s", Value: cookie})
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body struct{ Error string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body.Error
}

// Why: the guard is the one place every signed-in route passes; its stage rules decide what a half or enrolling
// session may reach, and a refused account or a moved epoch must end the session, whatever the route.
func TestGuardStageAndRefusalRules(t *testing.T) {
	e := newGuardEnv(t)
	cases := []struct {
		have   Stage
		want   []Stage
		status int
		code   string
	}{
		{StageHalf, []Stage{StageHalf}, 204, ""},
		{StageHalf, []Stage{StageFull}, 401, "second_factor_required"},
		{StageEnroll, []Stage{StageFull}, 403, "enrollment_required"},
		{StageEnroll, []Stage{StageEnroll, StageFull}, 204, ""},
		{StageFull, []Stage{StageHalf}, 409, "already_verified"},
		{StageFull, []Stage{StageFull}, 204, ""},
	}
	for _, c := range cases {
		ck := e.start(t, "a", c.have)
		if st, code := e.call(ck, c.want...); st != c.status || code != c.code {
			t.Errorf("%s on %v = %d %q, want %d %q", c.have, c.want, st, code, c.status, c.code)
		}
	}
	if st, code := e.call(""); st != 401 || code != "unauthorized" {
		t.Errorf("no cookie = %d %q", st, code)
	}

	refused := e.start(t, "r", StageFull)
	e.acc.refused["r"] = true
	moved := e.start(t, "m", StageFull)
	e.acc.epoch["m"]++
	expired := e.start(t, "x", StageHalf)
	for name, ck := range map[string]string{"refused account": refused, "epoch moved": moved} {
		if st, _ := e.call(ck, StageFull); st != 401 {
			t.Errorf("%s = %d", name, st)
		}
	}
	e.acc.refused["r"], e.acc.epoch["m"] = false, e.acc.epoch["m"]-1
	for name, ck := range map[string]string{"refused account": refused, "epoch moved": moved} {
		if st, _ := e.call(ck, StageFull); st != 401 {
			t.Errorf("%s, once lifted = %d: the session must have been ended", name, st)
		}
	}
	e.now = e.now.Add(HalfTTL)
	if st, _ := e.call(expired, StageHalf); st != 401 {
		t.Errorf("expired half session = %d", st)
	}
}

// Why: every session that passed the account checks spends its account's bucket, whatever its stage and even when
// the route refuses that stage, so a half or enrolling session cannot call the guarded routes without a limit.
// A session refused before that (no account, moved epoch) spends nothing.
func TestGuardLimitsEveryStage(t *testing.T) {
	e := newGuardEnv(t)
	half, enroll, full := e.start(t, "h", StageHalf), e.start(t, "e", StageEnroll), e.start(t, "f", StageFull)
	e.call(half, StageHalf)
	e.call(enroll, StageEnroll, StageFull)
	e.call(full, StageFull)
	if st, code := e.call(full, StageHalf); st != 409 || code != "already_verified" {
		t.Fatalf("full on a half route = %d %q", st, code)
	}
	if got := strings.Join(e.acc.spent, ","); got != "h,e,f,f" {
		t.Fatalf("spent = %s, want h,e,f,f", got)
	}

	gone := e.start(t, "g", StageFull)
	e.acc.refused["g"] = true
	e.call(gone, StageFull)
	if got := strings.Join(e.acc.spent, ","); got != "h,e,f,f" {
		t.Fatalf("a refused account spent: %s", got)
	}

	e.acc.limited = true
	hits := e.hits
	for _, ck := range []string{half, enroll, full} {
		if st, code := e.call(ck, StageHalf, StageEnroll, StageFull); st != 429 || code != "rate_limited" {
			t.Errorf("limited = %d %q", st, code)
		}
	}
	if e.hits != hits {
		t.Fatal("a limited request reached the handler")
	}
}
