package portal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	db, table := dynamotest.NewTable(t)
	return &Store{DB: db, Table: table}
}

func mkUser(t *testing.T, st *Store, email string) User {
	t.Helper()
	u := User{ID: id.New(), Email: email, Lang: "en", PasswordHash: "h", CreatedAt: time.Now().UTC()}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatalf("create %s: %v", email, err)
	}
	return u
}

func rawItem(t *testing.T, st *Store, key item) item {
	t.Helper()
	out, err := st.DB.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: key, ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.Item
}

// Why: an email is one account however it is typed, and a taken address must not leave a stray user item behind.
func TestCreateUserRejectsDuplicateEmail(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	mkUser(t, st, "a@x.com")
	dup := User{ID: id.New(), Email: "  A@X.com ", PasswordHash: "h", CreatedAt: time.Now()}
	if err := st.CreateUser(ctx, dup); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("err = %v, want ErrEmailTaken", err)
	}
	if rawItem(t, st, userKey(dup.ID)) != nil {
		t.Fatal("the refused sign-up left a user item")
	}
	if _, err := st.GetUserByEmail(ctx, "A@x.com  "); err != nil {
		t.Fatalf("lookup by a differently typed address: %v", err)
	}
}

// Why: the personal workspace and its links must appear together with the verified flag, or never.
func TestVerifyEmailCreatesPersonalWorkspace(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	u := mkUser(t, st, "a@x.com")
	tid, now := id.New(), time.Now().UTC()
	if err := st.VerifyEmail(ctx, u.ID, Tenant{ID: tid, Kind: KindOrg, Name: "ignored"}, now); err != nil {
		t.Fatal(err)
	}
	for _, key := range []item{userKey(u.ID), emailKey("a@x.com")} {
		if _, has := rawItem(t, st, key)["ttl"]; has {
			t.Fatalf("%v still has a ttl", key)
		}
	}
	got, err := st.GetUser(ctx, u.ID)
	if err != nil || got.EmailVerifiedAt == nil || got.PersonalTenantID != tid {
		t.Fatalf("user = %+v, err %v", got, err)
	}
	tn, err := st.GetTenant(ctx, tid)
	if err != nil || tn.Kind != KindPersonal || tn.Name != "" || tn.CreatedBy != u.ID {
		t.Fatalf("tenant = %+v, err %v (the caller's kind and name must not win)", tn, err)
	}
	if rawItem(t, st, tenantIndexKey(tid)) == nil {
		t.Fatal("no TENANTS index item")
	}
	m, err := st.GetMembership(ctx, tid, u.ID)
	if err != nil || m.Role != RoleOwner {
		t.Fatalf("membership = %+v, err %v", m, err)
	}
	if getS(rawItem(t, st, userTenantKey(u.ID, tid)), "role") != RoleOwner {
		t.Fatal("the user's side of the pair is missing")
	}
	ms, err := st.ListMemberships(ctx, u.ID)
	if err != nil || len(ms) != 1 || ms[0].TenantID != tid {
		t.Fatalf("list = %+v, err %v", ms, err)
	}
	if err := st.VerifyEmail(ctx, u.ID, Tenant{ID: id.New()}, now); !errors.Is(err, ErrAlreadyVerified) {
		t.Fatalf("second verify = %v, want ErrAlreadyVerified", err)
	}
}

// Why: an address nobody confirms must free itself, so a typo does not lock a person out of their own email for good.
func TestUnverifiedUserCarriesSevenDayTTL(t *testing.T) {
	st := newStore(t)
	u := mkUser(t, st, "a@x.com")
	want := u.CreatedAt.Add(7 * 24 * time.Hour).Unix()
	for _, key := range []item{userKey(u.ID), emailKey("a@x.com")} {
		if got := getN64(rawItem(t, st, key), "ttl"); got != want {
			t.Fatalf("%v ttl = %d, want %d", key, got, want)
		}
	}
}

// Why: DynamoDB reaps expired items late; until then the address must already be free and the account gone.
func TestExpiredUnverifiedAccountFreesItsEmail(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	u := mkUser(t, st, "a@x.com")
	st.Now = func() time.Time { return u.CreatedAt.Add(8 * 24 * time.Hour) }
	if _, err := st.GetUserByEmail(ctx, "a@x.com"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("lookup = %v, want ErrUserNotFound", err)
	}
	if err := st.VerifyEmail(ctx, u.ID, Tenant{ID: id.New()}, st.Now()); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("verify = %v, want ErrUserNotFound", err)
	}
	again := User{ID: id.New(), Email: "a@x.com", PasswordHash: "h", CreatedAt: st.Now()}
	if err := st.CreateUser(ctx, again); err != nil {
		t.Fatalf("sign-up on a lapsed address: %v", err)
	}
	if got, err := st.GetUserByEmail(ctx, "a@x.com"); err != nil || got.ID != again.ID {
		t.Fatalf("lookup = %+v, %v", got, err)
	}
}

// Why: a link must work once, only until it lapses, and only on its own route.
func TestUseTokenIsSingleUseAndExpires(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	put := func(hash, purpose string, exp time.Time) {
		t.Helper()
		if err := st.PutToken(ctx, Token{Hash: hash, Purpose: purpose, UserID: "u1", Email: "new@x.com", ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	put("h1", PurposeVerify, now.Add(time.Hour))
	tok, err := st.UseToken(ctx, PurposeVerify, "h1", now)
	if err != nil || tok.UserID != "u1" || tok.Email != "new@x.com" || tok.Purpose != PurposeVerify {
		t.Fatalf("first use = %+v, %v", tok, err)
	}
	if _, err := st.UseToken(ctx, PurposeVerify, "h1", now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("second use = %v, want ErrTokenInvalid", err)
	}
	put("h2", PurposeReset, now.Add(time.Hour))
	if _, err := st.UseToken(ctx, PurposeReset, "h2", now.Add(time.Hour)); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("use at expiry = %v, want ErrTokenInvalid", err)
	}
	put("h3", PurposeReset, now.Add(time.Hour))
	if _, err := st.UseToken(ctx, PurposeVerify, "h3", now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("wrong purpose = %v, want ErrTokenInvalid", err)
	}
	if _, err := st.UseToken(ctx, PurposeReset, "h3", now); err != nil {
		t.Fatalf("a wrong-purpose try burnt the token: %v", err)
	}
	if _, err := st.UseToken(ctx, PurposeReset, "nope", now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("unknown = %v, want ErrTokenInvalid", err)
	}
}

// Why: the lock is what stops password guessing; it must trip on the fifth failure, not before.
func TestFiveFailuresLockFifteenMinutes(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	u := mkUser(t, st, "a@x.com")
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	for i := 1; i <= 4; i++ {
		if err := st.RecordLoginFailure(ctx, u.ID, now); err != nil {
			t.Fatal(err)
		}
		if got, _ := st.GetUser(ctx, u.ID); got.FailedCount != i || got.LockedUntil != nil {
			t.Fatalf("after %d: count %d lock %v", i, got.FailedCount, got.LockedUntil)
		}
	}
	if err := st.RecordLoginFailure(ctx, u.ID, now); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetUser(ctx, u.ID)
	if got.LockedUntil == nil || !got.LockedUntil.Equal(now.Add(15*time.Minute)) || got.FailedCount != 0 {
		t.Fatalf("after 5: count %d lock %v", got.FailedCount, got.LockedUntil)
	}
	if err := st.ResetLoginFailures(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetUser(ctx, u.ID); got.LockedUntil != nil {
		t.Fatal("reset left the lock")
	}
	if err := st.RecordLoginFailure(ctx, "no-such-user", now); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown user = %v, want ErrUserNotFound", err)
	}
}

// Why: two sessions must not both write the first factor; the loser learns when to retry, and a stale holder cannot free a newer lease.
func TestFirstFactorLease(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	u := mkUser(t, st, "a@x.com")
	now := time.Now()
	if _, err := st.ClaimFirstFactorLease(ctx, u.ID, "A", now, now.Add(25*time.Second)); err != nil {
		t.Fatal(err)
	}
	until, err := st.ClaimFirstFactorLease(ctx, u.ID, "B", now.Add(time.Second), now.Add(26*time.Second))
	if !errors.Is(err, ErrLeaseHeld) || until.UnixMilli() != now.Add(25*time.Second).UnixMilli() {
		t.Fatalf("second claim = %v until %v", err, until)
	}
	if err := st.ReleaseFirstFactorLease(ctx, u.ID, "B"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimFirstFactorLease(ctx, u.ID, "C", now.Add(2*time.Second), now.Add(27*time.Second)); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("B released A's lease: %v", err)
	}
	if err := st.ReleaseFirstFactorLease(ctx, u.ID, "A"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimFirstFactorLease(ctx, u.ID, "C", now.Add(2*time.Second), now.Add(27*time.Second)); err != nil {
		t.Fatalf("claim after release: %v", err)
	}
	if _, err := st.ClaimFirstFactorLease(ctx, "no-such-user", "A", now, now.Add(time.Second)); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("unknown user = %v", err)
	}
}

// Why: of two password changes read at one epoch only one may win, or a reset in another tab is silently overwritten.
func TestBumpEpochIsConditional(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	u := mkUser(t, st, "a@x.com")
	if err := st.BumpEpoch(ctx, u.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.BumpEpoch(ctx, u.ID, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale bump = %v, want ErrConflict", err)
	}
	if err := st.EndSessions(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetUser(ctx, u.ID); got.SessionEpoch != 2 {
		t.Fatalf("epoch = %d, want 2", got.SessionEpoch)
	}
}

// Why: a session swap must not resurrect a session that was signed out meanwhile.
func TestReplaceSessionRequiresTheOldOne(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := Session{Hash: "old", UserID: "u", Stage: StageHalf, ActiveTenantID: "t1"}
	if err := st.PutSession(ctx, old, now); err != nil {
		t.Fatal(err)
	}
	next := Session{Hash: "new", UserID: "u", Stage: StageFull, FactorProved: true, ActiveTenantID: "t1"}
	if err := st.ReplaceSession(ctx, "old", next, now); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSession(ctx, "new")
	if err != nil || !got.FactorProved || got.ActiveTenantID != "t1" || got.Stage != StageFull {
		t.Fatalf("new = %+v, %v", got, err)
	}
	if _, err := st.GetSession(ctx, "old"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("old still there: %v", err)
	}
	if err := st.ReplaceSession(ctx, "old", Session{Hash: "third", UserID: "u", Stage: StageFull}, now); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("replace of a gone session = %v", err)
	}
	if _, err := st.GetSession(ctx, "third"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatal("a failed replace stored the new session")
	}
}

// Why: a replayed token id must be refused; the audit log must stay in its own tenant and expire in 180 days.
func TestJTIAndAudit(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	exp := time.Now().Add(time.Minute)
	if err := st.UseJTI(ctx, "j1", exp); err != nil {
		t.Fatal(err)
	}
	if err := st.UseJTI(ctx, "j1", exp); !errors.Is(err, ErrReplayed) {
		t.Fatalf("replay = %v", err)
	}
	at := time.Now().UTC()
	e := AuditEntry{TenantID: "t1", ActorID: "u1", Action: "create-client", Target: "c1", Result: "success", IP: "1.2.3.4", At: at,
		Detail: map[string]string{"k": "v"}}
	if err := st.PutAudit(ctx, e); err != nil {
		t.Fatal(err)
	}
	other := e
	other.TenantID = "t2"
	if err := st.PutAudit(ctx, other); err != nil {
		t.Fatal(err)
	}
	got, next, err := st.ListAudit(ctx, "t1", "")
	if err != nil || next != "" || len(got) != 1 {
		t.Fatalf("list = %+v next %q err %v", got, next, err)
	}
	if got[0].ActorID != "u1" || got[0].Action != "create-client" || got[0].Detail["k"] != "v" || !got[0].At.Equal(at) {
		t.Fatalf("entry = %+v", got[0])
	}
	// The ttl is on the stored item: 180 days after the entry.
	out, _ := st.DB.Query(ctx, &dynamodb.QueryInput{
		TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
		KeyConditionExpression:    aws.String("pk = :p"),
		ExpressionAttributeValues: item{":p": s("TENANT#t1")},
	})
	if len(out.Items) != 1 || getN64(out.Items[0], "ttl") != at.Add(180*24*time.Hour).Unix() {
		t.Fatalf("audit ttl = %v", out.Items)
	}
}
