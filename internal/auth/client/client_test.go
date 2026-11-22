package client_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
	"github.com/huy090202/auth-gate/internal/platform/id"
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

func TestBackfillIsIdempotentAndKeepsIndexState(t *testing.T) {
	svc, db, table := newSvc(t)
	ctx := context.Background()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC).Format(time.RFC3339)
	s := func(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }
	for _, id := range []string{"legacy1", "legacy2"} {
		item := dynamo.Key("C#"+id, "META")
		item["name"], item["rpId"], item["createdAt"] = s("old "+id), s("example.com"), s(created)
		item["origins"] = &types.AttributeValueMemberSS{Value: []string{"https://example.com"}}
		item["keyHashes"] = &types.AttributeValueMemberSS{Value: []string{"h-" + id}}
		for k, v := range map[string]string{"pinLength": "6", "pinMaxAttempts": "5", "pinLockoutSec": "900", "tokenTtlSec": "300"} {
			item[k] = &types.AttributeValueMemberN{Value: v}
		}
		for _, it := range []map[string]types.AttributeValue{item, dynamo.Key("K#h-"+id, "META"), dynamo.Key("S#"+id, "x")} {
			if _, err := db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: it}); err != nil {
				t.Fatal(err)
			}
		}
	}
	written, skipped, err := client.Backfill(ctx, db, table)
	if err != nil || written != 2 || skipped != 0 {
		t.Fatalf("first run: written %d skipped %d err %v, want 2 0", written, skipped, err)
	}
	if _, err := svc.Update(ctx, "legacy1", client.Patch{Name: aws.String("renamed")}); err != nil {
		t.Fatal(err)
	}
	snap := func(pk, sk string) map[string]types.AttributeValue {
		out, err := db.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: dynamo.Key(pk, sk), ConsistentRead: aws.Bool(true)})
		if err != nil {
			t.Fatal(err)
		}
		return out.Item
	}
	keyBefore, sessBefore := snap("K#h-legacy1", "META"), snap("S#legacy1", "x")
	written, skipped, err = client.Backfill(ctx, db, table)
	if err != nil || written != 0 || skipped != 2 {
		t.Fatalf("second run: written %d skipped %d err %v, want 0 2", written, skipped, err)
	}
	if v, _ := snap("CLIENTS", "C#legacy1")["name"].(*types.AttributeValueMemberS); v == nil || v.Value != "renamed" {
		t.Fatalf("index name %v, want renamed", snap("CLIENTS", "C#legacy1")["name"])
	}
	if !reflect.DeepEqual(snap("K#h-legacy1", "META"), keyBefore) || !reflect.DeepEqual(snap("S#legacy1", "x"), sessBefore) {
		t.Fatal("K# or S# item changed")
	}
}

func TestBackfillSkipsDeletedAndCopiesOwner(t *testing.T) {
	svc, db, table := newSvc(t)
	ctx := context.Background()
	t1 := newTenant()
	owned, _ := mustOwned(t, svc, t1)
	gone, _ := mustOwned(t, svc, t1)
	if err := svc.Delete(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(table), Key: dynamo.Key("CLIENTS", "C#"+owned.ID)}); err != nil {
		t.Fatal(err)
	}
	written, skipped, err := client.Backfill(ctx, db, table)
	if err != nil || written != 1 || skipped != 0 {
		t.Fatalf("written %d skipped %d err %v, want 1 0", written, skipped, err)
	}
	if got := attrS(rawItemSK(t, db, table, "CLIENTS", "C#"+owned.ID), "owner"); got != client.OwnerOf(t1) {
		t.Fatalf("backfilled owner %q, want %q", got, client.OwnerOf(t1))
	}
	if it := rawItemSK(t, db, table, "CLIENTS", "C#"+gone.ID); it != nil {
		t.Fatalf("deleted client is back in the list: %v", it)
	}
}

// --- ownership, quota and soft delete ---

func newTenant() string { return id.New() }

func createOwned(svc *client.Service, tenant string) (client.Client, string, error) {
	return svc.Create(context.Background(), client.CreateInput{
		Owner: client.OwnerOf(tenant), Name: "tenant app", RPID: "example.com", Origins: []string{"https://app.example.com"},
	})
}

func mustOwned(t *testing.T, svc *client.Service, tenant string) (client.Client, string) {
	t.Helper()
	cl, raw, err := createOwned(svc, tenant)
	if err != nil {
		t.Fatalf("create owned: %v", err)
	}
	return cl, raw
}

func rawItemSK(t *testing.T, db *dynamodb.Client, table, pk, sk string) map[string]types.AttributeValue {
	t.Helper()
	out, err := db.GetItem(context.Background(), &dynamodb.GetItemInput{TableName: aws.String(table), Key: dynamo.Key(pk, sk), ConsistentRead: aws.Bool(true)})
	if err != nil {
		t.Fatal(err)
	}
	return out.Item
}

func attrS(it map[string]types.AttributeValue, k string) string {
	v, _ := it[k].(*types.AttributeValueMemberS)
	if v == nil {
		return ""
	}
	return v.Value
}

func attrN(it map[string]types.AttributeValue, k string) string {
	v, _ := it[k].(*types.AttributeValueMemberN)
	if v == nil {
		return ""
	}
	return v.Value
}

func mustCount(t *testing.T, svc *client.Service, tenant string, want int) {
	t.Helper()
	o, err := svc.GetOwner(context.Background(), tenant)
	if err != nil || o.ClientCount != want {
		t.Fatalf("clientCount %d (%v), want %d", o.ClientCount, err, want)
	}
}

func TestOwnedCreateWritesFourItemsInOneTransaction(t *testing.T) {
	svc, db, table := newSvc(t)
	t1 := newTenant()
	cl, _, err := svc.Create(context.Background(), client.CreateInput{
		Owner: client.OwnerOf(t1), Name: "shop", RPID: "example.com", Origins: []string{"https://app.example.com"},
		RateLimit: &ratelimit.Settings{Burst: 20, PerSecond: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	item := rawItem(t, db, table, "C#"+cl.ID)
	if attrS(item, "owner") != "T#"+t1 || attrN(item, "rateLimitBurst") != "20" || attrN(item, "rateLimitPerSecond") != "10" {
		t.Fatalf("client item %v", item)
	}
	if got := attrS(rawItemSK(t, db, table, "CLIENTS", "C#"+cl.ID), "owner"); got != "T#"+t1 {
		t.Fatalf("CLIENTS owner %q", got)
	}
	if got := attrS(rawItemSK(t, db, table, "OWNER#"+t1, "C#"+cl.ID), "name"); got != "shop" {
		t.Fatalf("owner index name %q", got)
	}
	if got := attrN(rawItem(t, db, table, "OWNER#"+t1), "clientCount"); got != "1" {
		t.Fatalf("clientCount %q", got)
	}
}

// failingTxStore makes CreateClient fail after the key item was written, to
// show a failed create leaves no client or owner item behind.
func TestFailedOwnedCreateLeavesNoItems(t *testing.T) {
	svc, db, table := newSvc(t)
	t1 := newTenant()
	// An already-used id cannot be forced, so make the owner refuse instead: quota 0.
	if _, err := svc.SetQuota(context.Background(), t1, 0); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createOwned(svc, t1); !errors.Is(err, client.ErrQuotaExceeded) {
		t.Fatalf("want ErrQuotaExceeded, got %v", err)
	}
	out, err := db.Scan(context.Background(), &dynamodb.ScanInput{TableName: aws.String(table)})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range out.Items {
		if pk := attrS(it, "pk"); pk != "OWNER#"+t1 {
			t.Fatalf("unexpected item %v after a refused create", it)
		}
	}
	mustCount(t, svc, t1, 0)
}

func TestOperatorCreateWritesNoOwnerItems(t *testing.T) {
	svc, db, table := newSvc(t)
	cl, _ := mustCreate(t, svc)
	if _, ok := rawItem(t, db, table, "C#"+cl.ID)["owner"]; ok {
		t.Fatal("operator client has an owner attribute")
	}
	if _, ok := rawItemSK(t, db, table, "CLIENTS", "C#"+cl.ID)["owner"]; ok {
		t.Fatal("operator index item has an owner attribute")
	}
	out, err := db.Scan(context.Background(), &dynamodb.ScanInput{TableName: aws.String(table), FilterExpression: aws.String("begins_with(pk, :p)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":p": &types.AttributeValueMemberS{Value: "OWNER#"}}})
	if err != nil || out.Count != 0 {
		t.Fatalf("OWNER# items: %v %v", out.Count, err)
	}
}

func TestQuotaHoldsUnderConcurrency(t *testing.T) {
	svc, _, _ := newSvc(t)
	t1 := newTenant()
	const n = 10
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { _, _, err := createOwned(svc, t1); errs <- err }()
	}
	ok, full := 0, 0
	for i := 0; i < n; i++ {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, client.ErrQuotaExceeded):
			full++
		default:
			t.Errorf("unexpected error %v", err)
		}
	}
	if ok != 5 || full != 5 {
		t.Fatalf("%d created, %d refused; want 5 and 5", ok, full)
	}
	mustCount(t, svc, t1, 5)
}

func TestLoweredQuotaRefusesCreatesKeepsClients(t *testing.T) {
	svc, _, _ := newSvc(t)
	ctx := context.Background()
	t1 := newTenant()
	var ids []string
	for i := 0; i < 3; i++ {
		cl, _ := mustOwned(t, svc, t1)
		ids = append(ids, cl.ID)
	}
	if _, err := svc.SetQuota(ctx, t1, 1); err != nil {
		t.Fatal(err)
	}
	if _, _, err := createOwned(svc, t1); !errors.Is(err, client.ErrQuotaExceeded) {
		t.Fatalf("want ErrQuotaExceeded, got %v", err)
	}
	for _, cid := range ids {
		if _, err := svc.Get(ctx, cid); err != nil {
			t.Fatalf("existing client %s: %v", cid, err)
		}
	}
	for _, cid := range ids {
		if err := svc.Delete(ctx, cid); err != nil {
			t.Fatal(err)
		}
	}
	mustCount(t, svc, t1, 0)
	mustOwned(t, svc, t1)
	if _, _, err := createOwned(svc, t1); !errors.Is(err, client.ErrQuotaExceeded) {
		t.Fatalf("second create: want ErrQuotaExceeded, got %v", err)
	}
}

func TestSetQuotaBounds(t *testing.T) {
	svc, _, _ := newSvc(t)
	t1 := newTenant()
	for _, q := range []int{-1, 1001} {
		if _, err := svc.SetQuota(context.Background(), t1, q); !errors.Is(err, client.ErrInvalid) {
			t.Fatalf("quota %d: want ErrInvalid, got %v", q, err)
		}
	}
	if o, err := svc.SetQuota(context.Background(), t1, 1000); err != nil || o.ClientQuota != 1000 {
		t.Fatalf("quota 1000: %+v %v", o, err)
	}
}

// A suspended owner is only reachable through a raw item until a later task adds the writer.
func TestSuspendedOwnerRefusesCreate(t *testing.T) {
	svc, db, table := newSvc(t)
	t1 := newTenant()
	_, err := db.PutItem(context.Background(), &dynamodb.PutItemInput{TableName: aws.String(table), Item: map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: "OWNER#" + t1}, "sk": &types.AttributeValueMemberS{Value: "META"},
		"suspendedAt": &types.AttributeValueMemberS{Value: "2026-11-22T00:00:00Z"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := createOwned(svc, t1); !errors.Is(err, client.ErrOwnerSuspended) {
		t.Fatalf("want ErrOwnerSuspended, got %v", err)
	}
	if o, err := svc.GetOwner(context.Background(), t1); err != nil || o.SuspendedAt == nil {
		t.Fatalf("owner %+v %v", o, err)
	}
}

func TestDeleteHidesClientAndFreesSlot(t *testing.T) {
	svc, _, _ := newSvc(t)
	ctx := context.Background()
	t1 := newTenant()
	cl, raw := mustOwned(t, svc, t1)
	mustCount(t, svc, t1, 1)
	if err := svc.Delete(ctx, cl.ID); err != nil {
		t.Fatal(err)
	}
	notFound := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, client.ErrNotFound) {
			t.Fatalf("%s: want ErrNotFound, got %v", what, err)
		}
	}
	_, err := svc.Get(ctx, cl.ID)
	notFound("Get", err)
	name := "x"
	_, err = svc.Update(ctx, cl.ID, client.Patch{Name: &name})
	notFound("Update", err)
	_, err = svc.Disable(ctx, cl.ID)
	notFound("Disable", err)
	_, err = svc.Enable(ctx, cl.ID)
	notFound("Enable", err)
	_, err = svc.RotateKey(ctx, cl.ID)
	notFound("RotateKey", err)
	_, err = svc.RevokePreviousKeys(ctx, cl.ID)
	notFound("RevokePreviousKeys", err)
	if _, err := svc.Authenticate(ctx, raw); !errors.Is(err, client.ErrUnauthorized) {
		t.Fatalf("Authenticate: want ErrUnauthorized, got %v", err)
	}
	for _, list := range []func() ([]client.Summary, string, error){
		func() ([]client.Summary, string, error) { return svc.List(ctx, "") },
		func() ([]client.Summary, string, error) { return svc.ListOwned(ctx, t1, "") },
	} {
		if got, _, err := list(); err != nil || len(got) != 0 {
			t.Fatalf("list after delete: %v %v", got, err)
		}
	}
	mustCount(t, svc, t1, 0)
	notFound("second Delete", svc.Delete(ctx, cl.ID))
	mustCount(t, svc, t1, 0)
}

func TestDeleteRemovesKeyItems(t *testing.T) {
	svc, db, table := newSvc(t)
	cl, raw := mustCreate(t, svc)
	if _, err := svc.RotateKey(context.Background(), cl.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(context.Background(), cl.ID); err != nil {
		t.Fatal(err)
	}
	if it := rawItem(t, db, table, "K#"+client.HashKey(raw)); it != nil {
		t.Fatalf("retired key item survived: %v", it)
	}
	if got := attrS(rawItem(t, db, table, "C#"+cl.ID), "deletedAt"); got == "" {
		t.Fatal("client item lost its deletedAt")
	}
}

func TestWriteAfterDeleteDoesNotResurrect(t *testing.T) {
	c, table := dynamotest.NewTable(t)
	ctx := context.Background()
	plain := &client.Service{Store: client.NewDynamoStore(c, table)}
	seam := &seamStore{Store: plain.Store}
	svc := &client.Service{Store: seam}
	cl, _ := mustCreate(t, plain)
	deleteBetween := func() { seam.afterGet = func() { _ = plain.Delete(ctx, cl.ID) } }

	name := "renamed"
	deleteBetween()
	if _, err := svc.Update(ctx, cl.ID, client.Patch{Name: &name}); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("Update: %v", err)
	}
	// The client is deleted now; the next reads are hidden, so use the store directly for the rest.
	st := plain.Store
	stored, err := st.GetClient(ctx, cl.ID)
	if err != nil || stored.DeletedAt == nil {
		t.Fatalf("stored %+v %v", stored, err)
	}
	now := time.Now()
	if err := st.SetDisabled(ctx, stored, &now); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("SetDisabled: %v", err)
	}
	if err := st.UpdateClient(ctx, stored, client.Patch{Name: &name}); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("UpdateClient with name: %v", err)
	}
	ttl := 60
	if err := st.UpdateClient(ctx, stored, client.Patch{TokenTTLSec: &ttl}); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("UpdateClient: %v", err)
	}
	if err := st.SetKeys(ctx, cl.ID, stored.KeyHashes, []string{"h"}, nil); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("SetKeys: %v", err)
	}
	if err := st.ClearRetiredKeys(ctx, cl.ID, []string{"h"}); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("ClearRetiredKeys: %v", err)
	}
	item := rawItem(t, c, table, "C#"+cl.ID)
	if attrS(item, "deletedAt") == "" || attrS(item, "name") != "shop" || item["disabledAt"] != nil {
		t.Fatalf("item changed after delete: %v", item)
	}
	if idx := rawItemSK(t, c, table, "CLIENTS", "C#"+cl.ID); idx != nil {
		t.Fatalf("index item came back: %v", idx)
	}
}

func TestDisableRacingDeleteDoesNotResurrect(t *testing.T) {
	c, table := dynamotest.NewTable(t)
	ctx := context.Background()
	plain := &client.Service{Store: client.NewDynamoStore(c, table)}
	seam := &seamStore{Store: plain.Store}
	svc := &client.Service{Store: seam}
	cl, _ := mustCreate(t, plain)
	seam.afterGet = func() { _ = plain.Delete(ctx, cl.ID) }
	if _, err := svc.Disable(ctx, cl.ID); !errors.Is(err, client.ErrNotFound) {
		t.Fatalf("Disable: %v", err)
	}
	if rawItem(t, c, table, "C#"+cl.ID)["disabledAt"] != nil || rawItemSK(t, c, table, "CLIENTS", "C#"+cl.ID) != nil {
		t.Fatal("Disable wrote to a deleted client")
	}
}

func TestListOwnedPagesOnlyThatTenant(t *testing.T) {
	svc, _, _ := newSvc(t)
	ctx := context.Background()
	t1, t2 := newTenant(), newTenant()
	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		cl, _ := mustOwned(t, svc, t1)
		want[cl.ID] = true
	}
	mustOwned(t, svc, t2)
	mustOwned(t, svc, t2)
	mustCreate(t, svc)

	svc.PageSize = 2
	got, cursor, err := svc.ListOwned(ctx, t1, "")
	if err != nil || len(got) != 2 || cursor == "" {
		t.Fatalf("page 1: %d %q %v", len(got), cursor, err)
	}
	rest, cursor2, err := svc.ListOwned(ctx, t1, cursor)
	if err != nil || len(rest) != 1 || cursor2 != "" {
		t.Fatalf("page 2: %d %q %v", len(rest), cursor2, err)
	}
	for _, s := range append(got, rest...) {
		if !want[s.ID] || s.Owner != client.OwnerOf(t1) {
			t.Fatalf("unexpected summary %+v", s)
		}
		delete(want, s.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing %v", want)
	}

	svc.PageSize = 50
	all, _, err := svc.List(ctx, "")
	if err != nil || len(all) != 6 {
		t.Fatalf("List: %d %v", len(all), err)
	}
	owned := 0
	for _, s := range all {
		if s.Owner != "" {
			owned++
		}
	}
	if owned != 5 {
		t.Fatalf("%d summaries carry an owner, want 5", owned)
	}
}

func TestRenameAndDisableKeepOwnerIndexInStep(t *testing.T) {
	svc, db, table := newSvc(t)
	ctx := context.Background()
	t1 := newTenant()
	cl, _ := mustOwned(t, svc, t1)
	name := "renamed"
	if _, err := svc.Update(ctx, cl.ID, client.Patch{Name: &name}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Disable(ctx, cl.ID); err != nil {
		t.Fatal(err)
	}
	it := rawItemSK(t, db, table, "OWNER#"+t1, "C#"+cl.ID)
	if attrS(it, "name") != "renamed" || attrS(it, "disabledAt") == "" {
		t.Fatalf("owner index item %v", it)
	}
	if _, err := svc.Enable(ctx, cl.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := rawItemSK(t, db, table, "OWNER#"+t1, "C#"+cl.ID)["disabledAt"]; ok {
		t.Fatal("owner index item kept disabledAt after enable")
	}
}

func TestGetOwnerDefaults(t *testing.T) {
	svc, _, _ := newSvc(t)
	t1 := newTenant()
	o, err := svc.GetOwner(context.Background(), t1)
	if err != nil || o.ClientCount != 0 || o.ClientQuota != 5 || o.SuspendedAt != nil || o.TenantID != t1 {
		t.Fatalf("%+v %v", o, err)
	}
}
