package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/viewerip"
)

// auditNote is what a request tells the audit log about itself while it runs:
// who the session is (filled by the session middleware), which workspace it
// acted in (filled by a.member, or by a handler for an account-level action),
// what it touched and a few facts about it. It never holds a secret; the request
// body and the response are not read for it, and each detail is set by the
// handler from a field it decoded.
type auditNote struct {
	ActorID, TenantID, Target string
	Detail                    map[string]string
}

type noteKey struct{}

func noteFrom(ctx context.Context) *auditNote {
	n, _ := ctx.Value(noteKey{}).(*auditNote)
	return n
}

func setAuditActor(ctx context.Context, userID string) {
	if n := noteFrom(ctx); n != nil {
		n.ActorID = userID
	}
}

// setAuditTenant names the workspace whose log gets the entry. An account-level
// action (password, factors) names the user's personal workspace.
func setAuditTenant(ctx context.Context, tenantID string) {
	if n := noteFrom(ctx); n != nil {
		n.TenantID = tenantID
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

// audited records the outcome of a write in the log of the workspace it acted
// in: "success", or "failed:<code>" with the error code the handler answered.
// The entry carries the session's user and the viewer address. An action that
// names no workspace (sign-up, sign-in and the other routes without a session
// or a tenant) has no log to go to and is written to the process log instead,
// with the same facts. A failed audit write is logged and does not change the
// answer. The handler names what the action touches with setAuditTarget, and
// only once that value has passed its check or been accepted upstream, so a
// refused request leaves the target empty.
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
		e := AuditEntry{
			TenantID: note.TenantID, ActorID: note.ActorID, Action: action,
			Target: truncate(note.Target, maxAuditTarget), Result: result, IP: a.auditIP(r), Detail: note.Detail, At: a.now(),
		}
		if e.TenantID != "" {
			a.writeAudit(r, e)
		} else {
			slog.Info("portal: action without a workspace", "action", action, "actor", e.ActorID, "ip", e.IP, "result", result)
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
		slog.Error("portal: audit write failed", "action", e.Action, "result", e.Result, "err", err)
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
