package dynamo

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// TransactWrite runs items as one all-or-nothing transaction.
func TransactWrite(ctx context.Context, c *dynamodb.Client, items ...types.TransactWriteItem) error {
	_, err := c.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: items})
	if err != nil {
		return fmt.Errorf("transact write: %w", err)
	}
	return nil
}

// IsConditionFailed reports whether err is a failed condition: a single write
// rejected by its condition, or a transaction cancelled because one of its
// conditions failed.
func IsConditionFailed(err error) bool {
	var single *types.ConditionalCheckFailedException
	if errors.As(err, &single) {
		return true
	}
	var tx *types.TransactionCanceledException
	if errors.As(err, &tx) {
		for _, r := range tx.CancellationReasons {
			if r.Code != nil && *r.Code == "ConditionalCheckFailed" {
				return true
			}
		}
	}
	return false
}
