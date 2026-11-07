package console

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
)

// Stage is how far a session has come in the sign-in flow.
type Stage string

const (
	StageHalf   Stage = "half"
	StageEnroll Stage = "enroll"
	StageFull   Stage = "full"
)

// Role is what an admin may do.
type Role string

const (
	RoleMaster Role = "master"
	RoleAdmin  Role = "admin"
)

// Admin is an operator account.
type Admin struct {
	ID, Username       string
	Role               Role
	PasswordHash       string
	MustChangePassword bool
	FailedCount        int
	LockedUntil        *time.Time
	DisabledAt         *time.Time
	CreatedAt          time.Time
	// SessionEpoch is bumped to end every open session of this admin at once.
	SessionEpoch int
}

// Session is a server-side session. Hash is the hex sha256 of the cookie token;
// the token itself is never stored.
type Session struct {
	Hash, AdminID, Username string
	Stage                   Stage
	Epoch                   int
	ChallengeID             string
	RegistrationID          string
	FactorProved            bool
	CreatedAt, LastSeenAt   time.Time
}

var (
	ErrUsernameTaken   = errors.New("console: username taken")
	ErrAdminNotFound   = errors.New("console: admin not found")
	ErrSessionNotFound = errors.New("console: session not found")
	ErrReplayed        = errors.New("console: token already used")
	// ErrConflict means a guarded write was refused: the acting master is no
	// longer an active master, targeted itself, would remove the last active
	// master, or lost a race with another transaction.
	ErrConflict = errors.New("console: conflict")
	// ErrBusy is a transaction cancelled by TransactionConflict: another write was
	// in flight on the same items and no guard was judged. It wraps ErrConflict so
	// callers that only refuse stay correct; a caller that can answer truthfully
	// checks it first, because a retry may well succeed.
	ErrBusy = fmt.Errorf("%w: another write was in flight, retry", ErrConflict)
)

const (
	maxFailures = 5
	lockFor     = 15 * time.Minute
	auditPage   = 50
	adminsPK    = "ADMINS"
)

// Store is the Console's DynamoDB table.
type Store struct {
	DB    *dynamodb.Client
	Table string

	// afterSurvivor, when set, runs between choosing the survivor and the write. Tests use it to interleave.
	afterSurvivor func()
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

func adminKey(username string) item {
	return item{"pk": s(adminsPK), "sk": s("A#" + username)}
}

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

func decodeAdmin(it item) (Admin, error) {
	a := Admin{
		ID: getS(it, "adminId"), Username: getS(it, "username"), Role: Role(getS(it, "role")),
		PasswordHash: getS(it, "passwordHash"), FailedCount: getN(it, "failedCount"),
		SessionEpoch: getN(it, "sessionEpoch"),
	}
	if b, ok := it["mustChangePassword"].(*types.AttributeValueMemberBOOL); ok {
		a.MustChangePassword = b.Value
	}
	var err error
	if a.LockedUntil, err = getT(it, "lockedUntil"); err != nil {
		return Admin{}, err
	}
	if a.DisabledAt, err = getT(it, "disabledAt"); err != nil {
		return Admin{}, err
	}
	c, err := getT(it, "createdAt")
	if err != nil {
		return Admin{}, err
	}
	if c != nil {
		a.CreatedAt = *c
	}
	return a, nil
}

// CreateAdmin writes a new account; an existing username gives ErrUsernameTaken.
func (st *Store) CreateAdmin(ctx context.Context, a Admin) error {
	it := adminKey(a.Username)
	it["adminId"], it["username"], it["role"] = s(a.ID), s(a.Username), s(string(a.Role))
	it["passwordHash"] = s(a.PasswordHash)
	it["mustChangePassword"] = &types.AttributeValueMemberBOOL{Value: a.MustChangePassword}
	it["failedCount"], it["sessionEpoch"] = n(int64(a.FailedCount)), n(int64(a.SessionEpoch))
	it["createdAt"] = s(ts(a.CreatedAt))
	if a.LockedUntil != nil {
		it["lockedUntil"] = s(ts(*a.LockedUntil))
	}
	if a.DisabledAt != nil {
		it["disabledAt"] = s(ts(*a.DisabledAt))
	}
	_, err := st.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(st.Table), Item: it,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if dynamo.IsConditionFailed(err) {
		return ErrUsernameTaken
	}
	if err != nil {
		return fmt.Errorf("create admin: %w", err)
	}
	return nil
}

// GetAdmin reads an account with a consistent read.
func (st *Store) GetAdmin(ctx context.Context, username string) (Admin, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: adminKey(username), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return Admin{}, fmt.Errorf("get admin: %w", err)
	}
	if out.Item == nil {
		return Admin{}, ErrAdminNotFound
	}
	return decodeAdmin(out.Item)
}

// ListAdmins returns every account.
func (st *Store) ListAdmins(ctx context.Context) ([]Admin, error) {
	var all []Admin
	var start item
	for {
		out, err := st.DB.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
			KeyConditionExpression:    aws.String("pk = :p"),
			ExpressionAttributeValues: item{":p": s(adminsPK)},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("list admins: %w", err)
		}
		for _, it := range out.Items {
			a, err := decodeAdmin(it)
			if err != nil {
				return nil, err
			}
			all = append(all, a)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return all, nil
		}
		start = out.LastEvaluatedKey
	}
}

// update runs one UpdateItem on an existing admin; a missing one gives ErrAdminNotFound.
func (st *Store) update(ctx context.Context, username, expr, cond string, names map[string]string, vals item) error {
	in := &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: adminKey(username),
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

const exists = "attribute_exists(pk)"

// RecordLoginFailure counts one failed password. The fifth failure sets
// lockedUntil to now+15m and zeroes the count. Both steps are conditional, so
// concurrent failures cannot skip the lock or lock twice.
func (st *Store) RecordLoginFailure(ctx context.Context, username string, now time.Time) error {
	for range 4 {
		err := st.update(ctx, username, "ADD failedCount :one", exists+" AND (attribute_not_exists(failedCount) OR failedCount < :last)",
			nil, item{":one": n(1), ":last": n(maxFailures - 1)})
		if err == nil {
			return nil
		}
		if !dynamo.IsConditionFailed(err) {
			return fmt.Errorf("record login failure: %w", err)
		}
		err = st.update(ctx, username, "SET lockedUntil = :l, failedCount = :zero", exists+" AND failedCount >= :last",
			nil, item{":l": s(ts(now.Add(lockFor))), ":zero": n(0), ":last": n(maxFailures - 1)})
		if err == nil {
			return nil
		}
		if !dynamo.IsConditionFailed(err) {
			return fmt.Errorf("record login failure: %w", err)
		}
		// Either the admin is gone or another request changed the count between
		// the two steps; tell them apart, then go round again.
		if _, gerr := st.GetAdmin(ctx, username); gerr != nil {
			return gerr
		}
	}
	return fmt.Errorf("record login failure: %w", ErrConflict)
}

// ResetLoginFailures zeroes the count and clears any lock.
func (st *Store) ResetLoginFailures(ctx context.Context, username string) error {
	return st.mapNotFound(st.update(ctx, username, "SET failedCount = :zero REMOVE lockedUntil", exists, nil, item{":zero": n(0)}), "reset login failures")
}

// SetPassword stores a new hash and clears the lock. With endSessions it also
// ends every open session of the admin.
func (st *Store) SetPassword(ctx context.Context, username, hash string, mustChange, endSessions bool) error {
	expr := "SET passwordHash = :h, mustChangePassword = :m, failedCount = :zero REMOVE lockedUntil"
	if endSessions {
		expr += " ADD sessionEpoch :one"
	}
	vals := item{":h": s(hash), ":m": &types.AttributeValueMemberBOOL{Value: mustChange}, ":zero": n(0)}
	if endSessions {
		vals[":one"] = n(1)
	}
	return st.mapNotFound(st.update(ctx, username, expr, exists, nil, vals), "set password")
}

// ChangePassword stores a new password for a self-service change: the
// temporary flag goes, the lock clears and sessionEpoch moves up by one, so every
// older session of the admin ends. It is conditional on the epoch the caller
// read, so two changes cannot both win; the loser gets ErrConflict.
func (st *Store) ChangePassword(ctx context.Context, username, hash string, epoch int) error {
	err := st.update(ctx, username,
		"SET passwordHash = :h, mustChangePassword = :m, failedCount = :zero REMOVE lockedUntil ADD sessionEpoch :one", epochCond(epoch), nil,
		item{":h": s(hash), ":m": &types.AttributeValueMemberBOOL{Value: false}, ":zero": n(0), ":one": n(1), ":e": n(int64(epoch))})
	if dynamo.IsConditionFailed(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	return nil
}

// epochCond holds while the admin exists and its sessionEpoch is still the one read.
func epochCond(epoch int) string {
	if epoch == 0 {
		return exists + " AND (attribute_not_exists(sessionEpoch) OR sessionEpoch = :e)"
	}
	return exists + " AND sessionEpoch = :e"
}

// BumpEpoch moves sessionEpoch up by one if it is still epoch, which ends every
// session of the admin that was opened at the old one. Of two callers holding the
// same epoch only one wins; the other gets ErrConflict.
func (st *Store) BumpEpoch(ctx context.Context, username string, epoch int) error {
	err := st.update(ctx, username, "ADD sessionEpoch :one", epochCond(epoch), nil, item{":one": n(1), ":e": n(int64(epoch))})
	if dynamo.IsConditionFailed(err) {
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("bump epoch: %w", err)
	}
	return nil
}

// ErrLeaseHeld means another session holds the first-factor lease.
var ErrLeaseHeld = errors.New("console: first-factor lease held")

// ClaimFirstFactorLease takes the admin's first-factor lease for holder until
// until, if no lease is set or the one set has run out at now. The lease lives
// on the Admin item as store-only attributes: no view or log reads them. A lost
// claim returns ErrLeaseHeld and when the holder's lease ends.
func (st *Store) ClaimFirstFactorLease(ctx context.Context, username, holder string, now, until time.Time) (time.Time, error) {
	err := st.update(ctx, username, "SET firstFactorLeaseUntil = :until, firstFactorLeaseBy = :by",
		exists+" AND (attribute_not_exists(firstFactorLeaseUntil) OR firstFactorLeaseUntil <= :now)", nil,
		item{":until": n(until.UnixMilli()), ":by": s(holder), ":now": n(now.UnixMilli())})
	if err == nil {
		return time.Time{}, nil
	}
	if !dynamo.IsConditionFailed(err) {
		return time.Time{}, fmt.Errorf("claim first-factor lease: %w", err)
	}
	// Lost: the admin is gone, or the lease is held. Read to tell which.
	out, gerr := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: adminKey(username), ConsistentRead: aws.Bool(true),
	})
	if gerr != nil {
		return time.Time{}, fmt.Errorf("claim first-factor lease: %w", gerr)
	}
	if out.Item == nil {
		return time.Time{}, ErrAdminNotFound
	}
	return time.UnixMilli(int64(getN(out.Item, "firstFactorLeaseUntil"))), ErrLeaseHeld
}

// ReleaseFirstFactorLease drops the lease if holder still holds it, so a holder
// whose lease ran out cannot remove a newer one. Nothing to release is not an error.
func (st *Store) ReleaseFirstFactorLease(ctx context.Context, username, holder string) error {
	err := st.update(ctx, username, "REMOVE firstFactorLeaseUntil, firstFactorLeaseBy",
		exists+" AND firstFactorLeaseBy = :by", nil, item{":by": s(holder)})
	if err != nil && !dynamo.IsConditionFailed(err) {
		return fmt.Errorf("release first-factor lease: %w", err)
	}
	return nil
}

// EndSessions ends every open session of the admin: each carries the old epoch.
func (st *Store) EndSessions(ctx context.Context, username string) error {
	return st.mapNotFound(st.update(ctx, username, "ADD sessionEpoch :one", exists, nil, item{":one": n(1)}), "end sessions")
}

func (st *Store) mapNotFound(err error, what string) error {
	if dynamo.IsConditionFailed(err) {
		return ErrAdminNotFound
	}
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// SetDisabled disables the account at the given time, or enables it when at is
// nil. Disabling also ends its sessions. See guarded for the actor rules.
func (st *Store) SetDisabled(ctx context.Context, actor *Admin, username string, at *time.Time) error {
	expr, vals := "REMOVE disabledAt", item(nil)
	if at != nil {
		expr, vals = "SET disabledAt = :d ADD sessionEpoch :one", item{":d": s(ts(*at)), ":one": n(1)}
	}
	return st.guarded(ctx, actor, username, expr, nil, vals, at != nil)
}

// SetRole changes the account's role. See guarded for the actor rules.
func (st *Store) SetRole(ctx context.Context, actor *Admin, username string, r Role) error {
	return st.guarded(ctx, actor, username, "SET #role = :r", map[string]string{"#role": "role"},
		item{":r": s(string(r))}, r != RoleMaster)
}

// guarded applies a change to one account. With an actor (the web path) it runs
// a transaction that also checks the actor is still an active master, so the
// last active master cannot be removed: the actor cannot target itself and so
// survives, and two masters acting on each other cannot both win, because the
// transactions touch each other's items. A transaction cancelled by a
// TransactionConflict (the other one was in flight) is refused with ErrConflict,
// never reported as success. With no actor (the operator CLI) it counts the
// active master that will survive, and then runs the same transaction with it as the actor, so a
// master removed between the check and the write makes the write fail. removesMaster says the change may take a master away.
func (st *Store) guarded(ctx context.Context, actor *Admin, username, expr string, names map[string]string, vals item, removesMaster bool) error {
	if actor == nil {
		if !removesMaster {
			return st.mapNotFound(st.update(ctx, username, expr, exists, names, vals), "update admin")
		}
		survivor, err := st.survivor(ctx, username)
		if err != nil {
			return err
		}
		if survivor == nil { // the target is no active master: nothing to protect
			return st.mapNotFound(st.update(ctx, username, expr, exists, names, vals), "update admin")
		}
		if st.afterSurvivor != nil {
			st.afterSurvivor()
		}
		actor = survivor
	}
	if actor.Username == username {
		return ErrConflict
	}
	upd := &types.Update{
		TableName: aws.String(st.Table), Key: adminKey(username),
		UpdateExpression:    aws.String(expr),
		ConditionExpression: aws.String(exists),
	}
	if len(names) > 0 {
		upd.ExpressionAttributeNames = names
	}
	if len(vals) > 0 {
		upd.ExpressionAttributeValues = vals
	}
	check := &types.ConditionCheck{
		TableName: aws.String(st.Table), Key: adminKey(actor.Username),
		ConditionExpression:       aws.String("attribute_exists(pk) AND #role = :master AND attribute_not_exists(disabledAt)"),
		ExpressionAttributeNames:  map[string]string{"#role": "role"},
		ExpressionAttributeValues: item{":master": s(string(RoleMaster))},
	}
	err := dynamo.TransactWrite(ctx, st.DB,
		types.TransactWriteItem{ConditionCheck: check},
		types.TransactWriteItem{Update: upd})
	if err == nil {
		return nil
	}
	var tx *types.TransactionCanceledException
	if errors.As(err, &tx) {
		codes := make([]string, len(tx.CancellationReasons))
		for i, r := range tx.CancellationReasons {
			codes[i] = aws.ToString(r.Code)
		}
		switch {
		case len(codes) == 2 && codes[0] == "ConditionalCheckFailed":
			return ErrConflict
		case len(codes) == 2 && codes[1] == "ConditionalCheckFailed":
			return ErrAdminNotFound
		case contains(codes, "TransactionConflict"):
			return ErrBusy
		}
	}
	return fmt.Errorf("update admin: %w", err)
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// survivor returns another active master than username, to act as the guard
// when username is itself an active master. It returns nil when username is not
// an active master, and ErrConflict when it is the last one.
func (st *Store) survivor(ctx context.Context, username string) (*Admin, error) {
	all, err := st.ListAdmins(ctx)
	if err != nil {
		return nil, err
	}
	var other *Admin
	targetMaster := false
	for i := range all {
		a := all[i]
		if a.Role != RoleMaster || a.DisabledAt != nil {
			continue
		}
		if a.Username == username {
			targetMaster = true
		} else if other == nil {
			other = &a
		}
	}
	if !targetMaster {
		return nil, nil
	}
	if other == nil {
		return nil, ErrConflict
	}
	return other, nil
}

// ---- sessions ----

func sesKey(hash string) item { return item{"pk": s("SES#" + hash), "sk": s("META")} }

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
	it["adminId"], it["username"], it["stage"] = s(x.AdminID), s(x.Username), s(string(x.Stage))
	it["epoch"] = n(int64(x.Epoch))
	it["createdAt"], it["lastSeenAt"] = s(ts(x.CreatedAt)), s(ts(x.LastSeenAt))
	it["ttl"] = n(x.expiresAt().Unix())
	if x.ChallengeID != "" {
		it["challengeId"] = s(x.ChallengeID)
	}
	if x.RegistrationID != "" {
		it["registrationId"] = s(x.RegistrationID)
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
		Hash: hash, AdminID: getS(it, "adminId"), Username: getS(it, "username"),
		Stage: Stage(getS(it, "stage")), Epoch: getN(it, "epoch"),
		ChallengeID: getS(it, "challengeId"), RegistrationID: getS(it, "registrationId"),
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

// SetRegistration records the session's open passkey registration.
func (st *Store) SetRegistration(ctx context.Context, hash, registrationID string) error {
	return st.setSessionRef(ctx, hash, "registrationId", s(registrationID))
}

// SetFactorProved records that the session itself enrolled the account's first factor.
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
