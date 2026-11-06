package client

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

// Store persists clients and access key items.
type Store interface {
	PutClient(ctx context.Context, c Client) error
	GetClient(ctx context.Context, id string) (Client, error)
	// UpdateClient writes only the attributes set in p; a missing client is ErrNotFound.
	UpdateClient(ctx context.Context, c Client, p Patch) error
	// SetKeyHashes replaces keyHashes and nothing else; a missing client is ErrNotFound.
	SetKeyHashes(ctx context.Context, id string, hashes []string) error
	// PutKey stores a key item; a nil expiresAt means the key is active.
	PutKey(ctx context.Context, hash, clientID string, expiresAt *time.Time) error
	GetKey(ctx context.Context, hash string) (clientID string, expiresAt *time.Time, err error)
	ExpireKey(ctx context.Context, hash string, at time.Time) error
}

type dynamoStore struct {
	c     *dynamodb.Client
	table string
}

// NewDynamoStore returns a Store backed by the single table.
func NewDynamoStore(c *dynamodb.Client, table string) Store { return &dynamoStore{c: c, table: table} }

func clientKey(id string) map[string]types.AttributeValue { return dynamo.Key("C#"+id, "META") }
func keyKey(hash string) map[string]types.AttributeValue  { return dynamo.Key("K#"+hash, "META") }

func str(s string) types.AttributeValue { return &types.AttributeValueMemberS{Value: s} }
func num(n int) types.AttributeValue    { return &types.AttributeValueMemberN{Value: strconv.Itoa(n)} }

func (s *dynamoStore) PutClient(ctx context.Context, c Client) error {
	item := clientKey(c.ID)
	item["name"] = str(c.Name)
	item["rpId"] = str(c.RPID)
	item["origins"] = &types.AttributeValueMemberSS{Value: c.Origins}
	item["pinLength"] = num(c.PinPolicy.Length)
	item["pinMaxAttempts"] = num(c.PinPolicy.MaxAttempts)
	item["pinLockoutSec"] = num(c.PinPolicy.LockoutSec)
	item["tokenTtlSec"] = num(c.TokenTTLSec)
	item["rateLimitBurst"] = num(c.RateLimit.Burst)
	item["rateLimitPerSecond"] = &types.AttributeValueMemberN{Value: strconv.FormatFloat(c.RateLimit.PerSecond, 'g', -1, 64)}
	item["createdAt"] = str(c.CreatedAt.UTC().Format(time.RFC3339))
	if len(c.KeyHashes) > 0 {
		item["keyHashes"] = &types.AttributeValueMemberSS{Value: c.KeyHashes}
	}
	if _, err := s.c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.table), Item: item}); err != nil {
		return fmt.Errorf("put client: %w", err)
	}
	return nil
}

func (s *dynamoStore) GetClient(ctx context.Context, id string) (Client, error) {
	out, err := s.c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: clientKey(id), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return Client{}, fmt.Errorf("get client: %w", err)
	}
	if out.Item == nil {
		return Client{}, ErrNotFound
	}
	it := out.Item
	created, err := time.Parse(time.RFC3339, getS(it, "createdAt"))
	if err != nil {
		return Client{}, fmt.Errorf("client %s: bad createdAt: %w", id, err)
	}
	return Client{
		ID:      id,
		Name:    getS(it, "name"),
		RPID:    getS(it, "rpId"),
		Origins: getSS(it, "origins"),
		PinPolicy: PinPolicy{
			Length:      getN(it, "pinLength"),
			MaxAttempts: getN(it, "pinMaxAttempts"),
			LockoutSec:  getN(it, "pinLockoutSec"),
		},
		TokenTTLSec: getN(it, "tokenTtlSec"),
		RateLimit:   rateLimitOf(it),
		CreatedAt:   created,
		KeyHashes:   getSS(it, "keyHashes"),
	}, nil
}

// rateLimitOf reads the bucket settings. An item without both attributes (a
// client created before they existed) gets the default, and so does one whose
// stored values would not pass validation, so a zero rate is never returned.
func rateLimitOf(it map[string]types.AttributeValue) ratelimit.Settings {
	burst, okB := getF(it, "rateLimitBurst")
	rate, okR := getF(it, "rateLimitPerSecond")
	if !okB || !okR || burst < 1 || !(rate > 0) {
		return DefaultRateLimit()
	}
	return ratelimit.Settings{Burst: int(burst), PerSecond: rate}
}

func (s *dynamoStore) UpdateClient(ctx context.Context, c Client, p Patch) error {
	var sets []string
	vals := map[string]types.AttributeValue{}
	if r := p.RateLimit; r != nil {
		sets = append(sets, "rateLimitBurst = :rb", "rateLimitPerSecond = :rp")
		vals[":rb"] = num(r.Burst)
		vals[":rp"] = &types.AttributeValueMemberN{Value: strconv.FormatFloat(r.PerSecond, 'g', -1, 64)}
	}
	if len(sets) == 0 {
		return fmt.Errorf("%w: nothing to update", ErrInvalid)
	}
	return s.update(ctx, c.ID, "SET "+strings.Join(sets, ", "), vals, "update client")
}

func (s *dynamoStore) SetKeyHashes(ctx context.Context, id string, hashes []string) error {
	return s.update(ctx, id, "SET keyHashes = :h", map[string]types.AttributeValue{":h": &types.AttributeValueMemberSS{Value: hashes}}, "set key hashes")
}

// update runs a conditional UpdateItem on a client item.
func (s *dynamoStore) update(ctx context.Context, id, expr string, vals map[string]types.AttributeValue, what string) error {
	_, err := s.c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(s.table),
		Key:                       clientKey(id),
		UpdateExpression:          aws.String(expr),
		ConditionExpression:       aws.String("attribute_exists(pk)"),
		ExpressionAttributeValues: vals,
	})
	var missing *types.ConditionalCheckFailedException
	if errors.As(err, &missing) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func (s *dynamoStore) PutKey(ctx context.Context, hash, clientID string, expiresAt *time.Time) error {
	item := keyKey(hash)
	item["clientId"] = str(clientID)
	item["createdAt"] = str(time.Now().UTC().Format(time.RFC3339))
	if expiresAt != nil {
		item["ttl"] = &types.AttributeValueMemberN{Value: strconv.FormatInt(expiresAt.Unix(), 10)}
	}
	if _, err := s.c.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(s.table), Item: item}); err != nil {
		return fmt.Errorf("put key: %w", err)
	}
	return nil
}

func (s *dynamoStore) GetKey(ctx context.Context, hash string) (string, *time.Time, error) {
	out, err := s.c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: keyKey(hash), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return "", nil, fmt.Errorf("get key: %w", err)
	}
	if out.Item == nil {
		return "", nil, ErrNotFound
	}
	var exp *time.Time
	if v, ok := out.Item["ttl"].(*types.AttributeValueMemberN); ok {
		secs, err := strconv.ParseInt(v.Value, 10, 64)
		if err != nil {
			return "", nil, fmt.Errorf("key ttl: %w", err)
		}
		t := time.Unix(secs, 0)
		exp = &t
	}
	return getS(out.Item, "clientId"), exp, nil
}

func (s *dynamoStore) ExpireKey(ctx context.Context, hash string, at time.Time) error {
	_, err := s.c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(s.table),
		Key:                       keyKey(hash),
		UpdateExpression:          aws.String("SET #t = :t"),
		ConditionExpression:       aws.String("attribute_exists(pk)"),
		ExpressionAttributeNames:  map[string]string{"#t": "ttl"},
		ExpressionAttributeValues: map[string]types.AttributeValue{":t": &types.AttributeValueMemberN{Value: strconv.FormatInt(at.Unix(), 10)}},
	})
	if err != nil {
		return fmt.Errorf("expire key: %w", err)
	}
	return nil
}

func getS(it map[string]types.AttributeValue, k string) string {
	if v, ok := it[k].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func getN(it map[string]types.AttributeValue, k string) int {
	if v, ok := it[k].(*types.AttributeValueMemberN); ok {
		n, _ := strconv.Atoi(v.Value)
		return n
	}
	return 0
}

func getF(it map[string]types.AttributeValue, k string) (float64, bool) {
	if v, ok := it[k].(*types.AttributeValueMemberN); ok {
		f, err := strconv.ParseFloat(v.Value, 64)
		return f, err == nil
	}
	return 0, false
}

func getSS(it map[string]types.AttributeValue, k string) []string {
	if v, ok := it[k].(*types.AttributeValueMemberSS); ok {
		return v.Value
	}
	return nil
}
