// Package lease is the first-factor lease: a time-limited claim on an account
// item that serialises the write of an account's first factor. The Console and
// the Portal share it; each gives the account's key. It is a lease with
// timeouts, not a fence (see docs/memo/console-sessions.md).
package lease

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
	// ErrHeld means another holder's lease is still running.
	ErrHeld = errors.New("lease: held")
	// ErrNotFound means the account item does not exist.
	ErrNotFound = errors.New("lease: account not found")
)

// Store keeps the lease as store-only attributes on the account item: no view
// or log reads them.
type Store struct {
	DB    *dynamodb.Client
	Table string
}

const exists = "attribute_exists(pk)"

func (s Store) update(ctx context.Context, key dynamo.Item, expr, cond string, vals dynamo.Item) error {
	_, err := s.DB.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.Table), Key: key, UpdateExpression: aws.String(expr),
		ConditionExpression: aws.String(cond), ExpressionAttributeValues: vals,
	})
	return err
}

func ms(t time.Time) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.FormatInt(t.UnixMilli(), 10)}
}

// Claim takes the lease for holder until until, if none is set or the one set
// has run out at now. A lost claim returns ErrHeld and when the holder's lease
// ends.
func (s Store) Claim(ctx context.Context, key dynamo.Item, holder string, now, until time.Time) (time.Time, error) {
	err := s.update(ctx, key, "SET firstFactorLeaseUntil = :until, firstFactorLeaseBy = :by",
		exists+" AND (attribute_not_exists(firstFactorLeaseUntil) OR firstFactorLeaseUntil <= :now)",
		dynamo.Item{":until": ms(until), ":by": &types.AttributeValueMemberS{Value: holder}, ":now": ms(now)})
	if err == nil {
		return time.Time{}, nil
	}
	if !dynamo.IsConditionFailed(err) {
		return time.Time{}, fmt.Errorf("claim first-factor lease: %w", err)
	}
	// Lost: the account is gone, or the lease is held. Read to tell which.
	out, gerr := s.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.Table), Key: key, ConsistentRead: aws.Bool(true)})
	if gerr != nil {
		return time.Time{}, fmt.Errorf("claim first-factor lease: %w", gerr)
	}
	if out.Item == nil {
		return time.Time{}, ErrNotFound
	}
	var until64 int64
	if v, ok := out.Item["firstFactorLeaseUntil"].(*types.AttributeValueMemberN); ok {
		until64, _ = strconv.ParseInt(v.Value, 10, 64)
	}
	return time.UnixMilli(until64), ErrHeld
}

// Release drops the lease if holder still holds it, so a holder whose lease ran
// out cannot remove a newer one. Nothing to release is not an error.
func (s Store) Release(ctx context.Context, key dynamo.Item, holder string) error {
	err := s.update(ctx, key, "REMOVE firstFactorLeaseUntil, firstFactorLeaseBy",
		exists+" AND firstFactorLeaseBy = :by", dynamo.Item{":by": &types.AttributeValueMemberS{Value: holder}})
	if err != nil && !dynamo.IsConditionFailed(err) {
		return fmt.Errorf("release first-factor lease: %w", err)
	}
	return nil
}
