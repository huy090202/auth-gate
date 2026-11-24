package portal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/id"
)

func mkOrg(t *testing.T, st *Store, u User) Tenant {
	t.Helper()
	tn, err := st.CreateOrg(context.Background(), u, id.New(), "Acme", time.Now().UTC())
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	return tn
}

// addMember stands in for the invite accept of a later task: it writes the pair and the counters directly.
func addMember(t *testing.T, st *Store, tid string, u User, role string) {
	t.Helper()
	ctx := context.Background()
	now := ts(time.Now().UTC())
	for _, key := range []item{memberKey(tid, u.ID), userTenantKey(u.ID, tid)} {
		key["role"], key["joinedAt"] = s(role), s(now)
		if _, err := st.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(st.Table), Item: key}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: tenantKey(tid),
		UpdateExpression:          aws.String("ADD memberCount :one"),
		ExpressionAttributeValues: item{":one": n(1)},
	}); err != nil {
		t.Fatal(err)
	}
}

func owner(tid, uid string) Membership {
	return Membership{TenantID: tid, UserID: uid, Role: RoleOwner}
}

// Why: the quota is a count of creations per person; under a race it must still stop at three, not four.
func TestCreateOrgQuotaUnderRace(t *testing.T) {
	st := newStore(t)
	u := mkUser(t, st, "a@x.com")
	errs := make([]error, 4)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = st.CreateOrg(context.Background(), u, id.New(), "Org", time.Now().UTC())
		}()
	}
	wg.Wait()
	ok, quota := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrOrgQuota):
			quota++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != maxOrgsCreated || quota != 1 {
		t.Fatalf("ok = %d, quota = %d, want %d and 1", ok, quota, maxOrgsCreated)
	}
	got, err := st.GetUser(context.Background(), u.ID)
	if err != nil || got.OrgsCreated != maxOrgsCreated {
		t.Fatalf("orgsCreated = %d, err %v", got.OrgsCreated, err)
	}
}

// Why: a new organisation must start with its creator as the one owner and counters that match, or the guards later misfire.
func TestCreateOrgWritesOwner(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	u := mkUser(t, st, "a@x.com")
	tn := mkOrg(t, st, u)
	m, err := st.GetMembership(ctx, tn.ID, u.ID)
	if err != nil || m.Role != RoleOwner {
		t.Fatalf("membership = %+v, err %v", m, err)
	}
	got, err := st.GetTenant(ctx, tn.ID)
	if err != nil || got.Kind != KindOrg || got.Name != "Acme" || got.Counts != (OrgCounts{1, 1, 0}) {
		t.Fatalf("tenant = %+v, err %v", got, err)
	}
	if rawItem(t, st, tenantIndexKey(tn.ID)) == nil {
		t.Fatal("no index item")
	}
	list, err := st.ListMemberships(ctx, u.ID)
	if err != nil || len(list) != 1 || list[0].TenantID != tn.ID || list[0].Role != RoleOwner {
		t.Fatalf("memberships = %+v, err %v", list, err)
	}
}

// Why: two owners stepping down at once must not leave an organisation with no owner.
func TestTwoOwnersDemotingEachOtherLeaveOne(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a, b := mkUser(t, st, "a@x.com"), mkUser(t, st, "b@x.com")
	tn := mkOrg(t, st, a)
	addMember(t, st, tn.ID, b, RoleMember)
	if _, err := st.SetRole(ctx, tn.ID, owner(tn.ID, a.ID), b.ID, RoleOwner); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, errs[0] = st.SetRole(ctx, tn.ID, owner(tn.ID, a.ID), b.ID, RoleAdmin) }()
	go func() { defer wg.Done(); _, errs[1] = st.SetRole(ctx, tn.ID, owner(tn.ID, b.ID), a.ID, RoleAdmin) }()
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrLastOwner), errors.Is(err, ErrConflict):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d demotions succeeded, want 1 (%v)", ok, errs)
	}
	got, _ := st.GetTenant(ctx, tn.ID)
	owners := 0
	for _, u := range []User{a, b} {
		if m, _ := st.GetMembership(ctx, tn.ID, u.ID); m.Role == RoleOwner {
			owners++
		}
	}
	if got.Counts.Owners != 1 || owners != 1 {
		t.Fatalf("ownerCount = %d, owner memberships = %d, want 1 and 1", got.Counts.Owners, owners)
	}
}

// Why: the sole owner leaving or stepping down would orphan the organisation.
func TestLastOwnerCannotLeaveOrStepDown(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a := mkUser(t, st, "a@x.com")
	tn := mkOrg(t, st, a)
	if _, err := st.RemoveMember(ctx, tn.ID, owner(tn.ID, a.ID), a.ID); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("remove self: %v, want ErrLastOwner", err)
	}
	if _, err := st.SetRole(ctx, tn.ID, owner(tn.ID, a.ID), a.ID, RoleAdmin); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("step down: %v, want ErrLastOwner", err)
	}
}

// Why: a removal must take both items of the pair and one off the member count, or the quota and the listings drift.
func TestRemoveMemberCounts(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a, b := mkUser(t, st, "a@x.com"), mkUser(t, st, "b@x.com")
	tn := mkOrg(t, st, a)
	addMember(t, st, tn.ID, b, RoleMember)
	old, err := st.RemoveMember(ctx, tn.ID, owner(tn.ID, a.ID), b.ID)
	if err != nil || old.Role != RoleMember || old.UserID != b.ID {
		t.Fatalf("removed = %+v, err %v", old, err)
	}
	got, _ := st.GetTenant(ctx, tn.ID)
	if got.Counts != (OrgCounts{1, 1, 0}) {
		t.Fatalf("counts = %+v", got.Counts)
	}
	if rawItem(t, st, memberKey(tn.ID, b.ID)) != nil || rawItem(t, st, userTenantKey(b.ID, tn.ID)) != nil {
		t.Fatal("a half of the pair is left")
	}
}

// Why: an owner demoted a moment ago must not still be able to change roles on the strength of a stale read.
func TestActorDemotedMidwayIsRefused(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a, b := mkUser(t, st, "a@x.com"), mkUser(t, st, "b@x.com")
	tn := mkOrg(t, st, a)
	addMember(t, st, tn.ID, b, RoleMember)
	for _, key := range []item{memberKey(tn.ID, a.ID), userTenantKey(a.ID, tn.ID)} {
		if _, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(st.Table), Key: key,
			UpdateExpression:          aws.String("SET #r = :r"),
			ExpressionAttributeNames:  map[string]string{"#r": "role"},
			ExpressionAttributeValues: item{":r": s(RoleAdmin)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SetRole(ctx, tn.ID, owner(tn.ID, a.ID), b.ID, RoleAdmin); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

// Why: a personal workspace has no members to manage; the store must refuse before any write.
func TestPersonalRefused(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	u := mkUser(t, st, "a@x.com")
	tid := id.New()
	if err := st.VerifyEmail(ctx, u.ID, Tenant{ID: tid}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetRole(ctx, tid, owner(tid, u.ID), u.ID, RoleAdmin); !errors.Is(err, ErrPersonal) {
		t.Fatalf("SetRole: %v, want ErrPersonal", err)
	}
	if err := st.RenameOrg(ctx, tid, "New"); !errors.Is(err, ErrPersonal) {
		t.Fatalf("RenameOrg: %v, want ErrPersonal", err)
	}
}

// Why: a rename must reach the index item the Console lists from, and must not touch an organisation being deleted.
func TestRenameOrgAndListMembers(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	a, b := mkUser(t, st, "a@x.com"), mkUser(t, st, "b@x.com")
	for _, x := range []struct {
		u    *User
		name string
	}{{&a, "zed"}, {&b, "Amy"}} {
		x.u.DisplayName = x.name
		if err := st.update(ctx, x.u.ID, "SET displayName = :d", exists, nil, item{":d": s(x.name)}); err != nil {
			t.Fatal(err)
		}
	}
	tn := mkOrg(t, st, a)
	addMember(t, st, tn.ID, b, RoleMember)
	ghost := User{ID: id.New()}
	addMember(t, st, tn.ID, ghost, RoleAdmin)
	if err := st.RenameOrg(ctx, tn.ID, "Beta"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetTenant(ctx, tn.ID); got.Name != "Beta" || getS(rawItem(t, st, tenantIndexKey(tn.ID)), "name") != "Beta" {
		t.Fatalf("name = %q, index %q", got.Name, getS(rawItem(t, st, tenantIndexKey(tn.ID)), "name"))
	}
	ms, err := st.ListMembers(ctx, tn.ID)
	if err != nil || len(ms) != 2 || ms[0].UserID != a.ID || ms[1].UserID != b.ID {
		t.Fatalf("members = %+v, err %v (owner first, the user-less admin skipped)", ms, err)
	}
	if _, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: tenantKey(tn.ID),
		UpdateExpression:          aws.String("SET deletingAt = :d"),
		ExpressionAttributeValues: item{":d": s(ts(time.Now()))},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.RenameOrg(ctx, tn.ID, "Gamma"); !errors.Is(err, ErrTenantDeleting) {
		t.Fatalf("rename while deleting: %v", err)
	}
}
