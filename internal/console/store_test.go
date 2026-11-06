package console

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, table := dynamotest.NewTable(t)
	return &Store{DB: db, Table: table}
}

func mkAdmin(t *testing.T, st *Store, name string, role Role) Admin {
	t.Helper()
	a := Admin{ID: id.New(), Username: name, Role: role, PasswordHash: "h", CreatedAt: time.Now().UTC()}
	if err := st.CreateAdmin(context.Background(), a); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return a
}

// Why: two operators must not be able to claim the same username.
func TestCreateAdminDuplicate(t *testing.T) {
	st := newStore(t)
	mkAdmin(t, st, "huy", RoleMaster)
	err := st.CreateAdmin(context.Background(), Admin{ID: id.New(), Username: "huy", Role: RoleAdmin})
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("err = %v, want ErrUsernameTaken", err)
	}
}

// Why: the lock is what stops password guessing; it must trip on the fifth failure, not before.
func TestFiveFailuresLockFifteenMinutes(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	mkAdmin(t, st, "huy", RoleAdmin)
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	for i := 1; i <= 4; i++ {
		if err := st.RecordLoginFailure(ctx, "huy", now); err != nil {
			t.Fatal(err)
		}
		a, _ := st.GetAdmin(ctx, "huy")
		if a.FailedCount != i || a.LockedUntil != nil {
			t.Fatalf("after %d: count=%d locked=%v", i, a.FailedCount, a.LockedUntil)
		}
	}
	if err := st.RecordLoginFailure(ctx, "huy", now); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetAdmin(ctx, "huy")
	if a.FailedCount != 0 || a.LockedUntil == nil || !a.LockedUntil.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("after 5: count=%d locked=%v", a.FailedCount, a.LockedUntil)
	}
	if err := st.SetPassword(ctx, "huy", "h2", true, false); err != nil {
		t.Fatal(err)
	}
	if a, _ = st.GetAdmin(ctx, "huy"); a.LockedUntil != nil || !a.MustChangePassword || a.PasswordHash != "h2" {
		t.Fatalf("SetPassword left %+v", a)
	}
}

// Why: a single-use token that works twice is a replayable second factor.
func TestUseJTISingleUse(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Minute)
	if err := st.UseJTI(ctx, "j1", exp); err != nil {
		t.Fatal(err)
	}
	if err := st.UseJTI(ctx, "j1", exp); !errors.Is(err, ErrReplayed) {
		t.Fatalf("second use = %v, want ErrReplayed", err)
	}
}

// Why: the audit screen pages by cursor; a page boundary must neither repeat nor drop an entry.
func TestAuditNewestFirstAcrossPages(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	const total = 120
	for i := 0; i < total; i++ {
		// Fractions that Go's RFC3339Nano would trim differently still have to sort.
		at := base.Add(time.Duration(i) * 500 * time.Millisecond)
		if err := st.PutAudit(ctx, AuditEntry{AdminID: "a", Username: "u", Action: fmt.Sprint(i), At: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutAudit(ctx, AuditEntry{Action: "other-month", At: base.AddDate(0, 1, 0)}); err != nil {
		t.Fatal(err)
	}
	var got []string
	cursor, pages := "", 0
	for {
		es, next, err := st.ListAudit(ctx, "2026-11", cursor)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, e := range es {
			got = append(got, e.Action)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != total || pages != 3 {
		t.Fatalf("got %d entries in %d pages", len(got), pages)
	}
	for i, a := range got {
		if a != fmt.Sprint(total-1-i) {
			t.Fatalf("entry %d = %s, want %d", i, a, total-1-i)
		}
	}
}

// Why: the last-master rule must hold when two masters disable each other at once.
func TestMutualDisableCannotRemoveBothMasters(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for round := 0; round < 10; round++ {
		an, bn := fmt.Sprintf("a%d", round), fmt.Sprintf("b%d", round)
		a, b := mkAdmin(t, st, an, RoleMaster), mkAdmin(t, st, bn, RoleMaster)
		at := time.Now()
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i, pair := range [][2]Admin{{a, b}, {b, a}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = st.SetDisabled(ctx, &pair[0], pair[1].Username, &at)
			}()
		}
		close(start)
		wg.Wait()
		wins := 0
		for _, e := range errs {
			switch {
			case e == nil:
				wins++
			case !errors.Is(e, ErrConflict):
				t.Fatalf("round %d: unexpected error %v", round, e)
			}
		}
		if wins > 1 {
			t.Fatalf("round %d: both disables succeeded", round)
		}
		active := 0
		for _, n := range []string{an, bn} {
			if x, _ := st.GetAdmin(ctx, n); x.DisabledAt == nil {
				active++
			}
		}
		if active < 1 {
			t.Fatalf("round %d: no active master left", round)
		}
	}
}

// Why: the CLI must not disable a master that a concurrent web action has just made the last one.
func TestCLIDisableLosesRaceWithWebDisable(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a, b := mkAdmin(t, st, "a", RoleMaster), mkAdmin(t, st, "b", RoleMaster)
	at := time.Now()
	// Between the CLI choosing its survivor and writing, A disables B on the web.
	st.afterSurvivor = func() {
		st.afterSurvivor = nil
		if err := st.SetDisabled(ctx, &a, "b", &at); err != nil {
			t.Errorf("web disable: %v", err)
		}
	}
	if err := st.SetDisabled(ctx, nil, "a", &at); !errors.Is(err, ErrConflict) {
		t.Fatalf("cli disable = %v, want ErrConflict", err)
	}
	if x, _ := st.GetAdmin(ctx, "a"); x.DisabledAt != nil {
		t.Fatal("last active master was disabled")
	}
	// The last master is also refused plainly, for disable and demote.
	if err := st.SetDisabled(ctx, nil, "a", &at); !errors.Is(err, ErrConflict) {
		t.Fatalf("plain cli disable = %v", err)
	}
	if err := st.SetRole(ctx, nil, "a", RoleAdmin); !errors.Is(err, ErrConflict) {
		t.Fatalf("plain cli demote = %v", err)
	}
	_ = b
}

// Why: recording a challenge must not bring back a session that was signed out
// between the handler's read and its write, as an unconditional put would.
func TestSetChallengeDoesNotResurrectSession(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	x := Session{Hash: "h1", AdminID: "a", Username: "huy", Stage: StageHalf, Epoch: 0}
	if err := st.PutSession(ctx, x, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChallenge(ctx, "h1", "c1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRegistration(ctx, "h1", "r1"); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(ctx, "h1")
	if err != nil || got.ChallengeID != "c1" || got.RegistrationID != "r1" || got.Stage != StageHalf {
		t.Fatalf("session = %+v (%v)", got, err)
	}
	if err := st.DeleteSession(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetChallenge(ctx, "h1", "c2"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SetChallenge on a deleted session = %v, want ErrSessionNotFound", err)
	}
	if err := st.SetRegistration(ctx, "h1", "r2"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("SetRegistration on a deleted session = %v, want ErrSessionNotFound", err)
	}
	if _, err := st.GetSession(ctx, "h1"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("session came back: %v", err)
	}
}
