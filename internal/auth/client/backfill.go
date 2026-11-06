package client

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
)

// Backfill writes the list-index item for every client item that has none, so
// clients created before the index existed show up in the list. An index item
// that already exists is never overwritten: it may hold a newer name or
// disabledAt than the client item the scan just read. It is safe to rerun.
// The "C#" prefix does not match the "CLIENTS" index partition.
func Backfill(ctx context.Context, db *dynamodb.Client, table string) (written, skipped int, err error) {
	in := &dynamodb.ScanInput{
		TableName:                 aws.String(table),
		FilterExpression:          aws.String("begins_with(pk, :c) AND sk = :m"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":c": str("C#"), ":m": str("META")},
		ConsistentRead:            aws.Bool(true),
	}
	for p := dynamodb.NewScanPaginator(db, in); p.HasMorePages(); {
		page, err := p.NextPage(ctx)
		if err != nil {
			return written, skipped, fmt.Errorf("scan clients: %w", err)
		}
		for _, it := range page.Items {
			id := getS(it, "pk")[len("C#"):]
			idx := dynamo.Key(indexPK, "C#"+id)
			for _, a := range []string{"name", "rpId", "createdAt", "disabledAt"} {
				if v, ok := it[a]; ok {
					idx[a] = v
				}
			}
			_, err := db.PutItem(ctx, &dynamodb.PutItemInput{
				TableName: aws.String(table), Item: idx, ConditionExpression: aws.String("attribute_not_exists(pk)"),
			})
			switch {
			case err == nil:
				written++
			case dynamo.IsConditionFailed(err):
				skipped++
			default:
				return written, skipped, fmt.Errorf("write index for %s: %w", id, err)
			}
		}
	}
	return written, skipped, nil
}
