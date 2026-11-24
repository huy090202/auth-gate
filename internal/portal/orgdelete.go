package portal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
	pmail "github.com/huy090202/auth-gate/internal/portal/mail"
)

// mountOrgDelete serves the deletion of an organisation. It is the one route a
// tenant being deleted still answers, so a stopped deletion can be run again.
func (a *App) mountOrgDelete(mux *http.ServeMux) {
	full := a.session(StageFull)
	mux.Handle("DELETE /api/t/{tenantId}", a.audited("org-delete", full(a.memberAny(true, RoleOwner)(webapi.HandlerFunc(a.deleteOrg)))))
}

// deleteOrg deletes an organisation (spec section 4) once its owner has typed its
// name again and passed a step-up. A deletion AuthGate stopped answers 502 and
// keeps deletingAt, so the next DELETE or a member's next workspace list ends it.
func (a *App) deleteOrg(w http.ResponseWriter, r *http.Request) error {
	x, u := sessionFrom(r.Context())
	ctx := r.Context()
	t := tenantFrom(ctx)
	if t.Kind != KindOrg {
		return orgErr(ErrPersonal)
	}
	tok, err := a.checkStepUp(r, x, "delete-org", t.ID)
	if err != nil {
		return err
	}
	var in struct{ Name string }
	if err := webapi.DecodeBody(r, &in); err != nil {
		return err
	}
	setAuditTarget(ctx, t.ID)
	if strings.TrimSpace(in.Name) != t.Name {
		return apiErr(http.StatusBadRequest, "name_mismatch")
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	if t, err = a.Store.MarkDeleting(ctx, t.ID, a.now()); err != nil {
		return orgErr(err)
	}
	a.auditOrgDelete(r, u, t.ID)
	auditWritten(ctx)
	if err := a.finishOrgDeletion(ctx, t, u.ID); err != nil {
		slog.Warn("portal: organisation deletion stopped; it resumes on the next visit", "tenant", t.ID, "err", err)
		if errors.Is(err, errAuthGateDown) {
			return errAuthGateDown
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// auditOrgDelete writes the org-delete entry in the organisation's log at once,
// before the log gets its 30-day ttl: written when the request ends, as audited
// does, it would carry its own 180 days and outlive the organisation.
func (a *App) auditOrgDelete(r *http.Request, u User, tenantID string) {
	a.writeAudit(r, AuditEntry{
		TenantID: tenantID, ActorID: u.ID, ActorName: u.DisplayName, Action: "org-delete",
		Target: tenantID, Result: "success", IP: a.auditIP(r), At: a.now(),
	})
}

// finishOrgDeletion runs steps 2 to 7 of spec section 4 on a tenant marked
// deletingAt. Every step skips what is already gone, so it may run any number
// of times: the clients at AuthGate, the invites, every other member's
// membership, the store's end of the tenant (FinishDeletion), and the actor's
// membership last, so the actor can still run it again if anything before
// fails. Each member is mailed org-deleted once their membership is dropped.
// An AuthGate failure is wrapped around errAuthGateDown.
func (a *App) finishOrgDeletion(ctx context.Context, t Tenant, actorID string) error {
	members, err := a.Store.tenantMemberships(ctx, t.ID)
	if err != nil {
		return err
	}
	ids, err := a.workspaceClients(ctx, t.ID)
	if err != nil {
		return fmt.Errorf("%w: list clients: %w", errAuthGateDown, err)
	}
	if err := a.deleteClients(ctx, t.ID, ids); err != nil {
		return err
	}
	if err := a.Store.DeleteAllInvites(ctx, t.ID); err != nil {
		return err
	}
	drop := func(userID string) error {
		if err := a.Store.DropMembership(ctx, t.ID, userID); err != nil {
			return err
		}
		a.mailUser(ctx, userID, "org-deleted", map[string]string{"org": t.Name})
		return nil
	}
	for _, m := range members {
		if m.UserID == actorID {
			continue
		}
		if err := drop(m.UserID); err != nil {
			return err
		}
	}
	if err := a.Store.FinishDeletion(ctx, t, a.now()); err != nil {
		return err
	}
	// Always, even when the tenant's side was not listed: a user side left
	// behind would make every workspace list run this again.
	return drop(actorID)
}

// deleteClients deletes the workspace's clients at AuthGate. One already gone
// (404) is skipped; any other failure stops it, wrapped around errAuthGateDown.
func (a *App) deleteClients(ctx context.Context, tenantID string, ids []string) error {
	for _, c := range ids {
		var ae *authgate.Error
		err := a.AuthGate.Tenant(tenantID).Delete(ctx, c)
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: delete client: %w", errAuthGateDown, err)
		}
	}
	return nil
}

// mailUser sends a notice to a user in their own language. It is best effort:
// a user who cannot be read (or is gone) is logged, not answered.
func (a *App) mailUser(ctx context.Context, userID, kind string, vars map[string]string) {
	to, err := a.Store.GetUser(ctx, userID)
	if err != nil {
		slog.Warn("portal: org notice not sent", "kind", kind, "err", err)
		return
	}
	a.mailBestEffort(ctx, pmail.Message{To: to.Email, Lang: to.Lang, Kind: kind, Vars: vars})
}
