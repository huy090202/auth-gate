package portal

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/id"
)

// Stage is how far a session has come in the sign-in flow.
type Stage string

const (
	StageHalf   Stage = "half"
	StageEnroll Stage = "enroll"
	StageFull   Stage = "full"
)

// Workspace kinds and member roles.
const (
	KindPersonal = "personal"
	KindOrg      = "org"

	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Email token purposes.
const (
	PurposeVerify      = "verify"
	PurposeReset       = "reset"
	PurposeFactorReset = "factor-reset"
	PurposeEmailChange = "email-change"
)

// User is a Portal account, keyed by id. Email is normalised (NormalizeEmail).
type User struct {
	ID, Email, DisplayName, Lang, PasswordHash string
	EmailVerifiedAt                            *time.Time
	// SessionEpoch is bumped to end every open session of this user at once.
	SessionEpoch     int
	OrgsCreated      int
	SuspendedAt      *time.Time
	FailedCount      int
	LockedUntil      *time.Time
	AvatarKey        string
	PersonalTenantID string
	CreatedAt        time.Time

	// The first-factor lease, store-owned: CreateUser ignores them, the claim and
	// release methods set them, and no view or log reads them.
	FirstFactorLeaseUntil time.Time
	FirstFactorLeaseBy    string
}

// Tenant is a workspace. A personal workspace has an empty Name (the UI labels it).
type Tenant struct {
	ID, Kind, Name, CreatedBy string
	CreatedAt                 time.Time
	SuspendedAt, DeletingAt   *time.Time
}

// Membership links a user to a tenant.
type Membership struct {
	TenantID, UserID, Role string
	JoinedAt               time.Time
}

// Token is an email token. Only the hash of the secret is stored.
type Token struct {
	Hash, Purpose, UserID string
	Email                 string // the new address, for email-change
	ExpiresAt             time.Time
}

// Session is a server-side session. Hash is the hex sha256 of the cookie token;
// the token itself is never stored.
type Session struct {
	Hash, UserID string
	Stage        Stage
	Epoch        int
	ChallengeID  string
	// The step-up the open challenge was started for, so its verify can be audited.
	ChallengeAction, ChallengeFactor, ChallengeTarget string
	RegistrationID                                    string
	FactorProved                                      bool
	// ActiveTenantID is informational (what the UI opens on). Authorisation reads
	// the tenant in the URL and the membership, never this.
	ActiveTenantID        string
	CreatedAt, LastSeenAt time.Time
}

var (
	ErrEmailTaken      = errors.New("portal: email taken")
	ErrUserNotFound    = errors.New("portal: user not found")
	ErrAlreadyVerified = errors.New("portal: email already verified")
	ErrTokenInvalid    = errors.New("portal: token invalid")
	ErrNotMember       = errors.New("portal: not a member")
	ErrSessionNotFound = errors.New("portal: session not found")
	ErrReplayed        = errors.New("portal: token already used")
	// ErrConflict means a guarded write lost a race or its condition no longer holds.
	ErrConflict = errors.New("portal: conflict")
	// ErrLeaseHeld means another session holds the first-factor lease.
	ErrLeaseHeld = errors.New("portal: first-factor lease held")
)

const (
	maxFailures = 5
	lockFor     = 15 * time.Minute
	auditPage   = 50
	auditTTL    = 180 * 24 * time.Hour
	// unverifiedTTL is how long an account that never verified its email keeps its
	// user and email items, which frees the address.
	unverifiedTTL = 7 * 24 * time.Hour
)

// Store is the Portal's DynamoDB table.
type Store struct {
	DB    *dynamodb.Client
	Table string
	Now   func() time.Time // nil means time.Now
}

func (st *Store) now() time.Time {
	if st.Now != nil {
		return st.Now()
	}
	return time.Now()
}

// tsLayout is RFC 3339 with a fixed nine fractional digits, so timestamps sort
// as strings (RFC3339Nano drops trailing zeros and would not).
const tsLayout = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
func n(v int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

type item = map[string]types.AttributeValue

const exists = "attribute_exists(pk)"

// NormalizeEmail is the one spelling of an address used in keys and comparisons.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func userKey(userID string) item          { return item{"pk": s("USER#" + userID), "sk": s("META")} }
func emailKey(email string) item          { return item{"pk": s("EMAIL#" + email), "sk": s("META")} }
func tenantKey(tenantID string) item      { return item{"pk": s("TENANT#" + tenantID), "sk": s("META")} }
func tenantIndexKey(tenantID string) item { return item{"pk": s("TENANTS"), "sk": s("T#" + tenantID)} }
func memberKey(tenantID, userID string) item {
	return item{"pk": s("TENANT#" + tenantID), "sk": s("MEMBER#" + userID)}
}
func userTenantKey(userID, tenantID string) item {
	return item{"pk": s("USER#" + userID), "sk": s("TENANT#" + tenantID)}
}
func tokenKey(hash string) item { return item{"pk": s("ETOKEN#" + hash), "sk": s("META")} }
func sesKey(hash string) item   { return item{"pk": s("SESSION#" + hash), "sk": s("META")} }

func getS(it item, k string) string {
	if v, ok := it[k].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func getN(it item, k string) int {
	if v, ok := it[k].(*types.AttributeValueMemberN); ok {
		i, _ := strconv.Atoi(v.Value)
		return i
	}
	return 0
}

func getN64(it item, k string) int64 {
	if v, ok := it[k].(*types.AttributeValueMemberN); ok {
		i, _ := strconv.ParseInt(v.Value, 10, 64)
		return i
	}
	return 0
}

func getT(it item, k string) (*time.Time, error) {
	v := getS(it, k)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return nil, fmt.Errorf("attribute %s: %w", k, err)
	}
	return &t, nil
}

func putT(it item, k string, t *time.Time) {
	if t != nil {
		it[k] = s(ts(*t))
	}
}

// expired reports whether an item carries a ttl at or before now: an unverified
// account past its seven days, not yet reaped (DynamoDB's TTL deletion is lazy).
func expired(it item, now time.Time) bool {
	_, has := it["ttl"]
	return has && getN64(it, "ttl") <= now.Unix()
}

// ---- users ----

func decodeUser(it item) (User, error) {
	u := User{
		ID: getS(it, "userId"), Email: getS(it, "email"), DisplayName: getS(it, "displayName"), Lang: getS(it, "lang"),
		PasswordHash: getS(it, "passwordHash"), SessionEpoch: getN(it, "sessionEpoch"), OrgsCreated: getN(it, "orgsCreated"),
		FailedCount: getN(it, "failedCount"), AvatarKey: getS(it, "avatarKey"), PersonalTenantID: getS(it, "personalTenantId"),
		FirstFactorLeaseBy: getS(it, "firstFactorLeaseBy"),
	}
	if ms := getN64(it, "firstFactorLeaseUntil"); ms != 0 {
		u.FirstFactorLeaseUntil = time.UnixMilli(ms)
	}
	var err error
	if u.EmailVerifiedAt, err = getT(it, "emailVerifiedAt"); err != nil {
		return User{}, err
	}
	if u.SuspendedAt, err = getT(it, "suspendedAt"); err != nil {
		return User{}, err
	}
	if u.LockedUntil, err = getT(it, "lockedUntil"); err != nil {
		return User{}, err
	}
	c, err := getT(it, "createdAt")
	if err != nil {
		return User{}, err
	}
	if c != nil {
		u.CreatedAt = *c
	}
	return u, nil
}

// CreateUser writes the user and its EMAIL item in one transaction. An address
// already held gives ErrEmailTaken, unless the holder is an unverified account
// past its ttl that DynamoDB has not reaped yet. An unverified user (no
// EmailVerifiedAt) carries a seven-day ttl on both items; VerifyEmail removes it.
// u.CreatedAt is the clock for that ttl.
func (st *Store) CreateUser(ctx context.Context, u User) error {
	u.Email = NormalizeEmail(u.Email)
	ui := userKey(u.ID)
	ui["userId"], ui["email"], ui["lang"], ui["passwordHash"] = s(u.ID), s(u.Email), s(u.Lang), s(u.PasswordHash)
	ui["sessionEpoch"], ui["orgsCreated"], ui["failedCount"] = n(int64(u.SessionEpoch)), n(int64(u.OrgsCreated)), n(int64(u.FailedCount))
	ui["createdAt"] = s(ts(u.CreatedAt))
	if u.DisplayName != "" {
		ui["displayName"] = s(u.DisplayName)
	}
	if u.AvatarKey != "" {
		ui["avatarKey"] = s(u.AvatarKey)
	}
	if u.PersonalTenantID != "" {
		ui["personalTenantId"] = s(u.PersonalTenantID)
	}
	putT(ui, "emailVerifiedAt", u.EmailVerifiedAt)
	putT(ui, "suspendedAt", u.SuspendedAt)
	putT(ui, "lockedUntil", u.LockedUntil)
	ei := emailKey(u.Email)
	ei["userId"] = s(u.ID)
	if u.EmailVerifiedAt == nil {
		ttl := n(u.CreatedAt.Add(unverifiedTTL).Unix())
		ui["ttl"], ei["ttl"] = ttl, ttl
	}
	err := dynamo.TransactWrite(ctx, st.DB,
		types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(st.Table), Item: ui, ConditionExpression: aws.String("attribute_not_exists(pk)"),
		}},
		types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(st.Table), Item: ei,
			ConditionExpression:       aws.String("attribute_not_exists(pk) OR #ttl <= :now"),
			ExpressionAttributeNames:  map[string]string{"#ttl": "ttl"},
			ExpressionAttributeValues: item{":now": n(u.CreatedAt.Unix())},
		}})
	var tx *types.TransactionCanceledException
	if errors.As(err, &tx) && len(tx.CancellationReasons) == 2 {
		if aws.ToString(tx.CancellationReasons[1].Code) == "ConditionalCheckFailed" {
			return ErrEmailTaken
		}
	}
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

// GetUser reads an account with a consistent read. An unverified account past
// its ttl counts as gone.
func (st *Store) GetUser(ctx context.Context, userID string) (User, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: userKey(userID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return User{}, fmt.Errorf("get user: %w", err)
	}
	if out.Item == nil || expired(out.Item, st.now()) {
		return User{}, ErrUserNotFound
	}
	return decodeUser(out.Item)
}

// GetUserByEmail finds an account by address, after normalising it.
func (st *Store) GetUserByEmail(ctx context.Context, email string) (User, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: emailKey(NormalizeEmail(email)), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return User{}, fmt.Errorf("get user by email: %w", err)
	}
	if out.Item == nil || expired(out.Item, st.now()) {
		return User{}, ErrUserNotFound
	}
	return st.GetUser(ctx, getS(out.Item, "userId"))
}

// VerifyEmail marks the address verified and creates the user's personal
// workspace in one transaction: the ttl goes from the user and email items,
// emailVerifiedAt is set, and the tenant META, its TENANTS index entry and the
// owner membership pair are written. The tenant's kind, creator and creation
// time are set here whatever t holds, so only t.ID is the caller's. A user who
// is gone or past the ttl gives ErrUserNotFound; one already verified gives
// ErrAlreadyVerified.
func (st *Store) VerifyEmail(ctx context.Context, userID string, t Tenant, now time.Time) error {
	u, err := st.GetUser(ctx, userID)
	if err != nil {
		return err
	}
	if u.EmailVerifiedAt != nil {
		return ErrAlreadyVerified
	}
	t.Kind, t.Name, t.CreatedBy, t.CreatedAt = KindPersonal, "", userID, now
	ti := tenantKey(t.ID)
	ti["tenantId"], ti["kind"], ti["createdBy"], ti["createdAt"] = s(t.ID), s(t.Kind), s(userID), s(ts(now))
	idx := tenantIndexKey(t.ID)
	idx["kind"], idx["createdAt"] = s(t.Kind), s(ts(now))
	mi := memberKey(t.ID, userID)
	mi["role"], mi["joinedAt"] = s(RoleOwner), s(ts(now))
	umi := userTenantKey(userID, t.ID)
	umi["role"], umi["joinedAt"] = s(RoleOwner), s(ts(now))
	names := map[string]string{"#ttl": "ttl"}
	err = dynamo.TransactWrite(ctx, st.DB,
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: userKey(userID),
			UpdateExpression:          aws.String("SET emailVerifiedAt = :now, personalTenantId = :t REMOVE #ttl"),
			ConditionExpression:       aws.String(exists + " AND attribute_not_exists(emailVerifiedAt) AND (attribute_not_exists(#ttl) OR #ttl > :nowSec)"),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: item{":now": s(ts(now)), ":t": s(t.ID), ":nowSec": n(now.Unix())},
		}},
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: emailKey(u.Email),
			UpdateExpression:          aws.String("REMOVE #ttl"),
			ConditionExpression:       aws.String(exists + " AND userId = :u"),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: item{":u": s(userID)},
		}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: ti, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: idx}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: mi}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: umi}})
	if dynamo.IsConditionFailed(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("verify email: %w", err)
	}
	return nil
}

// update runs one UpdateItem on an existing user; a missing one fails the condition.
func (st *Store) update(ctx context.Context, userID, expr, cond string, names map[string]string, vals item) error {
	in := &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: userKey(userID),
		UpdateExpression:    aws.String(expr),
		ConditionExpression: aws.String(cond),
	}
	if len(names) > 0 {
		in.ExpressionAttributeNames = names
	}
	if len(vals) > 0 {
		in.ExpressionAttributeValues = vals
	}
	_, err := st.DB.UpdateItem(ctx, in)
	return err
}

func (st *Store) mapNotFound(err error, what string) error {
	if dynamo.IsConditionFailed(err) {
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// ---- lockout, epoch, lease ----

// RecordLoginFailure counts one failed password. The fifth failure sets
// lockedUntil to now+15m and zeroes the count. Both steps are conditional, so
// concurrent failures cannot skip the lock or lock twice.
func (st *Store) RecordLoginFailure(ctx context.Context, userID string, now time.Time) error {
	for range 4 {
		err := st.update(ctx, userID, "ADD failedCount :one", exists+" AND (attribute_not_exists(failedCount) OR failedCount < :last)",
			nil, item{":one": n(1), ":last": n(maxFailures - 1)})
		if err == nil {
			return nil
		}
		if !dynamo.IsConditionFailed(err) {
			return fmt.Errorf("record login failure: %w", err)
		}
		err = st.update(ctx, userID, "SET lockedUntil = :l, failedCount = :zero", exists+" AND failedCount >= :last",
			nil, item{":l": s(ts(now.Add(lockFor))), ":zero": n(0), ":last": n(maxFailures - 1)})
		if err == nil {
			return nil
		}
		if !dynamo.IsConditionFailed(err) {
			return fmt.Errorf("record login failure: %w", err)
		}
		// Either the user is gone or another request changed the count between
		// the two steps; tell them apart, then go round again.
		if _, gerr := st.GetUser(ctx, userID); gerr != nil {
			return gerr
		}
	}
	return fmt.Errorf("record login failure: %w", ErrConflict)
}

// ResetLoginFailures zeroes the count and clears any lock.
func (st *Store) ResetLoginFailures(ctx context.Context, userID string) error {
	return st.mapNotFound(st.update(ctx, userID, "SET failedCount = :zero REMOVE lockedUntil", exists, nil, item{":zero": n(0)}), "reset login failures")
}

// epochCond holds while the user exists and its sessionEpoch is still the one read.
func epochCond(epoch int) string {
	if epoch == 0 {
		return exists + " AND (attribute_not_exists(sessionEpoch) OR sessionEpoch = :e)"
	}
	return exists + " AND sessionEpoch = :e"
}

// BumpEpoch moves sessionEpoch up by one if it is still epoch, which ends every
// session of the user that was opened at the old one. Of two callers holding the
// same epoch only one wins; the other gets ErrConflict.
func (st *Store) BumpEpoch(ctx context.Context, userID string, epoch int) error {
	err := st.update(ctx, userID, "ADD sessionEpoch :one", epochCond(epoch), nil, item{":one": n(1), ":e": n(int64(epoch))})
	if dynamo.IsConditionFailed(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("bump epoch: %w", err)
	}
	return nil
}

// EndSessions ends every open session of the user: each carries the old epoch.
func (st *Store) EndSessions(ctx context.Context, userID string) error {
	return st.mapNotFound(st.update(ctx, userID, "ADD sessionEpoch :one", exists, nil, item{":one": n(1)}), "end sessions")
}

// ClaimFirstFactorLease takes the user's first-factor lease for holder until
// until, if no lease is set or the one set has run out at now. A lost claim
// returns ErrLeaseHeld and when the holder's lease ends.
func (st *Store) ClaimFirstFactorLease(ctx context.Context, userID, holder string, now, until time.Time) (time.Time, error) {
	err := st.update(ctx, userID, "SET firstFactorLeaseUntil = :until, firstFactorLeaseBy = :by",
		exists+" AND (attribute_not_exists(firstFactorLeaseUntil) OR firstFactorLeaseUntil <= :now)", nil,
		item{":until": n(until.UnixMilli()), ":by": s(holder), ":now": n(now.UnixMilli())})
	if err == nil {
		return time.Time{}, nil
	}
	if !dynamo.IsConditionFailed(err) {
		return time.Time{}, fmt.Errorf("claim first-factor lease: %w", err)
	}
	// Lost: the user is gone, or the lease is held. Read to tell which.
	out, gerr := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: userKey(userID), ConsistentRead: aws.Bool(true),
	})
	if gerr != nil {
		return time.Time{}, fmt.Errorf("claim first-factor lease: %w", gerr)
	}
	if out.Item == nil {
		return time.Time{}, ErrUserNotFound
	}
	return time.UnixMilli(getN64(out.Item, "firstFactorLeaseUntil")), ErrLeaseHeld
}

// ReleaseFirstFactorLease drops the lease if holder still holds it, so a holder
// whose lease ran out cannot remove a newer one. Nothing to release is not an error.
func (st *Store) ReleaseFirstFactorLease(ctx context.Context, userID, holder string) error {
	err := st.update(ctx, userID, "REMOVE firstFactorLeaseUntil, firstFactorLeaseBy",
		exists+" AND firstFactorLeaseBy = :by", nil, item{":by": s(holder)})
	if err != nil && !dynamo.IsConditionFailed(err) {
		return fmt.Errorf("release first-factor lease: %w", err)
	}
	return nil
}

// ---- email tokens ----

// PutToken stores an email token. Its ttl is its expiry; UseToken checks the
// expiry itself, because DynamoDB's TTL deletion is lazy.
func (st *Store) PutToken(ctx context.Context, t Token) error {
	it := tokenKey(t.Hash)
	it["purpose"], it["userId"], it["expiresAt"] = s(t.Purpose), s(t.UserID), s(ts(t.ExpiresAt))
	it["ttl"] = n(t.ExpiresAt.Unix())
	if t.Email != "" {
		it["email"] = s(t.Email)
	}
	_, err := st.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.Table), Item: it, ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		return fmt.Errorf("put token: %w", err)
	}
	return nil
}

// UseToken deletes an unexpired token of the given purpose and returns it. A
// token that is absent, used, expired or of another purpose gives ErrTokenInvalid
// whichever it was, so the answer says nothing; a token of another purpose is
// left in place, so a link opened on the wrong route is not burnt.
func (st *Store) UseToken(ctx context.Context, purpose, hash string, now time.Time) (Token, error) {
	out, err := st.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(st.Table), Key: tokenKey(hash), ReturnValues: types.ReturnValueAllOld,
		ConditionExpression:       aws.String(exists + " AND purpose = :p AND expiresAt > :now"),
		ExpressionAttributeValues: item{":p": s(purpose), ":now": s(ts(now))},
	})
	if dynamo.IsConditionFailed(err) {
		return Token{}, ErrTokenInvalid
	}
	if err != nil {
		return Token{}, fmt.Errorf("use token: %w", err)
	}
	exp, err := getT(out.Attributes, "expiresAt")
	if err != nil || exp == nil {
		return Token{}, fmt.Errorf("token expiresAt: %v", err)
	}
	return Token{Hash: hash, Purpose: getS(out.Attributes, "purpose"), UserID: getS(out.Attributes, "userId"),
		Email: getS(out.Attributes, "email"), ExpiresAt: *exp}, nil
}

// ---- tenants and memberships ----

// GetMembership reads the membership with a consistent read; none gives ErrNotMember.
func (st *Store) GetMembership(ctx context.Context, tenantID, userID string) (Membership, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: memberKey(tenantID, userID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return Membership{}, fmt.Errorf("get membership: %w", err)
	}
	if out.Item == nil {
		return Membership{}, ErrNotMember
	}
	return decodeMembership(tenantID, userID, out.Item)
}

func decodeMembership(tenantID, userID string, it item) (Membership, error) {
	j, err := getT(it, "joinedAt")
	if err != nil || j == nil {
		return Membership{}, fmt.Errorf("membership joinedAt: %v", err)
	}
	return Membership{TenantID: tenantID, UserID: userID, Role: getS(it, "role"), JoinedAt: *j}, nil
}

// ListMemberships returns the tenants a user belongs to, from the user's side of
// the membership pair.
func (st *Store) ListMemberships(ctx context.Context, userID string) ([]Membership, error) {
	var all []Membership
	var start item
	for {
		out, err := st.DB.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
			KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :t)"),
			ExpressionAttributeValues: item{":p": s("USER#" + userID), ":t": s("TENANT#")},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("list memberships: %w", err)
		}
		for _, it := range out.Items {
			m, err := decodeMembership(strings.TrimPrefix(getS(it, "sk"), "TENANT#"), userID, it)
			if err != nil {
				return nil, err
			}
			all = append(all, m)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return all, nil
		}
		start = out.LastEvaluatedKey
	}
}

// GetTenant reads a workspace; an absent one gives ErrTenantNotFound.
func (st *Store) GetTenant(ctx context.Context, tenantID string) (Tenant, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: tenantKey(tenantID), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return Tenant{}, fmt.Errorf("get tenant: %w", err)
	}
	if out.Item == nil {
		return Tenant{}, ErrTenantNotFound
	}
	it := out.Item
	t := Tenant{ID: tenantID, Kind: getS(it, "kind"), Name: getS(it, "name"), CreatedBy: getS(it, "createdBy")}
	var terr error
	var c *time.Time
	if c, terr = getT(it, "createdAt"); terr != nil || c == nil {
		return Tenant{}, fmt.Errorf("tenant createdAt: %v", terr)
	}
	t.CreatedAt = *c
	if t.SuspendedAt, terr = getT(it, "suspendedAt"); terr != nil {
		return Tenant{}, terr
	}
	if t.DeletingAt, terr = getT(it, "deletingAt"); terr != nil {
		return Tenant{}, terr
	}
	return t, nil
}

// ErrTenantNotFound means the workspace does not exist.
var ErrTenantNotFound = errors.New("portal: tenant not found")

// ---- sessions ----

const (
	halfTTL    = 10 * time.Minute
	enrollTTL  = 30 * time.Minute
	fullIdle   = 30 * time.Minute
	fullAbsMax = 8 * time.Hour
	touchAfter = 60 * time.Second
)

// expiresAt is when the session ends. The middleware checks it in code, because
// DynamoDB's TTL deletion is lazy.
func (x Session) expiresAt() time.Time {
	switch x.Stage {
	case StageHalf:
		return x.CreatedAt.Add(halfTTL)
	case StageEnroll:
		return x.CreatedAt.Add(enrollTTL)
	default:
		idle, abs := x.LastSeenAt.Add(fullIdle), x.CreatedAt.Add(fullAbsMax)
		if abs.Before(idle) {
			return abs
		}
		return idle
	}
}

// sessionItem is the stored form of a session. Zero times default to now.
func sessionItem(x Session, now time.Time) item {
	if x.CreatedAt.IsZero() {
		x.CreatedAt = now
	}
	if x.LastSeenAt.IsZero() {
		x.LastSeenAt = now
	}
	it := sesKey(x.Hash)
	it["userId"], it["stage"], it["epoch"] = s(x.UserID), s(string(x.Stage)), n(int64(x.Epoch))
	it["createdAt"], it["lastSeenAt"] = s(ts(x.CreatedAt)), s(ts(x.LastSeenAt))
	it["ttl"] = n(x.expiresAt().Unix())
	if x.ChallengeID != "" {
		it["challengeId"] = s(x.ChallengeID)
	}
	if x.RegistrationID != "" {
		it["registrationId"] = s(x.RegistrationID)
	}
	if x.ActiveTenantID != "" {
		it["activeTenantId"] = s(x.ActiveTenantID)
	}
	if x.FactorProved {
		it["factorProved"] = &types.AttributeValueMemberBOOL{Value: true}
	}
	return it
}

// PutSession stores a session with its ttl. Zero times default to now. It
// overwrites; use ReplaceSession to swap one session for another.
func (st *Store) PutSession(ctx context.Context, x Session, now time.Time) error {
	if _, err := st.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(st.Table), Item: sessionItem(x, now)}); err != nil {
		return fmt.Errorf("put session: %w", err)
	}
	return nil
}

// ReplaceSession stores x and deletes the session oldHash in one transaction,
// conditional on the old one still existing. A session signed out in the
// meantime gives ErrSessionNotFound and nothing is stored.
func (st *Store) ReplaceSession(ctx context.Context, oldHash string, x Session, now time.Time) error {
	err := dynamo.TransactWrite(ctx, st.DB,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(st.Table), Key: sesKey(oldHash), ConditionExpression: aws.String(exists),
		}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: sessionItem(x, now)}})
	if dynamo.IsConditionFailed(err) {
		return ErrSessionNotFound
	}
	if err != nil {
		return fmt.Errorf("replace session: %w", err)
	}
	return nil
}

// GetSession reads a session with a consistent read.
func (st *Store) GetSession(ctx context.Context, hash string) (Session, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: sesKey(hash), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	if out.Item == nil {
		return Session{}, ErrSessionNotFound
	}
	it := out.Item
	x := Session{
		Hash: hash, UserID: getS(it, "userId"), Stage: Stage(getS(it, "stage")), Epoch: getN(it, "epoch"),
		ChallengeID: getS(it, "challengeId"), RegistrationID: getS(it, "registrationId"), ActiveTenantID: getS(it, "activeTenantId"),
		ChallengeAction: getS(it, "challengeAction"), ChallengeFactor: getS(it, "challengeFactor"), ChallengeTarget: getS(it, "challengeTarget"),
	}
	if v, ok := it["factorProved"].(*types.AttributeValueMemberBOOL); ok {
		x.FactorProved = v.Value
	}
	c, err := getT(it, "createdAt")
	if err != nil || c == nil {
		return Session{}, fmt.Errorf("session createdAt: %v", err)
	}
	l, err := getT(it, "lastSeenAt")
	if err != nil || l == nil {
		return Session{}, fmt.Errorf("session lastSeenAt: %v", err)
	}
	x.CreatedAt, x.LastSeenAt = *c, *l
	return x, nil
}

// touchSession moves lastSeenAt and the ttl forward. It will not bring back a
// session that was deleted in the meantime.
func (st *Store) touchSession(ctx context.Context, x Session) error {
	_, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: sesKey(x.Hash),
		UpdateExpression:          aws.String("SET lastSeenAt = :l, #ttl = :t"),
		ConditionExpression:       aws.String(exists),
		ExpressionAttributeNames:  map[string]string{"#ttl": "ttl"},
		ExpressionAttributeValues: item{":l": s(ts(x.LastSeenAt)), ":t": n(x.expiresAt().Unix())},
	})
	if err != nil && !dynamo.IsConditionFailed(err) {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// DeleteSession removes a session; a missing one is not an error.
func (st *Store) DeleteSession(ctx context.Context, hash string) error {
	if _, err := st.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(st.Table), Key: sesKey(hash)}); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// UseJTI records a token id until the token's exp. A second use gives ErrReplayed.
func (st *Store) UseJTI(ctx context.Context, jti string, exp time.Time) error {
	_, err := st.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(st.Table),
		Item:                item{"pk": s("JTI#" + jti), "sk": s("META"), "ttl": n(exp.Unix())},
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if dynamo.IsConditionFailed(err) {
		return ErrReplayed
	}
	if err != nil {
		return fmt.Errorf("use jti: %w", err)
	}
	return nil
}

// SetChallenge records the session's open login or step-up challenge.
func (st *Store) SetChallenge(ctx context.Context, hash, challengeID string) error {
	return st.setSessionRef(ctx, hash, "challengeId", s(challengeID))
}

// SetStepUpChallenge records a step-up challenge with what it was started for
// (action, factor and target, none of them secret), for the audit of its verify.
func (st *Store) SetStepUpChallenge(ctx context.Context, hash, challengeID, action, factor, target string) error {
	_, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: sesKey(hash),
		UpdateExpression:          aws.String("SET challengeId = :c, challengeAction = :a, challengeFactor = :f, challengeTarget = :t"),
		ConditionExpression:       aws.String(exists),
		ExpressionAttributeValues: item{":c": s(challengeID), ":a": s(action), ":f": s(factor), ":t": s(target)},
	})
	if dynamo.IsConditionFailed(err) {
		return ErrSessionNotFound
	}
	if err != nil {
		return fmt.Errorf("set step-up challenge: %w", err)
	}
	return nil
}

// SetRegistration records the session's open passkey registration.
func (st *Store) SetRegistration(ctx context.Context, hash, registrationID string) error {
	return st.setSessionRef(ctx, hash, "registrationId", s(registrationID))
}

// SetFactorProved records that the session itself enrolled the user's first factor.
func (st *Store) SetFactorProved(ctx context.Context, hash string) error {
	return st.setSessionRef(ctx, hash, "factorProved", &types.AttributeValueMemberBOOL{Value: true})
}

// setSessionRef sets one attribute of an existing session. Like touchSession it
// will not bring back a session that was signed out in the meantime: that
// gives ErrSessionNotFound.
func (st *Store) setSessionRef(ctx context.Context, hash, attr string, value types.AttributeValue) error {
	_, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: sesKey(hash),
		UpdateExpression:          aws.String("SET #a = :v"),
		ConditionExpression:       aws.String(exists),
		ExpressionAttributeNames:  map[string]string{"#a": attr},
		ExpressionAttributeValues: item{":v": value},
	})
	if dynamo.IsConditionFailed(err) {
		return ErrSessionNotFound
	}
	if err != nil {
		return fmt.Errorf("set session %s: %w", attr, err)
	}
	return nil
}

// ---- audit ----

// AuditEntry is one line of a workspace's audit log. It never holds a key, PIN,
// password or token, nor a hash of one. Detail is a small allow-listed set of
// facts about the action, built field by field where the action runs.
type AuditEntry struct {
	TenantID, ActorID, Action, Target, Result, IP string
	Detail                                        map[string]string
	At                                            time.Time
}

// PutAudit appends one entry to its tenant's partition, with a 180-day ttl.
func (st *Store) PutAudit(ctx context.Context, e AuditEntry) error {
	at := e.At.UTC()
	it := item{
		"pk": s("TENANT#" + e.TenantID), "sk": s("AUDIT#" + ts(at) + "#" + id.New()),
		"actorId": s(e.ActorID), "action": s(e.Action), "target": s(e.Target), "result": s(e.Result), "ip": s(e.IP),
		"ttl": n(at.Add(auditTTL).Unix()),
	}
	if len(e.Detail) > 0 {
		m := make(map[string]types.AttributeValue, len(e.Detail))
		for k, v := range e.Detail {
			m[k] = s(truncate(v, maxAuditTarget))
		}
		it["detail"] = &types.AttributeValueMemberM{Value: m}
	}
	if _, err := st.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(st.Table), Item: it}); err != nil {
		return fmt.Errorf("put audit: %w", err)
	}
	return nil
}

// ListAudit returns up to 50 entries of a tenant, newest first. The returned
// cursor is empty on the last page; pass it back to read the next.
func (st *Store) ListAudit(ctx context.Context, tenantID, cursor string) ([]AuditEntry, string, error) {
	pk := "TENANT#" + tenantID
	in := &dynamodb.QueryInput{
		TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
		KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :a)"),
		ExpressionAttributeValues: item{":p": s(pk), ":a": s("AUDIT#")},
		ScanIndexForward:          aws.Bool(false),
		Limit:                     aws.Int32(auditPage),
	}
	if cursor != "" {
		in.ExclusiveStartKey = item{"pk": s(pk), "sk": s(cursor)}
	}
	out, err := st.DB.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("list audit: %w", err)
	}
	entries := make([]AuditEntry, 0, len(out.Items))
	for _, it := range out.Items {
		var t time.Time
		if rest, ok := strings.CutPrefix(getS(it, "sk"), "AUDIT#"); ok {
			if stamp, _, ok := strings.Cut(rest, "#"); ok {
				t, _ = time.Parse(time.RFC3339Nano, stamp)
			}
		}
		e := AuditEntry{
			TenantID: tenantID, ActorID: getS(it, "actorId"), Action: getS(it, "action"),
			Target: getS(it, "target"), Result: getS(it, "result"), IP: getS(it, "ip"), At: t,
		}
		if m, ok := it["detail"].(*types.AttributeValueMemberM); ok {
			e.Detail = make(map[string]string, len(m.Value))
			for k := range m.Value {
				e.Detail[k] = getS(m.Value, k)
			}
		}
		entries = append(entries, e)
	}
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		next = getS(out.LastEvaluatedKey, "sk")
	}
	return entries, next, nil
}
