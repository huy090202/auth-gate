package console_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/authgate"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func cliDeps(h *consoletest.Harness) console.CLIDeps {
	return console.CLIDeps{
		Store:           h.Store(),
		AuthGate:        func(context.Context) (*authgate.Client, error) { return h.AuthGate, nil },
		ConsoleClientID: h.ClientID,
		Now:             h.Clock.Now,
	}
}

// Why: a secret on argv shows in ps and shell history, and a weak one defeats the policy.
func TestCLIReadsPasswordFromStdinOnly(t *testing.T) {
	h := consoletest.New(t)
	ctx := context.Background()
	d := cliDeps(h)
	secret := "argv-secret-password"
	var out strings.Builder

	err := console.RunAdmin(ctx, []string{"create", "--username", "zed", "--role", "master", "--password", secret}, strings.NewReader(secret+"\n"), &out, d)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(out.String(), secret) {
		t.Fatalf("--password: err %v out %q", err, out.String())
	}
	if _, e := h.Store().GetAdmin(ctx, "zed"); e == nil {
		t.Fatal("an admin was written after --password")
	}
	if err := console.RunAdmin(ctx, []string{"create", "--username", "zed", "--role", "master"}, strings.NewReader("short\n"), &out, d); err == nil {
		t.Fatal("a weak password was accepted")
	}
	if _, e := h.Store().GetAdmin(ctx, "zed"); e == nil {
		t.Fatal("an admin was written with a weak password")
	}

	good := "stdin-long-password"
	if err := console.RunAdmin(ctx, []string{"create", "--username", "Zed", "--role", "master"}, strings.NewReader(good+"\n"), &out, d); err != nil {
		t.Fatal(err)
	}
	a := getAdmin(t, h, "zed")
	if a.Role != console.RoleMaster || !a.MustChangePassword || !console.VerifyPassword(good, a.PasswordHash) || strings.Contains(out.String(), good) {
		t.Fatalf("created %+v out %q", a, out.String())
	}

	next := "another-stdin-password"
	if err := console.RunAdmin(ctx, []string{"reset-password", "--username", "zed"}, strings.NewReader(next), &out, d); err != nil {
		t.Fatal(err)
	}
	r := getAdmin(t, h, "zed")
	if !console.VerifyPassword(next, r.PasswordHash) || r.SessionEpoch != a.SessionEpoch+1 || !r.MustChangePassword || strings.Contains(out.String(), next) {
		t.Fatalf("reset %+v", r)
	}
}

// Why: factors live at AuthGate, so the command must call it, in the order that keeps a stale session from re-adding one.
func TestCLIClearFactorsCallsAuthGate(t *testing.T) {
	h := consoletest.New(t)
	ctx := context.Background()
	h.CreateAdmin("ada", pw, console.RoleMaster, false)
	bob := h.CreateAdmin("bob", pw, console.RoleAdmin, false)
	withPIN(t, h, bob, pin1)

	var calls []string
	epochAtPurge := -1
	rec := *h.AuthGate
	base := rec.HTTP.Transport
	rec.HTTP = &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
		calls = append(calls, req.Method+" "+req.URL.Path)
		epochAtPurge = getAdmin(t, h, "bob").SessionEpoch
		return base.RoundTrip(req)
	})}
	d := cliDeps(h)
	d.AuthGate = func(context.Context) (*authgate.Client, error) { return &rec, nil }
	var out strings.Builder
	if err := console.RunAdmin(ctx, []string{"clear-factors", "--username", "bob"}, strings.NewReader(""), &out, d); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !strings.HasSuffix(calls[0], "/api/clients/"+h.ClientID+"/subjects/"+bob.ID) || !strings.HasPrefix(calls[0], "DELETE ") {
		t.Fatalf("calls = %v", calls)
	}
	if epochAtPurge != bob.SessionEpoch+1 || getAdmin(t, h, "bob").SessionEpoch != bob.SessionEpoch+2 {
		t.Fatalf("epoch at purge %d, final %d, start %d", epochAtPurge, getAdmin(t, h, "bob").SessionEpoch, bob.SessionEpoch)
	}
	if f := factorsOf(t, h, bob); f.PIN {
		t.Fatal("PIN remains")
	}

	// No AuthGate client, no change at all.
	e := getAdmin(t, h, "bob").SessionEpoch
	d.AuthGate = func(context.Context) (*authgate.Client, error) { return nil, context.DeadlineExceeded }
	if err := console.RunAdmin(ctx, []string{"clear-factors", "--username", "bob"}, strings.NewReader(""), &out, d); err == nil {
		t.Fatal("clear-factors worked without AuthGate")
	}
	if getAdmin(t, h, "bob").SessionEpoch != e {
		t.Fatal("sessions were ended although AuthGate was out of reach")
	}
}
