package reset

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
)

const (
	batchSize   = 25
	maxAttempts = 8
	baseBackoff = 50 * time.Millisecond
)

var errUnprocessed = errors.New("unprocessed deletes after retries")

// tableOrder is the order of the report lines.
var tableOrder = []Table{AuthGate, Console, Portal, AuthApp}

// Tables maps each Table to its DynamoDB table name.
type Tables map[Table]string

// KeptClient is a client that survives the reset, as read from its META item.
type KeptClient struct{ ID, Name, RPID string }

// Count is the number of items of one label in one table that were kept and
// that were deleted (or, in a dry run, would be deleted).
type Count struct {
	Table         Table
	Label         string
	Kept, Deleted int
}

// CheckKeep reads C#<id>/META in the AuthGate table for every id. It errors if
// an id is empty, the client is missing, or it has deletedAt or disabledAt.
func CheckKeep(ctx context.Context, db *dynamodb.Client, authgate string, ids []string) ([]KeptClient, error) {
	if len(ids) == 0 {
		return nil, errors.New("keep set is empty")
	}
	out := make([]KeptClient, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			return nil, errors.New("keep set has an empty client id")
		}
		res, err := db.GetItem(ctx, &dynamodb.GetItemInput{
			TableName:                aws.String(authgate),
			Key:                      dynamo.Key("C#"+id, "META"),
			ConsistentRead:           aws.Bool(true),
			ProjectionExpression:     aws.String("#n, rpId, deletedAt, disabledAt"),
			ExpressionAttributeNames: map[string]string{"#n": "name"},
		})
		if err != nil {
			return nil, fmt.Errorf("read client %s: %w", id, err)
		}
		if len(res.Item) == 0 {
			return nil, fmt.Errorf("client %s does not exist in %s", id, authgate)
		}
		if _, ok := res.Item["deletedAt"]; ok {
			return nil, fmt.Errorf("client %s is deleted", id)
		}
		if _, ok := res.Item["disabledAt"]; ok {
			return nil, fmt.Errorf("client %s is disabled", id)
		}
		out = append(out, KeptClient{ID: id, Name: strAttr(res.Item, "name"), RPID: strAttr(res.Item, "rpId")})
	}
	return out, nil
}

func strAttr(it map[string]types.AttributeValue, k string) string {
	if s, ok := it[k].(*types.AttributeValueMemberS); ok {
		return s.Value
	}
	return ""
}

// Run scans every table, decides each item with Decide and, when apply is set,
// deletes the doomed ones. Without apply nothing is written and Deleted means
// "would delete". It refuses an empty keep set or one without the Console client.
func Run(ctx context.Context, db *dynamodb.Client, tables Tables, k Keep, apply bool) ([]Count, error) {
	if len(k.Clients) == 0 || k.Console == "" || !k.Clients[k.Console] {
		return nil, errors.New("keep set must name the Console client")
	}
	type key struct {
		t     Table
		label string
	}
	tally := map[key]*Count{}
	for _, t := range tableOrder {
		name, ok := tables[t]
		if !ok {
			return nil, fmt.Errorf("no table name for %s", t)
		}
		var doomed []map[string]types.AttributeValue
		p := dynamodb.NewScanPaginator(db, &dynamodb.ScanInput{
			TableName:            aws.String(name),
			ProjectionExpression: aws.String("pk, sk, clientId"),
			ConsistentRead:       aws.Bool(true),
		})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				return nil, fmt.Errorf("scan %s: %w", name, err)
			}
			for _, it := range page.Items {
				label, del := Decide(t, strAttr(it, "pk"), strAttr(it, "sk"), strAttr(it, "clientId"), k)
				c := tally[key{t, label}]
				if c == nil {
					c = &Count{Table: t, Label: label}
					tally[key{t, label}] = c
				}
				if del {
					c.Deleted++
					doomed = append(doomed, map[string]types.AttributeValue{"pk": it["pk"], "sk": it["sk"]})
				} else {
					c.Kept++
				}
			}
		}
		if apply {
			if err := deleteKeys(ctx, db, name, doomed, baseBackoff); err != nil {
				return nil, err
			}
		}
	}
	counts := make([]Count, 0, len(tally))
	for _, c := range tally {
		counts = append(counts, *c)
	}
	rank := map[Table]int{}
	for i, t := range tableOrder {
		rank[t] = i
	}
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].Table != counts[j].Table {
			return rank[counts[i].Table] < rank[counts[j].Table]
		}
		return counts[i].Label < counts[j].Label
	})
	return counts, nil
}

// batchWriter is the part of the DynamoDB client deleteKeys needs.
type batchWriter interface {
	BatchWriteItem(ctx context.Context, in *dynamodb.BatchWriteItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error)
}

// deleteKeys deletes keys in batches of 25, retrying UnprocessedItems with
// exponential backoff from base, at most maxAttempts calls per batch.
func deleteKeys(ctx context.Context, bw batchWriter, table string, keys []map[string]types.AttributeValue, base time.Duration) error {
	for start := 0; start < len(keys); start += batchSize {
		end := min(start+batchSize, len(keys))
		reqs := make([]types.WriteRequest, 0, end-start)
		for _, k := range keys[start:end] {
			reqs = append(reqs, types.WriteRequest{DeleteRequest: &types.DeleteRequest{Key: k}})
		}
		pending := map[string][]types.WriteRequest{table: reqs}
		for attempt := 0; len(pending) > 0; attempt++ {
			if attempt >= maxAttempts {
				return fmt.Errorf("%s: %w", table, errUnprocessed)
			}
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(base << (attempt - 1)):
				}
			}
			out, err := bw.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: pending})
			if err != nil {
				return fmt.Errorf("%s: batch delete: %w", table, err)
			}
			pending = out.UnprocessedItems
		}
	}
	return nil
}
