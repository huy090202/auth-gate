// Package ratelimit is a token bucket stored in the single DynamoDB table.
// Lambda instances share nothing, so the bucket lives in an item and every
// spend is an atomic conditional write.
package ratelimit

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
)

// microPerToken is the unit Allow calculates in, so a refill and the amounts in
// a condition are exact. Micro-tokens become a decimal string only in num6.
const microPerToken = 1_000_000

// Settings is one bucket's shape: it holds Burst tokens and refills PerSecond.
type Settings struct {
	Burst     int
	PerSecond float64
}

// Decision is the answer to one Allow call.
type Decision struct {
	Allowed       bool
	RetryAfterSec int // whole seconds, at least 1, set when denied
}

// Limiter spends tokens from buckets in Table.
type Limiter struct {
	DB    *dynamodb.Client
	Table string
	TTL   time.Duration    // 0 writes no ttl attribute
	Now   func() time.Time // nil means time.Now
}

func (l *Limiter) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

// Allow spends one token from the bucket RL#<key>. It never returns an error:
// a DynamoDB failure is logged and the request is allowed, because the caller
// needs the same table anyway and failing here adds no protection.
//
// updatedAt (integer microseconds) marks the start of the refill window nobody
// has claimed yet. The first writer to see an unclaimed window moves updatedAt
// and takes the refill with its token, even when the refill is nothing. Everyone
// else who read that window lost the claim and spends with a plain decrement,
// so callers on one item do not have to take turns.
func (l *Limiter) Allow(ctx context.Context, key string, s Settings) Decision {
	now := l.now()
	k := dynamo.Key("RL#"+key, "META")

	b, err := l.read(ctx, k)
	if err != nil {
		slog.Error("ratelimit read", "key", key, "err", err)
		return Decision{Allowed: true}
	}
	if b == nil {
		err := l.create(ctx, k, s, now)
		if err == nil {
			return Decision{Allowed: true}
		}
		if !lostCondition(err) {
			slog.Error("ratelimit write", "key", key, "err", err)
			return Decision{Allowed: true}
		}
		// Another caller created it first. Its value decides, so read it once more.
		if b, err = l.read(ctx, k); err != nil {
			slog.Error("ratelimit read", "key", key, "err", err)
			return Decision{Allowed: true}
		}
		if b == nil {
			slog.Error("ratelimit read", "key", key, "err", "bucket item gone right after it was created")
			return Decision{Allowed: true}
		}
	}

	burst := int64(s.Burst) * microPerToken
	elapsed := now.UnixMicro() - b.updatedAt
	have := refilled(b.tokens, burst, elapsed, s.PerSecond)
	if have < microPerToken {
		return denied(have, s.PerSecond) // no write: the window stays unclaimed
	}

	if elapsed > 0 || b.tokens > burst {
		// refill is negative when a PATCH lowered burst below the stored tokens.
		refill := have - b.tokens
		err := l.write(ctx, k, now,
			"tokens = tokens + :delta, updatedAt = :next",
			"updatedAt = :read AND tokens >= :need",
			map[string]types.AttributeValue{
				":delta": num6(refill - microPerToken),
				":need":  num6(microPerToken - refill),
				":next":  num(max(now.UnixMicro(), b.updatedAt+1)),
				":read":  num(b.updatedAt),
			})
		if err == nil {
			return Decision{Allowed: true}
		}
		if !lostCondition(err) {
			slog.Error("ratelimit write", "key", key, "err", err)
			return Decision{Allowed: true}
		}
	}

	err = l.write(ctx, k, now, "tokens = tokens - :one", "tokens >= :one",
		map[string]types.AttributeValue{":one": num6(microPerToken)})
	if err == nil {
		return Decision{Allowed: true}
	}
	if !lostCondition(err) {
		slog.Error("ratelimit write", "key", key, "err", err)
		return Decision{Allowed: true}
	}
	return denied(have, s.PerSecond)
}

// refilled is min(burst, tokens + floor(elapsedUS * perSecond)), in micro-tokens:
// tokens per second over microseconds is already micro-tokens. Time that does not
// run forward earns nothing. The comparison happens in float64 because a long idle
// time at a high rate does not fit an int64.
func refilled(tokens, burst, elapsedUS int64, perSecond float64) int64 {
	if elapsedUS <= 0 {
		return min(burst, tokens)
	}
	earned := math.Floor(float64(elapsedUS) * perSecond)
	if earned >= float64(burst-tokens) {
		return burst
	}
	return tokens + int64(earned)
}

func denied(have int64, perSecond float64) Decision {
	missing := float64(max(0, microPerToken-have)) / microPerToken
	return Decision{RetryAfterSec: max(1, int(math.Ceil(missing/perSecond)))}
}

// bucket is the stored item, tokens in micro-tokens.
type bucket struct {
	tokens    int64
	updatedAt int64
}

// read returns nil when the item does not exist. A malformed item is an error.
func (l *Limiter) read(ctx context.Context, k map[string]types.AttributeValue) (*bucket, error) {
	out, err := l.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(l.Table), Key: k, ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, err
	}
	if out.Item == nil {
		return nil, nil
	}
	tokens, updatedAt, err := parse(out.Item)
	if err != nil {
		return nil, err
	}
	return &bucket{tokens: tokens, updatedAt: updatedAt}, nil
}

func (l *Limiter) create(ctx context.Context, k map[string]types.AttributeValue, s Settings, now time.Time) error {
	item := map[string]types.AttributeValue{
		"pk":        k["pk"],
		"sk":        k["sk"],
		"tokens":    num6((int64(s.Burst) - 1) * microPerToken),
		"updatedAt": num(now.UnixMicro()),
	}
	if l.TTL > 0 {
		item["ttl"] = num(now.Add(l.TTL).Unix())
	}
	_, err := l.DB.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(l.Table), Item: item, ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	return err
}

// write runs one conditional UpdateItem and adds the ttl when the limiter has one.
func (l *Limiter) write(ctx context.Context, k map[string]types.AttributeValue, now time.Time, set, cond string, vals map[string]types.AttributeValue) error {
	in := &dynamodb.UpdateItemInput{
		TableName: aws.String(l.Table), Key: k, ConditionExpression: aws.String(cond), ExpressionAttributeValues: vals,
	}
	if l.TTL > 0 {
		set += ", #ttl = :ttl" // ttl is a DynamoDB reserved word
		in.ExpressionAttributeNames = map[string]string{"#ttl": "ttl"}
		vals[":ttl"] = num(now.Add(l.TTL).Unix())
	}
	in.UpdateExpression = aws.String("SET " + set)
	_, err := l.DB.UpdateItem(ctx, in)
	return err
}

func lostCondition(err error) bool {
	var lost *types.ConditionalCheckFailedException
	return errors.As(err, &lost)
}

func num(n int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}

// num6 renders micro-tokens as a decimal number with 6 places.
func num6(micro int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatFloat(float64(micro)/microPerToken, 'f', 6, 64)}
}

func parse(it map[string]types.AttributeValue) (tokens, updatedAt int64, err error) {
	tv, ok := it["tokens"].(*types.AttributeValueMemberN)
	uv, ok2 := it["updatedAt"].(*types.AttributeValueMemberN)
	if !ok || !ok2 {
		return 0, 0, errors.New("bucket item missing tokens or updatedAt")
	}
	f, err := strconv.ParseFloat(tv.Value, 64)
	if err != nil {
		return 0, 0, err
	}
	if updatedAt, err = strconv.ParseInt(uv.Value, 10, 64); err != nil {
		return 0, 0, err
	}
	return int64(math.Round(f * microPerToken)), updatedAt, nil
}
