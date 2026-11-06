package console_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// captureLogs routes slog to a buffer at debug level until the test ends.
func captureLogs(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}

// scanTable returns every item of the Console's table as text.
func scanTable(t *testing.T, h *consoletest.Harness) string {
	t.Helper()
	var sb strings.Builder
	var start map[string]types.AttributeValue
	for {
		out, err := h.Deps.DB.Scan(context.Background(), &dynamodb.ScanInput{TableName: aws.String(h.Deps.Config.Table), ExclusiveStartKey: start, ConsistentRead: aws.Bool(true)})
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range out.Items {
			for k, v := range it {
				sb.WriteString(k + "=")
				switch v := v.(type) {
				case *types.AttributeValueMemberS:
					sb.WriteString(v.Value)
				case *types.AttributeValueMemberN:
					sb.WriteString(v.Value)
				}
				sb.WriteByte(' ')
			}
			sb.WriteByte('\n')
		}
		if len(out.LastEvaluatedKey) == 0 {
			return sb.String()
		}
		start = out.LastEvaluatedKey
	}
}

const viewerHdr = "CloudFront-Viewer-Address"

type auditView struct {
	AdminID, Username, Action, Target, Result, IP string
}

func auditOf(t *testing.T, b *consoletest.Browser, query string) ([]auditView, consoletest.Response) {
	t.Helper()
	r := b.Do("GET", "/api/audit"+query, nil)
	want(t, r, 200, "")
	var out struct {
		Entries []auditView `json:"entries"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out.Entries, r
}

// Why: a patch that renames only is routine; it must not ask for a token.
func TestPatchNameOnlyNeedsNoStepUp(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	id, _ := newClient(t, b, "app")
	r := b.Do("PATCH", "/api/clients/"+id, map[string]any{"name": "renamed"})
	want(t, r, 200, "")
	if r.Str("name") != "renamed" || clientOf(t, h, id).Name != "renamed" {
		t.Fatalf("not renamed: %s", r.Body)
	}
}

// Why: Go's JSON decoding ignores key case and AuthGate would apply {"Origins":...}, so the Console must not decide from raw keys.
func TestPatchCaseVariantKeyStillNeedsStepUp(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	id, _ := newClient(t, b, "app")
	path := "/api/clients/" + id
	before := clientOf(t, h, id)
	for _, body := range []string{
		`{"Origins":["https://evil.app.test"]}`,
		`{"NAME":"x","rateLimit":{"burst":5,"perSecond":1}}`,
		`{"name":"x","PinPolicy":{"length":4}}`,
		`{"TOKENTTLSEC":900}`,
		`{"origins":[]}`,
	} {
		want(t, b.Do("PATCH", path, json.RawMessage(body)), 403, "step_up_required")
		if !reflect.DeepEqual(before, clientOf(t, h, id)) {
			t.Fatalf("%s changed the client", body)
		}
	}
	// With a token the same spelling is applied, once, under its canonical name.
	r := b.Do("PATCH", path, json.RawMessage(`{"Origins":["https://b.app.test"],"NAME":"x"}`), tokHeader, b.StepUp("update-client"))
	want(t, r, 200, "")
	if c := clientOf(t, h, id); c.Name != "x" || !reflect.DeepEqual(c.Origins, []string{"https://b.app.test"}) {
		t.Fatalf("client = %+v", c)
	}
}

// Why: the relying party id is fixed at creation; the Console must hand it to AuthGate so AuthGate refuses it, however it is spelled.
func TestPatchRPIDRefused(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	id, _ := newClient(t, b, "app")
	path := "/api/clients/" + id
	before := clientOf(t, h, id)
	for _, body := range []string{`{"rpId":"evil.test"}`, `{"RPID":null}`, `{"name":"x","RpId":"evil.test"}`} {
		want(t, b.Do("PATCH", path, json.RawMessage(body)), 403, "step_up_required")
		want(t, b.Do("PATCH", path, json.RawMessage(body), tokHeader, b.StepUp("update-client")), 400, "invalid_client")
		if !reflect.DeepEqual(before, clientOf(t, h, id)) {
			t.Fatalf("%s changed the client", body)
		}
	}
}

// Why: the key is shown once and must not be cached or readable again.
func TestCreateAndRotateReturnKeyOnceWithNoStore(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	c := b.Do("POST", "/api/clients", map[string]any{"name": "app", "rpId": "app.test", "origins": []string{"https://app.test"}})
	want(t, c, 201, "")
	id, key := c.Str("clientId"), c.Str("accessKey")
	if id == "" || key == "" || c.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create = %d %s cache=%q", c.Status, c.Body, c.Header.Get("Cache-Control"))
	}
	r := b.Do("POST", "/api/clients/"+id+"/keys", struct{}{}, tokHeader, b.StepUp("rotate-key"))
	want(t, r, 201, "")
	key2 := r.Str("accessKey")
	if key2 == "" || key2 == key || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("rotate = %d %s cache=%q", r.Status, r.Body, r.Header.Get("Cache-Control"))
	}
	for _, p := range []string{"/api/clients", "/api/clients/" + id} {
		g := b.Do("GET", p, nil)
		want(t, g, 200, "")
		if strings.Contains(string(g.Body), key) || strings.Contains(string(g.Body), key2) || g.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s leaks a key or is cacheable: %s", p, g.Body)
		}
	}
}

// Why: the Console authenticates to AuthGate with its own client; a mistake there locks every operator out (spec conflict 7).
func TestOwnClientIsProtected(t *testing.T) {
	h := consoletest.New(t)
	b, a := fullAdmin(t, h, "ada", console.RoleAdmin)
	own := "/api/clients/" + h.ClientID
	before := clientOf(t, h, h.ClientID)
	ops := []struct {
		action, method, path string
		body                 any
	}{
		{"update-client", "PATCH", own, map[string]any{"origins": []string{"https://evil.test"}}},
		{"rotate-key", "POST", own + "/keys", struct{}{}},
		{"revoke-previous-keys", "DELETE", own + "/keys/previous", nil},
		{"disable-client", "POST", own + "/disable", struct{}{}},
		{"purge-subject", "DELETE", own + "/subjects/" + a.ID, nil},
	}
	for _, o := range ops {
		tok := b.StepUp(o.action)
		want(t, b.Do(o.method, o.path, o.body, tokHeader, tok), 409, "own_client_protected")
	}
	// Even a rename, which needs no token, is refused.
	want(t, b.Do("PATCH", own, map[string]any{"name": "mine"}), 409, "own_client_protected")
	if !reflect.DeepEqual(before, clientOf(t, h, h.ClientID)) {
		t.Fatal("the Console's own client changed")
	}
	if f, err := h.AuthGate.Factors(context.Background(), a.ID); err != nil || !f.PIN {
		t.Fatalf("the purge ran: %+v %v", f, err)
	}
	// A refusal costs no token: the same one still rotates another client.
	other, _ := newClient(t, b, "app")
	tok := b.StepUp("rotate-key")
	want(t, b.Do("POST", own+"/keys", struct{}{}, tokHeader, tok), 409, "own_client_protected")
	want(t, b.Do("POST", "/api/clients/"+other+"/keys", struct{}{}, tokHeader, tok), 201, "")
	// Reading and enabling stay open.
	want(t, b.Do("GET", own, nil), 200, "")
}

// Why: a refused action must leave a trace, with who asked and from where.
func TestAuditWrittenOnFailure(t *testing.T) {
	h := consoletest.New(t)
	b, a := fullAdmin(t, h, "ada", console.RoleAdmin)
	ip := "203.0.113.7:4443"
	id, _ := newClient(t, b, "app")
	h.Clock.Advance(time.Second)
	want(t, b.Do("POST", "/api/clients/"+id+"/keys", struct{}{}, viewerHdr, ip), 403, "step_up_required")
	h.Clock.Advance(time.Second)
	want(t, b.Do("POST", "/api/clients/"+id+"/keys", struct{}{}, tokHeader, b.StepUp("rotate-key"), viewerHdr, ip), 201, "")

	all, _ := auditOf(t, b, "")
	var entries []auditView // the rotations, newest first
	for _, e := range all {
		if e.Action == "rotate-key" {
			entries = append(entries, e)
		}
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", all)
	}
	ok, failed := entries[0], entries[1]
	for _, e := range []auditView{ok, failed} {
		if e.AdminID != a.ID || e.Username != "ada" || e.Action != "rotate-key" || e.Target != id || e.IP != "203.0.113.7" {
			t.Fatalf("entry = %+v", e)
		}
	}
	if ok.Result != "success" || failed.Result != "failed:step_up_required" {
		t.Fatalf("results = %q, %q", ok.Result, failed.Result)
	}
}

// Why: the log is read newest first, month by month; a bad month or cursor is the caller's mistake, not a 500.
func TestAuditMonthAndBadMonth(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	now := h.Clock.Now().UTC()
	month := now.Format("2006-01")
	for i := range 55 {
		e := console.AuditEntry{AdminID: "x", Username: "seed", Action: "seed", Target: "t", Result: "success", At: now.Add(time.Duration(i+1) * time.Millisecond)}
		if err := h.Store().PutAudit(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	// Default is the current UTC month.
	first, r := auditOf(t, b, "")
	if len(first) != 50 || r.Str("month") != month || r.Str("nextCursor") == "" {
		t.Fatalf("first page = %d entries, month %q, cursor %q", len(first), r.Str("month"), r.Str("nextCursor"))
	}
	second, r2 := auditOf(t, b, "?month="+month+"&cursor="+url.QueryEscape(r.Str("nextCursor")))
	if len(second) < 5 || r2.Str("nextCursor") != "" {
		t.Fatalf("second page = %d entries, cursor %q", len(second), r2.Str("nextCursor"))
	}
	// Another month is empty, not an error.
	if old, _ := auditOf(t, b, "?month=2020-01"); len(old) != 0 {
		t.Fatalf("2020-01 = %+v", old)
	}
	for _, q := range []string{"?month=2026-13", "?month=abc", "?month=2026-1", "?month=2026-01-01", "?month=%0a"} {
		want(t, b.Do("GET", "/api/audit"+q, nil), 400, "invalid_month")
	}
	for _, c := range []string{"x", "2020-01-01", strings.Repeat("a", 500), "2026-11-06T00:00:00.000000000Z%23nope", "1999-01-01T00:00:00.000000000Z%2301ARZ3NDEKTSV4RRFFQ69G5FAV"} {
		want(t, b.Do("GET", "/api/audit?month="+month+"&cursor="+c, nil), 400, "invalid_cursor")
	}
}

// Why: in a full session every change to one's own credentials asks for a step-up token, from a PIN or a passkey.
func TestFullSelfServiceNeedsStepUp(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2)), 403, "step_up_required")
	want(t, b.EnrollPIN(pin2), 403, "step_up_required")
	want(t, b.Do("DELETE", "/api/me/pin", nil), 403, "step_up_required")
	want(t, b.StartPasskey(), 403, "step_up_required")
	want(t, b.Do("DELETE", "/api/me/passkeys/abc", nil), 403, "step_up_required")
	want(t, h.NewBrowser().Login("ada", pw), 200, "") // nothing changed

	// With tokens, one of them from a passkey.
	pk := h.NewPasskey()
	st := b.Do("POST", "/api/me/passkeys/start", struct{}{}, tokHeader, b.StepUp("add-factor"))
	want(t, st, 200, "")
	want(t, b.FinishPasskey(st.Str("registrationId"), pk.Attest(h, st)), 200, "")
	pk.Authn.AddCredential(pk.Credential)
	want(t, b.Do("DELETE", "/api/me/pin", nil, tokHeader, b.StepUpPasskey(pk, "remove-factor")), 200, "")
	want(t, b.Do("POST", "/api/me/pin", map[string]string{"pin": pin2}, tokHeader, b.StepUpPasskey(pk, "add-factor")), 200, "")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2), tokHeader, b.StepUpPasskey(pk, "change-password")), 200, "")
	want(t, h.NewBrowser().Login("ada", pw2), 200, "")
}
