// Package lockout counts failed password attempts on an account item and locks
// the account after too many. The Console and the Portal share it; each gives
// the account's key.
package lockout

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

var (
	// ErrNotFound means the account item does not exist.
	ErrNotFound = errors.New("lockout: account not found")
	// ErrConflict means concurrent writes kept changing the count.
	ErrConflict = errors.New("lockout: conflict")
)

// Counter keeps failedCount and lockedUntil on an existing account item.
type Counter struct {
	DB    *dynamodb.Client
	Table string
	Max   int           // the Max-th failure locks
	For   time.Duration // how long a lock lasts
}

const exists = "attribute_exists(pk)"

func num(v int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(v, 10)}
}

func (c Counter) update(ctx context.Context, key dynamo.Item, expr, cond string, vals dynamo.Item) error {
	_, err := c.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(c.Table), Key: key, UpdateExpression: aws.String(expr),
		ConditionExpression: aws.String(cond), ExpressionAttributeValues: vals,
	})
	return err
}

// Record counts one failed password. The Max-th failure sets lockedUntil to
// now+For and zeroes the count. Both steps are conditional, so concurrent
// failures cannot skip the lock or lock twice.
func (c Counter) Record(ctx context.Context, key dynamo.Item, now time.Time) error {
	last := num(int64(c.Max - 1))
	for range 4 {
		err := c.update(ctx, key, "ADD failedCount :one", exists+" AND (attribute_not_exists(failedCount) OR failedCount < :last)",
			dynamo.Item{":one": num(1), ":last": last})
		if err == nil {
			return nil
		}
		if !dynamo.IsConditionFailed(err) {
			return fmt.Errorf("record login failure: %w", err)
		}
		err = c.update(ctx, key, "SET lockedUntil = :l, failedCount = :zero", exists+" AND failedCount >= :last",
			dynamo.Item{":l": &types.AttributeValueMemberS{Value: dynamo.TS(now.Add(c.For))}, ":zero": num(0), ":last": last})
		if err == nil {
			return nil
		}
		if !dynamo.IsConditionFailed(err) {
			return fmt.Errorf("record login failure: %w", err)
		}
		// Either the account is gone or another request changed the count between
		// the two steps; tell them apart, then go round again.
		out, gerr := c.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(c.Table), Key: key, ConsistentRead: aws.Bool(true)})
		if gerr != nil {
			return fmt.Errorf("record login failure: %w", gerr)
		}
		if out.Item == nil {
			return ErrNotFound
		}
	}
	return fmt.Errorf("record login failure: %w", ErrConflict)
}

// Reset zeroes the count and clears any lock.
func (c Counter) Reset(ctx context.Context, key dynamo.Item) error {
	err := c.update(ctx, key, "SET failedCount = :zero REMOVE lockedUntil", exists, dynamo.Item{":zero": num(0)})
	if dynamo.IsConditionFailed(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("reset login failures: %w", err)
	}
	return nil
}
