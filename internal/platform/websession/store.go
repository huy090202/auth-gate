// Package websession is the server-side session model shared by the Console and
// the Portal: a session item keyed by the hash of the cookie token, the
// account's sessionEpoch checked on every request, and the stage rules. Each
// app gives the table, the key prefix, the cookie name and how to load its
// account; see docs/memo/console-sessions.md for why.
package websession

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

var (
	ErrNotFound = errors.New("websession: session not found")
	ErrReplayed = errors.New("websession: token already used")
)

// Session is a server-side session. Hash is the hex sha256 of the cookie token;
// the token itself is never stored. Extra holds the app's own string attributes
// (who the session belongs to), stored under the names the app chooses.
type Session struct {
	Hash        string
	Stage       Stage
	Epoch       int
	ChallengeID string
	// The step-up the open challenge was started for, so its verify can be audited.
	ChallengeAction, ChallengeFactor, ChallengeTarget string
	RegistrationID                                    string
	FactorProved                                      bool
	CreatedAt, LastSeenAt                             time.Time
	Extra                                             map[string]string
}

const (
	HalfTTL    = 10 * time.Minute
	EnrollTTL  = 30 * time.Minute
	FullIdle   = 30 * time.Minute
	FullAbsMax = 8 * time.Hour
	TouchAfter = 60 * time.Second
)

// ExpiresAt is when the session ends. The middleware checks it in code, because
// DynamoDB's TTL deletion is lazy.
func (x Session) ExpiresAt() time.Time {
	switch x.Stage {
	case StageHalf:
		return x.CreatedAt.Add(HalfTTL)
	case StageEnroll:
		return x.CreatedAt.Add(EnrollTTL)
	default:
		idle, abs := x.LastSeenAt.Add(FullIdle), x.CreatedAt.Add(FullAbsMax)
		if abs.Before(idle) {
			return abs
		}
		return idle
	}
}

// Store reads and writes session items (and used token ids) in one table.
// Prefix is the partition key prefix ("SES#", "SESSION#").
type Store struct {
	DB            *dynamodb.Client
	Table, Prefix string
}

type item = dynamo.Item

const exists = "attribute_exists(pk)"

func s(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
func n(v int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

func (st Store) key(hash string) item { return item{"pk": s(st.Prefix + hash), "sk": s("META")} }

func (st Store) item(x Session, now time.Time) item {
	if x.CreatedAt.IsZero() {
		x.CreatedAt = now
	}
	if x.LastSeenAt.IsZero() {
		x.LastSeenAt = now
	}
	it := st.key(x.Hash)
	for k, v := range x.Extra {
		if v != "" {
			it[k] = s(v)
		}
	}
	it["stage"], it["epoch"] = s(string(x.Stage)), n(int64(x.Epoch))
	it["createdAt"], it["lastSeenAt"] = s(dynamo.TS(x.CreatedAt)), s(dynamo.TS(x.LastSeenAt))
	it["ttl"] = n(x.ExpiresAt().Unix())
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

// Put stores a session with its ttl. Zero times default to now. It overwrites;
// use Replace to swap one session for another.
func (st Store) Put(ctx context.Context, x Session, now time.Time) error {
	if _, err := st.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(st.Table), Item: st.item(x, now)}); err != nil {
		return fmt.Errorf("put session: %w", err)
	}
	return nil
}

// Replace stores x and deletes the session oldHash in one transaction,
// conditional on the old one still existing. A session signed out in the
// meantime gives ErrNotFound and nothing is stored.
func (st Store) Replace(ctx context.Context, oldHash string, x Session, now time.Time) error {
	err := dynamo.TransactWrite(ctx, st.DB,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(st.Table), Key: st.key(oldHash), ConditionExpression: aws.String(exists),
		}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: st.item(x, now)}})
	if dynamo.IsConditionFailed(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("replace session: %w", err)
	}
	return nil
}

func getS(it item, k string) string {
	if v, ok := it[k].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func getT(it item, k string) (time.Time, error) {
	v := getS(it, k)
	if v == "" {
		return time.Time{}, fmt.Errorf("session %s missing", k)
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("session %s: %w", k, err)
	}
	return t, nil
}

// Get reads a session with a consistent read. extra names the app's own
// attributes to read into Session.Extra.
func (st Store) Get(ctx context.Context, hash string, extra ...string) (Session, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(st.Table), Key: st.key(hash), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return Session{}, fmt.Errorf("get session: %w", err)
	}
	if out.Item == nil {
		return Session{}, ErrNotFound
	}
	it := out.Item
	x := Session{
		Hash: hash, Stage: Stage(getS(it, "stage")), ChallengeID: getS(it, "challengeId"), RegistrationID: getS(it, "registrationId"),
		ChallengeAction: getS(it, "challengeAction"), ChallengeFactor: getS(it, "challengeFactor"), ChallengeTarget: getS(it, "challengeTarget"),
		Extra: map[string]string{},
	}
	if v, ok := it["epoch"].(*types.AttributeValueMemberN); ok {
		x.Epoch, _ = strconv.Atoi(v.Value)
	}
	for _, k := range extra {
		x.Extra[k] = getS(it, k)
	}
	if v, ok := it["factorProved"].(*types.AttributeValueMemberBOOL); ok {
		x.FactorProved = v.Value
	}
	if x.CreatedAt, err = getT(it, "createdAt"); err != nil {
		return Session{}, err
	}
	if x.LastSeenAt, err = getT(it, "lastSeenAt"); err != nil {
		return Session{}, err
	}
	return x, nil
}

// Touch moves lastSeenAt and the ttl forward. It will not bring back a session
// that was deleted in the meantime.
func (st Store) Touch(ctx context.Context, x Session) error {
	_, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: st.key(x.Hash),
		UpdateExpression:          aws.String("SET lastSeenAt = :l, #ttl = :t"),
		ConditionExpression:       aws.String(exists),
		ExpressionAttributeNames:  map[string]string{"#ttl": "ttl"},
		ExpressionAttributeValues: item{":l": s(dynamo.TS(x.LastSeenAt)), ":t": n(x.ExpiresAt().Unix())},
	})
	if err != nil && !dynamo.IsConditionFailed(err) {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// Delete removes a session; a missing one is not an error.
func (st Store) Delete(ctx context.Context, hash string) error {
	if _, err := st.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(st.Table), Key: st.key(hash)}); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// UseJTI records a token id until the token's exp. A second use gives ErrReplayed.
func (st Store) UseJTI(ctx context.Context, jti string, exp time.Time) error {
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
func (st Store) SetChallenge(ctx context.Context, hash, challengeID string) error {
	return st.setRef(ctx, hash, "challengeId", s(challengeID))
}

// SetStepUpChallenge records a step-up challenge with what it was started for
// (action, factor and target, none of them secret), for the audit of its verify.
func (st Store) SetStepUpChallenge(ctx context.Context, hash, challengeID, action, factor, target string) error {
	_, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: st.key(hash),
		UpdateExpression:          aws.String("SET challengeId = :c, challengeAction = :a, challengeFactor = :f, challengeTarget = :t"),
		ConditionExpression:       aws.String(exists),
		ExpressionAttributeValues: item{":c": s(challengeID), ":a": s(action), ":f": s(factor), ":t": s(target)},
	})
	if dynamo.IsConditionFailed(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("set step-up challenge: %w", err)
	}
	return nil
}

// SetRegistration records the session's open passkey registration.
func (st Store) SetRegistration(ctx context.Context, hash, registrationID string) error {
	return st.setRef(ctx, hash, "registrationId", s(registrationID))
}

// SetFactorProved records that the session itself enrolled the account's first factor.
func (st Store) SetFactorProved(ctx context.Context, hash string) error {
	return st.setRef(ctx, hash, "factorProved", &types.AttributeValueMemberBOOL{Value: true})
}

// setRef sets one attribute of an existing session. Like Touch it will not
// bring back a session that was signed out in the meantime: that gives ErrNotFound.
func (st Store) setRef(ctx context.Context, hash, attr string, value types.AttributeValue) error {
	_, err := st.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: st.key(hash),
		UpdateExpression:          aws.String("SET #a = :v"),
		ConditionExpression:       aws.String(exists),
		ExpressionAttributeNames:  map[string]string{"#a": attr},
		ExpressionAttributeValues: item{":v": value},
	})
	if dynamo.IsConditionFailed(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("set session %s: %w", attr, err)
	}
	return nil
}
