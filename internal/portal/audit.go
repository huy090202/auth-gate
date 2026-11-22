package portal

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/auditlog"
	"github.com/huy090202/auth-gate/internal/platform/viewerip"
)

const (
	auditWriteTimeout = 5 * time.Second
	maxAuditTarget    = auditlog.MaxTarget
)

func setAuditActor(ctx context.Context, userID string) { auditlog.SetActor(ctx, userID, "") }
func truncate(v string, n int) string                  { return auditlog.Truncate(v, n) }

// setAuditTenant names the workspace whose log gets the entry. An account-level
// action (password, factors) names the user's personal workspace.
func setAuditTenant(ctx context.Context, tenantID string) { auditlog.SetScope(ctx, tenantID) }

// setAuditTarget names what the action touches, once that value has passed its
// check or been accepted upstream, so a refused request leaves it empty.
func setAuditTarget(ctx context.Context, target string) { auditlog.SetTarget(ctx, target) }

// setAuditDetail records one fact about the action. Keys are fixed names in the
// handler; the value comes from a decoded field, never from a raw body, an
// upstream error or a credential.
func setAuditDetail(ctx context.Context, k, v string) { auditlog.SetDetail(ctx, k, v) }

// audited records the outcome of a write in the log of the workspace it acted
// in: "success", or "failed:<code>" with the error code the handler answered.
// The entry carries the session's user and the viewer address. An action that
// names no workspace (sign-up, sign-in and the other routes without a session
// or a tenant) has no log to go to and is written to the process log instead,
// with the same facts. A failed audit write is logged and does not change the
// answer.
func (a *App) audited(action string, h http.Handler) http.Handler {
	return auditlog.Wrap(action, h, func(r *http.Request, e auditlog.Entry) {
		entry := AuditEntry{
			TenantID: e.Note.Scope, ActorID: e.Note.ActorID, Action: action,
			Target: e.Note.Target, Result: e.Result, IP: a.auditIP(r), Detail: e.Note.Detail, At: a.now(),
		}
		if entry.TenantID != "" {
			a.writeAudit(r, entry)
		} else {
			slog.Info("portal: action without a workspace", "action", action, "actor", entry.ActorID, "ip", entry.IP, "result", entry.Result)
		}
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
