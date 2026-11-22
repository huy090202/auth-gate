package console

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/viewerip"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// AuditEntry is one line of the audit log. It never holds a key, PIN, password
// or token, nor a hash of one. Source is "web" or "cli". Detail is a small
// allow-listed set of facts about the action, built field by field where the
// action runs; its values are cut like Target.
type AuditEntry struct {
	AdminID, Username, Action, Target, Result, IP, Source string
	Detail                                                map[string]string
	At                                                    time.Time
}

var monthRE = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// PutAudit appends one entry under its UTC month.
func (st *Store) PutAudit(ctx context.Context, e AuditEntry) error {
	at := e.At.UTC()
	it := item{
		"pk": s("AUDIT#" + at.Format("2006-01")), "sk": s(ts(at) + "#" + id.New()),
		"adminId": s(e.AdminID), "username": s(e.Username), "action": s(e.Action),
		"target": s(e.Target), "result": s(e.Result), "ip": s(e.IP), "source": s(e.Source),
	}
	if len(e.Detail) > 0 {
		m := make(map[string]types.AttributeValue, len(e.Detail))
		for k, v := range e.Detail {
			m[k] = s(truncate(v, maxAuditTarget))
		}
		it["detail"] = &types.AttributeValueMemberM{Value: m}
	}
	if _, err := st.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(st.Table), Item: it}); err != nil {
		return fmt.Errorf("put audit: %w", err)
	}
	return nil
}

// ListAudit returns up to 50 entries of a month (yyyy-mm), newest first. The
// returned cursor is empty on the last page; pass it back to read the next.
func (st *Store) ListAudit(ctx context.Context, month, cursor string) ([]AuditEntry, string, error) {
	if !monthRE.MatchString(month) {
		return nil, "", fmt.Errorf("list audit: bad month %q", month)
	}
	pk := "AUDIT#" + month
	in := &dynamodb.QueryInput{
		TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
		KeyConditionExpression:    aws.String("pk = :p"),
		ExpressionAttributeValues: item{":p": s(pk)},
		ScanIndexForward:          aws.Bool(false),
		Limit:                     aws.Int32(auditPage),
	}
	if cursor != "" {
		in.ExclusiveStartKey = item{"pk": s(pk), "sk": s(cursor)}
	}
	out, err := st.DB.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("list audit: %w", err)
	}
	entries := make([]AuditEntry, 0, len(out.Items))
	for _, it := range out.Items {
		var t time.Time
		if stamp, _, ok := strings.Cut(getS(it, "sk"), "#"); ok {
			t, _ = time.Parse(time.RFC3339Nano, stamp)
		}
		entries = append(entries, AuditEntry{
			AdminID: getS(it, "adminId"), Username: getS(it, "username"), Action: getS(it, "action"),
			Target: getS(it, "target"), Result: getS(it, "result"), IP: getS(it, "ip"), Source: getS(it, "source"), At: t,
		})
		if m, ok := it["detail"].(*types.AttributeValueMemberM); ok {
			e := &entries[len(entries)-1]
			e.Detail = make(map[string]string, len(m.Value))
			for k := range m.Value {
				e.Detail[k] = getS(m.Value, k)
			}
		}
	}
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		next = getS(out.LastEvaluatedKey, "sk")
	}
	return entries, next, nil
}

// auditNote is what a request tells the audit log about itself while it runs:
// who the session is (filled by the session middleware, or by sign-in for the
// account it was tried against), what the action touched and a few facts about
// it. It never holds a secret; the request body and the response are not read
// for it, and each detail is set by the handler from a field it decoded.
type auditNote struct {
	AdminID, Username, Target string
	Detail                    map[string]string
}

type noteKey struct{}

func noteFrom(ctx context.Context) *auditNote {
	n, _ := ctx.Value(noteKey{}).(*auditNote)
	return n
}

func setAuditActor(ctx context.Context, adminID, username string) {
	if n := noteFrom(ctx); n != nil {
		n.AdminID, n.Username = adminID, username
	}
}

func setAuditTarget(ctx context.Context, target string) {
	if n := noteFrom(ctx); n != nil {
		n.Target = target
	}
}

// setAuditDetail records one fact about the action. Keys are fixed names in the
// handler; the value comes from a decoded field, never from a raw body, an
// upstream error or a credential.
func setAuditDetail(ctx context.Context, k, v string) {
	if n := noteFrom(ctx); n != nil {
		if n.Detail == nil {
			n.Detail = map[string]string{}
		}
		n.Detail[k] = v
	}
}

// bufWriter holds a response back until the audit item is written, so a client
// that has its answer can read the log.
type bufWriter struct {
	h      http.Header
	status int
	body   bytes.Buffer
}

func (b *bufWriter) Header() http.Header { return b.h }
func (b *bufWriter) WriteHeader(code int) {
	if b.status == 0 {
		b.status = code
	}
}
func (b *bufWriter) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}

const (
	auditWriteTimeout = 5 * time.Second
	maxAuditTarget    = 200
)

// audited records the outcome of a write: "success", or "failed:<code>" with the
// error code the handler answered. The entry carries the session's admin and the
// viewer address. Sign-in and the other /api/auth routes are always stored, with
// empty identity when no account was found (the submitted name is never kept,
// people type passwords into that field); they are rate limited per address. A
// write elsewhere without a session is unbounded and unlimited, so it goes to the
// log as a warning, not to the table. A failed audit write is logged and does not
// change the answer. The handler names what the action touches with
// setAuditTarget, and only once that value has passed its check or been accepted
// upstream, so a refused request leaves the target empty.
func (a *App) audited(action string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		note := &auditNote{}
		bw := &bufWriter{h: w.Header()}
		h.ServeHTTP(bw, r.WithContext(context.WithValue(r.Context(), noteKey{}, note)))
		if bw.status == 0 {
			bw.status = http.StatusOK
		}
		result := "success"
		if bw.status >= 400 {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(bw.body.Bytes(), &e)
			if e.Error == "" {
				e.Error = fmt.Sprintf("http_%d", bw.status)
			}
			result = truncate("failed:"+e.Error, maxAuditTarget)
		}
		switch {
		case note.AdminID != "" || strings.HasPrefix(r.URL.Path, "/api/auth/"):
			a.writeAudit(r, AuditEntry{
				AdminID: note.AdminID, Username: note.Username, Action: action,
				Target: truncate(note.Target, maxAuditTarget), Result: result, IP: a.auditIP(r), Source: "web",
				Detail: note.Detail, At: a.now(),
			})
		default:
			slog.Warn("console: write without a session", "route", truncate(r.Method+" "+r.URL.Path, maxAuditTarget), "ip", a.auditIP(r), "result", result)
		}
		w.WriteHeader(bw.status)
		_, _ = w.Write(bw.body.Bytes())
	})
}

func (a *App) writeAudit(r *http.Request, e AuditEntry) {
	// The request may be over by the time this matters; the entry should still land.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditWriteTimeout)
	defer cancel()
	if err := a.Store.PutAudit(ctx, e); err != nil {
		slog.Error("console: audit write failed", "action", e.Action, "result", e.Result, "err", err)
	}
}

func truncate(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return strings.ToValidUTF8(v[:n], "")
}

// auditIP is the viewer address CloudFront sets; in Dev, the socket address.
func (a *App) auditIP(r *http.Request) string {
	if ip := viewerip.FromHeader(r.Header); ip != "" {
		return ip
	}
	if a.Config.Dev {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			return host
		}
	}
	return ""
}

var cursorRE = regexp.MustCompile(`^(\d{4}-\d{2})-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z#[0-9A-Z]{26}$`)

// listAudit serves GET /api/audit?month=YYYY-MM&cursor=. The month defaults to
// the current UTC month. The cursor is the one a page handed out; anything
// else, or one from another month, is 400 before DynamoDB sees it.
func (a *App) listAudit(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	month := q.Get("month")
	if month == "" {
		month = a.now().UTC().Format("2006-01")
	}
	if !monthRE.MatchString(month) {
		return &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_month"}
	}
	cursor := q.Get("cursor")
	if cursor != "" {
		if m := cursorRE.FindStringSubmatch(cursor); m == nil || m[1] != month {
			return &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_cursor"}
		}
	}
	entries, next, err := a.Store.ListAudit(r.Context(), month, cursor)
	if err != nil {
		return err
	}
	type view struct {
		AdminID  string            `json:"adminId"`
		Username string            `json:"username"`
		Action   string            `json:"action"`
		Target   string            `json:"target"`
		Result   string            `json:"result"`
		IP       string            `json:"ip"`
		Source   string            `json:"source"`
		Detail   map[string]string `json:"detail,omitempty"`
		At       time.Time         `json:"at"`
	}
	out := make([]view, 0, len(entries))
	for _, e := range entries {
		out = append(out, view(e))
	}
	resp := map[string]any{"month": month, "entries": out}
	if next != "" {
		resp["nextCursor"] = next
	}
	httpx.WriteJSON(w, http.StatusOK, resp)
	return nil
}
