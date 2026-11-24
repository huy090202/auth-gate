package portal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/id"
)

func mkInvite(tid, email, hash string, now time.Time) Invite {
	return Invite{
		ID: id.New(), TenantID: tid, Email: NormalizeEmail(email), Role: RoleMember, InvitedBy: "inviter",
		TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(inviteTTL),
	}
}

func orgCounts(t *testing.T, st *Store, tid string) OrgCounts {
	t.Helper()
	tn, err := st.GetTenant(context.Background(), tid)
	if err != nil {
		t.Fatal(err)
	}
	return tn.Counts
}

// Why: the pending-invite cap is a counter on the tenant; under a race it must stop at twenty, not twenty-one.
func TestInvitesCapUnderRace(t *testing.T) {
	st := newStore(t)
	o := mkUser(t, st, "o@x.com")
	tn := mkOrg(t, st, o)
	now := time.Now().UTC()
	errs := make([]error, maxPendingInvites+1)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = st.CreateInvite(context.Background(), mkInvite(tn.ID, fmt.Sprintf("p%d@x.com", i), fmt.Sprintf("h%d", i), now), now)
		}()
	}
	wg.Wait()
	ok, full := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrInvitesFull):
			full++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != maxPendingInvites || full != 1 {
		t.Fatalf("ok = %d, full = %d, want %d and 1", ok, full, maxPendingInvites)
	}
	if c := orgCounts(t, st, tn.ID); c.Invites != maxPendingInvites {
		t.Fatalf("inviteCount = %d", c.Invites)
	}
}

// Why: the member cap must hold at accept time, and a refused accept must not burn the invite.
func TestAcceptIntoFullOrgRefused(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	o := mkUser(t, st, "o@x.com")
	tn := mkOrg(t, st, o)
	u := mkUser(t, st, "ann@x.com")
	now := time.Now().UTC()
	inv := mkInvite(tn.ID, "ann@x.com", "h1", now)
	if err := st.CreateInvite(ctx, inv, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: tenantKey(tn.ID),
		UpdateExpression:          aws.String("SET memberCount = :m"),
		ExpressionAttributeValues: item{":m": n(maxMembers)},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptInvite(ctx, inv, u, now); !errors.Is(err, ErrMembersFull) {
		t.Fatalf("accept = %v, want ErrMembersFull", err)
	}
	if _, err := st.PeekInvite(ctx, tn.ID, "h1", now); err != nil {
		t.Fatalf("invite gone after refused accept: %v", err)
	}
}

// Why: an invite is for one address only, and case or spaces must not break the match (Review Focus 1).
func TestInviteOnlyForInvitedAddress(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	o := mkUser(t, st, "o@x.com")
	tn := mkOrg(t, st, o)
	now := time.Now().UTC()
	inv := mkInvite(tn.ID, " ann@x.com ", "h1", now)
	inv.Role = RoleAdmin
	if err := st.CreateInvite(ctx, inv, now); err != nil {
		t.Fatal(err)
	}
	other := mkUser(t, st, "bob@x.com")
	if err := st.AcceptInvite(ctx, inv, other, now); !errors.Is(err, ErrInviteNotFound) {
		t.Fatalf("other address = %v, want ErrInviteNotFound", err)
	}
	ann := mkUser(t, st, "Ann@X.com")
	if err := st.AcceptInvite(ctx, inv, ann, now); err != nil {
		t.Fatalf("accept: %v", err)
	}
	m, err := st.GetMembership(ctx, tn.ID, ann.ID)
	if err != nil || m.Role != RoleAdmin {
		t.Fatalf("membership = %+v, %v", m, err)
	}
	if c := orgCounts(t, st, tn.ID); c.Members != 2 || c.Invites != 0 {
		t.Fatalf("counts = %+v", c)
	}
}

// Why: a link is single use; a second look at the same token must find nothing.
func TestInviteTokenSingleUse(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	o := mkUser(t, st, "o@x.com")
	tn := mkOrg(t, st, o)
	ann := mkUser(t, st, "ann@x.com")
	now := time.Now().UTC()
	inv := mkInvite(tn.ID, "ann@x.com", "h1", now)
	if err := st.CreateInvite(ctx, inv, now); err != nil {
		t.Fatal(err)
	}
	if err := st.AcceptInvite(ctx, inv, ann, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PeekInvite(ctx, tn.ID, "h1", now); !errors.Is(err, ErrInviteNotFound) {
		t.Fatalf("peek = %v", err)
	}
	if err := st.AcceptInvite(ctx, inv, ann, now); !errors.Is(err, ErrInviteNotFound) {
		t.Fatalf("second accept = %v", err)
	}
}

// Why: an expired invite must not be usable and must free its slot even before DynamoDB's TTL removes it.
func TestExpiredInviteRefusedAndFreed(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	o := mkUser(t, st, "o@x.com")
	tn := mkOrg(t, st, o)
	now := time.Now().UTC()
	if err := st.CreateInvite(ctx, mkInvite(tn.ID, "ann@x.com", "h1", now), now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(inviteTTL + time.Second)
	if _, err := st.PeekInvite(ctx, tn.ID, "h1", later); !errors.Is(err, ErrInviteNotFound) {
		t.Fatalf("peek = %v", err)
	}
	list, err := st.ListInvites(ctx, tn.ID, later)
	if err != nil || len(list) != 0 {
		t.Fatalf("list = %v, %v", list, err)
	}
	if c := orgCounts(t, st, tn.ID); c.Invites != 0 {
		t.Fatalf("inviteCount = %d", c.Invites)
	}
}

// Why: re-inviting an address whose old invite lapsed must work and keep the count right (Review Focus 2).
func TestReinviteAfterExpiry(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	o := mkUser(t, st, "o@x.com")
	tn := mkOrg(t, st, o)
	now := time.Now().UTC()
	if err := st.CreateInvite(ctx, mkInvite(tn.ID, "ann@x.com", "h1", now), now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(inviteTTL + time.Second)
	if err := st.CreateInvite(ctx, mkInvite(tn.ID, "ann@x.com", "h2", later), later); err != nil {
		t.Fatalf("reinvite: %v", err)
	}
	if c := orgCounts(t, st, tn.ID); c.Invites != 1 {
		t.Fatalf("inviteCount = %d, want 1", c.Invites)
	}
	if _, err := st.PeekInvite(ctx, tn.ID, "h2", later); err != nil {
		t.Fatalf("new invite: %v", err)
	}
	if got := rawItem(t, st, item{"pk": s("TENANT#" + tn.ID), "sk": s("INVITE#h1")}); got != nil {
		t.Fatal("old invite item still present")
	}
}

// Why: a re-sent invite must kill the link already in the inbox.
func TestResendKillsOldToken(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	o := mkUser(t, st, "o@x.com")
	tn := mkOrg(t, st, o)
	now := time.Now().UTC()
	inv := mkInvite(tn.ID, "ann@x.com", "h1", now)
	if err := st.CreateInvite(ctx, inv, now); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	got, err := st.ResendInvite(ctx, tn.ID, inv.ID, "h2", later)
	if err != nil || got.ID != inv.ID || got.TokenHash != "h2" || !got.ExpiresAt.Equal(later.Add(inviteTTL)) {
		t.Fatalf("resend = %+v, %v", got, err)
	}
	if _, err := st.PeekInvite(ctx, tn.ID, "h1", later); !errors.Is(err, ErrInviteNotFound) {
		t.Fatalf("old hash = %v", err)
	}
	if _, err := st.PeekInvite(ctx, tn.ID, "h2", later); err != nil {
		t.Fatalf("new hash: %v", err)
	}
	if c := orgCounts(t, st, tn.ID); c.Invites != 1 {
		t.Fatalf("inviteCount = %d", c.Invites)
	}
}

// Why: two live invites to one address would leave two valid links and a wrong count.
func TestInviteDuplicateRefused(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	o := mkUser(t, st, "o@x.com")
	tn := mkOrg(t, st, o)
	now := time.Now().UTC()
	if err := st.CreateInvite(ctx, mkInvite(tn.ID, "ann@x.com", "h1", now), now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInvite(ctx, mkInvite(tn.ID, "ANN@x.com", "h2", now), now); !errors.Is(err, ErrAlreadyInvited) {
		t.Fatalf("duplicate = %v", err)
	}
	if c := orgCounts(t, st, tn.ID); c.Invites != 1 {
		t.Fatalf("inviteCount = %d", c.Invites)
	}
}
