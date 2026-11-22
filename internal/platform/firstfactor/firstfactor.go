// Package firstfactor is the part of the first-factor lease that the Console and
// the Portal share: the lease's timing rules, the send window of the write it
// protects, and taking and releasing it. What an app does around it (reading the
// factors, bumping the epoch, reissuing the session) stays in the app. The
// reasoning is in docs/memo/console-sessions.md: it is a lease with timeouts,
// not a fence.
package firstfactor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/authgate"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/lease"
	"github.com/huy090202/auth-gate/internal/platform/webapi"
)

// LeaseFor is how long a lease lasts, counted from the claim. It holds only
// while the write lands before it runs out. A write lands at most
// authgate.Timeout after it is sent (AuthGate's own function timeout), so the
// write is sent only within SendWindow of the claim. The app's Lambda timeout
// bounds nothing here: a write sent just before it can still land later.
const LeaseFor = 25 * time.Second

// SendMargin covers the network before AuthGate's clock starts, and clock skew
// between instances.
const SendMargin = 2 * time.Second

// SendWindow is how long after the claim the first-factor write may still be
// sent: the lease less the time a sent write can take to land, less SendMargin.
const SendWindow = LeaseFor - authgate.Timeout - SendMargin

// ErrSendWindowClosed is a write that was not sent because its send window had
// closed. Nothing went out, so nothing can land and the lease goes.
var ErrSendWindowClosed = fmt.Errorf("%w: first-factor write not sent, past its send window", authgate.ErrUnavailable)

// Lease is what Acquire hands the write. Release must be called once the write
// is over, with the write's error; Send runs the write.
type Lease struct {
	Release func(writeErr error)
	SendBy  time.Time // the end of the send window; zero when no lease was taken
	Now     func() time.Time
}

// None is the Lease of a write that needs no lease.
func None() Lease { return Lease{Release: func(error) {}} }

// Send runs the write under a deadline at the end of the send window, so a write
// that cannot go out in time is not sent. One already past it is not tried at all
// and answers ErrSendWindowClosed; one cut off by the deadline after it may have
// gone out is a no-answer failure, and its lease is kept.
func (l Lease) Send(ctx context.Context, write func(context.Context) error) error {
	if l.SendBy.IsZero() {
		return write(ctx)
	}
	left := l.SendBy.Sub(l.Now())
	if left <= 0 {
		return ErrSendWindowClosed
	}
	ctx, cancel := context.WithTimeout(ctx, left)
	defer cancel()
	return write(ctx)
}

// Account is the app's side of the lease: the claim and the release on its
// account item. Claim returns the end of the running lease and lease.ErrHeld
// when another holder has it; any other error is passed on as it is.
type Account struct {
	Claim   func(ctx context.Context, holder string, now, until time.Time) (time.Time, error)
	Release func(ctx context.Context, holder string) error
}

// Acquire claims the account's lease. A lease held by another session gives 409
// enrollment_in_progress with Retry-After and no AuthGate call. The returned
// Lease's Release keeps the lease when the write may still land (no answer, or
// an answered 5xx) and drops it otherwise.
func Acquire(ctx context.Context, acct Account, now func() time.Time) (Lease, error) {
	holder := id.New() // not the session token, which must stay secret
	t := now()
	held, err := acct.Claim(ctx, holder, t, t.Add(LeaseFor))
	if errors.Is(err, lease.ErrHeld) {
		wait := int(math.Ceil(held.Sub(t).Seconds()))
		return Lease{}, &webapi.Error{Status: http.StatusConflict, Code: "enrollment_in_progress", RetryAfter: max(wait, 1)}
	}
	if err != nil {
		return Lease{}, err
	}
	release := func(writeErr error) {
		if errors.Is(writeErr, authgate.ErrNoAnswer) || errors.Is(writeErr, authgate.ErrServerError) {
			return // the write may still land: let the lease run out
		}
		// The request may be cancelled; the lease should still go.
		if err := acct.Release(context.WithoutCancel(ctx), holder); err != nil {
			slog.Warn("firstfactor: release lease", "err", err)
		}
	}
	return Lease{Release: release, SendBy: t.Add(SendWindow), Now: now}, nil
}
