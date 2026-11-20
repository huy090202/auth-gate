package console

import (
	"errors"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/httpx"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

var (
	errForbidden  = &apiError{Status: http.StatusForbidden, Code: "forbidden"}
	errSelf       = &apiError{Status: http.StatusConflict, Code: "cannot_modify_self"}
	errAdminGone  = &apiError{Status: http.StatusNotFound, Code: "not_found"}
	errLastMaster = &apiError{Status: http.StatusConflict, Code: "last_master"}
	// errBusy is a transient clash between two writes. The step-up token was
	// spent, so the retry needs a new one.
	errBusy = &apiError{Status: http.StatusServiceUnavailable, Code: "busy", RetryAfter: 1}
)

// adminRoutes serves /api/admins* for a full session; the handlers then require
// the master role, read fresh with the session. The catch-alls keep the answer
// to a wrong stage, and to a path that is not a route.
func (a *App) adminRoutes(mux *http.ServeMux) {
	full := a.session(StageFull)
	mux.Handle("GET /api/admins", full(handlerFunc(a.listAdmins)))
	mux.Handle("POST /api/admins", a.audited("admin-create", full(handlerFunc(a.createAdmin))))
	mux.Handle("POST /api/admins/{username}/reset-password", a.audited("admin-reset-password", full(handlerFunc(a.resetPassword))))
	mux.Handle("POST /api/admins/{username}/clear-factors", a.audited("admin-clear-factors", full(handlerFunc(a.clearFactors))))
	mux.Handle("POST /api/admins/{username}/disable", a.audited("admin-disable", full(handlerFunc(a.disableAdmin))))
	mux.Handle("POST /api/admins/{username}/enable", a.audited("admin-enable", full(handlerFunc(a.enableAdmin))))
	mux.Handle("POST /api/admins/{username}/role", a.audited("admin-role", full(handlerFunc(a.setAdminRole))))
	for _, p := range []string{"/api/admins", "/api/admins/"} {
		mux.Handle(p, full(http.HandlerFunc(notFound)))
	}
}

// adminView is what GET /admins shows. Admin itself is never serialized: it
// carries the password hash.
type adminView struct {
	Username           string     `json:"username"`
	Role               Role       `json:"role"`
	MustChangePassword bool       `json:"mustChangePassword"`
	LockedUntil        *time.Time `json:"lockedUntil"`
	DisabledAt         *time.Time `json:"disabledAt"`
	CreatedAt          time.Time  `json:"createdAt"`
}

func (a *App) listAdmins(w http.ResponseWriter, r *http.Request) error {
	if _, actor := sessionFrom(r.Context()); actor.Role != RoleMaster {
		return errForbidden
	}
	all, err := a.Store.ListAdmins(r.Context())
	if err != nil {
		return err
	}
	now := a.now()
	out := make([]adminView, 0, len(all))
	for _, ad := range all {
		v := adminView{Username: ad.Username, Role: ad.Role, MustChangePassword: ad.MustChangePassword,
			DisabledAt: ad.DisabledAt, CreatedAt: ad.CreatedAt}
		if ad.LockedUntil != nil && now.Before(*ad.LockedUntil) {
			v.LockedUntil = ad.LockedUntil
		}
		out = append(out, v)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"admins": out})
	return nil
}

func validRole(r Role) bool { return r == RoleMaster || r == RoleAdmin }

// adminWriteError turns a failed guarded write into the Console's answer. A
// transient clash is told apart from a failed guard: ErrBusy wraps ErrConflict,
// so it is checked first.
func adminWriteError(err error) error {
	switch {
	case errors.Is(err, ErrBusy):
		return errBusy
	case errors.Is(err, ErrConflict):
		return errLastMaster
	case errors.Is(err, ErrAdminNotFound):
		return errAdminGone
	}
	return err
}

func (a *App) writing() {
	if a.beforeAdminWrite != nil {
		a.beforeAdminWrite()
	}
}

func tempPasswordBody(w http.ResponseWriter, status int, username, temp string) {
	httpx.WriteJSON(w, status, map[string]string{"username": username, "temporaryPassword": temp})
}

func (a *App) createAdmin(w http.ResponseWriter, r *http.Request) error {
	x, actor := sessionFrom(r.Context())
	var in struct {
		Username string `json:"username"`
		Role     Role   `json:"role"`
	}
	// Decoded first so that a refusal below still records whose account was asked for.
	decErr := decodeBody(r, &in)
	name, nameErr := NormalizeUsername(in.Username)
	if decErr == nil && nameErr == nil {
		setAuditTarget(r.Context(), name)
	}
	if actor.Role != RoleMaster {
		return errForbidden
	}
	if decErr != nil {
		return decErr
	}
	if nameErr != nil {
		return &apiError{Status: http.StatusBadRequest, Code: "invalid_username"}
	}
	if !validRole(in.Role) {
		return &apiError{Status: http.StatusBadRequest, Code: "invalid_role"}
	}
	setAuditDetail(r.Context(), "role", string(in.Role))
	tok, err := a.checkStepUp(r, x, "admin-create", name+"/"+string(in.Role))
	if err != nil {
		return err
	}
	temp := NewTempPassword()
	hash, err := HashPassword(temp)
	if err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	a.writing()
	err = a.Store.CreateAdmin(r.Context(), Admin{ID: id.New(), Username: name, Role: in.Role,
		PasswordHash: hash, MustChangePassword: true, CreatedAt: a.now().UTC()})
	if errors.Is(err, ErrUsernameTaken) {
		return &apiError{Status: http.StatusConflict, Code: "username_taken"}
	}
	if err != nil {
		return err
	}
	tempPasswordBody(w, http.StatusCreated, name, temp)
	return nil
}

// adminTarget is the start of every /admins/{username}/* action: the caller must
// be a master (as read with this request, so a demotion counts at once, before
// any token work), the name must be one and not the caller's own (so no PIN is
// asked for a refusal), the token must be valid for the action (checked, not
// spent) for this action on this username (qualify adds what else the token is
// bound to, the role of admin-role; its failure answers before the token is
// looked at), and the target must exist. The caller spends the token itself,
// right before it writes.
func (a *App) adminTarget(r *http.Request, action string, qualify func() (string, error)) (*stepUp, Admin, Admin, error) {
	x, actor := sessionFrom(r.Context())
	name, err := NormalizeUsername(r.PathValue("username"))
	if err == nil {
		setAuditTarget(r.Context(), name) // recorded for a refusal by the role check too
	}
	if actor.Role != RoleMaster {
		return nil, Admin{}, Admin{}, errForbidden
	}
	if err != nil {
		return nil, Admin{}, Admin{}, &apiError{Status: http.StatusBadRequest, Code: "invalid_username"}
	}
	if name == actor.Username {
		return nil, Admin{}, Admin{}, errSelf
	}
	stepTarget := name
	if qualify != nil {
		q, err := qualify()
		if err != nil {
			return nil, Admin{}, Admin{}, err
		}
		stepTarget += "/" + q
	}
	tok, err := a.checkStepUp(r, x, action, stepTarget)
	if err != nil {
		return nil, Admin{}, Admin{}, err
	}
	target, err := a.Store.GetAdmin(r.Context(), name)
	if errors.Is(err, ErrAdminNotFound) {
		return nil, Admin{}, Admin{}, errAdminGone
	}
	if err != nil {
		return nil, Admin{}, Admin{}, err
	}
	return tok, actor, target, nil
}

func (a *App) resetPassword(w http.ResponseWriter, r *http.Request) error {
	tok, _, target, err := a.adminTarget(r, "admin-reset-password", nil)
	if err != nil {
		return err
	}
	temp := NewTempPassword()
	hash, err := HashPassword(temp)
	if err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	a.writing()
	if err := a.Store.SetPassword(r.Context(), target.Username, hash, true, true); err != nil {
		return adminWriteError(err)
	}
	tempPasswordBody(w, http.StatusOK, target.Username, temp)
	return nil
}

// clearFactors removes every factor the admin has at AuthGate. The order is
// bump, purge, bump: the first bump ends the sessions that exist, so a stale one
// cannot read zero factors and add one that survives the purge; the second ends
// a session opened between the first bump and the purge. The purge goes through
// the AuthGate client directly, not the protected /clients routes.
func (a *App) clearFactors(w http.ResponseWriter, r *http.Request) error {
	tok, _, target, err := a.adminTarget(r, "admin-clear-factors", nil)
	if err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	a.writing()
	if err := a.Store.EndSessions(r.Context(), target.Username); err != nil {
		return adminWriteError(err)
	}
	if err := a.AuthGate.PurgeSubject(r.Context(), a.Config.AuthGateClientID, target.ID); err != nil {
		return agError(err)
	}
	if err := a.Store.EndSessions(r.Context(), target.Username); err != nil {
		return adminWriteError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"username": target.Username})
	return nil
}

func (a *App) disableAdmin(w http.ResponseWriter, r *http.Request) error {
	tok, actor, target, err := a.adminTarget(r, "admin-disable", nil)
	if err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	a.writing()
	at := a.now().UTC()
	if err := a.Store.SetDisabled(r.Context(), &actor, target.Username, &at); err != nil {
		return adminWriteError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"username": target.Username})
	return nil
}

func (a *App) enableAdmin(w http.ResponseWriter, r *http.Request) error {
	tok, actor, target, err := a.adminTarget(r, "admin-enable", nil)
	if err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	a.writing()
	if err := a.Store.SetDisabled(r.Context(), &actor, target.Username, nil); err != nil {
		return adminWriteError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"username": target.Username})
	return nil
}

func (a *App) setAdminRole(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Role Role `json:"role"`
	}
	tok, actor, target, err := a.adminTarget(r, "admin-role", func() (string, error) {
		if err := decodeBody(r, &in); err != nil {
			return "", err
		}
		if !validRole(in.Role) {
			return "", &apiError{Status: http.StatusBadRequest, Code: "invalid_role"}
		}
		setAuditDetail(r.Context(), "role", string(in.Role))
		return string(in.Role), nil
	})
	if err != nil {
		return err
	}
	if err := a.spendStepUp(r, tok); err != nil {
		return err
	}
	a.writing()
	if err := a.Store.SetRole(r.Context(), &actor, target.Username, in.Role); err != nil {
		return adminWriteError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"username": target.Username, "role": in.Role})
	return nil
}
