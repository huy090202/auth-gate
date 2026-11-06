// Package ratelimit is a token bucket stored in the single DynamoDB table.
// Lambda instances share nothing, so the bucket lives in an item and every
// spend is a conditional write.
package ratelimit

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
)

const attempts = 3

// epsilon forgives the 1 microsecond that "updatedAt = max(now, read+1)" takes
// from the refill when two writes share a clock tick, and the rounding of the
// stored 6 decimals. Without it a bucket that should hold exactly 1 token can
// read 0.999998 and deny.
const epsilon = 1e-5

// Settings is one bucket's shape: it holds Burst tokens and refills PerSecond.
type Settings struct {
	Burst     int
	PerSecond float64
}

// Decision is the answer to one Allow call.
type Decision struct {
	Allowed       bool
	RetryAfterSec int  // whole seconds, at least 1, set when denied
	Conflict      bool // denied after lost writes, not because the bucket is empty
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
// updatedAt is integer microseconds and strictly increases on every write, so
// the condition "updatedAt = the value read" is a real version check even when
// two writers share a clock tick or the clock goes backwards.
func (l *Limiter) Allow(ctx context.Context, key string, s Settings) Decision {
	k := dynamo.Key("RL#"+key, "META")
	for attempt := 0; attempt < attempts; attempt++ {
		nowUS := l.now().UnixMicro()
		out, err := l.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(l.Table), Key: k, ConsistentRead: aws.Bool(true)})
		if err != nil {
			slog.Error("ratelimit read", "key", key, "err", err)
			return Decision{Allowed: true}
		}

		var tokens float64
		var next int64
		var cond string
		var vals map[string]types.AttributeValue
		if out.Item == nil {
			tokens, next = float64(s.Burst), nowUS
			cond = "attribute_not_exists(pk)"
		} else {
			tk, read, err := parse(out.Item)
			if err != nil {
				slog.Error("ratelimit parse", "key", key, "err", err)
				return Decision{Allowed: true}
			}
			tokens = math.Min(float64(s.Burst), tk+float64(max(0, nowUS-read))/1e6*s.PerSecond)
			next = max(nowUS, read+1)
			cond = "updatedAt = :read"
			vals = map[string]types.AttributeValue{":read": num(read)}
		}

		if tokens < 1-epsilon {
			return Decision{RetryAfterSec: max(1, int(math.Ceil((1-tokens)/s.PerSecond)))}
		}

		item := map[string]types.AttributeValue{
			"pk":        k["pk"],
			"sk":        k["sk"],
			"tokens":    &types.AttributeValueMemberN{Value: strconv.FormatFloat(math.Max(0, tokens-1), 'f', 6, 64)},
			"updatedAt": num(next),
		}
		if l.TTL > 0 {
			item["ttl"] = num(l.now().Add(l.TTL).Unix())
		}
		in := &dynamodb.PutItemInput{TableName: aws.String(l.Table), Item: item, ConditionExpression: aws.String(cond), ExpressionAttributeValues: vals}
		_, err = l.DB.PutItem(ctx, in)
		if err == nil {
			return Decision{Allowed: true}
		}
		var lost *types.ConditionalCheckFailedException
		if !errors.As(err, &lost) {
			slog.Error("ratelimit write", "key", key, "err", err)
			return Decision{Allowed: true}
		}
		if attempt < attempts-1 {
			jitter := 5 + rand.IntN(21) //nolint:gosec // jitter, not a secret
			time.Sleep(time.Duration(jitter) * time.Millisecond)
		}
	}
	return Decision{RetryAfterSec: 1, Conflict: true}
}

func num(n int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(n, 10)}
}

func parse(it map[string]types.AttributeValue) (tokens float64, updatedAt int64, err error) {
	tv, ok := it["tokens"].(*types.AttributeValueMemberN)
	uv, ok2 := it["updatedAt"].(*types.AttributeValueMemberN)
	if !ok || !ok2 {
		return 0, 0, errors.New("bucket item missing tokens or updatedAt")
	}
	if tokens, err = strconv.ParseFloat(tv.Value, 64); err != nil {
		return 0, 0, err
	}
	if updatedAt, err = strconv.ParseInt(uv.Value, 10, 64); err != nil {
		return 0, 0, err
	}
	return tokens, updatedAt, nil
}
