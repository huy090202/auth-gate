package console

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/portalapi"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// ownerConcurrency bounds the AuthGate owner calls that enrich one tenants page.
const ownerConcurrency = 8

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
			raw, err := a.AuthGate.GetOwner(r.Context(), tid)
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
