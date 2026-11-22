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
	// For an owned client the same transaction writes the owner index item and
	// counts it against the owner's quota: ErrQuotaExceeded or ErrOwnerSuspended
	// when the owner may not have another client, and nothing is written.
	CreateClient(ctx context.Context, c Client) error
	// GetClient returns the client including a soft-deleted one (DeletedAt set);
	// the Service hides those.
	GetClient(ctx context.Context, id string) (Client, error)
	// ListClients returns up to limit summaries, newest first, after cursor, and
	// the next cursor (empty on the last page). A bad cursor is ErrBadCursor.
	ListClients(ctx context.Context, cursor string, limit int32) ([]Summary, string, error)
	// ListOwned is ListClients for one tenant's clients.
	ListOwned(ctx context.Context, tenantID, cursor string, limit int32) ([]Summary, string, error)
	// DeleteClient soft-deletes c in one transaction: deletedAt is set, the index
	// items go and the owner's count drops by one. A missing or already deleted
	// client is ErrNotFound and nothing changes. Key items are left to the caller.
	// A tenant (by != ByOperator) never deletes a client an operator disabled:
	// that is ErrDisabledByOperator, checked in the same transaction.
	DeleteClient(ctx context.Context, c Client, at time.Time, by DisabledBy) error
	// GetOwner returns the tenant's counters; an unknown tenant gets the defaults.
	GetOwner(ctx context.Context, tenantID string) (Owner, error)
	// SetQuota sets the tenant's client quota, creating its counters if needed.
	SetQuota(ctx context.Context, tenantID string, quota int) error
	// SetSuspended sets the tenant's suspendedAt to at, or clears it when at is nil.
	// A tenant without counters gets an item holding only that attribute.
	SetSuspended(ctx context.Context, tenantID string, at *time.Time) error
	// UpdateClient writes only the attributes set in p; a missing client is ErrNotFound.
	// c is the stored client. A name change also updates the index item.
	UpdateClient(ctx context.Context, c Client, p Patch) error
	// SetDisabled sets disabledAt to at (keeping an earlier value) and records who
	// disabled the client, or clears both when at is nil. A tenant never replaces
	// an operator's mark.
	SetDisabled(ctx context.Context, c Client, at *time.Time, by DisabledBy) error
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

// liveCond guards every write to an existing client item: a soft-deleted client
// is never written again, so a write that raced a delete cannot resurrect it.
const liveCond = "attribute_exists(pk) AND attribute_not_exists(deletedAt)"

func clientKey(id string) map[string]types.AttributeValue { return dynamo.Key("C#"+id, "META") }
func indexKey(id string) map[string]types.AttributeValue  { return dynamo.Key(indexPK, "C#"+id) }
func keyKey(hash string) map[string]types.AttributeValue  { return dynamo.Key("K#"+hash, "META") }

func str(s string) types.AttributeValue { return &types.AttributeValueMemberS{Value: s} }
func num(n int) types.AttributeValue    { return &types.AttributeValueMemberN{Value: strconv.Itoa(n)} }
func ts(t time.Time) types.AttributeValue {
	return str(t.UTC().Format(time.RFC3339))
}

func ownerIndexKey(tenantID, clientID string) map[string]types.AttributeValue {
	return dynamo.Key(ownerPK(tenantID), "C#"+clientID)
}
func ownerMetaKey(tenantID string) map[string]types.AttributeValue {
	return dynamo.Key(ownerPK(tenantID), "META")
}
func ownerPK(tenantID string) string { return "OWNER#" + tenantID }

// tenantOf returns the tenant id of an owner value ("T#<tenantId>"), or "" for an operator's client.
func tenantOf(owner string) string { return strings.TrimPrefix(owner, "T#") }

// maxTxAttempts bounds the retries of a transaction that lost to a concurrent
// one on the same item (TransactionConflict); the owner META item is shared by
// every create and delete of a tenant.
const maxTxAttempts = 8

// transact runs items as one transaction, retrying while another transaction
// holds one of the items. The cancellation reasons of the last failure are
// still in the returned error.
func (s *dynamoStore) transact(ctx context.Context, items ...types.TransactWriteItem) error {
	var err error
	for attempt := 0; attempt < maxTxAttempts; attempt++ {
		if err = dynamo.TransactWrite(ctx, s.c, items...); err == nil || !txConflict(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(attempt+1) * 10 * time.Millisecond):
		}
	}
	return err
}

func txConflict(err error) bool {
	var tx *types.TransactionCanceledException
	if !errors.As(err, &tx) {
		return false
	}
	for _, r := range tx.CancellationReasons {
		if r.Code != nil && *r.Code == "TransactionConflict" {
			return true
		}
	}
	return false
}

// failedAt reports whether the transaction was cancelled because the condition of item i failed.
func failedAt(err error, i int) bool {
	var tx *types.TransactionCanceledException
	if !errors.As(err, &tx) || i >= len(tx.CancellationReasons) {
		return false
	}
	r := tx.CancellationReasons[i]
	return r.Code != nil && *r.Code == "ConditionalCheckFailed"
}

// createCond lets an owner create a client while it is not suspended and its
// count is below its quota; an absent clientQuota means DefaultClientQuota and
// an absent clientCount means zero.
const createCond = "attribute_not_exists(suspendedAt) AND " +
	"((attribute_not_exists(clientQuota) AND (attribute_not_exists(clientCount) OR clientCount < :defaultQuota)) OR " +
	"(attribute_exists(clientQuota) AND (attribute_not_exists(clientCount) OR clientCount < clientQuota)))"

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
	if c.Owner != "" {
		item["owner"] = str(c.Owner)
	}
	if len(c.KeyHashes) > 0 {
		item["keyHashes"] = &types.AttributeValueMemberSS{Value: c.KeyHashes}
	}
	idx := indexKey(c.ID)
	idx["name"] = str(c.Name)
	idx["rpId"] = str(c.RPID)
	idx["createdAt"] = ts(c.CreatedAt)
	if c.Owner != "" {
		idx["owner"] = str(c.Owner)
	}
	items := []types.TransactWriteItem{
		{Put: &types.Put{TableName: aws.String(s.table), Item: item, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
		{Put: &types.Put{TableName: aws.String(s.table), Item: idx}},
	}
	metaAt := -1
	if tenant := tenantOf(c.Owner); tenant != "" {
		oidx := ownerIndexKey(tenant, c.ID)
		oidx["name"] = str(c.Name)
		oidx["rpId"] = str(c.RPID)
		oidx["createdAt"] = ts(c.CreatedAt)
		metaAt = len(items) + 1
		items = append(items,
			types.TransactWriteItem{Put: &types.Put{TableName: aws.String(s.table), Item: oidx}},
			types.TransactWriteItem{Update: &types.Update{
				TableName: aws.String(s.table), Key: ownerMetaKey(tenant),
				UpdateExpression:    aws.String("SET clientCount = if_not_exists(clientCount, :zero) + :one"),
				ConditionExpression: aws.String(createCond),
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":zero": num(0), ":one": num(1), ":defaultQuota": num(DefaultClientQuota),
				},
			}},
		)
	}
	if err := s.transact(ctx, items...); err != nil {
		if metaAt >= 0 && failedAt(err, metaAt) {
			// Say which rule refused: a suspended owner, or a full quota.
			o, gerr := s.GetOwner(ctx, tenantOf(c.Owner))
			if gerr != nil {
				return gerr
			}
			if o.SuspendedAt != nil {
				return ErrOwnerSuspended
			}
			return ErrQuotaExceeded
		}
		return fmt.Errorf("create client: %w", err)
	}
	return nil
}

// DeleteClient: see Store.
func (s *dynamoStore) DeleteClient(ctx context.Context, c Client, at time.Time, by DisabledBy) error {
	cond := "attribute_exists(pk) AND attribute_not_exists(deletedAt)"
	vals := map[string]types.AttributeValue{":at": ts(at)}
	if by != ByOperator {
		// A tenant's delete must not remove a client an operator disabled, even one disabled after the caller's read.
		cond += " AND (attribute_not_exists(disabledBy) OR disabledBy <> :op)"
		vals[":op"] = str(string(ByOperator))
	}
	items := []types.TransactWriteItem{
		{Update: &types.Update{
			TableName: aws.String(s.table), Key: clientKey(c.ID),
			UpdateExpression:          aws.String("SET deletedAt = :at"),
			ConditionExpression:       aws.String(cond),
			ExpressionAttributeValues: vals,
		}},
		{Delete: &types.Delete{TableName: aws.String(s.table), Key: indexKey(c.ID)}},
	}
	if tenant := tenantOf(c.Owner); tenant != "" {
		items = append(items,
			types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(s.table), Key: ownerIndexKey(tenant, c.ID)}},
			types.TransactWriteItem{Update: &types.Update{
				TableName: aws.String(s.table), Key: ownerMetaKey(tenant),
				UpdateExpression:          aws.String("SET clientCount = clientCount - :one"),
				ConditionExpression:       aws.String("clientCount > :zero"),
				ExpressionAttributeValues: map[string]types.AttributeValue{":zero": num(0), ":one": num(1)},
			}},
		)
	}
	err := s.transact(ctx, items...)
	if by != ByOperator && dynamo.IsConditionFailed(err) {
		// Gone, or an operator's mark is there: look to tell which.
		if cur, gerr := s.GetClient(ctx, c.ID); gerr == nil && cur.DeletedAt == nil && cur.DisabledBy == string(ByOperator) {
			return ErrDisabledByOperator
		}
	}
	return s.txError(err, "delete client")
}

func (s *dynamoStore) GetOwner(ctx context.Context, tenantID string) (Owner, error) {
	out, err := s.c.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(s.table), Key: ownerMetaKey(tenantID), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return Owner{}, fmt.Errorf("get owner: %w", err)
	}
	o := Owner{TenantID: tenantID, ClientQuota: DefaultClientQuota}
	if out.Item == nil {
		return o, nil
	}
	o.ClientCount = getN(out.Item, "clientCount")
	if _, ok := out.Item["clientQuota"]; ok {
		o.ClientQuota = getN(out.Item, "clientQuota")
	}
	if _, ok := out.Item["suspendedAt"]; ok {
		// Present but unreadable still means suspended.
		t := getT(out.Item, "suspendedAt")
		if t == nil {
			t = &time.Time{}
		}
		o.SuspendedAt = t
	}
	return o, nil
}

func (s *dynamoStore) SetQuota(ctx context.Context, tenantID string, quota int) error {
	// clientCount is created with the quota so the create condition never sees a
	// quota without a count (a quota of 0 must refuse the first create).
	_, err := s.c.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.table), Key: ownerMetaKey(tenantID),
		UpdateExpression:          aws.String("SET clientQuota = :q, clientCount = if_not_exists(clientCount, :zero)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":q": num(quota), ":zero": num(0)},
	})
	if err != nil {
		return fmt.Errorf("set quota: %w", err)
	}
	return nil
}

func (s *dynamoStore) ListClients(ctx context.Context, cursor string, limit int32) ([]Summary, string, error) {
	return s.listIndex(ctx, indexPK, cursor, limit)
}

func (s *dynamoStore) ListOwned(ctx context.Context, tenantID, cursor string, limit int32) ([]Summary, string, error) {
	return s.listIndex(ctx, ownerPK(tenantID), cursor, limit)
}

// listIndex pages one index partition (CLIENTS or OWNER#<tenantId>), newest first.
func (s *dynamoStore) listIndex(ctx context.Context, pk, cursor string, limit int32) ([]Summary, string, error) {
	in := &dynamodb.QueryInput{
		TableName:                 aws.String(s.table),
		KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :c)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":p": str(pk), ":c": str("C#")},
		ScanIndexForward:          aws.Bool(false),
		Limit:                     aws.Int32(limit + 1), // one extra tells whether a next page exists
		ConsistentRead:            aws.Bool(true),
	}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || id.Validate(string(raw)) != nil {
			return nil, "", ErrBadCursor
		}
		in.ExclusiveStartKey = dynamo.Key(pk, "C#"+string(raw))
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
		sm := Summary{ID: strings.TrimPrefix(getS(it, "sk"), "C#"), Name: getS(it, "name"), RPID: getS(it, "rpId"), Owner: getS(it, "owner")}
		if tenant, ok := strings.CutPrefix(pk, "OWNER#"); ok {
			sm.Owner = OwnerOf(tenant) // the owner index item does not repeat it
		}
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
	// A deletedAt that is present but unreadable must not mean "live".
	deleted, err := timeOf(it, "deletedAt")
	if err != nil {
		return Client{}, fmt.Errorf("client %s: %w", id, err)
	}
	return Client{
		ID:      id,
		Owner:   getS(it, "owner"),
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
		DisabledBy:  getS(it, "disabledBy"),
		DeletedAt:   deleted,
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
	items := []types.TransactWriteItem{{Update: &types.Update{
		TableName: aws.String(s.table), Key: clientKey(c.ID), UpdateExpression: aws.String(expr),
		ConditionExpression:       aws.String(liveCond),
		ExpressionAttributeNames:  names,
		ExpressionAttributeValues: vals,
	}}}
	items = append(items, s.indexUpdates(c, "SET #name = :n, rpId = if_not_exists(rpId, :r), createdAt = if_not_exists(createdAt, :c)", map[string]types.AttributeValue{":n": str(*p.Name)})...)
	return s.txError(s.transact(ctx, items...), "update client")
}

// indexUpdates builds the writes to the client's index items: the CLIENTS item
// and, for an owned client, its OWNER# item. They always set name, rpId and
// createdAt when missing, so a client created before the index existed still
// gets a complete item. expr must contain the name assignment.
func (s *dynamoStore) indexUpdates(c Client, expr string, vals map[string]types.AttributeValue) []types.TransactWriteItem {
	v := map[string]types.AttributeValue{":r": str(c.RPID), ":c": ts(c.CreatedAt)}
	for k, x := range vals {
		v[k] = x
	}
	if _, ok := v[":n"]; !ok {
		v[":n"] = str(c.Name)
	}
	upd := func(key map[string]types.AttributeValue) types.TransactWriteItem {
		return types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(s.table), Key: key, UpdateExpression: aws.String(expr),
			ExpressionAttributeNames:  map[string]string{"#name": "name"},
			ExpressionAttributeValues: v,
		}}
	}
	out := []types.TransactWriteItem{upd(indexKey(c.ID))}
	if tenant := tenantOf(c.Owner); tenant != "" {
		out = append(out, upd(ownerIndexKey(tenant, c.ID)))
	}
	return out
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

func (s *dynamoStore) SetDisabled(ctx context.Context, c Client, at *time.Time, by DisabledBy) error {
	if by != ByOperator && by != ByTenant {
		return fmt.Errorf("%w: disabledBy %q", ErrInvalid, by)
	}
	clientExpr, idxExpr := "REMOVE disabledAt, disabledBy", "SET #name = if_not_exists(#name, :n), rpId = if_not_exists(rpId, :r), createdAt = if_not_exists(createdAt, :c) REMOVE disabledAt"
	vals := map[string]types.AttributeValue{}
	cond := liveCond
	// Values only the client item uses: DynamoDB rejects unused values on the index items.
	clientVals := map[string]types.AttributeValue{}
	if at != nil {
		byExpr := ":by"
		if by != ByOperator {
			byExpr = "if_not_exists(disabledBy, :by)" // a tenant's disable never replaces an operator's
		}
		clientExpr = "SET disabledAt = if_not_exists(disabledAt, :now), disabledBy = " + byExpr
		idxExpr = "SET #name = if_not_exists(#name, :n), rpId = if_not_exists(rpId, :r), createdAt = if_not_exists(createdAt, :c), disabledAt = if_not_exists(disabledAt, :now)"
		vals[":now"] = ts(*at)
		clientVals[":by"] = str(string(by))
	} else if by != ByOperator {
		// A tenant's enable must not erase an operator's disable that landed after the caller's read.
		cond += " AND (attribute_not_exists(disabledBy) OR disabledBy <> :op)"
		clientVals[":op"] = str(string(ByOperator))
	}
	upd := &types.Update{
		TableName: aws.String(s.table), Key: clientKey(c.ID), UpdateExpression: aws.String(clientExpr),
		ConditionExpression: aws.String(cond),
	}
	if len(vals)+len(clientVals) > 0 {
		cv := map[string]types.AttributeValue{}
		for k, v := range vals {
			cv[k] = v
		}
		for k, v := range clientVals {
			cv[k] = v
		}
		upd.ExpressionAttributeValues = cv
	}
	items := append([]types.TransactWriteItem{{Update: upd}}, s.indexUpdates(c, idxExpr, vals)...)
	err := s.transact(ctx, items...)
	if at == nil && by != ByOperator && dynamo.IsConditionFailed(err) {
		// Gone, or an operator's mark is there: look to tell which.
		if cur, gerr := s.GetClient(ctx, c.ID); gerr == nil && cur.DeletedAt == nil && cur.DisabledBy == string(ByOperator) {
			return ErrDisabledByOperator
		}
	}
	return s.txError(err, "set disabled")
}

func (s *dynamoStore) SetSuspended(ctx context.Context, tenantID string, at *time.Time) error {
	in := &dynamodb.UpdateItemInput{
		TableName: aws.String(s.table), Key: ownerMetaKey(tenantID),
		UpdateExpression: aws.String("REMOVE suspendedAt"),
	}
	if at != nil {
		in.UpdateExpression = aws.String("SET suspendedAt = :at")
		in.ExpressionAttributeValues = map[string]types.AttributeValue{":at": ts(*at)}
	}
	if _, err := s.c.UpdateItem(ctx, in); err != nil {
		return fmt.Errorf("set suspended: %w", err)
	}
	return nil
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
	cond := liveCond + " AND attribute_not_exists(keyHashes)"
	if len(expected) > 0 {
		cond = liveCond + " AND keyHashes = :old"
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
		if c, gerr := s.GetClient(ctx, id); gerr != nil {
			return gerr
		} else if c.DeletedAt != nil {
			return ErrNotFound
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
		ConditionExpression:       aws.String(liveCond),
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
	return timeOf(it, "disabledAt")
}

func timeOf(it map[string]types.AttributeValue, k string) (*time.Time, error) {
	v, ok := it[k]
	if !ok {
		return nil, nil
	}
	sv, ok := v.(*types.AttributeValueMemberS)
	if !ok {
		return nil, fmt.Errorf("bad %s: not a string", k)
	}
	t, err := time.Parse(time.RFC3339, sv.Value)
	if err != nil {
		return nil, fmt.Errorf("bad %s: %w", k, err)
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
