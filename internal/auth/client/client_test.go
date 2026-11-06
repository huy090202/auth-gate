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
	svc, _, _ := newSvc(t)
	cl, _ := mustCreate(t, svc)
	ctx := context.Background()
	want := ratelimit.Settings{Burst: 5, PerSecond: 0.5}
	if _, err := svc.Update(ctx, cl.ID, client.Patch{RateLimit: &want}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RotateKey(ctx, cl.ID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(ctx, cl.ID)
	if err != nil || got.RateLimit != want {
		t.Fatalf("after rotate: %+v %v", got.RateLimit, err)
	}
}
