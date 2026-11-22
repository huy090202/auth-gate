package factor_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/auth/factor"
	"github.com/huy090202/auth-gate/internal/auth/pin"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
)

func TestPINLockoutAfterMaxAttempts(t *testing.T) {
	c, table := dynamotest.NewTable(t)
	s := factor.NewDynamoStore(c, table)
	ctx := context.Background()
	rec := pin.Record{Hash: []byte("h"), Salt: []byte("s"), Params: pin.Params{Memory: 64, Time: 1, Threads: 1}}
	if err := s.PutPIN(ctx, "c1", "u1", factor.PIN{Record: rec, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPIN(ctx, "c1", "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Record.Params != rec.Params {
		t.Fatalf("params = %+v, want %+v", got.Record.Params, rec.Params)
	}

	now := time.Now().UTC().Truncate(time.Second)
	for i := 1; i <= 4; i++ {
		n, locked, err := s.RecordPINFailure(ctx, "c1", "u1", 5, 15*time.Minute, now)
		if err != nil || n != i || locked != nil {
			t.Fatalf("failure %d: n=%d locked=%v err=%v", i, n, locked, err)
		}
	}
	n, locked, err := s.RecordPINFailure(ctx, "c1", "u1", 5, 15*time.Minute, now)
	if err != nil || n != 5 || locked == nil || !locked.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("5th failure: n=%d locked=%v err=%v", n, locked, err)
	}
	got, err = s.GetPIN(ctx, "c1", "u1")
	if err != nil || got.FailedCount != 0 || got.LockedUntil == nil {
		t.Fatalf("after lock: %+v err=%v", got, err)
	}
	if err := s.ResetPINFailures(ctx, "c1", "u1"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetPIN(ctx, "c1", "u1")
	if err != nil || got.LockedUntil != nil || got.FailedCount != 0 {
		t.Fatalf("after reset: %+v err=%v", got, err)
	}
}

func TestPurgeSubjectRemovesPINAndCredentials(t *testing.T) {
	c, table := dynamotest.NewTable(t)
	s := factor.NewDynamoStore(c, table)
	ctx := context.Background()
	rec := pin.Record{Hash: []byte("h"), Salt: []byte("s"), Params: pin.Params{Memory: 64, Time: 1, Threads: 1}}
	if err := s.PutPIN(ctx, "c1", "u1", factor.PIN{Record: rec, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"cred-a", "cred-b"} {
		cred := factor.Credential{ID: id, PublicKey: []byte("pk"), Transports: []string{"usb"}, CreatedAt: time.Now()}
		if err := s.PutCredential(ctx, "c1", "u1", cred); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PurgeSubject(ctx, "c1", "u1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPIN(ctx, "c1", "u1"); !errors.Is(err, factor.ErrNotFound) {
		t.Fatalf("GetPIN err = %v, want ErrNotFound", err)
	}
	creds, err := s.ListCredentials(ctx, "c1", "u1")
	if err != nil || creds == nil || len(creds) != 0 {
		t.Fatalf("creds = %#v err=%v", creds, err)
	}
}

// PruneDeleted is an operator tool that deletes real data: a dry run must delete
// nothing, and -apply must touch only the factors of soft-deleted clients.
func TestPruneDeletedTouchesOnlyDeletedClients(t *testing.T) {
	c, table := dynamotest.NewTable(t)
	s := factor.NewDynamoStore(c, table)
	ctx := context.Background()
	rec := pin.Record{Hash: []byte("h"), Salt: []byte("s"), Params: pin.Params{Memory: 64, Time: 1, Threads: 1}}
	// 30 subjects for the deleted client: more than one 25-item batch.
	for i := range 30 {
		if err := s.PutPIN(ctx, "gone", fmt.Sprintf("u%d", i), factor.PIN{Record: rec, UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	cred := factor.Credential{ID: "cred-a", PublicKey: []byte("pk"), Transports: []string{"usb"}, CreatedAt: time.Now()}
	if err := s.PutCredential(ctx, "gone", "u0", cred); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPIN(ctx, "live", "u1", factor.PIN{Record: rec, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutPIN(ctx, "unknown", "u1", factor.PIN{Record: rec, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for id, attrs := range map[string]map[string]types.AttributeValue{
		"gone": {"deletedAt": &types.AttributeValueMemberS{Value: "2026-11-22T00:00:00Z"}},
		"live": {},
	} {
		item := dynamo.Key("C#"+id, "META")
		maps.Copy(item, attrs)
		if _, err := c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: item}); err != nil {
			t.Fatal(err)
		}
	}
	count := func(clientID string) int {
		t.Helper()
		n := 0
		p := dynamodb.NewScanPaginator(c, &dynamodb.ScanInput{
			TableName:                 aws.String(table),
			FilterExpression:          aws.String("begins_with(pk, :p)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{":p": &types.AttributeValueMemberS{Value: "S#" + clientID + "#"}},
			ConsistentRead:            aws.Bool(true),
		})
		for p.HasMorePages() {
			page, err := p.NextPage(ctx)
			if err != nil {
				t.Fatal(err)
			}
			n += len(page.Items)
		}
		return n
	}

	got, err := factor.PruneDeleted(ctx, c, table, false)
	if err != nil || len(got) != 1 || got["gone"] != 31 {
		t.Fatalf("dry run = %v err=%v, want only gone:31", got, err)
	}
	if count("gone") != 31 {
		t.Fatalf("dry run deleted items: %d left", count("gone"))
	}
	got, err = factor.PruneDeleted(ctx, c, table, true)
	if err != nil || len(got) != 1 || got["gone"] != 31 {
		t.Fatalf("apply = %v err=%v, want only gone:31", got, err)
	}
	if n := count("gone"); n != 0 {
		t.Fatalf("apply left %d items of the deleted client", n)
	}
	if count("live") != 1 || count("unknown") != 1 {
		t.Fatal("apply touched a live client or one without a META item")
	}
	if got, err = factor.PruneDeleted(ctx, c, table, true); err != nil || len(got) != 0 {
		t.Fatalf("rerun = %v err=%v, want empty", got, err)
	}
}
