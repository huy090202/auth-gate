package console

import (
	"encoding/json"
	"net/http"

	"github.com/huy090202/auth-gate/internal/console/authgate"
	"github.com/huy090202/auth-gate/internal/platform/httpx"
)

// meRoutes serves /api/me/*. Every write calls requireStepUp first, which lets
// an enrolling session through and asks a full one for a token.
func (a *App) meRoutes(mux *http.ServeMux) {
	anyStage := a.session(StageHalf, StageEnroll, StageFull)
	self := a.session(StageEnroll, StageFull)
	mux.Handle("GET /api/me", anyStage(handlerFunc(a.me)))
	mux.Handle("POST /api/me/password", self(handlerFunc(a.changePassword)))
	mux.Handle("POST /api/me/pin", self(handlerFunc(a.setPIN)))
	mux.Handle("DELETE /api/me/pin", self(handlerFunc(a.deletePIN)))
	mux.Handle("POST /api/me/passkeys/start", self(handlerFunc(a.passkeyStart)))
	mux.Handle("POST /api/me/passkeys/finish", self(handlerFunc(a.passkeyFinish)))
	mux.Handle("DELETE /api/me/passkeys/{id}", self(handlerFunc(a.deletePasskey)))
}

// me describes the signed-in admin. A half session has only proved the password,
// so it does not see the factor list.
func (a *App) me(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	out := map[string]any{
		"username": admin.Username, "role": admin.Role, "stage": x.Stage, "mustChangePassword": admin.MustChangePassword,
	}
	if x.Stage != StageHalf {
		f, err := a.AuthGate.Factors(r.Context(), admin.ID)
		if err != nil {
			return agError(err)
		}
		if f.WebAuthn == nil {
			f.WebAuthn = []authgate.Credential{}
		}
		out["factors"] = f
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

// settle moves an enrolling session to full once the password is no longer
// temporary and a factor exists, and returns the stage the session is in.
func (a *App) settle(w http.ResponseWriter, r *http.Request, x Session, mustChange bool) (Stage, error) {
	if x.Stage != StageEnroll || mustChange {
		return x.Stage, nil
	}
	f, err := a.AuthGate.Factors(r.Context(), x.AdminID)
	if err != nil {
		return "", agError(err)
	}
	if !f.PIN && len(f.WebAuthn) == 0 {
		return StageEnroll, nil
	}
	if err := a.promote(r.Context(), w, x, StageFull); err != nil {
		return "", err
	}
	return StageFull, nil
}

func (a *App) changePassword(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	if err := a.requireStepUp(r, x, "change-password"); err != nil {
		return err
	}
	var in struct{ CurrentPassword, NewPassword string }
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if !a.verifyPassword(in.CurrentPassword, admin.PasswordHash) {
		return &apiError{Status: http.StatusBadRequest, Code: "wrong_password"}
	}
	// The current password has just been verified, so equal strings mean "same as current".
	if in.NewPassword == in.CurrentPassword || ValidatePassword(in.NewPassword) != nil {
		return &apiError{Status: http.StatusBadRequest, Code: "invalid_password"}
	}
	hash, err := HashPassword(in.NewPassword)
	if err != nil {
		return err
	}
	if err := a.Store.SetPassword(r.Context(), admin.Username, hash, false, false); err != nil {
		return err
	}
	st, err := a.settle(w, r, x, false)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": st})
	return nil
}

func (a *App) setPIN(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	if err := a.requireStepUp(r, x, "add-factor"); err != nil {
		return err
	}
	var in struct{ PIN string }
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if err := a.AuthGate.SetPIN(r.Context(), admin.ID, in.PIN); err != nil {
		return agError(err)
	}
	st, err := a.settle(w, r, x, admin.MustChangePassword)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": st})
	return nil
}

func (a *App) deletePIN(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	if err := a.requireStepUp(r, x, "remove-factor"); err != nil {
		return err
	}
	f, err := a.AuthGate.Factors(r.Context(), admin.ID)
	if err != nil {
		return agError(err)
	}
	if !f.PIN {
		return &apiError{Status: http.StatusConflict, Code: "factor_not_enrolled"}
	}
	if len(f.WebAuthn) == 0 {
		return &apiError{Status: http.StatusConflict, Code: "last_factor"}
	}
	if err := a.AuthGate.DeletePIN(r.Context(), admin.ID); err != nil {
		return agError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": x.Stage})
	return nil
}

// passkeyStart is where a full session is asked for its step-up token; the finish
// is tied to this start by the registration id kept in the session.
func (a *App) passkeyStart(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	if err := a.requireStepUp(r, x, "add-factor"); err != nil {
		return err
	}
	id, publicKey, err := a.AuthGate.StartRegistration(r.Context(), admin.ID, admin.Username)
	if err != nil {
		return agError(err)
	}
	if err := a.Store.SetRegistration(r.Context(), x.Hash, id); err != nil {
		return sessionGone(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"registrationId": id, "publicKey": publicKey})
	return nil
}

func (a *App) passkeyFinish(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	var in struct {
		RegistrationID string          `json:"registrationId"`
		Credential     json.RawMessage `json:"credential"`
	}
	if err := decodeBody(r, &in); err != nil {
		return err
	}
	if x.RegistrationID == "" || in.RegistrationID != x.RegistrationID {
		return errChallengeGone
	}
	credID, err := a.AuthGate.FinishRegistration(r.Context(), in.RegistrationID, in.Credential)
	if err != nil {
		return agError(err)
	}
	st, err := a.settle(w, r, x, admin.MustChangePassword)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"credentialId": credID, "stage": st})
	return nil
}

func (a *App) deletePasskey(w http.ResponseWriter, r *http.Request) error {
	x, admin := sessionFrom(r.Context())
	if err := a.requireStepUp(r, x, "remove-factor"); err != nil {
		return err
	}
	id := r.PathValue("id")
	f, err := a.AuthGate.Factors(r.Context(), admin.ID)
	if err != nil {
		return agError(err)
	}
	found := false
	for _, c := range f.WebAuthn {
		found = found || c.CredentialID == id
	}
	if !found {
		return &apiError{Status: http.StatusConflict, Code: "factor_not_enrolled"}
	}
	if !f.PIN && len(f.WebAuthn) == 1 {
		return &apiError{Status: http.StatusConflict, Code: "last_factor"}
	}
	if err := a.AuthGate.DeleteCredential(r.Context(), admin.ID, id); err != nil {
		return agError(err)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"stage": x.Stage})
	return nil
}
