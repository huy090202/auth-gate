package console_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os/user"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/console"
	"github.com/huy090202/auth-gate/internal/console/consoletest"
	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/password"
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
	// The =value form is refused too, and a name that merely starts with "password" is not a flag.
	if err := console.RunAdmin(ctx, []string{"create", "--username", "zed", "--role", "master", "--password=x"}, strings.NewReader(secret+"\n"), &out, d); err == nil {
		t.Fatal("--password=x was accepted")
	}
	if err := console.RunAdmin(ctx, []string{"create", "--username", "password-admin", "--role", "admin"}, strings.NewReader("a-fine-long-password\n"), &out, d); err != nil {
		t.Fatalf("a username starting with password was refused: %v", err)
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
	if a.Role != console.RoleMaster || !a.MustChangePassword || !password.Verify(good, a.PasswordHash) || strings.Contains(out.String(), good) {
		t.Fatalf("created %+v out %q", a, out.String())
	}

	next := "another-stdin-password"
	if err := console.RunAdmin(ctx, []string{"reset-password", "--username", "zed"}, strings.NewReader(next), &out, d); err != nil {
		t.Fatal(err)
	}
	r := getAdmin(t, h, "zed")
	if !password.Verify(next, r.PasswordHash) || r.SessionEpoch != a.SessionEpoch+1 || !r.MustChangePassword || strings.Contains(out.String(), next) {
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

// Why: the commands are the only way in when the web is out of reach, so they must leave a trace, whoever ran them and whatever came of it.
func TestCLICommandsAreAudited(t *testing.T) {
	h := consoletest.New(t)
	ctx := context.Background()
	b, _ := fullAdmin(t, h, "ada", console.RoleMaster)
	d := cliDeps(h)
	who := "cli:unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		who = "cli:" + u.Username
	}
	secret := "cli-audit-secret-password"
	next := "cli-audit-next-password"
	run := func(stdin string, args ...string) error {
		return console.RunAdmin(ctx, args, strings.NewReader(stdin), &strings.Builder{}, d)
	}
	h.Clock.Advance(time.Second)
	if err := run(secret+"\n", "create", "--username", "Zed", "--role", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := run(next+"\n", "reset-password", "--username", "zed"); err != nil {
		t.Fatal(err)
	}
	if err := run("", "disable", "--username", "zed"); err != nil {
		t.Fatal(err)
	}
	if err := run("", "clear-factors", "--username", "zed"); err != nil {
		t.Fatal(err)
	}
	if err := run("", "disable", "--username", "ada"); err == nil { // the last active master
		t.Fatal("the last master was disabled")
	}
	failed := "failed-command-password"
	if err := run(failed+"\n", "reset-password", "--username", "nobody"); err == nil {
		t.Fatal("reset of a missing admin worked")
	}
	if err := run("short\n", "create", "--username", "yan", "--role", "master"); err == nil {
		t.Fatal("a weak password was accepted")
	}

	entries, _ := auditOf(t, b, "")
	var cli []auditView
	for _, e := range entries {
		if e.Source == "cli" {
			cli = append(cli, e)
		}
	}
	type row struct{ action, target, result string }
	want := []row{ // newest first
		{"admin-create", "yan", "failed:invalid_password"}, {"admin-reset-password", "nobody", "failed:not_found"},
		{"admin-disable", "ada", "failed:last_master"}, {"admin-clear-factors", "zed", "success"},
		{"admin-disable", "zed", "success"}, {"admin-reset-password", "zed", "success"}, {"admin-create", "zed", "success"},
	}
	if len(cli) != len(want) {
		t.Fatalf("%d cli entries, want %d: %+v", len(cli), len(want), cli)
	}
	for i, e := range cli {
		if (row{e.Action, e.Target, e.Result}) != want[i] || e.Username != who || e.AdminID != "" || e.IP != "" {
			t.Errorf("entry %d = %+v, want %v as %s", i, e, want[i], who)
		}
	}
	detailIs(t, cli[6], map[string]string{"role": "admin"})
	items := scanTable(t, h)
	for _, s := range []string{secret, next, failed, "short"} {
		if strings.Contains(items, s) {
			t.Errorf("a command's password was stored")
		}
	}
}

// Why: the audit write must never turn a command that worked into a failure, or the operator reruns it, but the gap must be visible.
func TestCLIAuditWriteFailureIsOnlyAWarning(t *testing.T) {
	h := consoletest.New(t)
	ctx := context.Background()
	h.CreateAdmin("ada", pw, console.RoleMaster, false)
	h.CreateAdmin("zed", pw, console.RoleAdmin, false)
	opts := h.Store().DB.Options()
	base := opts.HTTPClient
	opts.HTTPClient = httpDo(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.Header.Get("X-Amz-Target"), ".PutItem") {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if bytes.Contains(raw, []byte("AUDIT#")) {
				return &http.Response{StatusCode: 400, Header: http.Header{"Content-Type": {"application/x-amz-json-1.0"}},
					Body: io.NopCloser(strings.NewReader(`{"__type":"com.amazon.coral.validate#ValidationException","message":"no"}`))}, nil
			}
		}
		return base.Do(r)
	})
	d := cliDeps(h)
	d.Store = &console.Store{DB: dynamodb.New(opts), Table: h.Deps.Config.Table}
	var stderr strings.Builder
	d.Stderr = &stderr

	if err := console.RunAdmin(ctx, []string{"disable", "--username", "zed"}, strings.NewReader(""), &strings.Builder{}, d); err != nil {
		t.Fatalf("a failed audit write changed the outcome: %v", err)
	}
	if getAdmin(t, h, "zed").DisabledAt == nil || !strings.Contains(stderr.String(), "warning") {
		t.Fatalf("zed disabled %v, stderr %q", getAdmin(t, h, "zed").DisabledAt != nil, stderr.String())
	}
	// A command that fails keeps its own error, not the audit's.
	err := console.RunAdmin(ctx, []string{"disable", "--username", "ada"}, strings.NewReader(""), &strings.Builder{}, d)
	if err == nil || !strings.Contains(err.Error(), "last active master") {
		t.Fatalf("err = %v", err)
	}
}

type httpDo func(*http.Request) (*http.Response, error)

func (f httpDo) Do(r *http.Request) (*http.Response, error) { return f(r) }
