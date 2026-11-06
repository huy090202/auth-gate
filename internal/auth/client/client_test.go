package client_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

func TestCreateInputValidate(t *testing.T) {
	ttl := func(n int) *int { return &n }
	base := func() client.CreateInput {
		return client.CreateInput{Name: "shop", RPID: "example.com", Origins: []string{"https://app.example.com"}}
	}
	tests := []struct {
		name    string
		mutate  func(*client.CreateInput)
		wantErr bool
	}{
		{"ok", func(*client.CreateInput) {}, false},
		{"ttl 901", func(in *client.CreateInput) { in.TokenTTLSec = ttl(901) }, true},
		{"origin without scheme", func(in *client.CreateInput) { in.Origins = []string{"app.example.com"} }, true},
		{"rpId not a suffix of origin host", func(in *client.CreateInput) { in.RPID = "other.com" }, true},
		{"duplicate origin", func(in *client.CreateInput) {
			in.Origins = []string{"https://app.example.com", "https://app.example.com"}
		}, true},
		{"rpId equals origin host", func(in *client.CreateInput) { in.RPID = "app.example.com" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mutate(&in)
			err := in.Validate()
			if tc.wantErr && !errors.Is(err, client.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
		})
	}
}

func TestAuthenticateByHashAndRotation(t *testing.T) {
	c, table := dynamotest.NewTable(t)
	ctx := context.Background()
	now := time.Now()
	svc := &client.Service{Store: client.NewDynamoStore(c, table), Now: func() time.Time { return now }}

	cl, raw, err := svc.Create(ctx, client.CreateInput{Name: "shop", RPID: "example.com", Origins: []string{"https://app.example.com"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := svc.Authenticate(ctx, raw)
	if err != nil || got.ID != cl.ID {
		t.Fatalf("authenticate: %v %v", got.ID, err)
	}
	if _, err := svc.Authenticate(ctx, "ag_nope"); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("unknown key: want ErrUnauthorized, got %v", err)
	}

	newRaw, err := svc.RotateKey(ctx, cl.ID)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := svc.Authenticate(ctx, newRaw); err != nil {
		t.Fatalf("new key: %v", err)
	}
	if _, err := svc.Authenticate(ctx, raw); err != nil {
		t.Fatalf("old key within grace: %v", err)
	}

	now = now.Add(25 * time.Hour)
	if _, err := svc.Authenticate(ctx, raw); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("old key after grace: want ErrUnauthorized, got %v", err)
	}
	if _, err := svc.Authenticate(ctx, newRaw); err != nil {
		t.Fatalf("new key after grace: %v", err)
	}
}

func newSvc(t *testing.T) (*client.Service, *dynamodb.Client, string) {
	t.Helper()
	c, table := dynamotest.NewTable(t)
	return &client.Service{Store: client.NewDynamoStore(c, table)}, c, table
}

func mustCreate(t *testing.T, svc *client.Service) (client.Client, string) {
	t.Helper()
	cl, raw, err := svc.Create(context.Background(), client.CreateInput{Name: "shop", RPID: "example.com", Origins: []string{"https://app.example.com"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return cl, raw
}

func TestRateLimitValidation(t *testing.T) {
	tests := []struct {
		name    string
		s       ratelimit.Settings
		wantErr bool
	}{
		{"burst 0", ratelimit.Settings{Burst: 0, PerSecond: 1}, true},
		{"burst 10001", ratelimit.Settings{Burst: 10001, PerSecond: 1}, true},
		{"perSecond 0", ratelimit.Settings{Burst: 1, PerSecond: 0}, true},
		{"perSecond -1", ratelimit.Settings{Burst: 1, PerSecond: -1}, true},
		{"perSecond 5001", ratelimit.Settings{Burst: 1, PerSecond: 5001}, true},
		{"lowest", ratelimit.Settings{Burst: 1, PerSecond: 0.01}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := client.Patch{RateLimit: &tc.s}.Validate()
			if tc.wantErr && !errors.Is(err, client.ErrInvalid) {
				t.Fatalf("want ErrInvalid, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
		})
	}
}

func TestLegacyItemGetsDefaultRateLimit(t *testing.T) {
	svc, db, table := newSvc(t)
	cl, _ := mustCreate(t, svc)
	ctx := context.Background()
	update := func(expr string, vals map[string]types.AttributeValue) {
		t.Helper()
		_, err := db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
			TableName: aws.String(table), Key: dynamo.Key("C#"+cl.ID, "META"),
			UpdateExpression: aws.String(expr), ExpressionAttributeValues: vals,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(label string) {
		t.Helper()
		got, err := svc.Get(ctx, cl.ID)
		if err != nil || got.RateLimit != client.DefaultRateLimit() {
			t.Fatalf("%s: got %+v %v, want the default", label, got.RateLimit, err)
		}
	}
	n := func(v string) map[string]types.AttributeValue {
		return map[string]types.AttributeValue{":v": &types.AttributeValueMemberN{Value: v}}
	}
	update("REMOVE rateLimitBurst, rateLimitPerSecond", nil)
	check("both absent")
	update("SET rateLimitBurst = :v", n("7"))
	check("only burst")
	update("REMOVE rateLimitBurst SET rateLimitPerSecond = :v", n("3"))
	check("only perSecond")
	update("SET rateLimitBurst = :v, rateLimitPerSecond = :z", map[string]types.AttributeValue{
		":v": &types.AttributeValueMemberN{Value: "7"}, ":z": &types.AttributeValueMemberN{Value: "0"}})
	check("stored zero rate")
}

func TestFractionalPerSecondRoundTrips(t *testing.T) {
	svc, _, _ := newSvc(t)
	cl, _ := mustCreate(t, svc)
	ctx := context.Background()
	if _, err := svc.Update(ctx, cl.ID, client.Patch{RateLimit: &ratelimit.Settings{Burst: 3, PerSecond: 0.01}}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, cl.ID)
	if err != nil || got.RateLimit != (ratelimit.Settings{Burst: 3, PerSecond: 0.01}) {
		t.Fatalf("got %+v %v", got.RateLimit, err)
	}
}

func TestUpdateKeepsKeyHashes(t *testing.T) {
	svc, _, _ := newSvc(t)
	cl, raw := mustCreate(t, svc)
	ctx := context.Background()
	if _, err := svc.Update(ctx, cl.ID, client.Patch{RateLimit: &ratelimit.Settings{Burst: 5, PerSecond: 1}}); err != nil {
		t.Fatal(err)
	}
	if got, err := svc.Authenticate(ctx, raw); err != nil || got.ID != cl.ID {
		t.Fatalf("authenticate after patch: %v %v", got.ID, err)
	}
}

func TestRotateKeepsRateLimit(t *testing.T) {
	svc, db, table := newSvc(t)
	cl, _ := mustCreate(t, svc)
	ctx := context.Background()
	want := ratelimit.Settings{Burst: 5, PerSecond: 0.5}
	if _, err := svc.Update(ctx, cl.ID, client.Patch{RateLimit: &want}); err != nil {
		t.Fatal(err)
	}
	// An attribute Client does not model: a whole-item rewrite would drop it.
	key := dynamo.Key("C#"+cl.ID, "META")
	if _, err := db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(table), Key: key,
		UpdateExpression:          aws.String("SET disabledAt = :v"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":v": &types.AttributeValueMemberS{Value: "2026-11-06T00:00:00Z"}},
	}); err != nil {
		t.Fatal(err)
	}
	newRaw, err := svc.RotateKey(ctx, cl.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, cl.ID)
	if err != nil || got.RateLimit != want {
		t.Fatalf("after rotate: %+v %v", got.RateLimit, err)
	}
	out, err := db.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: key, ConsistentRead: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := out.Item["disabledAt"].(*types.AttributeValueMemberS); !ok || v.Value != "2026-11-06T00:00:00Z" {
		t.Fatalf("rotate dropped an attribute it does not own: %v", out.Item["disabledAt"])
	}
	hashes, _ := out.Item["keyHashes"].(*types.AttributeValueMemberSS)
	if hashes == nil || len(hashes.Value) != 1 || hashes.Value[0] != client.HashKey(newRaw) {
		t.Fatalf("keyHashes %v, want only the new hash", out.Item["keyHashes"])
	}
}

func rawItem(t *testing.T, db *dynamodb.Client, table, pk string) map[string]types.AttributeValue {
	t.Helper()
	out, err := db.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: dynamo.Key(pk, "META"), ConsistentRead: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	return out.Item
}

func TestCreateWritesIndexInOneTransaction(t *testing.T) {
	svc, db, table := newSvc(t)
	cl, _ := mustCreate(t, svc)
	out, err := db.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: dynamo.Key("CLIENTS", "C#"+cl.ID), ConsistentRead: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	s := func(k string) string {
		v, _ := out.Item[k].(*types.AttributeValueMemberS)
		if v == nil {
			return ""
		}
		return v.Value
	}
	if s("name") != "shop" || s("rpId") != "example.com" || s("createdAt") != cl.CreatedAt.UTC().Format(time.RFC3339) {
		t.Fatalf("index item %v", out.Item)
	}
}

func TestRotateDropsExpiredRetiredKeys(t *testing.T) {
	c, table := dynamotest.NewTable(t)
	ctx := context.Background()
	now := time.Now()
	svc := &client.Service{Store: client.NewDynamoStore(c, table), Now: func() time.Time { return now }}
	cl, k1, err := svc.Create(ctx, client.CreateInput{Name: "shop", RPID: "example.com", Origins: []string{"https://app.example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	k2, err := svc.RotateKey(ctx, cl.ID)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(25 * time.Hour) // k1's grace is over
	if _, err := svc.RotateKey(ctx, cl.ID); err != nil {
		t.Fatal(err)
	}
	m, _ := rawItem(t, c, table, "C#"+cl.ID)["retiredKeys"].(*types.AttributeValueMemberM)
	if m == nil || len(m.Value) != 1 {
		t.Fatalf("retiredKeys %v, want only k2", m)
	}
	if _, ok := m.Value[client.HashKey(k2)]; !ok {
		t.Fatalf("retiredKeys %v lacks k2", m.Value)
	}
	if _, ok := m.Value[client.HashKey(k1)]; ok {
		t.Fatal("expired k1 is still recorded")
	}
}

// seamStore runs afterGet once, right after the next GetClient has read, so a
// test can interleave a second operation between a first one's read and write.
type seamStore struct {
	client.Store
	afterGet func()
}

func (s *seamStore) GetClient(ctx context.Context, id string) (client.Client, error) {
	c, err := s.Store.GetClient(ctx, id)
	if f := s.afterGet; f != nil && err == nil {
		s.afterGet = nil
		f()
	}
	return c, err
}

func TestConcurrentRotateLoserGetsConflict(t *testing.T) {
	c, table := dynamotest.NewTable(t)
	ctx := context.Background()
	seam := &seamStore{Store: client.NewDynamoStore(c, table)}
	svc := &client.Service{Store: seam}
	cl, _ := mustCreate(t, svc)
	var winner string
	seam.afterGet = func() {
		var err error
		if winner, err = svc.RotateKey(ctx, cl.ID); err != nil {
			t.Error(err)
		}
	}
	loser, err := svc.RotateKey(ctx, cl.ID)
	if !errors.Is(err, client.ErrRotateConflict) || loser != "" {
		t.Fatalf("loser got key %q, err %v; want ErrRotateConflict", loser, err)
	}
	if _, err := svc.Authenticate(ctx, winner); err != nil {
		t.Fatalf("winner's key: %v", err)
	}
	// Only the first key, the winner's and nothing else are key items: the loser's was deleted.
	out, err := c.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(table), FilterExpression: aws.String("begins_with(pk, :p)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":p": &types.AttributeValueMemberS{Value: "K#"}}})
	if err != nil || out.Count != 2 {
		t.Fatalf("key items: %v %v, want 2 (first and winner's)", out.Count, err)
	}
}

func TestRevokeKeepsEntriesItNeverRead(t *testing.T) {
	c, table := dynamotest.NewTable(t)
	ctx := context.Background()
	seam := &seamStore{Store: client.NewDynamoStore(c, table)}
	svc := &client.Service{Store: seam}
	cl, _ := mustCreate(t, svc)
	if _, err := svc.RotateKey(ctx, cl.ID); err != nil {
		t.Fatal(err)
	}
	// The key the interleaved rotate will retire is the current one now.
	got, err := svc.Get(ctx, cl.ID)
	if err != nil {
		t.Fatal(err)
	}
	cur := got.KeyHashes[0]
	// Revoke reads retired={k0}; a rotate then adds cur before revoke writes.
	seam.afterGet = func() {
		if _, err := svc.RotateKey(ctx, cl.ID); err != nil {
			t.Error(err)
		}
	}
	if n, err := svc.RevokePreviousKeys(ctx, cl.ID); err != nil || n != 1 {
		t.Fatalf("first revoke: %d %v", n, err)
	}
	m, _ := rawItem(t, c, table, "C#"+cl.ID)["retiredKeys"].(*types.AttributeValueMemberM)
	if m == nil {
		t.Fatal("retiredKeys gone: the entry revoke never read was erased")
	}
	if _, ok := m.Value[cur]; !ok || len(m.Value) != 1 {
		t.Fatalf("retiredKeys %v, want only %s", m.Value, cur)
	}
	if n, err := svc.RevokePreviousKeys(ctx, cl.ID); err != nil || n != 1 {
		t.Fatalf("second revoke: %d %v, want 1", n, err)
	}
	if _, _, err := seam.GetKey(ctx, cur); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("the interleaved key item still exists: %v", err)
	}
}

func TestDisableKeepsIndexName(t *testing.T) {
	svc, db, table := newSvc(t)
	cl, _ := mustCreate(t, svc)
	ctx := context.Background()
	idx := dynamo.Key("CLIENTS", "C#"+cl.ID)
	if _, err := db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(table), Key: idx,
		UpdateExpression:          aws.String("SET #n = :v"),
		ExpressionAttributeNames:  map[string]string{"#n": "name"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":v": &types.AttributeValueMemberS{Value: "renamed meanwhile"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, act := range []func(context.Context, string) (client.Client, error){svc.Disable, svc.Enable} {
		if _, err := act(ctx, cl.ID); err != nil {
			t.Fatal(err)
		}
		out, err := db.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: idx, ConsistentRead: aws.Bool(true)})
		if err != nil {
			t.Fatal(err)
		}
		if v, _ := out.Item["name"].(*types.AttributeValueMemberS); v == nil || v.Value != "renamed meanwhile" {
			t.Fatalf("index name %v, want it kept", out.Item["name"])
		}
	}
}
