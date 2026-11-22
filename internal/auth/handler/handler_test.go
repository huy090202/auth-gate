package handler_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/auth/challenge"
	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/auth/factor"
	"github.com/huy090202/auth-gate/internal/auth/handler"
	"github.com/huy090202/auth-gate/internal/auth/service"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

const (
	adminKey  = "admin-secret"
	portalKey = "portal-secret"
)

type env struct {
	verify, admin, portal http.Handler
	clients               *client.Service
	db                    *dynamodb.Client
	table                 string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, table := dynamotest.NewTable(t)
	// A nanosecond owner cache makes a suspension refuse keys on the next call.
	clients := &client.Service{Store: client.NewDynamoStore(db, table), OwnerCacheTTL: time.Nanosecond}
	svc := &service.Service{
		Clients:    clients,
		Factors:    factor.NewDynamoStore(db, table),
		Challenges: challenge.NewDynamoStore(db, table),
	}
	return &env{
		verify:  handler.NewVerify(svc, clients, &ratelimit.Limiter{DB: db, Table: table}),
		admin:   handler.NewAdmin(svc, adminKey),
		portal:  handler.NewPortal(svc, portalKey),
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
	return doTenant(h, method, path, key, "", body)
}

// doTenant is do with an x-tenant-id header, sent when tenant is not empty.
func doTenant(h http.Handler, method, path, key, tenant, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		r.Header.Set("x-internal-access-key", key)
	}
	if tenant != "" {
		r.Header.Set("x-tenant-id", tenant)
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

// A WebAuthn user handle is at most 64 bytes, so a longer subject must be a 400, not a 500.
func TestBeginRegistrationSubjectLength(t *testing.T) {
	e := newEnv(t)
	_, key := e.newClient(t, ratelimit.Settings{Burst: 100, PerSecond: 100})
	post := func(n int) *httptest.ResponseRecorder {
		return do(e.verify, http.MethodPost, "/api/webauthn/registrations", key,
			`{"subjectId":"`+strings.Repeat("a", n)+`","displayName":"x"}`)
	}
	if w := post(65); w.Code != http.StatusBadRequest || strings.TrimSpace(w.Body.String()) != `{"error":"invalid_id"}` {
		t.Fatalf("65 bytes: got %d %s", w.Code, w.Body.String())
	}
	if w := post(64); w.Code != http.StatusCreated {
		t.Fatalf("64 bytes: got %d %s", w.Code, w.Body.String())
	}
}

func fast() ratelimit.Settings { return ratelimit.Settings{Burst: 1000, PerSecond: 1000} }

func (e *env) admin_(method, path, body string) *httptest.ResponseRecorder {
	return do(e.admin, method, path, adminKey, body)
}

type listPage struct {
	Clients []struct {
		ClientID   string  `json:"clientId"`
		Name       string  `json:"name"`
		DisabledAt *string `json:"disabledAt"`
	} `json:"clients"`
	NextCursor *string `json:"nextCursor"`
}

func (e *env) list(t *testing.T, query string) listPage {
	t.Helper()
	w := e.admin_(http.MethodGet, "/api/clients"+query, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	var p listPage
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *env) rotate(t *testing.T, id string) string {
	t.Helper()
	w := e.admin_(http.MethodPost, "/api/clients/"+id+"/keys", "")
	var b struct {
		AccessKey string `json:"accessKey"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &b) != nil || b.AccessKey == "" {
		t.Fatalf("rotate: %d %s", w.Code, w.Body)
	}
	return b.AccessKey
}

func TestListPagesNewestFirstWithoutKeyMaterial(t *testing.T) {
	e := newEnv(t)
	e.clients.PageSize = 2
	c1, _ := e.newClient(t, fast())
	c2, _ := e.newClient(t, fast())
	c3, _ := e.newClient(t, fast())

	w := e.admin_(http.MethodGet, "/api/clients", "")
	body := strings.ToLower(w.Body.String())
	if strings.Contains(body, "hash") || strings.Contains(body, "accesskey") || strings.Contains(body, "retired") {
		t.Fatalf("list leaks key material: %s", w.Body)
	}
	p := e.list(t, "")
	if len(p.Clients) != 2 || p.Clients[0].ClientID != c3.ID || p.Clients[1].ClientID != c2.ID || p.NextCursor == nil {
		t.Fatalf("page 1: %s", w.Body)
	}
	p = e.list(t, "?cursor="+url.QueryEscape(*p.NextCursor))
	if len(p.Clients) != 1 || p.Clients[0].ClientID != c1.ID || p.NextCursor != nil {
		t.Fatalf("page 2: %+v", p)
	}
}

func TestListBadCursor(t *testing.T) {
	e := newEnv(t)
	for _, cur := range []string{"!!!", base64.RawURLEncoding.EncodeToString([]byte("a#b"))} {
		w := e.admin_(http.MethodGet, "/api/clients?cursor="+url.QueryEscape(cur), "")
		if w.Code != http.StatusBadRequest || w.Body.String() != "{\"error\":\"invalid_cursor\"}\n" {
			t.Fatalf("cursor %q: %d %s", cur, w.Code, w.Body)
		}
	}
}

func TestPatchKeepsKeyHashesAndRejectsRPID(t *testing.T) {
	e := newEnv(t)
	c, raw := e.newClient(t, fast())
	path := "/api/clients/" + c.ID
	// An attribute Client does not model: a whole-item rewrite would drop it.
	key := dynamo.Key("C#"+c.ID, "META")
	if _, err := e.db.UpdateItem(context.Background(), &dynamodb.UpdateItemInput{
		TableName: aws.String(e.table), Key: key,
		UpdateExpression:          aws.String("SET probeAttr = :v"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":v": &types.AttributeValueMemberS{Value: "keep"}},
	}); err != nil {
		t.Fatal(err)
	}
	// One patch without a name change (single UpdateItem), one with (transaction).
	for _, body := range []string{`{"tokenTtlSec":60}`, `{"name":"renamed"}`} {
		if w := e.admin_(http.MethodPatch, path, body); w.Code != http.StatusOK {
			t.Fatalf("patch %s: %d %s", body, w.Code, w.Body)
		}
	}
	for _, body := range []string{`{"rpId":"x.com"}`, `{"rpId":null}`} {
		w := e.admin_(http.MethodPatch, path, body)
		if w.Code != http.StatusBadRequest || w.Body.String() != "{\"error\":\"invalid_client\"}\n" {
			t.Fatalf("patch %s: %d %s", body, w.Code, w.Body)
		}
	}
	out, err := e.db.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(e.table), Key: key, ConsistentRead: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := out.Item["probeAttr"].(*types.AttributeValueMemberS); !ok || v.Value != "keep" {
		t.Fatalf("patch dropped an attribute it does not own: %v", out.Item["probeAttr"])
	}
	hashes, _ := out.Item["keyHashes"].(*types.AttributeValueMemberSS)
	if hashes == nil || len(hashes.Value) != 1 || hashes.Value[0] != client.HashKey(raw) {
		t.Fatalf("keyHashes %v, want the original hash", out.Item["keyHashes"])
	}
	if w := e.call(raw); w.Code != http.StatusOK {
		t.Fatalf("old key after patch: %d", w.Code)
	}
}

func TestPatchOriginsStillPassSuffixCheck(t *testing.T) {
	e := newEnv(t)
	c, _ := e.newClient(t, fast())
	path := "/api/clients/" + c.ID
	for _, body := range []string{`{"origins":["https://other.com"]}`, `{"origins":[]}`} {
		w := e.admin_(http.MethodPatch, path, body)
		if w.Code != http.StatusBadRequest || w.Body.String() != "{\"error\":\"invalid_client\"}\n" {
			t.Fatalf("patch %s: %d %s", body, w.Code, w.Body)
		}
	}
	w := e.admin_(http.MethodPatch, path, `{"origins":["https://b.example.com"]}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"origins":["https://b.example.com"]`) {
		t.Fatalf("valid origins: %d %s", w.Code, w.Body)
	}
}

func TestPatchNameUpdatesIndex(t *testing.T) {
	e := newEnv(t)
	c, _ := e.newClient(t, fast())
	if w := e.admin_(http.MethodPatch, "/api/clients/"+c.ID, `{"name":"renamed"}`); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if p := e.list(t, ""); len(p.Clients) != 1 || p.Clients[0].Name != "renamed" {
		t.Fatalf("list %+v", p)
	}
}

func TestPatchPinPolicyMergesZeroFields(t *testing.T) {
	e := newEnv(t)
	c, _ := e.newClient(t, fast())
	path := "/api/clients/" + c.ID
	pol := func(body string) string {
		t.Helper()
		w := e.admin_(http.MethodPatch, path, body)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body)
		}
		var v struct {
			PinPolicy json.RawMessage `json:"pinPolicy"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return string(v.PinPolicy)
	}
	if got := pol(`{"pinPolicy":{"length":8}}`); got != `{"length":8,"maxAttempts":5,"lockoutSec":900}` {
		t.Fatalf("got %s", got)
	}
	if got := pol(`{"pinPolicy":{"maxAttempts":3}}`); got != `{"length":8,"maxAttempts":3,"lockoutSec":900}` {
		t.Fatalf("got %s", got)
	}
}

func TestDisabledKeyAnswersSameAsUnknown(t *testing.T) {
	e := newEnv(t)
	c, key := e.newClient(t, fast())
	w := e.admin_(http.MethodPost, "/api/clients/"+c.ID+"/disable", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"disabledAt"`) {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	got, unknown := e.call(key), e.call("ag_unknown")
	if got.Code != http.StatusUnauthorized || got.Body.String() != unknown.Body.String() {
		t.Fatalf("disabled %d %q, unknown %d %q", got.Code, got.Body, unknown.Code, unknown.Body)
	}
}

func TestEnableRestoresKeys(t *testing.T) {
	e := newEnv(t)
	c, key := e.newClient(t, fast())
	e.admin_(http.MethodPost, "/api/clients/"+c.ID+"/disable", "")
	w := e.admin_(http.MethodPost, "/api/clients/"+c.ID+"/enable", "")
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "disabledAt") {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	if w := e.call(key); w.Code != http.StatusOK {
		t.Fatalf("key after enable: %d", w.Code)
	}
	if p := e.list(t, ""); len(p.Clients) != 1 || p.Clients[0].DisabledAt != nil {
		t.Fatalf("list %+v", p)
	}
}

func TestDisableTwiceKeepsFirstTime(t *testing.T) {
	e := newEnv(t)
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	now := t0
	e.clients.Now = func() time.Time { return now }
	c, _ := e.newClient(t, fast())
	path := "/api/clients/" + c.ID + "/disable"
	e.admin_(http.MethodPost, path, "")
	now = t0.Add(time.Hour)
	w := e.admin_(http.MethodPost, path, "")
	if !strings.Contains(w.Body.String(), `"disabledAt":"2026-11-06T10:00:00Z"`) {
		t.Fatalf("second disable: %d %s", w.Code, w.Body)
	}
	if p := e.list(t, ""); p.Clients[0].DisabledAt == nil || *p.Clients[0].DisabledAt != "2026-11-06T10:00:00Z" {
		t.Fatalf("list %+v", p)
	}
}

func TestDisableUnknownClient(t *testing.T) {
	e := newEnv(t)
	for _, act := range []string{"disable", "enable"} {
		w := e.admin_(http.MethodPost, "/api/clients/01HZZZZZZZZZZZZZZZZZZZZZZZ/"+act, "")
		if w.Code != http.StatusNotFound || w.Body.String() != "{\"error\":\"client_not_found\"}\n" {
			t.Fatalf("%s: %d %s", act, w.Code, w.Body)
		}
	}
}

func TestRotateDoesNotReEnable(t *testing.T) {
	e := newEnv(t)
	c, old := e.newClient(t, fast())
	e.admin_(http.MethodPost, "/api/clients/"+c.ID+"/disable", "")
	fresh := e.rotate(t, c.ID)
	if w := e.call(fresh); w.Code != http.StatusUnauthorized {
		t.Fatalf("new key of a disabled client: %d", w.Code)
	}
	if w := e.call(old); w.Code != http.StatusUnauthorized {
		t.Fatalf("old key of a disabled client: %d", w.Code)
	}
	if w := e.admin_(http.MethodGet, "/api/clients/"+c.ID, ""); !strings.Contains(w.Body.String(), `"disabledAt"`) {
		t.Fatalf("disabledAt lost: %s", w.Body)
	}
}

func TestTwoClientsAreIsolated(t *testing.T) {
	e := newEnv(t)
	_, a := e.newClient(t, fast())
	_, b := e.newClient(t, fast())
	if w := do(e.verify, http.MethodPost, "/api/factors/pin", b, `{"subjectId":"u1","pin":"493817"}`); w.Code != http.StatusCreated {
		t.Fatalf("enroll: %d %s", w.Code, w.Body)
	}
	w := do(e.verify, http.MethodPost, "/api/challenges", b, `{"subjectId":"u1","purpose":"login","factor":"pin"}`)
	var ch struct {
		ChallengeID string `json:"challengeId"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &ch) != nil {
		t.Fatalf("challenge: %d %s", w.Code, w.Body)
	}
	if w := e.call(a); w.Body.String() != "{\"pin\":false,\"webauthn\":[]}\n" {
		t.Fatalf("A sees B's factors: %s", w.Body)
	}
	if w := do(e.verify, http.MethodPost, "/api/challenges", a, `{"subjectId":"u1","purpose":"login","factor":"pin"}`); w.Code != http.StatusNotFound {
		t.Fatalf("A opened a PIN challenge on B's subject: %d %s", w.Code, w.Body)
	}
	if w := do(e.verify, http.MethodPost, "/api/challenges/"+ch.ChallengeID+"/verify", a, `{"pin":"493817"}`); w.Code != http.StatusGone {
		t.Fatalf("A verified B's challenge: %d %s", w.Code, w.Body)
	}
}

func TestRevokePreviousKeysEndsGraceAtOnce(t *testing.T) {
	e := newEnv(t)
	c, k1 := e.newClient(t, fast())
	k2 := e.rotate(t, c.ID)
	k3 := e.rotate(t, c.ID)
	for _, k := range []string{k1, k2, k3} {
		if w := e.call(k); w.Code != http.StatusOK {
			t.Fatalf("key within grace: %d", w.Code)
		}
	}
	path := "/api/clients/" + c.ID + "/keys/previous"
	if w := e.admin_(http.MethodDelete, path, ""); w.Code != http.StatusOK || w.Body.String() != "{\"revoked\":2}\n" {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	for _, k := range []string{k1, k2} {
		if w := e.call(k); w.Code != http.StatusUnauthorized {
			t.Fatalf("revoked key: %d", w.Code)
		}
	}
	if w := e.call(k3); w.Code != http.StatusOK {
		t.Fatalf("current key: %d", w.Code)
	}
	if w := e.admin_(http.MethodDelete, path, ""); w.Body.String() != "{\"revoked\":0}\n" {
		t.Fatalf("second revoke: %d %s", w.Code, w.Body)
	}
	if w := e.admin_(http.MethodGet, "/api/clients/"+c.ID, ""); strings.Contains(strings.ToLower(w.Body.String()), "retired") {
		t.Fatalf("view leaks retiredKeys: %s", w.Body)
	}
}

func TestRevokeLeavesDisabledClientDisabled(t *testing.T) {
	e := newEnv(t)
	c, _ := e.newClient(t, fast())
	k2 := e.rotate(t, c.ID)
	e.admin_(http.MethodPost, "/api/clients/"+c.ID+"/disable", "")
	if w := e.admin_(http.MethodDelete, "/api/clients/"+c.ID+"/keys/previous", ""); w.Code != http.StatusOK || w.Body.String() != "{\"revoked\":1}\n" {
		t.Fatalf("revoke: %d %s", w.Code, w.Body)
	}
	if w := e.call(k2); w.Code != http.StatusUnauthorized {
		t.Fatalf("current key of a disabled client: %d", w.Code)
	}
	if w := e.admin_(http.MethodGet, "/api/clients/"+c.ID, ""); !strings.Contains(w.Body.String(), `"disabledAt"`) {
		t.Fatalf("disabledAt lost: %s", w.Body)
	}
}

func TestRevokeUnknownClient(t *testing.T) {
	e := newEnv(t)
	w := e.admin_(http.MethodDelete, "/api/clients/01HZZZZZZZZZZZZZZZZZZZZZZZ/keys/previous", "")
	if w.Code != http.StatusNotFound || w.Body.String() != "{\"error\":\"client_not_found\"}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestMalformedDisabledAtDeniesKey(t *testing.T) {
	e := newEnv(t)
	c, key := e.newClient(t, fast())
	for _, v := range []types.AttributeValue{
		&types.AttributeValueMemberS{Value: "not a time"},
		&types.AttributeValueMemberN{Value: "1"},
	} {
		if _, err := e.db.UpdateItem(context.Background(), &dynamodb.UpdateItemInput{
			TableName: aws.String(e.table), Key: dynamo.Key("C#"+c.ID, "META"),
			UpdateExpression:          aws.String("SET disabledAt = :v"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":v": v},
		}); err != nil {
			t.Fatal(err)
		}
		if w := e.call(key); w.Code != http.StatusUnauthorized {
			t.Fatalf("disabledAt %v: key answered %d, want 401", v, w.Code)
		}
	}
}

type afterGetStore struct {
	client.Store
	f func()
}

func (s *afterGetStore) GetClient(ctx context.Context, id string) (client.Client, error) {
	c, err := s.Store.GetClient(ctx, id)
	if f := s.f; f != nil && err == nil {
		s.f = nil
		f()
	}
	return c, err
}

func TestRotateConflictAnswers409(t *testing.T) {
	e := newEnv(t)
	seam := &afterGetStore{Store: e.clients.Store}
	e.clients.Store = seam
	c, _ := e.newClient(t, fast())
	seam.f = func() { e.rotate(t, c.ID) }
	w := e.admin_(http.MethodPost, "/api/clients/"+c.ID+"/keys", "")
	if w.Code != http.StatusConflict || w.Body.String() != "{\"error\":\"rotate_conflict\"}\n" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

const newClientBody = `{"name":"shop","rpId":"example.com","origins":["https://app.example.com"]}`

func (e *env) portal_(method, path, tenant, body string) *httptest.ResponseRecorder {
	return doTenant(e.portal, method, path, portalKey, tenant, body)
}

// portalCreate creates a client as tenant and returns its id and first key.
func (e *env) portalCreate(t *testing.T, tenant string) (string, string) {
	t.Helper()
	w := e.portal_(http.MethodPost, "/api/portal/clients", tenant, newClientBody)
	var b struct {
		ClientID  string `json:"clientId"`
		AccessKey string `json:"accessKey"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &b) != nil || b.ClientID == "" || b.AccessKey == "" {
		t.Fatalf("portal create: %d %s", w.Code, w.Body)
	}
	return b.ClientID, b.AccessKey
}

func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status || w.Body.String() != `{"error":"`+code+`"}`+"\n" {
		t.Fatalf("got %d %s, want %d %s", w.Code, w.Body, status, code)
	}
}

// Why: this is the tenant boundary. A client of another tenant, of an operator
// or a deleted one must look exactly like an unknown id on every portal route,
// and the refused call must change nothing.
func TestPortalIsolation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, b := id.New(), id.New()
	ownA, _ := e.portalCreate(t, a)
	ofB, keyB := e.portalCreate(t, b)
	op, keyOp := e.newClient(t, fast())
	gone, _ := e.portalCreate(t, a)
	if w := e.portal_(http.MethodDelete, "/api/portal/clients/"+gone, a, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete own: %d %s", w.Code, w.Body)
	}
	// A PIN on each live target, so a purge that got through would show.
	for _, k := range []string{keyB, keyOp} {
		if w := do(e.verify, http.MethodPost, "/api/factors/pin", k, `{"subjectId":"u1","pin":"493817"}`); w.Code != http.StatusCreated {
			t.Fatalf("enroll: %d %s", w.Code, w.Body)
		}
	}
	routes := []struct{ method, suffix, body string }{
		{http.MethodGet, "", ""},
		{http.MethodPatch, "", `{"name":"stolen"}`},
		{http.MethodPost, "/disable", ""},
		{http.MethodPost, "/enable", ""},
		{http.MethodPost, "/keys", ""},
		{http.MethodDelete, "/keys/previous", ""},
		{http.MethodDelete, "/subjects/u1", ""},
		{http.MethodDelete, "", ""},
	}
	targets := []struct{ name, id, key string }{
		{"another tenant's client", ofB, keyB},
		{"an operator's client", op.ID, keyOp},
		{"a deleted client", gone, ""},
	}
	for _, tg := range targets {
		before := e.admin_(http.MethodGet, "/api/clients/"+tg.id, "").Body.String()
		stored, _ := e.clients.Store.GetClient(ctx, tg.id)
		for _, rt := range routes {
			w := e.portal_(rt.method, "/api/portal/clients/"+tg.id+rt.suffix, a, rt.body)
			if w.Code != http.StatusNotFound || w.Body.String() != "{\"error\":\"client_not_found\"}\n" {
				t.Errorf("%s: %s %s: %d %s", tg.name, rt.method, rt.suffix, w.Code, w.Body)
			}
		}
		if after := e.admin_(http.MethodGet, "/api/clients/"+tg.id, "").Body.String(); after != before {
			t.Errorf("%s changed:\n%s\n%s", tg.name, before, after)
		}
		now, _ := e.clients.Store.GetClient(ctx, tg.id)
		if strings.Join(now.KeyHashes, ",") != strings.Join(stored.KeyHashes, ",") || len(now.RetiredKeys) != len(stored.RetiredKeys) {
			t.Errorf("%s: keys changed", tg.name)
		}
		if tg.key == "" {
			continue
		}
		if w := e.call(tg.key); w.Code != http.StatusOK || w.Body.String() != "{\"pin\":true,\"webauthn\":[]}\n" {
			t.Errorf("%s: key or PIN lost: %d %s", tg.name, w.Code, w.Body)
		}
	}
	w := e.portal_(http.MethodGet, "/api/portal/clients", a, "")
	var p listPage
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Clients) != 1 || p.Clients[0].ClientID != ownA {
		t.Fatalf("tenant A's list: %d %s", w.Code, w.Body)
	}
}

// Why: each key opens only its own routes; a leaked portal key must not reach
// the operator's routes, and the admin key must not act as a tenant.
func TestPortalAndAdminKeysDoNotCross(t *testing.T) {
	e := newEnv(t)
	c, _ := e.newClient(t, fast())
	wantError(t, doTenant(e.portal, http.MethodGet, "/api/portal/clients", adminKey, id.New(), ""), http.StatusUnauthorized, "unauthorized")
	wantError(t, do(e.admin, http.MethodGet, "/api/clients", portalKey, ""), http.StatusUnauthorized, "unauthorized")
	wantError(t, do(e.admin, http.MethodDelete, "/api/clients/"+c.ID, portalKey, ""), http.StatusUnauthorized, "unauthorized")
	if w := e.admin_(http.MethodGet, "/api/clients/"+c.ID, ""); w.Code != http.StatusOK {
		t.Fatalf("client after a refused delete: %d %s", w.Code, w.Body)
	}
}

func TestPortalCreateForcesRateLimit(t *testing.T) {
	e := newEnv(t)
	tenant := id.New()
	w := e.portal_(http.MethodPost, "/api/portal/clients", tenant,
		`{"name":"shop","rpId":"example.com","origins":["https://app.example.com"],"rateLimit":{"burst":500,"perSecond":500}}`)
	var created struct {
		ClientID string `json:"clientId"`
	}
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	w = e.admin_(http.MethodGet, "/api/clients/"+created.ClientID, "")
	var view struct {
		Owner     string `json:"owner"`
		RateLimit struct {
			Burst     int     `json:"burst"`
			PerSecond float64 `json:"perSecond"`
		} `json:"rateLimit"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatalf("admin get: %d %s", w.Code, w.Body)
	}
	if view.RateLimit.Burst != 20 || view.RateLimit.PerSecond != 10 || view.Owner != tenant {
		t.Fatalf("view %s", w.Body)
	}
	wantError(t, e.portal_(http.MethodPatch, "/api/portal/clients/"+created.ClientID, tenant, `{"rateLimit":{"burst":20,"perSecond":10}}`),
		http.StatusBadRequest, "invalid_client")
}

func TestPortalTenantHeader(t *testing.T) {
	e := newEnv(t)
	for _, tenant := range []string{"", "nope", strings.ToLower(id.New())} {
		wantError(t, e.portal_(http.MethodGet, "/api/portal/clients", tenant, ""), http.StatusBadRequest, "invalid_tenant")
	}
}

type ownerBody struct {
	TenantID    string  `json:"tenantId"`
	ClientCount int     `json:"clientCount"`
	ClientQuota int     `json:"clientQuota"`
	SuspendedAt *string `json:"suspendedAt"`
}

func (e *env) owner(t *testing.T, method, path, body string) ownerBody {
	t.Helper()
	w := e.admin_(method, path, body)
	var o ownerBody
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &o) != nil {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	return o
}

func TestPortalQuotaAndSuspension(t *testing.T) {
	e := newEnv(t)
	tenant := id.New()
	var first, key string
	for i := 0; i < 5; i++ {
		c, k := e.portalCreate(t, tenant)
		if i == 0 {
			first, key = c, k
		}
	}
	wantError(t, e.portal_(http.MethodPost, "/api/portal/clients", tenant, newClientBody), http.StatusConflict, "quota_exceeded")

	path := "/api/owners/" + tenant
	if o := e.owner(t, http.MethodPatch, path, `{"clientQuota":6}`); o.TenantID != tenant || o.ClientQuota != 6 || o.ClientCount != 5 {
		t.Fatalf("patch quota: %+v", o)
	}
	e.portalCreate(t, tenant)
	if o := e.owner(t, http.MethodGet, path, ""); o.ClientCount != 6 || o.SuspendedAt != nil {
		t.Fatalf("get owner: %+v", o)
	}

	if w := e.call(key); w.Code != http.StatusOK {
		t.Fatalf("key before suspend: %d %s", w.Code, w.Body)
	}
	if o := e.owner(t, http.MethodPost, path+"/suspend", ""); o.SuspendedAt == nil {
		t.Fatalf("suspend: %+v", o)
	}
	wantError(t, e.portal_(http.MethodPost, "/api/portal/clients", tenant, newClientBody), http.StatusConflict, "tenant_suspended")
	wantError(t, e.portal_(http.MethodPost, "/api/portal/clients/"+first+"/keys", tenant, ""), http.StatusConflict, "tenant_suspended")
	if w := e.portal_(http.MethodGet, "/api/portal/clients/"+first, tenant, ""); w.Code != http.StatusOK {
		t.Fatalf("read while suspended: %d %s", w.Code, w.Body)
	}
	wantError(t, e.call(key), http.StatusUnauthorized, "unauthorized")

	if o := e.owner(t, http.MethodPost, path+"/unsuspend", ""); o.SuspendedAt != nil {
		t.Fatalf("unsuspend: %+v", o)
	}
	if w := e.call(key); w.Code != http.StatusOK {
		t.Fatalf("key after unsuspend: %d %s", w.Code, w.Body)
	}
	wantError(t, e.admin_(http.MethodGet, "/api/owners/nope", ""), http.StatusBadRequest, "invalid_id")
	wantError(t, e.admin_(http.MethodPost, "/api/owners/"+strings.ToLower(tenant)+"/suspend", ""), http.StatusBadRequest, "invalid_id")
}

func TestPortalEnableAfterOperatorDisable(t *testing.T) {
	e := newEnv(t)
	tenant := id.New()
	c, _ := e.portalCreate(t, tenant)
	if w := e.admin_(http.MethodPost, "/api/clients/"+c+"/disable", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"disabledBy":"operator"`) {
		t.Fatalf("admin disable: %d %s", w.Code, w.Body)
	}
	wantError(t, e.portal_(http.MethodPost, "/api/portal/clients/"+c+"/enable", tenant, ""), http.StatusConflict, "disabled_by_operator")
	if w := e.admin_(http.MethodGet, "/api/clients/"+c, ""); !strings.Contains(w.Body.String(), `"disabledAt"`) {
		t.Fatalf("still disabled: %s", w.Body)
	}
}

func TestAdminDeleteClient(t *testing.T) {
	e := newEnv(t)
	c, key := e.newClient(t, fast())
	if w := e.admin_(http.MethodDelete, "/api/clients/"+c.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	wantError(t, e.admin_(http.MethodGet, "/api/clients/"+c.ID, ""), http.StatusNotFound, "client_not_found")
	wantError(t, e.call(key), http.StatusUnauthorized, "unauthorized")
	if p := e.list(t, ""); len(p.Clients) != 0 {
		t.Fatalf("list %+v", p)
	}
	wantError(t, e.admin_(http.MethodDelete, "/api/clients/"+c.ID, ""), http.StatusNotFound, "client_not_found")
}

// Why: operator clients predate tenants; the Console and scripts read these
// views, so the new fields must not appear on them.
func TestLegacyClientViewUnchanged(t *testing.T) {
	e := newEnv(t)
	c, _ := e.newClient(t, fast())
	var view map[string]any
	if w := e.admin_(http.MethodGet, "/api/clients/"+c.ID, ""); json.Unmarshal(w.Body.Bytes(), &view) != nil {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	var page struct {
		Clients []map[string]any `json:"clients"`
	}
	if w := e.admin_(http.MethodGet, "/api/clients", ""); json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Clients) != 1 {
		t.Fatalf("list: %d %s", w.Code, w.Body)
	}
	for _, m := range []map[string]any{view, page.Clients[0]} {
		for _, k := range []string{"owner", "disabledBy"} {
			if _, ok := m[k]; ok {
				t.Errorf("%s present in %v", k, m)
			}
		}
	}
}

// beforeExpireStore runs f once, just before the first ExpireKey.
type beforeExpireStore struct {
	client.Store
	f func()
}

func (s *beforeExpireStore) ExpireKey(ctx context.Context, hash string, at time.Time) error {
	if f := s.f; f != nil {
		s.f = nil
		f()
	}
	return s.Store.ExpireKey(ctx, hash, at)
}

// Why: a delete that lands inside a rotate removes the key items the rotate
// is expiring; the rotate must then answer like a missing client, not 500.
func TestRotateRacingDeleteAnswers404(t *testing.T) {
	e := newEnv(t)
	seam := &beforeExpireStore{Store: e.clients.Store}
	e.clients.Store = seam
	tenant := id.New()
	own, _ := e.portalCreate(t, tenant)
	op, _ := e.newClient(t, fast())
	for _, tc := range []struct {
		id   string
		call func() *httptest.ResponseRecorder
	}{
		{own, func() *httptest.ResponseRecorder {
			return e.portal_(http.MethodPost, "/api/portal/clients/"+own+"/keys", tenant, "")
		}},
		{op.ID, func() *httptest.ResponseRecorder { return e.admin_(http.MethodPost, "/api/clients/"+op.ID+"/keys", "") }},
	} {
		seam.f = func() {
			if err := e.clients.Delete(context.Background(), tc.id); err != nil {
				t.Fatal(err)
			}
		}
		wantError(t, tc.call(), http.StatusNotFound, "client_not_found")
	}
}
