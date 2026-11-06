package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/auth/challenge"
	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/auth/factor"
	"github.com/huy090202/auth-gate/internal/auth/handler"
	"github.com/huy090202/auth-gate/internal/auth/service"
	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

const adminKey = "admin-secret"

type env struct {
	verify, admin http.Handler
	clients       *client.Service
	db            *dynamodb.Client
	table         string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, table := dynamotest.NewTable(t)
	clients := &client.Service{Store: client.NewDynamoStore(db, table)}
	svc := &service.Service{
		Clients:    clients,
		Factors:    factor.NewDynamoStore(db, table),
		Challenges: challenge.NewDynamoStore(db, table),
	}
	return &env{
		verify:  handler.NewVerify(svc, clients, &ratelimit.Limiter{DB: db, Table: table}),
		admin:   handler.NewAdmin(svc, adminKey),
		clients: clients, db: db, table: table,
	}
}

// newClient creates a client and sets its bucket through the clients service.
func (e *env) newClient(t *testing.T, s ratelimit.Settings) (client.Client, string) {
	t.Helper()
	c, raw, err := e.clients.Create(context.Background(), client.CreateInput{Name: "shop", RPID: "example.com", Origins: []string{"https://app.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.clients.Update(context.Background(), c.ID, client.Patch{RateLimit: &s}); err != nil {
		t.Fatal(err)
	}
	return c, raw
}

func do(h http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("x-internal-access-key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// call hits a cheap verify route that needs no signer.
func (e *env) call(key string) *httptest.ResponseRecorder {
	return do(e.verify, http.MethodGet, "/api/factors/u1", key, "")
}

func TestVerifyAnswers429WithRetryAfter(t *testing.T) {
	e := newEnv(t)
	_, key := e.newClient(t, ratelimit.Settings{Burst: 1, PerSecond: 0.5})
	if w := e.call(key); w.Code != http.StatusOK {
		t.Fatalf("first call: %d %s", w.Code, w.Body)
	}
	w := e.call(key)
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second call: %d", w.Code)
	}
	if got := w.Body.String(); got != "{\"error\":\"rate_limited\"}\n" {
		t.Fatalf("body %q", got)
	}
	if got := w.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After %q, want 2", got)
	}
}

func TestOneClientBucketDoesNotTouchAnother(t *testing.T) {
	e := newEnv(t)
	_, a := e.newClient(t, ratelimit.Settings{Burst: 1, PerSecond: 0.01})
	_, b := e.newClient(t, ratelimit.Settings{Burst: 1, PerSecond: 0.01})
	e.call(a)
	if w := e.call(a); w.Code != http.StatusTooManyRequests {
		t.Fatalf("a should be limited: %d", w.Code)
	}
	if w := e.call(b); w.Code != http.StatusOK {
		t.Fatalf("b should be untouched: %d", w.Code)
	}
}

func TestUnknownKeyTouchesNoBucket(t *testing.T) {
	e := newEnv(t)
	if w := e.call("ag_unknown"); w.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", w.Code)
	}
	out, err := e.db.Scan(context.Background(), &dynamodb.ScanInput{
		TableName:        aws.String(e.table),
		FilterExpression: aws.String("begins_with(pk, :p)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":p": &types.AttributeValueMemberS{Value: "RL#"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Count != 0 {
		t.Fatalf("found %d RL# items", out.Count)
	}
}

func TestAdminRoutesAreNotLimited(t *testing.T) {
	e := newEnv(t)
	c, _ := e.newClient(t, ratelimit.Settings{Burst: 1, PerSecond: 0.01})
	for i := 0; i < 3; i++ {
		if w := do(e.admin, http.MethodGet, "/api/clients/"+c.ID, adminKey, ""); w.Code != http.StatusOK {
			t.Fatalf("admin GET %d: %d", i, w.Code)
		}
	}
}

func TestPatchRateLimit(t *testing.T) {
	e := newEnv(t)
	c, key := e.newClient(t, client.DefaultRateLimit())
	path := "/api/clients/" + c.ID

	w := do(e.admin, http.MethodPatch, path, adminKey, `{"rateLimit":{"burst":5,"perSecond":0}}`)
	if w.Code != http.StatusBadRequest || w.Body.String() != "{\"error\":\"invalid_client\"}\n" {
		t.Fatalf("perSecond 0: %d %s", w.Code, w.Body)
	}

	w = do(e.admin, http.MethodPatch, path, adminKey, `{"rateLimit":{"burst":1,"perSecond":0.5}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("valid patch: %d %s", w.Code, w.Body)
	}
	var view struct {
		RateLimit struct {
			Burst     int     `json:"burst"`
			PerSecond float64 `json:"perSecond"`
		} `json:"rateLimit"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.RateLimit.Burst != 1 || view.RateLimit.PerSecond != 0.5 {
		t.Fatalf("view %+v", view.RateLimit)
	}

	if w := e.call(key); w.Code != http.StatusOK {
		t.Fatalf("first verify call: %d", w.Code)
	}
	if w := e.call(key); w.Code != http.StatusTooManyRequests {
		t.Fatalf("second verify call should use the new bucket: %d", w.Code)
	}
}

func TestPatchUnknownClient(t *testing.T) {
	e := newEnv(t)
	w := do(e.admin, http.MethodPatch, "/api/clients/01HZZZZZZZZZZZZZZZZZZZZZZZ", adminKey, `{"rateLimit":{"burst":5,"perSecond":1}}`)
	if w.Code != http.StatusNotFound || w.Body.String() != "{\"error\":\"client_not_found\"}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestPatchEmptyBody(t *testing.T) {
	e := newEnv(t)
	c, _ := e.newClient(t, client.DefaultRateLimit())
	w := do(e.admin, http.MethodPatch, "/api/clients/"+c.ID, adminKey, `{}`)
	if w.Code != http.StatusBadRequest || w.Body.String() != "{\"error\":\"invalid_client\"}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}
