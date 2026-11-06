package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/id"
	"github.com/huy090202/auth-gate/internal/platform/ratelimit"
)

// Store persists clients and access key items.
type Store interface {
	// CreateClient writes the client and its list-index item in one transaction.
	CreateClient(ctx context.Context, c Client) error
	GetClient(ctx context.Context, id string) (Client, error)
	// ListClients returns up to limit summaries, newest first, after cursor, and
	// the next cursor (empty on the last page). A bad cursor is ErrBadCursor.
	ListClients(ctx context.Context, cursor string, limit int32) ([]Summary, string, error)
	// UpdateClient writes only the attributes set in p; a missing client is ErrNotFound.
	// c is the stored client. A name change also updates the index item.
	UpdateClient(ctx context.Context, c Client, p Patch) error
	// SetDisabled sets disabledAt to at (keeping an earlier value), or clears it when at is nil.
	SetDisabled(ctx context.Context, c Client, at *time.Time) error
	// SetKeys replaces keyHashes and retiredKeys and nothing else, but only while
	// keyHashes still equals expected. A missing client is ErrNotFound; a changed
	// keyHashes is ErrRotateConflict.
	SetKeys(ctx context.Context, id string, expected, hashes []string, retired map[string]time.Time) error
	// ClearRetiredKeys removes the retiredKeys entries for the given hashes and
	// nothing else; a missing client is ErrNotFound.
	ClearRetiredKeys(ctx context.Context, id string, hashes []string) error
	// PutKey stores a key item; a nil expiresAt means the key is active.
	PutKey(ctx context.Context, hash, clientID string, expiresAt *time.Time) error
	GetKey(ctx context.Context, hash string) (clientID string, expiresAt *time.Time, err error)
	ExpireKey(ctx context.Context, hash string, at time.Time) error
	// DeleteKey removes a key item; a missing one is not an error.
	DeleteKey(ctx context.Context, hash string) error
}

type dynamoStore struct {
	c     *dynamodb.Client
	table string
}

// NewDynamoStore returns a Store backed by the single table.
func NewDynamoStore(c *dynamodb.Client, table string) Store { return &dynamoStore{c: c, table: table} }

const indexPK = "CLIENTS"

func clientKey(id string) map[string]types.AttributeValue { return dynamo.Key("C#"+id, "META") }
func indexKey(id string) map[string]types.AttributeValue  { return dynamo.Key(indexPK, "C#"+id) }
func keyKey(hash string) map[string]types.AttributeValue  { return dynamo.Key("K#"+hash, "META") }

func str(s string) types.AttributeValue { return &types.AttributeValueMemberS{Value: s} }
func num(n int) types.AttributeValue    { return &types.AttributeValueMemberN{Value: strconv.Itoa(n)} }
func ts(t time.Time) types.AttributeValue {
	return str(t.UTC().Format(time.RFC3339))
}

func (s *dynamoStore) CreateClient(ctx context.Context, c Client) error {
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
	item["createdAt"] = ts(c.CreatedAt)
	if len(c.KeyHashes) > 0 {
		item["keyHashes"] = &types.AttributeValueMemberSS{Value: c.KeyHashes}
	}
	idx := indexKey(c.ID)
	idx["name"] = str(c.Name)
	idx["rpId"] = str(c.RPID)
	idx["createdAt"] = ts(c.CreatedAt)
	err := dynamo.TransactWrite(ctx, s.c,
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(s.table), Item: item, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(s.table), Item: idx}},
	)
	if err != nil {
		return fmt.Errorf("create client: %w", err)
	}
	return nil
}

func (s *dynamoStore) ListClients(ctx context.Context, cursor string, limit int32) ([]Summary, string, error) {
	in := &dynamodb.QueryInput{
		TableName:                 aws.String(s.table),
		KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :c)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":p": str(indexPK), ":c": str("C#")},
		ScanIndexForward:          aws.Bool(false),
		Limit:                     aws.Int32(limit + 1), // one extra tells whether a next page exists
		ConsistentRead:            aws.Bool(true),
	}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || id.Validate(string(raw)) != nil {
			return nil, "", ErrBadCursor
		}
		in.ExclusiveStartKey = indexKey(string(raw))
	}
	out, err := s.c.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("list clients: %w", err)
	}
	items := out.Items
	next := ""
	if len(items) > int(limit) {
		items = items[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(strings.TrimPrefix(getS(items[len(items)-1], "sk"), "C#")))
	}
	res := make([]Summary, 0, len(items))
	for _, it := range items {
		sm := Summary{ID: strings.TrimPrefix(getS(it, "sk"), "C#"), Name: getS(it, "name"), RPID: getS(it, "rpId")}
		sm.CreatedAt, _ = time.Parse(time.RFC3339, getS(it, "createdAt"))
		sm.DisabledAt = getT(it, "disabledAt")
		res = append(res, sm)
	}
	return res, next, nil
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
	// A disabledAt that is present but unreadable must not mean "enabled".
	disabled, err := disabledOf(it)
	if err != nil {
		return Client{}, fmt.Errorf("client %s: %w", id, err)
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
		RetiredKeys: retiredOf(it),
		DisabledAt:  disabled,
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
	names := map[string]string{}
	vals := map[string]types.AttributeValue{}
	if p.Name != nil {
		sets = append(sets, "#name = :n")
		names["#name"] = "name"
		vals[":n"] = str(*p.Name)
	}
	if p.Origins != nil {
		sets = append(sets, "origins = :o")
		vals[":o"] = &types.AttributeValueMemberSS{Value: p.Origins}
	}
	if pp := p.PinPolicy; pp != nil {
		sets = append(sets, "pinLength = :pl", "pinMaxAttempts = :pm", "pinLockoutSec = :pk")
		vals[":pl"], vals[":pm"], vals[":pk"] = num(pp.Length), num(pp.MaxAttempts), num(pp.LockoutSec)
	}
	if p.TokenTTLSec != nil {
		sets = append(sets, "tokenTtlSec = :t")
		vals[":t"] = num(*p.TokenTTLSec)
	}
	if r := p.RateLimit; r != nil {
		sets = append(sets, "rateLimitBurst = :rb", "rateLimitPerSecond = :rp")
		vals[":rb"] = num(r.Burst)
		vals[":rp"] = &types.AttributeValueMemberN{Value: strconv.FormatFloat(r.PerSecond, 'g', -1, 64)}
	}
	if len(sets) == 0 {
		return fmt.Errorf("%w: nothing to update", ErrInvalid)
	}
	expr := "SET " + strings.Join(sets, ", ")
	if p.Name == nil {
		return s.update(ctx, c.ID, expr, nil, vals, "update client")
	}
	err := dynamo.TransactWrite(ctx, s.c,
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(s.table), Key: clientKey(c.ID), UpdateExpression: aws.String(expr),
			ConditionExpression:       aws.String("attribute_exists(pk)"),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: vals,
		}},
		s.indexUpdate(c, "SET #name = :n, rpId = if_not_exists(rpId, :r), createdAt = if_not_exists(createdAt, :c)", map[string]types.AttributeValue{":n": str(*p.Name)}),
	)
	return s.txError(err, "update client")
}

// indexUpdate builds the write to the client's index item. It always sets
// name, rpId and createdAt when missing, so a client created before the index
// existed still gets a complete item. expr must contain the name assignment.
func (s *dynamoStore) indexUpdate(c Client, expr string, vals map[string]types.AttributeValue) types.TransactWriteItem {
	v := map[string]types.AttributeValue{":r": str(c.RPID), ":c": ts(c.CreatedAt)}
	for k, x := range vals {
		v[k] = x
	}
	if _, ok := v[":n"]; !ok {
		v[":n"] = str(c.Name)
	}
	return types.TransactWriteItem{Update: &types.Update{
		TableName: aws.String(s.table), Key: indexKey(c.ID), UpdateExpression: aws.String(expr),
		ExpressionAttributeNames:  map[string]string{"#name": "name"},
		ExpressionAttributeValues: v,
	}}
}

func (s *dynamoStore) txError(err error, what string) error {
	if err == nil {
		return nil
	}
	if dynamo.IsConditionFailed(err) {
		return ErrNotFound
	}
	return fmt.Errorf("%s: %w", what, err)
}

func (s *dynamoStore) SetDisabled(ctx context.Context, c Client, at *time.Time) error {
	clientExpr, idxExpr := "REMOVE disabledAt", "SET #name = if_not_exists(#name, :n), rpId = if_not_exists(rpId, :r), createdAt = if_not_exists(createdAt, :c) REMOVE disabledAt"
	vals := map[string]types.AttributeValue{}
	if at != nil {
		clientExpr = "SET disabledAt = if_not_exists(disabledAt, :now)"
		idxExpr = "SET #name = if_not_exists(#name, :n), rpId = if_not_exists(rpId, :r), createdAt = if_not_exists(createdAt, :c), disabledAt = if_not_exists(disabledAt, :now)"
		vals[":now"] = ts(*at)
	}
	upd := &types.Update{
		TableName: aws.String(s.table), Key: clientKey(c.ID), UpdateExpression: aws.String(clientExpr),
		ConditionExpression: aws.String("attribute_exists(pk)"),
	}
	if len(vals) > 0 {
		upd.ExpressionAttributeValues = vals
	}
	err := dynamo.TransactWrite(ctx, s.c, types.TransactWriteItem{Update: upd}, s.indexUpdate(c, idxExpr, vals))
	return s.txError(err, "set disabled")
}

func (s *dynamoStore) SetKeys(ctx context.Context, id string, expected, hashes []string, retired map[string]time.Time) error {
	r := make(map[string]types.AttributeValue, len(retired))
	for h, exp := range retired {
		r[h] = &types.AttributeValueMemberN{Value: strconv.FormatInt(exp.Unix(), 10)}
	}
	vals := map[string]types.AttributeValue{
		":h": &types.AttributeValueMemberSS{Value: hashes},
		":r": &types.AttributeValueMemberM{Value: r},
	}
	cond := "attribute_exists(pk) AND attribute_not_exists(keyHashes)"
	if len(expected) > 0 {
		cond = "attribute_exists(pk) AND keyHashes = :old"
		vals[":old"] = &types.AttributeValueMemberSS{Value: expected}
	}
	_, err := s.c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.table), Key: clientKey(id),
		UpdateExpression:          aws.String("SET keyHashes = :h, retiredKeys = :r"),
		ConditionExpression:       aws.String(cond),
		ExpressionAttributeValues: vals,
	})
	if dynamo.IsConditionFailed(err) {
		// Either the client is gone or its keys changed; look to tell which.
		if _, gerr := s.GetClient(ctx, id); gerr != nil {
			return gerr
		}
		return ErrRotateConflict
	}
	if err != nil {
		return fmt.Errorf("set keys: %w", err)
	}
	return nil
}

func (s *dynamoStore) ClearRetiredKeys(ctx context.Context, id string, hashes []string) error {
	names := make(map[string]string, len(hashes))
	paths := make([]string, 0, len(hashes))
	for i, h := range hashes {
		ph := "#h" + strconv.Itoa(i)
		names[ph] = h
		paths = append(paths, "retiredKeys."+ph)
	}
	return s.update(ctx, id, "REMOVE "+strings.Join(paths, ", "), names, nil, "clear retired keys")
}

// update runs a conditional UpdateItem on a client item.
func (s *dynamoStore) update(ctx context.Context, id, expr string, names map[string]string, vals map[string]types.AttributeValue, what string) error {
	_, err := s.c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(s.table),
		Key:                       clientKey(id),
		UpdateExpression:          aws.String(expr),
		ConditionExpression:       aws.String("attribute_exists(pk)"),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: vals,
	})
	if dynamo.IsConditionFailed(err) {
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

// DeleteKey removes a key item; deleting a missing one succeeds.
func (s *dynamoStore) DeleteKey(ctx context.Context, hash string) error {
	if _, err := s.c.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(s.table), Key: keyKey(hash)}); err != nil {
		return fmt.Errorf("delete key: %w", err)
	}
	return nil
}

// retiredOf reads retiredKeys, skipping entries that are not a number.
func retiredOf(it map[string]types.AttributeValue) map[string]time.Time {
	m, ok := it["retiredKeys"].(*types.AttributeValueMemberM)
	if !ok {
		return nil
	}
	out := make(map[string]time.Time, len(m.Value))
	for h, v := range m.Value {
		if n, ok := v.(*types.AttributeValueMemberN); ok {
			if secs, err := strconv.ParseInt(n.Value, 10, 64); err == nil {
				out[h] = time.Unix(secs, 0)
			}
		}
	}
	return out
}

// disabledOf reads disabledAt: absent is nil, present but not an RFC 3339 string is an error.
func disabledOf(it map[string]types.AttributeValue) (*time.Time, error) {
	v, ok := it["disabledAt"]
	if !ok {
		return nil, nil
	}
	sv, ok := v.(*types.AttributeValueMemberS)
	if !ok {
		return nil, errors.New("bad disabledAt: not a string")
	}
	t, err := time.Parse(time.RFC3339, sv.Value)
	if err != nil {
		return nil, fmt.Errorf("bad disabledAt: %w", err)
	}
	return &t, nil
}

// getT is lenient: it is for the list view, where one bad item should not hide the rest.
func getT(it map[string]types.AttributeValue, k string) *time.Time {
	if t, err := time.Parse(time.RFC3339, getS(it, k)); err == nil {
		return &t
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
