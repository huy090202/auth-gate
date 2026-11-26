package console

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/huy090202/auth-gate/internal/platform/auditlog"
	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/portalapi"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// ownerConcurrency bounds the AuthGate owner calls that enrich one tenants page.
const ownerConcurrency = 8

// ownerEnrichTimeout bounds all of those calls together, well under the Lambda timeout.
const ownerEnrichTimeout = 8 * time.Second

// suspensionPages bounds how many audit pages the search for the last suspension
// reads in one month, so a busy month cannot make a tenant page slow.
const suspensionPages = 10

// portalError turns a Portal failure into the Console's answer. The Portal being down
// or misconfigured is 502 and the cause stays in the log; its own 4xx answers pass through.
func portalError(err error) error {
	var pe *portalapi.Error
	switch {
	case errors.Is(err, portalapi.ErrUnavailable):
		slog.Warn("console: portal unavailable", "err", err)
		return &webapi.Error{Status: http.StatusBadGateway, Code: "portal_unavailable"}
	case errors.As(err, &pe):
		return &webapi.Error{Status: pe.Status, Code: pe.Code, Extra: pe.Extra, RetryAfter: pe.RetryAfter}
	}
	return err
}

// operatorRoutes serves the tenant and user pages, for any full session. Each page
// shows what the Portal says and what AuthGate says apart, so a half-done
// suspension never looks finished.
func (a *App) operatorRoutes(mux *http.ServeMux) {
	full := a.session(StageFull)
	mux.Handle("GET /api/tenants", full(webapi.HandlerFunc(a.listTenants)))
	mux.Handle("GET /api/tenants/{tid}", full(webapi.HandlerFunc(a.getTenant)))
	mux.Handle("GET /api/tenants/{tid}/audit", full(webapi.HandlerFunc(a.tenantAudit)))
	mux.Handle("GET /api/users", full(webapi.HandlerFunc(a.findUser)))

	mux.Handle("POST /api/tenants/{tid}/suspend", a.audited("tenant-suspend", full(a.tenantSuspension("tenant-suspend", true))))
	mux.Handle("POST /api/tenants/{tid}/unsuspend", a.audited("tenant-unsuspend", full(a.tenantSuspension("tenant-unsuspend", false))))
	mux.Handle("PATCH /api/tenants/{tid}/quota", a.audited("tenant-quota", full(webapi.HandlerFunc(a.tenantQuota))))
	mux.Handle("POST /api/users/{uid}/suspend", a.audited("user-suspend", full(a.userWrite("user-suspend", true, a.Portal.SuspendUser))))
	mux.Handle("POST /api/users/{uid}/unsuspend", a.audited("user-unsuspend", full(a.userWrite("user-unsuspend", false, a.Portal.UnsuspendUser))))
	mux.Handle("POST /api/users/{uid}/resend-verification", a.audited("user-resend-verification", full(a.userWrite("user-resend-verification", false, a.Portal.ResendVerification))))
	mux.Handle("POST /api/users/{uid}/factor-reset", a.audited("user-factor-reset", full(a.userWrite("user-factor-reset", false, a.Portal.ForceFactorReset))))
}

// maxReasonRunes bounds the internal reason of a suspension.
const maxReasonRunes = 200

// readReason decodes {reason}, trimmed, 1 to maxReasonRunes runes. The reason is
// for the Console's audit only and never goes to the Portal.
func readReason(r *http.Request) (string, error) {
	var in struct {
		Reason string `json:"reason"`
	}
	if err := webapi.DecodeBody(r, &in); err != nil {
		return "", err
	}
	reason := strings.TrimSpace(in.Reason)
	if n := utf8.RuneCountInString(reason); n < 1 || n > maxReasonRunes {
		return "", &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_reason"}
	}
	return reason, nil
}

// portalAfterAuthGate is the Portal step of an action whose AuthGate step already
// succeeded. When it fails the audit entry says partial, so the half-done action
// is visible and the operator can repeat it (every step is idempotent).
func portalAfterAuthGate(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	auditlog.SetResult(ctx, "partial")
	return portalError(err)
}

// tenantSuspension suspends or unsuspends a tenant: AuthGate first, then the Portal.
// Only a suspension takes a reason. The checks run before the token is spent.
func (a *App) tenantSuspension(action string, suspend bool) webapi.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		tid, err := tenantID(r)
		if err != nil {
			return err
		}
		ctx := r.Context()
		setAuditTarget(ctx, tid)
		x, _ := sessionFrom(ctx)
		tok, err := a.checkStepUp(r, x, action, tid)
		if err != nil {
			return err
		}
		if suspend {
			reason, err := readReason(r)
			if err != nil {
				return err
			}
			setAuditDetail(ctx, "reason", reason)
		}
		if err := a.spendStepUp(r, tok); err != nil {
			return err
		}
		call, portal := a.AuthGate.UnsuspendOwner, a.Portal.UnsuspendTenant
		if suspend {
			call, portal = a.AuthGate.SuspendOwner, a.Portal.SuspendTenant
		}
		if _, err := call(ctx, tid); err != nil {
			return webapi.AuthGateError(err)
		}
		raw, err := portal(ctx, tid)
		if err := portalAfterAuthGate(ctx, err); err != nil {
			return err
		}
		webapi.WriteRaw(w, http.StatusOK, raw)
		return nil
	}
}

// tenantQuota sets a tenant's client quota: AuthGate's owner is read for the old
// value, then written, then the Portal's tenant audit is told.
func (a *App) tenantQuota(w http.ResponseWriter, r *http.Request) error {
	tid, err := tenantID(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	setAuditTarget(ctx, tid)
	x, _ := sessionFrom(ctx)
	tok, err := a.checkStepUp(r, x, "tenant-quota", tid)
	if err != nil {
		return err
	}
	var in struct {
		ClientQuota *int `json:"clientQuota"`
	}
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	if in.ClientQuota == nil || *in.ClientQuota < 0 {
		return &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_quota"}
	}
	to := *in.ClientQuota
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	raw, err := a.AuthGate.GetOwner(ctx, tid)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	var cur struct {
		ClientQuota *int `json:"clientQuota"`
	}
	if json.Unmarshal(raw, &cur) != nil || cur.ClientQuota == nil {
		return webapi.AuthGateError(authgate.ErrUnavailable)
	}
	from := *cur.ClientQuota
	out, err := a.AuthGate.SetClientQuota(ctx, tid, to)
	if err != nil {
		return webapi.AuthGateError(err)
	}
	setAuditDetail(ctx, "from", strconv.Itoa(from))
	setAuditDetail(ctx, "to", strconv.Itoa(to))
	if err := portalAfterAuthGate(ctx, a.Portal.TenantQuotaAudit(ctx, tid, from, to)); err != nil {
		return err
	}
	webapi.WriteRaw(w, http.StatusOK, out)
	return nil
}

// userWrite is a Portal-only action on a Portal user. Only a suspension takes a reason.
func (a *App) userWrite(action string, withReason bool, call func(context.Context, string) (json.RawMessage, error)) webapi.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		uid := r.PathValue("uid")
		if !clientIDRE.MatchString(uid) {
			return &webapi.Error{Status: http.StatusNotFound, Code: "not_found"}
		}
		ctx := r.Context()
		setAuditTarget(ctx, uid)
		x, _ := sessionFrom(ctx)
		tok, err := a.checkStepUp(r, x, action, uid)
		if err != nil {
			return err
		}
		if withReason {
			reason, err := readReason(r)
			if err != nil {
				return err
			}
			setAuditDetail(ctx, "reason", reason)
		}
		if err := a.spendStepUp(r, tok); err != nil {
			return err
		}
		raw, err := call(ctx, uid)
		if err != nil {
			return portalError(err)
		}
		webapi.WriteRaw(w, http.StatusOK, raw)
		return nil
	}
}

// tenantID returns the {tid} of the route, or 404 when it cannot be a tenant's.
// Tenant ids are ULIDs, like client ids, and one never goes into a path unchecked.
func tenantID(r *http.Request) (string, error) {
	tid := r.PathValue("tid")
	if !clientIDRE.MatchString(tid) {
		return "", &webapi.Error{Status: http.StatusNotFound, Code: "not_found"}
	}
	return tid, nil
}

// listTenants is the Portal's page with each row's client count and quota from AuthGate.
// A row whose owner call fails keeps those two out; the page still answers.
func (a *App) listTenants(w http.ResponseWriter, r *http.Request) error {
	raw, err := a.Portal.ListTenants(r.Context(), r.URL.Query().Get("cursor"))
	if err != nil {
		return portalError(err)
	}
	var page map[string]json.RawMessage
	var rows []map[string]any
	if json.Unmarshal(raw, &page) != nil || json.Unmarshal(page["tenants"], &rows) != nil {
		return portalError(portalapi.ErrUnavailable)
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	// A slow AuthGate degrades the page (counts absent) rather than outlasting the Lambda.
	ctx, cancel := context.WithTimeout(r.Context(), ownerEnrichTimeout)
	defer cancel()
	sem := make(chan struct{}, ownerConcurrency)
	var wg sync.WaitGroup
	for _, row := range rows {
		tid, _ := row["tenantId"].(string)
		if !clientIDRE.MatchString(tid) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			raw, err := a.AuthGate.GetOwner(ctx, tid)
			var o struct {
				ClientCount *int `json:"clientCount"`
				ClientQuota *int `json:"clientQuota"`
			}
			if err != nil || json.Unmarshal(raw, &o) != nil || o.ClientCount == nil || o.ClientQuota == nil {
				return
			}
			row["clientCount"], row["clientQuota"] = *o.ClientCount, *o.ClientQuota
		}()
	}
	wg.Wait()
	out := map[string]any{"tenants": rows}
	if c, ok := page["nextCursor"]; ok {
		out["nextCursor"] = c
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// getTenant answers the Portal's tenant, AuthGate's owner and the owner's first page of clients.
func (a *App) getTenant(w http.ResponseWriter, r *http.Request) error {
	tid, err := tenantID(r)
	if err != nil {
		return err
	}
	raw, err := a.Portal.GetTenant(r.Context(), tid)
	if err != nil {
		return portalError(err)
	}
	out := map[string]any{}
	if json.Unmarshal(raw, &out) != nil {
		return portalError(portalapi.ErrUnavailable)
	}
	if out["owner"], err = a.AuthGate.GetOwner(r.Context(), tid); err != nil {
		return webapi.AuthGateError(err)
	}
	if out["clients"], err = a.AuthGate.ListClientsOwned(r.Context(), tid, ""); err != nil {
		return webapi.AuthGateError(err)
	}
	if s := a.lastSuspension(r.Context(), "tenant-suspend", tid); s != nil {
		out["lastSuspension"] = s
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func (a *App) tenantAudit(w http.ResponseWriter, r *http.Request) error {
	tid, err := tenantID(r)
	if err != nil {
		return err
	}
	raw, err := a.Portal.TenantAudit(r.Context(), tid, r.URL.Query().Get("cursor"))
	if err != nil {
		return portalError(err)
	}
	webapi.WriteRaw(w, http.StatusOK, raw)
	return nil
}

// findUser is the Portal's user view for an exact email, plus the last suspension.
func (a *App) findUser(w http.ResponseWriter, r *http.Request) error {
	email := r.URL.Query().Get("email")
	if email == "" || len(email) > 320 {
		return &webapi.Error{Status: http.StatusBadRequest, Code: "invalid_email"}
	}
	raw, err := a.Portal.FindUser(r.Context(), email)
	if err != nil {
		return portalError(err)
	}
	out := map[string]any{}
	if json.Unmarshal(raw, &out) != nil {
		return portalError(portalapi.ErrUnavailable)
	}
	if uid, _ := out["userId"].(string); uid != "" {
		if s := a.lastSuspension(r.Context(), "user-suspend", uid); s != nil {
			out["lastSuspension"] = s
		}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

type suspensionView struct {
	Reason   string    `json:"reason"`
	Username string    `json:"username"`
	At       time.Time `json:"at"`
}

// lastSuspension finds the newest entry of action on target in the Console audit, this
// month and the one before. A suspension that did not happen (a refused or failed
// attempt) does not count. It is nil when there is none or the audit cannot be read:
// the reason is a courtesy on the page, never a reason to fail it.
func (a *App) lastSuspension(ctx context.Context, action, target string) *suspensionView {
	now := a.now().UTC()
	for _, month := range []string{now.Format("2006-01"), time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0).Format("2006-01")} {
		cursor := ""
		for range suspensionPages {
			entries, next, err := a.Store.ListAudit(ctx, month, cursor)
			if err != nil {
				slog.Warn("console: read audit for last suspension", "err", err)
				return nil
			}
			for _, e := range entries {
				if e.Action == action && e.Target == target && (e.Result == "success" || e.Result == "partial") {
					return &suspensionView{Reason: e.Detail["reason"], Username: e.Username, At: e.At}
				}
			}
			if next == "" {
				break
			}
			cursor = next
		}
	}
	return nil
}
