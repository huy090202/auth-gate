package factor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
)

// PruneDeleted finds the factor items (pk "S#<clientId>#<subjectId>") of clients
// that are soft-deleted (their C#<id>/META item has deletedAt) and, when apply is
// set, deletes them. It returns, per such client, the number of factor items found
// (dry run) or deleted (apply). A client with no META item or without deletedAt is
// left alone. It needs Scan, which no Lambda role has: run it under an operator
// profile (scripts/setup-and-deploy/07-prune-factors.sh does).
func PruneDeleted(ctx context.Context, db *dynamodb.Client, table string, apply bool) (map[string]int, error) {
	keys := map[string][]types.WriteRequest{}
	scan := dynamodb.NewScanPaginator(db, &dynamodb.ScanInput{
		TableName:                 aws.String(table),
		ProjectionExpression:      aws.String("pk, sk"),
		FilterExpression:          aws.String("begins_with(pk, :s)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":s": str("S#")},
		ConsistentRead:            aws.Bool(true),
	})
	for scan.HasMorePages() {
		page, err := scan.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("prune factors: scan: %w", err)
		}
		for _, it := range page.Items {
			pk, ok := it["pk"].(*types.AttributeValueMemberS)
			if !ok {
				continue
			}
			clientID, _, found := strings.Cut(strings.TrimPrefix(pk.Value, "S#"), "#")
			if !found || clientID == "" {
				continue
			}
			keys[clientID] = append(keys[clientID], types.WriteRequest{DeleteRequest: &types.DeleteRequest{
				Key: map[string]types.AttributeValue{"pk": it["pk"], "sk": it["sk"]},
			}})
		}
	}

	counts := map[string]int{}
	var doomed []types.WriteRequest
	for clientID, reqs := range keys {
		out, err := db.GetItem(ctx, &dynamodb.GetItemInput{
			TableName:            aws.String(table),
			Key:                  dynamo.Key("C#"+clientID, "META"),
			ProjectionExpression: aws.String("deletedAt"),
			ConsistentRead:       aws.Bool(true),
		})
		if err != nil {
			return nil, fmt.Errorf("prune factors: read client %s: %w", clientID, err)
		}
		if _, deleted := out.Item["deletedAt"]; !deleted {
			continue
		}
		counts[clientID] = len(reqs)
		doomed = append(doomed, reqs...)
	}
	if !apply {
		return counts, nil
	}
	for start := 0; start < len(doomed); start += batchSize {
		end := min(start+batchSize, len(doomed))
		pending := map[string][]types.WriteRequest{table: doomed[start:end]}
		for attempt := 0; len(pending) > 0; attempt++ {
			if attempt >= 5 {
				return nil, errors.New("prune factors: unprocessed deletes after retries")
			}
			out, err := db.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: pending})
			if err != nil {
				return nil, fmt.Errorf("prune factors: batch delete: %w", err)
			}
			pending = out.UnprocessedItems
			if len(pending) > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt+1) * 50 * time.Millisecond):
				}
			}
		}
	}
	return counts, nil
}
