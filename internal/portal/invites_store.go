package portal

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
	maxPendingInvites = 20
	inviteTTL         = 7 * 24 * time.Hour
)

var (
	// ErrInvitesFull means the organisation already has the most pending invites allowed.
	ErrInvitesFull = errors.New("portal: too many pending invites")
	// ErrMembersFull means the organisation already has the most members allowed.
	ErrMembersFull = errors.New("portal: organisation is full")
	// ErrAlreadyInvited means a live invite to the address exists.
	ErrAlreadyInvited = errors.New("portal: already invited")
	// ErrAlreadyMember means the user is already a member of the organisation.
	ErrAlreadyMember = errors.New("portal: already a member")
	// ErrInviteNotFound means the invite is absent, expired, spent, or for another address.
	ErrInviteNotFound = errors.New("portal: invite not found")
)

// Invite is a pending invitation. Email is normalised, ID is a ULID, and
// TokenHash is the hex SHA-256 of the token in the link; the token itself is
// never stored.
type Invite struct {
	ID, TenantID, Email, Role, InvitedBy, TokenHash string
	CreatedAt, ExpiresAt                            time.Time
}

func inviteKey(tenantID, hash string) item {
	return item{"pk": s("TENANT#" + tenantID), "sk": s("INVITE#" + hash)}
}

func inviteeKey(tenantID, email string) item {
	return item{"pk": s("TENANT#" + tenantID), "sk": s("INVITEE#" + email)}
}

func inviteItem(inv Invite) item {
	it := inviteKey(inv.TenantID, inv.TokenHash)
	it["inviteId"], it["email"], it["role"], it["invitedBy"] = s(inv.ID), s(inv.Email), s(inv.Role), s(inv.InvitedBy)
	it["createdAt"], it["expiresAt"] = s(ts(inv.CreatedAt)), s(ts(inv.ExpiresAt))
	return it
}

func decodeInvite(tenantID string, it item) (Invite, error) {
	created, err := getT(it, "createdAt")
	if err != nil || created == nil {
		return Invite{}, fmt.Errorf("invite createdAt: %v", err)
	}
	expires, err := getT(it, "expiresAt")
	if err != nil || expires == nil {
		return Invite{}, fmt.Errorf("invite expiresAt: %v", err)
	}
	return Invite{
		ID: getS(it, "inviteId"), TenantID: tenantID, Email: getS(it, "email"), Role: getS(it, "role"),
		InvitedBy: getS(it, "invitedBy"), TokenHash: getS(it, "sk")[len("INVITE#"):],
		CreatedAt: *created, ExpiresAt: *expires,
	}, nil
}

// invitesOf reads every invite item of a tenant, consistently.
func (st *Store) invitesOf(ctx context.Context, tenantID string) ([]Invite, error) {
	var all []Invite
	var start item
	for {
		out, err := st.DB.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
			KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :i)"),
			ExpressionAttributeValues: item{":p": s("TENANT#" + tenantID), ":i": s("INVITE#")},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("list invites: %w", err)
		}
		for _, it := range out.Items {
			inv, err := decodeInvite(tenantID, it)
			if err != nil {
				return nil, err
			}
			all = append(all, inv)
		}
		if len(out.LastEvaluatedKey) == 0 {
			return all, nil
		}
		start = out.LastEvaluatedKey
	}
}

func (st *Store) findInvite(ctx context.Context, tenantID, inviteID string) (Invite, error) {
	all, err := st.invitesOf(ctx, tenantID)
	if err != nil {
		return Invite{}, err
	}
	for _, inv := range all {
		if inv.ID == inviteID {
			return inv, nil
		}
	}
	return Invite{}, ErrInviteNotFound
}

// pruneInvites frees the slots of expired invites. They carry no DynamoDB ttl:
// TTL would delete them without counting inviteCount down, so this is the only
// remover (DeleteAllInvites covers an abandoned tenant). Each goes in its own transaction: delete the invite, delete its
// invitee item (only while that item still holds this invite's hash, so a
// newer invite to the address is left alone) and count one down. An invite
// someone else pruned first is skipped, so the count is never taken down twice.
func (st *Store) pruneInvites(ctx context.Context, tenantID string, now time.Time) error {
	all, err := st.invitesOf(ctx, tenantID)
	if err != nil {
		return err
	}
	for _, inv := range all {
		if inv.ExpiresAt.After(now) {
			continue
		}
		err := st.dropInvite(ctx, inv, true)
		if errors.Is(err, ErrInviteNotFound) {
			continue
		}
		if err != nil {
			return fmt.Errorf("prune invites: %w", err)
		}
	}
	return nil
}

// dropInvite deletes an invite, its invitee item and one from inviteCount. If
// the invitee item no longer holds this invite's hash and tolerateNewer is
// set, it is left alone and the other two still go. An invite already gone
// gives ErrInviteNotFound.
func (st *Store) dropInvite(ctx context.Context, inv Invite, tolerateNewer bool) error {
	build := func(withInvitee bool) []types.TransactWriteItem {
		items := []types.TransactWriteItem{
			{Delete: &types.Delete{TableName: aws.String(st.Table), Key: inviteKey(inv.TenantID, inv.TokenHash), ConditionExpression: aws.String(exists)}},
			{Update: &types.Update{
				TableName: aws.String(st.Table), Key: tenantKey(inv.TenantID),
				UpdateExpression:          aws.String("ADD inviteCount :neg"),
				ConditionExpression:       aws.String("inviteCount > :zero"),
				ExpressionAttributeValues: item{":neg": n(-1), ":zero": n(0)},
			}},
		}
		if withInvitee {
			items = append(items, types.TransactWriteItem{Delete: &types.Delete{
				TableName: aws.String(st.Table), Key: inviteeKey(inv.TenantID, inv.Email),
				ConditionExpression:       aws.String("tokenHash = :h"),
				ExpressionAttributeValues: item{":h": s(inv.TokenHash)},
			}})
		}
		return items
	}
	err := txWrite(ctx, st, build(true)...)
	if dynamo.IsConditionFailed(err) && tolerateNewer {
		err = txWrite(ctx, st, build(false)...)
	}
	if dynamo.IsConditionFailed(err) {
		got, gerr := st.getRaw(ctx, inviteKey(inv.TenantID, inv.TokenHash))
		switch {
		case gerr != nil:
			return gerr
		case got == nil:
			return ErrInviteNotFound
		}
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("drop invite: %w", err)
	}
	return nil
}

// getRaw reads one item consistently; an absent one is nil.
func (st *Store) getRaw(ctx context.Context, key item) (item, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(st.Table), Key: key, ConsistentRead: aws.Bool(true)})
	if err != nil {
		return nil, fmt.Errorf("get item: %w", err)
	}
	return out.Item, nil
}

// CreateInvite stores a pending invite. It frees expired invites first, then
// in one transaction puts the invite, claims the address (free, or held by an
// expired invite) and counts one up while the organisation has room. The
// failure says why: ErrTenantDeleting, ErrPersonal, ErrInvitesFull or ErrAlreadyInvited.
func (st *Store) CreateInvite(ctx context.Context, inv Invite, now time.Time) error {
	inv.Email = NormalizeEmail(inv.Email)
	inv.CreatedAt, inv.ExpiresAt = now, now.Add(inviteTTL)
	if err := st.pruneInvites(ctx, inv.TenantID, now); err != nil {
		return err
	}
	invitee := inviteeKey(inv.TenantID, inv.Email)
	invitee["tokenHash"], invitee["expiresAt"] = s(inv.TokenHash), s(ts(inv.ExpiresAt))
	err := txWrite(ctx, st,
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: inviteItem(inv)}},
		types.TransactWriteItem{Put: &types.Put{
			TableName: aws.String(st.Table), Item: invitee,
			ConditionExpression:       aws.String("attribute_not_exists(pk) OR expiresAt < :now"),
			ExpressionAttributeValues: item{":now": s(ts(now))},
		}},
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: tenantKey(inv.TenantID),
			UpdateExpression:          aws.String("ADD inviteCount :one"),
			ConditionExpression:       aws.String("kind = :org AND attribute_not_exists(deletingAt) AND inviteCount < :max"),
			ExpressionAttributeValues: item{":one": n(1), ":org": s(KindOrg), ":max": n(maxPendingInvites)},
		}})
	if dynamo.IsConditionFailed(err) {
		t, terr := st.orgTenant(ctx, inv.TenantID)
		if terr != nil {
			return terr
		}
		cur, gerr := st.getRaw(ctx, inviteeKey(inv.TenantID, inv.Email))
		if gerr != nil {
			return gerr
		}
		if cur != nil && getS(cur, "expiresAt") >= ts(now) {
			return ErrAlreadyInvited
		}
		if t.Counts.Invites >= maxPendingInvites {
			return ErrInvitesFull
		}
		return ErrConflict
	}
	if err != nil {
		return fmt.Errorf("create invite: %w", err)
	}
	return nil
}

// ListInvites returns the live invites, newest first, after freeing expired ones.
func (st *Store) ListInvites(ctx context.Context, tenantID string, now time.Time) ([]Invite, error) {
	if err := st.pruneInvites(ctx, tenantID, now); err != nil {
		return nil, err
	}
	all, err := st.invitesOf(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	live := all[:0]
	for _, inv := range all {
		if inv.ExpiresAt.After(now) {
			live = append(live, inv)
		}
	}
	sort.SliceStable(live, func(i, j int) bool {
		if !live[i].CreatedAt.Equal(live[j].CreatedAt) {
			return live[i].CreatedAt.After(live[j].CreatedAt)
		}
		return live[i].ID > live[j].ID
	})
	return live, nil
}

// ResendInvite gives an invite a new token and a new seven days. The old token
// stops working in the same transaction. The count is unchanged.
func (st *Store) ResendInvite(ctx context.Context, tenantID, inviteID, newHash string, now time.Time) (Invite, error) {
	if _, err := st.orgTenant(ctx, tenantID); err != nil {
		return Invite{}, err
	}
	old, err := st.findInvite(ctx, tenantID, inviteID)
	if err != nil {
		return Invite{}, err
	}
	next := old
	next.TokenHash, next.ExpiresAt = newHash, now.Add(inviteTTL)
	err = txWrite(ctx, st,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(st.Table), Key: inviteKey(tenantID, old.TokenHash), ConditionExpression: aws.String(exists),
		}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: inviteItem(next)}},
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: inviteeKey(tenantID, old.Email),
			UpdateExpression:          aws.String("SET tokenHash = :new, expiresAt = :exp"),
			ConditionExpression:       aws.String("tokenHash = :old"),
			ExpressionAttributeValues: item{":new": s(newHash), ":exp": s(ts(next.ExpiresAt)), ":old": s(old.TokenHash)},
		}})
	if dynamo.IsConditionFailed(err) {
		if _, ferr := st.findInvite(ctx, tenantID, inviteID); ferr != nil {
			return Invite{}, ferr
		}
		return Invite{}, ErrConflict
	}
	if err != nil {
		return Invite{}, fmt.Errorf("resend invite: %w", err)
	}
	return next, nil
}

// RevokeInvite deletes an invite by ID and returns it.
func (st *Store) RevokeInvite(ctx context.Context, tenantID, inviteID string) (Invite, error) {
	inv, err := st.findInvite(ctx, tenantID, inviteID)
	if err != nil {
		return Invite{}, err
	}
	if err := st.dropInvite(ctx, inv, false); err != nil {
		return Invite{}, err
	}
	return inv, nil
}

// DeclineInvite deletes the invite its invitee turned down.
func (st *Store) DeclineInvite(ctx context.Context, inv Invite) error {
	return st.dropInvite(ctx, inv, false)
}

// PeekInvite reads an unexpired invite by its token hash without spending it.
func (st *Store) PeekInvite(ctx context.Context, tenantID, tokenHash string, now time.Time) (Invite, error) {
	out, err := st.DB.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(st.Table), Key: inviteKey(tenantID, tokenHash), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return Invite{}, fmt.Errorf("peek invite: %w", err)
	}
	if out.Item == nil {
		return Invite{}, ErrInviteNotFound
	}
	inv, err := decodeInvite(tenantID, out.Item)
	if err != nil {
		return Invite{}, err
	}
	if !inv.ExpiresAt.After(now) {
		return Invite{}, ErrInviteNotFound
	}
	return inv, nil
}

// AcceptInvite spends an invite for u: it deletes the invite and its invitee
// item, moves the tenant's counters (members up, invites down, owners up for
// an owner invite) and writes the membership pair, in one transaction. u must
// have the invited address, else ErrInviteNotFound. Other failures give
// ErrMembersFull, ErrTenantDeleting, ErrPersonal or ErrAlreadyMember.
func (st *Store) AcceptInvite(ctx context.Context, inv Invite, u User, now time.Time) error {
	if NormalizeEmail(u.Email) != inv.Email {
		return ErrInviteNotFound
	}
	expr, vals := "ADD memberCount :one, inviteCount :neg", item{":one": n(1), ":neg": n(-1), ":max": n(maxMembers), ":org": s(KindOrg)}
	if inv.Role == RoleOwner {
		expr += ", ownerCount :one"
	}
	mi, umi := memberKey(inv.TenantID, u.ID), userTenantKey(u.ID, inv.TenantID)
	for _, it := range []item{mi, umi} {
		it["role"], it["joinedAt"] = s(inv.Role), s(ts(now))
	}
	err := txWrite(ctx, st,
		types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(st.Table), Key: inviteKey(inv.TenantID, inv.TokenHash),
			ConditionExpression:       aws.String(exists + " AND expiresAt > :now AND email = :e AND #role = :r"),
			ExpressionAttributeNames:  map[string]string{"#role": "role"},
			ExpressionAttributeValues: item{":now": s(ts(now)), ":e": s(inv.Email), ":r": s(inv.Role)},
		}},
		types.TransactWriteItem{Delete: &types.Delete{TableName: aws.String(st.Table), Key: inviteeKey(inv.TenantID, inv.Email)}},
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: tenantKey(inv.TenantID),
			UpdateExpression:          aws.String(expr),
			ConditionExpression:       aws.String("kind = :org AND memberCount < :max AND attribute_not_exists(deletingAt)"),
			ExpressionAttributeValues: vals,
		}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: mi, ConditionExpression: aws.String("attribute_not_exists(pk)")}},
		types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: umi}})
	if dynamo.IsConditionFailed(err) {
		return st.explainAcceptFailure(ctx, inv, u, now)
	}
	if err != nil {
		return fmt.Errorf("accept invite: %w", err)
	}
	return nil
}

// explainAcceptFailure re-reads after a failed accept to say which condition failed.
func (st *Store) explainAcceptFailure(ctx context.Context, inv Invite, u User, now time.Time) error {
	stored, err := st.PeekInvite(ctx, inv.TenantID, inv.TokenHash, now)
	if err != nil {
		return err
	}
	if stored.Email != inv.Email || stored.Role != inv.Role {
		return ErrInviteNotFound
	}
	t, err := st.orgTenant(ctx, inv.TenantID)
	if err != nil {
		return err
	}
	member, err := st.getRaw(ctx, memberKey(inv.TenantID, u.ID))
	if err != nil {
		return err
	}
	if member != nil {
		return ErrAlreadyMember
	}
	if t.Counts.Members >= maxMembers {
		return ErrMembersFull
	}
	return ErrConflict
}

// DeleteAllInvites removes every invite and invitee item of a tenant, without
// touching its counters. Organisation deletion uses it.
func (st *Store) DeleteAllInvites(ctx context.Context, tenantID string) error {
	for _, prefix := range []string{"INVITE#", "INVITEE#"} {
		var start item
		for {
			out, err := st.DB.Query(ctx, &dynamodb.QueryInput{
				TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
				KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :i)"),
				ExpressionAttributeValues: item{":p": s("TENANT#" + tenantID), ":i": s(prefix)},
				ExclusiveStartKey:         start,
			})
			if err != nil {
				return fmt.Errorf("delete invites: %w", err)
			}
			for _, it := range out.Items {
				key := item{"pk": it["pk"], "sk": it["sk"]}
				if _, err := st.DB.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(st.Table), Key: key}); err != nil {
					return fmt.Errorf("delete invites: %w", err)
				}
			}
			if len(out.LastEvaluatedKey) == 0 {
				break
			}
			start = out.LastEvaluatedKey
		}
	}
	return nil
}
