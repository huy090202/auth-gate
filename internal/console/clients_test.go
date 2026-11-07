package console_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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
	"github.com/huy090202/auth-gate/internal/console/authgate"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
	"github.com/huy090202/auth-gate/internal/platform/id"
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
				writeAttr(&sb, v)
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

// writeAttr renders a stored value as text, maps and lists included, so a secret cannot hide inside one.
func writeAttr(sb *strings.Builder, v types.AttributeValue) {
	switch v := v.(type) {
	case *types.AttributeValueMemberS:
		sb.WriteString(v.Value)
	case *types.AttributeValueMemberN:
		sb.WriteString(v.Value)
	case *types.AttributeValueMemberM:
		sb.WriteByte('{')
		for k, e := range v.Value {
			sb.WriteString(k + ":")
			writeAttr(sb, e)
			sb.WriteByte(',')
		}
		sb.WriteByte('}')
	case *types.AttributeValueMemberL:
		for _, e := range v.Value {
			writeAttr(sb, e)
			sb.WriteByte(',')
		}
	}
}

const viewerHdr = "CloudFront-Viewer-Address"

type auditView struct {
	AdminID, Username, Action, Target, Result, IP, Source string
	Detail                                                map[string]string
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
	r := b.Do("PATCH", path, json.RawMessage(`{"Origins":["https://b.app.test"],"NAME":"x"}`), tokHeader, b.StepUp("update-client", id))
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
		want(t, b.Do("PATCH", path, json.RawMessage(body), tokHeader, b.StepUp("update-client", id)), 400, "invalid_client")
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
	r := b.Do("POST", "/api/clients/"+id+"/keys", struct{}{}, tokHeader, b.StepUp("rotate-key", id))
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
		action, target, method, path string
		body                         any
	}{
		{"update-client", h.ClientID, "PATCH", own, map[string]any{"origins": []string{"https://evil.test"}}},
		{"rotate-key", h.ClientID, "POST", own + "/keys", struct{}{}},
		{"revoke-previous-keys", h.ClientID, "DELETE", own + "/keys/previous", nil},
		{"disable-client", h.ClientID, "POST", own + "/disable", struct{}{}},
		{"purge-subject", h.ClientID + "/" + a.ID, "DELETE", own + "/subjects/" + a.ID, nil},
	}
	for _, o := range ops {
		tok := b.StepUp(o.action, o.target)
		want(t, b.Do(o.method, o.path, o.body, tokHeader, tok), 409, "own_client_protected")
		requireUnspent(t, h, tok) // a refusal costs no token
	}
	// Even a rename, which needs no token, is refused.
	want(t, b.Do("PATCH", own, map[string]any{"name": "mine"}), 409, "own_client_protected")
	if !reflect.DeepEqual(before, clientOf(t, h, h.ClientID)) {
		t.Fatal("the Console's own client changed")
	}
	if f, err := h.AuthGate.Factors(context.Background(), a.ID); err != nil || !f.PIN {
		t.Fatalf("the purge ran: %+v %v", f, err)
	}
	// Reading and enabling stay open.
	want(t, b.Do("GET", own, nil), 200, "")
	want(t, b.Do("POST", own+"/enable", struct{}{}), 200, "")
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
	want(t, b.Do("POST", "/api/clients/"+id+"/keys", struct{}{}, tokHeader, b.StepUp("rotate-key", id), viewerHdr, ip), 201, "")

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
	st := b.Do("POST", "/api/me/passkeys/start", struct{}{}, tokHeader, b.StepUp("add-factor", ""))
	want(t, st, 200, "")
	want(t, b.FinishPasskey(st.Str("registrationId"), pk.Attest(h, st)), 200, "")
	pk.Authn.AddCredential(pk.Credential)
	want(t, b.Do("DELETE", "/api/me/pin", nil, tokHeader, b.StepUpPasskey(pk, "remove-factor")), 200, "")
	want(t, b.Do("POST", "/api/me/pin", map[string]string{"pin": pin2}, tokHeader, b.StepUpPasskey(pk, "add-factor")), 200, "")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2), tokHeader, b.StepUpPasskey(pk, "change-password")), 200, "")
	want(t, h.NewBrowser().Login("ada", pw2), 200, "")
}

// Why: an id is any path segment, and a front that decodes percent-encoding again could turn one into another
// route or the Console's own client; ids are checked at the door and AuthGate is not asked.
func TestMalformedIDsNeverReachAuthGate(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	rt := &countingRT{base: h.AuthGate.HTTP.Transport}
	deps := h.Deps
	deps.AuthGate = &authgate.Client{BaseURL: h.AuthGateURL, ClientKey: h.ClientKey, AdminKey: consoletest.AdminKey, HTTP: &http.Client{Transport: rt}}
	b := h.BrowserFor(console.New(deps))
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.SignInWithPIN(pin1), 200, "")
	ok := id.New()

	type route struct{ method, tmpl, action string }
	routes := []route{
		{"GET", "/api/clients/%s", ""},
		{"PATCH", "/api/clients/%s", "update-client"},
		{"POST", "/api/clients/%s/keys", "rotate-key"},
		{"DELETE", "/api/clients/%s/keys/previous", "revoke-previous-keys"},
		{"POST", "/api/clients/%s/disable", "disable-client"},
		{"POST", "/api/clients/%s/enable", ""},
		{"DELETE", "/api/clients/%s/subjects/s1", "purge-subject"},
	}
	// A dot segment is cleaned away by the router (a redirect) before any handler runs: status 0 means "any refusal".
	badIDs := []struct {
		raw    string
		status int
	}{
		{"A%252Fkeys%253F", 404}, {"A%2Fkeys", 404}, {"A%3Fx", 404}, {"A%23x", 404}, {"..", 0}, {"%2e%2e", 0}, {"abc", 404},
		{strings.ToLower(ok), 404}, {ok + "x", 404}, {"01ARZ3NDEKTSV4RRFFQ69G5FAU", 404}, // lower case, 27 chars, 'U' is not Crockford
	}
	for _, r := range routes {
		tok := ""
		if r.action != "" {
			target := ok
			if r.action == "purge-subject" {
				target = ok + "/s1"
			}
			tok = b.StepUp(r.action, target)
		}
		var body any
		if r.method != "GET" && r.method != "DELETE" {
			body = map[string]any{"name": "x"}
		}
		calls := rt.n.Load()
		for _, bad := range badIDs {
			var hdr []string
			if tok != "" {
				hdr = []string{tokHeader, tok}
			}
			resp := b.Do(r.method, strings.Replace(r.tmpl, "%s", bad.raw, 1), body, hdr...)
			if bad.status != 0 && (resp.Status != bad.status || resp.Str("error") != "not_found") || resp.Status/100 == 2 {
				t.Errorf("%s %s id %q = %d %s", r.method, r.tmpl, bad.raw, resp.Status, resp.Body)
			}
		}
		if got := rt.n.Load() - calls; got != 0 {
			t.Errorf("%s %s: AuthGate was called %d times", r.method, r.tmpl, got)
		}
	}
	// The subject of a purge.
	tok := b.StepUp("purge-subject", ok+"/s1")
	calls := rt.n.Load()
	for _, sub := range []string{"a%2Fb", "a%3Fb", "a%23b", "a%2525b", "%2e%2e", "%2e", "a%00b", "a%0ab", "a%7fb", strings.Repeat("s", 257)} {
		resp := b.Do("DELETE", "/api/clients/"+ok+"/subjects/"+sub, nil, tokHeader, tok)
		if resp.Status/100 == 2 || (resp.Status != 400 && resp.Status != 301 && resp.Status != 404) {
			t.Errorf("subject %q = %d %s", sub, resp.Status, resp.Body)
		}
		if resp.Status == 400 && resp.Str("error") != "invalid_id" {
			t.Errorf("subject %q = %s", sub, resp.Body)
		}
	}
	if got := rt.n.Load() - calls; got != 0 {
		t.Errorf("a bad subject reached AuthGate %d times", got)
	}
}

// Why: guessing usernames must leave a trace without storing what was typed, and must cost the same as guessing a real one.
func TestUnknownLoginIsAuditedWithoutTheName(t *testing.T) {
	logs := captureLogs(t)
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	typed := "hunter2-my-real-password"
	h.Clock.Advance(time.Second)
	want(t, h.NewBrowser().Login(typed, "whatever-password"), 401, "invalid_credentials")
	want(t, h.NewBrowser().Login("nobody", "whatever-password"), 401, "invalid_credentials")

	entries, _ := auditOf(t, b, "")
	n := 0
	for _, e := range entries {
		if e.Action == "login" && e.AdminID == "" {
			n++
			if e.Username != "" || e.Target != "" || e.Result != "failed:invalid_credentials" {
				t.Fatalf("entry = %+v", e)
			}
		}
	}
	if n != 2 {
		t.Fatalf("%d anonymous login entries, want 2: %+v", n, entries)
	}
	items := scanTable(t, h)
	for _, s := range []string{typed, "nobody", "whatever-password"} {
		if strings.Contains(items, s) || strings.Contains(logs.String(), s) {
			t.Errorf("%q was stored or logged", s)
		}
	}
}

// Why: a write with no session is unbounded and unlimited, so it is logged, not stored.
func TestNoSessionWriteIsLoggedNotStored(t *testing.T) {
	logs := captureLogs(t)
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	before, _ := auditOf(t, b, "")
	anon := h.NewBrowser()
	want(t, anon.Do("POST", "/api/clients", map[string]any{"name": "x"}, viewerHdr, "198.51.100.9:1"), 401, "unauthorized")
	after, _ := auditOf(t, b, "")
	if len(after) != len(before) {
		t.Fatalf("an item was stored: %+v", after[:len(after)-len(before)])
	}
	if l := logs.String(); !strings.Contains(l, "write without a session") || !strings.Contains(l, "198.51.100.9") || !strings.Contains(l, "failed:unauthorized") {
		t.Fatalf("no warning in the logs:\n%s", l)
	}
}

// find returns the newest entry with the action and target.
func findAudit(t *testing.T, entries []auditView, action, target string) auditView {
	t.Helper()
	for _, e := range entries {
		if e.Action == action && e.Target == target {
			return e
		}
	}
	t.Fatalf("no %s entry for %q in %+v", action, target, entries)
	return auditView{}
}

func detailIs(t *testing.T, e auditView, want map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(e.Detail, want) {
		t.Fatalf("%s detail = %v, want %v", e.Action, e.Detail, want)
	}
}

// Why: detail is built field by field at each action; if it ever copied a body, a key, PIN, token or password would sit in the table for good.
func TestAuditDetailIsAnAllowList(t *testing.T) {
	logs := captureLogs(t)
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleMaster)
	secrets := []string{pin1, pw, pw2}

	c := b.Do("POST", "/api/clients", map[string]any{"name": "app", "rpId": "app.test", "origins": []string{"https://app.test", "https://b.app.test"}})
	want(t, c, 201, "")
	id, key := c.Str("clientId"), c.Str("accessKey")
	secrets = append(secrets, key)

	tok := b.StepUp("update-client", id)
	secrets = append(secrets, tok)
	want(t, b.Do("PATCH", "/api/clients/"+id, map[string]any{"name": "renamed", "origins": []string{"https://c.app.test"}, "tokenTtlSec": 900}, tokHeader, tok), 200, "")
	rot := b.Do("POST", "/api/clients/"+id+"/keys", struct{}{}, tokHeader, b.StepUp("rotate-key", id))
	want(t, rot, 201, "")
	secrets = append(secrets, rot.Str("accessKey"))
	want(t, b.Do("DELETE", "/api/clients/"+id+"/keys/previous", nil, tokHeader, b.StepUp("revoke-previous-keys", id)), 200, "")
	nu := b.Do("POST", "/api/admins", map[string]string{"username": "Newbie", "role": "admin"}, tokHeader, b.StepUp("admin-create", "Newbie/admin"))
	want(t, nu, 201, "")
	secrets = append(secrets, nu.Str("temporaryPassword"))
	h.CreateAdmin("bob", pw, console.RoleAdmin, false)
	want(t, b.Do("POST", "/api/admins/bob/role", map[string]string{"role": "master"}, tokHeader, b.StepUp("admin-role", "bob/master")), 200, "")

	entries, raw := auditOf(t, b, "")
	if strings.Contains(string(raw.Body), `"detail":null`) {
		t.Fatal("an empty detail is sent as null")
	}
	detailIs(t, findAudit(t, entries, "create-client", id), map[string]string{"name": "app", "rpId": "app.test", "origins": "https://app.test,https://b.app.test"})
	detailIs(t, findAudit(t, entries, "update-client", id), map[string]string{
		"fields": "name,origins,tokenTtlSec", "name": "renamed", "origins": "https://c.app.test", "tokenTtlSec": "900"})
	detailIs(t, findAudit(t, entries, "revoke-previous-keys", id), map[string]string{"revoked": "1"})
	if e := findAudit(t, entries, "rotate-key", id); len(e.Detail) != 0 {
		t.Fatalf("rotate detail = %v", e.Detail)
	}
	detailIs(t, findAudit(t, entries, "admin-create", "newbie"), map[string]string{"role": "admin"})
	detailIs(t, findAudit(t, entries, "stepup-start", "admin-create"), map[string]string{"action": "admin-create", "factor": "pin", "target": "newbie/admin"}) // normalized
	detailIs(t, findAudit(t, entries, "admin-role", "bob"), map[string]string{"role": "master"})
	detailIs(t, findAudit(t, entries, "stepup-start", "admin-role"), map[string]string{"action": "admin-role", "factor": "pin", "target": "bob/master"})
	detailIs(t, findAudit(t, entries, "stepup-verify", ""), map[string]string{"action": "admin-role", "factor": "pin", "target": "bob/master"})
	for _, e := range entries {
		if e.Source != "web" {
			t.Fatalf("source of %s = %q", e.Action, e.Source)
		}
	}

	items := scanTable(t, h)
	for _, s := range secrets {
		if strings.Contains(items, s) || strings.Contains(logs.String(), s) {
			t.Errorf("a secret of %d bytes was stored or logged", len(s))
		}
	}
	if !strings.Contains(items, "https://c.app.test") {
		t.Fatal("the raw items do not hold the detail the API shows")
	}
}

// Why: the reason helps the operator and must reach the log without telling the caller which of the four it was.
func TestFailedLoginRecordsItsReason(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleMaster)
	ctx := context.Background()
	h.CreateAdmin("wrongpw", pw, console.RoleAdmin, false)
	h.CreateAdmin("lockedu", pw, console.RoleAdmin, false)
	h.CreateAdmin("disabledu", pw, console.RoleAdmin, false)
	for range 5 {
		if err := h.Store().RecordLoginFailure(ctx, "lockedu", h.Clock.Now()); err != nil {
			t.Fatal(err)
		}
	}
	at := h.Clock.Now().UTC()
	if err := h.Store().SetDisabled(ctx, nil, "disabledu", &at); err != nil {
		t.Fatal(err)
	}
	typed := "ghostly-typed-name"

	tries := []struct{ name, user, reason string }{
		{"wrong password", "wrongpw", "wrong_password"}, {"locked", "lockedu", "locked"},
		{"disabled", "disabledu", "disabled"}, {"unknown", typed, "unknown_user"},
	}
	var first consoletest.Response
	for i, c := range tries {
		r := h.NewBrowser().Login(c.user, pw+"-nope")
		want(t, r, 401, "invalid_credentials")
		if i == 0 {
			first = r
		} else if !bytes.Equal(r.Body, first.Body) || r.Header.Get("Content-Type") != first.Header.Get("Content-Type") || r.Header.Get("Set-Cookie") != first.Header.Get("Set-Cookie") {
			t.Fatalf("%s answered differently: %q vs %q", c.name, r.Body, first.Body)
		}
	}
	entries, _ := auditOf(t, b, "")
	got := map[string]string{}
	for _, e := range entries {
		if e.Action != "login" || e.Result != "failed:invalid_credentials" {
			continue
		}
		who := e.Username
		if who == "" {
			who = "(none)"
		}
		got[who] = e.Detail["reason"]
		if len(e.Detail) != 1 {
			t.Fatalf("login detail = %v", e.Detail)
		}
	}
	wantReasons := map[string]string{"wrongpw": "wrong_password", "lockedu": "locked", "disabledu": "disabled", "(none)": "unknown_user"}
	if !reflect.DeepEqual(got, wantReasons) {
		t.Fatalf("reasons = %v, want %v", got, wantReasons)
	}
	if strings.Contains(scanTable(t, h), typed) {
		t.Fatal("the unknown user's name was stored")
	}
}

// Why: a refused attempt on another admin's account is the one worth reading later, and it must say whose account.
func TestRefusedAdminAttemptRecordsTarget(t *testing.T) {
	h := consoletest.New(t)
	b, _ := fullAdmin(t, h, "ada", console.RoleAdmin)
	h.CreateAdmin("bob", pw, console.RoleAdmin, false)
	want(t, b.Do("POST", "/api/admins", map[string]string{"username": "Newbie", "role": "admin"}), 403, "forbidden")
	want(t, b.Do("POST", "/api/admins/BOB/disable", struct{}{}), 403, "forbidden")
	want(t, b.Do("POST", "/api/admins/bob/role", map[string]string{"role": "master"}), 403, "forbidden")
	entries, _ := auditOf(t, b, "")
	for _, c := range [][2]string{{"admin-create", "newbie"}, {"admin-disable", "bob"}, {"admin-role", "bob"}} {
		e := findAudit(t, entries, c[0], c[1])
		if e.Result != "failed:forbidden" {
			t.Fatalf("%s result = %q", c[0], e.Result)
		}
	}
}
