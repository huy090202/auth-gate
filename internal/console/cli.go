package console

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/huy090202/auth-gate/internal/console/authgate"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

// CLIDeps is what the operator commands need. AuthGate is only called by
// clear-factors, so the others never need the admin key.
type CLIDeps struct {
	Store           *Store
	AuthGate        func(ctx context.Context) (*authgate.Client, error)
	ConsoleClientID string
	Now             func() time.Time
}

const usage = "usage: console admin create --username U --role master|admin | reset-password --username U | disable --username U | clear-factors --username U (password on stdin)"

// RunAdmin runs one operator command. Passwords come from stdin only: a flag
// that names one is refused before anything is read or written. Nothing secret
// is written to stdout or into an error.
func RunAdmin(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, d CLIDeps) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") && strings.HasPrefix(strings.TrimLeft(a, "-"), "password") {
			return errors.New("passwords are read from stdin only; there is no --password flag")
		}
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	username := fs.String("username", "", "")
	role := fs.String("role", "", "")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return errors.New(usage)
	}
	name, err := NormalizeUsername(*username)
	if err != nil {
		return fmt.Errorf("--username: %w", err)
	}
	now := time.Now
	if d.Now != nil {
		now = d.Now
	}
	switch cmd {
	case "create":
		if !validRole(Role(*role)) {
			return errors.New("--role must be master or admin")
		}
		hash, err := hashFromStdin(stdin)
		if err != nil {
			return err
		}
		err = d.Store.CreateAdmin(ctx, Admin{ID: id.New(), Username: name, Role: Role(*role), PasswordHash: hash,
			MustChangePassword: true, CreatedAt: now().UTC()})
		if errors.Is(err, ErrUsernameTaken) {
			return fmt.Errorf("%s already exists", name)
		}
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "created %s %s; the password must be changed at first sign-in\n", *role, name)
	case "reset-password":
		hash, err := hashFromStdin(stdin)
		if err != nil {
			return err
		}
		if err := d.Store.SetPassword(ctx, name, hash, true, true); err != nil {
			return cliStoreError(err, name)
		}
		_, _ = fmt.Fprintf(stdout, "password of %s reset; sessions ended; it must be changed at next sign-in\n", name)
	case "disable":
		at := now().UTC()
		if err := d.Store.SetDisabled(ctx, nil, name, &at); err != nil {
			return cliStoreError(err, name)
		}
		_, _ = fmt.Fprintf(stdout, "disabled %s; sessions ended\n", name)
	case "clear-factors":
		return cliClearFactors(ctx, stdout, d, name)
	default:
		return errors.New(usage)
	}
	return nil
}

// cliClearFactors is the order of the web route: bump, purge, bump. The AuthGate
// client is obtained first, so a missing key changes nothing.
func cliClearFactors(ctx context.Context, stdout io.Writer, d CLIDeps, name string) error {
	if d.AuthGate == nil {
		return errors.New("no AuthGate client configured")
	}
	ag, err := d.AuthGate(ctx)
	if err != nil {
		return fmt.Errorf("authgate client: %w", err)
	}
	ad, err := d.Store.GetAdmin(ctx, name)
	if err != nil {
		return cliStoreError(err, name)
	}
	if err := d.Store.EndSessions(ctx, name); err != nil {
		return cliStoreError(err, name)
	}
	if err := ag.PurgeSubject(ctx, d.ConsoleClientID, ad.ID); err != nil {
		return fmt.Errorf("purge factors at AuthGate: %w (sessions of %s were ended; run it again)", err, name)
	}
	if err := d.Store.EndSessions(ctx, name); err != nil {
		return cliStoreError(err, name)
	}
	_, _ = fmt.Fprintf(stdout, "cleared the factors of %s; sessions ended; they enroll again at next sign-in\n", name)
	return nil
}

func cliStoreError(err error, name string) error {
	switch {
	case errors.Is(err, ErrBusy):
		return errors.New("another write was in flight; run the command again")
	case errors.Is(err, ErrConflict):
		return fmt.Errorf("refused: %s is the last active master", name)
	case errors.Is(err, ErrAdminNotFound):
		return fmt.Errorf("no admin %s", name)
	}
	return err
}

// hashFromStdin reads one line, checks the policy and returns its hash.
func hashFromStdin(stdin io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password: %w", err)
	}
	p := strings.TrimRight(line, "\r\n")
	if p == "" {
		return "", errors.New("the password goes on stdin")
	}
	if err := ValidatePassword(p); err != nil {
		return "", err
	}
	return HashPassword(p)
}
