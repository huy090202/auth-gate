package console_test

import (
	"context"
	"testing"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
)

// Why: the last factor is the only way back in; deleting it would lock the admin out.
func TestLastFactorRefused(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, true)
	pk := h.NewPasskey()
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.EnrollPIN(pin1), 200, "")
	r := b.EnrollPasskey(pk)
	want(t, r, 200, "")

	// With two factors, one can go.
	want(t, b.Do("DELETE", "/api/me/passkeys/"+r.Str("credentialId"), nil), 200, "")
	// The PIN is now the only one.
	want(t, b.Do("DELETE", "/api/me/pin", nil), 409, "last_factor")
	f, err := h.AuthGate.Factors(context.Background(), a.ID)
	if err != nil || !f.PIN || len(f.WebAuthn) != 0 {
		t.Fatalf("factors = %+v (%v)", f, err)
	}
	// A passkey that is the only factor is protected the same way.
	pk2 := h.NewPasskey()
	r = b.EnrollPasskey(pk2)
	want(t, r, 200, "")
	want(t, b.Do("DELETE", "/api/me/pin", nil), 200, "")
	want(t, b.Do("DELETE", "/api/me/passkeys/"+r.Str("credentialId"), nil), 409, "last_factor")
}

// Why: in a full session every change to one's own credentials asks for a step-up token.
// (Tests holding a token come with the routes that mint one.)
func TestFullSelfServiceWithoutTokenIsRefused(t *testing.T) {
	h := consoletest.New(t)
	a := h.CreateAdmin("ada", pw, console.RoleAdmin, false)
	withPIN(t, h, a, pin1)
	b := h.NewBrowser()
	want(t, b.Login("ada", pw), 200, "")
	want(t, b.SignInWithPIN(pin1), 200, "")
	want(t, b.Do("POST", "/api/me/password", passwordBody(pw, pw2)), 403, "step_up_required")
	want(t, b.EnrollPIN(pin2), 403, "step_up_required")
	want(t, b.Do("DELETE", "/api/me/pin", nil), 403, "step_up_required")
	want(t, b.StartPasskey(), 403, "step_up_required")
	want(t, b.Do("DELETE", "/api/me/passkeys/abc", nil), 403, "step_up_required")
	// Nothing changed.
	want(t, h.NewBrowser().Login("ada", pw), 200, "")
}
