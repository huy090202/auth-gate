package portal

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
)

const (
	maxOrgsCreated = 3
	maxMembers     = 20
)

var (
	// ErrOrgQuota means the person has already created the most organisations allowed.
	ErrOrgQuota = errors.New("portal: organisation quota reached")
	// ErrLastOwner means the change would leave an organisation without an owner.
	ErrLastOwner = errors.New("portal: last owner")
	// ErrPersonal means the workspace is personal, which has no members to manage.
	ErrPersonal = errors.New("portal: personal workspace")
	// ErrTenantDeleting means the organisation is being deleted.
	ErrTenantDeleting = errors.New("portal: tenant is being deleted")
)

// Member is a user's membership in an organisation, with the user's profile.
type Member struct {
	UserID, DisplayName, Email, Role string
	JoinedAt                         time.Time
}

// OrgCounts are the counters kept on the tenant item.
type OrgCounts struct{ Members, Owners, Invites int }

// txWrite is dynamo.TransactWrite, retried when DynamoDB cancels the
// transaction for touching an item another one is writing. Still contended
// after the retries, it gives ErrConflict.
func txWrite(ctx context.Context, st *Store, items ...types.TransactWriteItem) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = dynamo.TransactWrite(ctx, st.DB, items...); !isTxConflict(err) {
			return err
		}
		time.Sleep(time.Duration(i+1) * 5 * time.Millisecond)
	}
	return fmt.Errorf("%w: %v", ErrConflict, err)
}

func isTxConflict(err error) bool {
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

// CreateOrg makes an organisation owned by u, in one transaction with the
// user's orgsCreated count. A person at the quota gives ErrOrgQuota.
func (st *Store) CreateOrg(ctx context.Context, u User, tenantID, name string, now time.Time) (Tenant, error) {
	ti := tenantKey(tenantID)
	ti["tenantId"], ti["kind"], ti["name"], ti["createdBy"], ti["createdAt"] = s(tenantID), s(KindOrg), s(name), s(u.ID), s(ts(now))
	ti["memberCount"], ti["ownerCount"], ti["inviteCount"] = n(1), n(1), n(0)
	idx := tenantIndexKey(tenantID)
	idx["kind"], idx["name"], idx["createdAt"] = s(KindOrg), s(name), s(ts(now))
	mi := memberKey(tenantID, u.ID)
	mi["role"], mi["joinedAt"] = s(RoleOwner), s(ts(now))
	umi := userTenantKey(u.ID, tenantID)
	umi["role"], umi["joinedAt"] = s(RoleOwner), s(ts(now))
	put := func(it item) types.TransactWriteItem {
		return types.TransactWriteItem{Put: &types.Put{TableName: aws.String(st.Table), Item: it}}
	}
	err := txWrite(ctx, st,
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: userKey(u.ID),
			UpdateExpression:          aws.String("ADD orgsCreated :one"),
			ConditionExpression:       aws.String(exists + " AND (attribute_not_exists(orgsCreated) OR orgsCreated < :max)"),
			ExpressionAttributeValues: item{":one": n(1), ":max": n(maxOrgsCreated)},
		}},
		put(ti), put(idx), put(mi), put(umi))
	if dynamo.IsConditionFailed(err) {
		if _, gerr := st.GetUser(ctx, u.ID); gerr != nil {
			return Tenant{}, gerr
		}
		return Tenant{}, ErrOrgQuota
	}
	if err != nil {
		return Tenant{}, fmt.Errorf("create org: %w", err)
	}
	return Tenant{ID: tenantID, Kind: KindOrg, Name: name, CreatedBy: u.ID, CreatedAt: now, Counts: OrgCounts{Members: 1, Owners: 1}}, nil
}

// orgTenant reads a tenant that a member or name change may touch: a personal
// one gives ErrPersonal and one being deleted gives ErrTenantDeleting.
func (st *Store) orgTenant(ctx context.Context, tenantID string) (Tenant, error) {
	t, err := st.GetTenant(ctx, tenantID)
	if err != nil {
		return Tenant{}, err
	}
	if t.Kind != KindOrg {
		return Tenant{}, ErrPersonal
	}
	if t.DeletingAt != nil {
		return Tenant{}, ErrTenantDeleting
	}
	return t, nil
}

// RenameOrg changes an organisation's name on the tenant and its index item.
func (st *Store) RenameOrg(ctx context.Context, tenantID, name string) error {
	if _, err := st.orgTenant(ctx, tenantID); err != nil {
		return err
	}
	names := map[string]string{"#n": "name"}
	err := txWrite(ctx, st,
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: tenantKey(tenantID),
			UpdateExpression:          aws.String("SET #n = :n"),
			ConditionExpression:       aws.String("kind = :org AND attribute_not_exists(deletingAt)"),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: item{":n": s(name), ":org": s(KindOrg)},
		}},
		types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: tenantIndexKey(tenantID),
			UpdateExpression:          aws.String("SET #n = :n"),
			ConditionExpression:       aws.String(exists),
			ExpressionAttributeNames:  names,
			ExpressionAttributeValues: item{":n": s(name)},
		}})
	if dynamo.IsConditionFailed(err) {
		return st.explainOrgConflict(ctx, tenantID, false)
	}
	if err != nil {
		return fmt.Errorf("rename org: %w", err)
	}
	return nil
}

// explainOrgConflict turns a failed condition into the error that says why:
// the organisation is being deleted, the change would remove the last owner,
// or (otherwise) something moved under the caller.
func (st *Store) explainOrgConflict(ctx context.Context, tenantID string, losesOwner bool) error {
	t, err := st.GetTenant(ctx, tenantID)
	switch {
	case err != nil:
		return err
	case t.Kind != KindOrg:
		return ErrPersonal
	case t.DeletingAt != nil:
		return ErrTenantDeleting
	case losesOwner && t.Counts.Owners <= 1:
		return ErrLastOwner
	}
	return ErrConflict
}

// ListMembers returns the members with their profiles, owners first, then
// admins, then members, each group by display name. A membership whose user is
// gone is skipped.
func (st *Store) ListMembers(ctx context.Context, tenantID string) ([]Member, error) {
	var all []Member
	var start item
	for {
		out, err := st.DB.Query(ctx, &dynamodb.QueryInput{
			TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
			KeyConditionExpression:    aws.String("pk = :p AND begins_with(sk, :m)"),
			ExpressionAttributeValues: item{":p": s("TENANT#" + tenantID), ":m": s("MEMBER#")},
			ExclusiveStartKey:         start,
		})
		if err != nil {
			return nil, fmt.Errorf("list members: %w", err)
		}
		for _, it := range out.Items {
			uid := strings.TrimPrefix(getS(it, "sk"), "MEMBER#")
			m, err := decodeMembership(tenantID, uid, it)
			if err != nil {
				return nil, err
			}
			u, err := st.GetUser(ctx, uid)
			if errors.Is(err, ErrUserNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			all = append(all, Member{UserID: uid, DisplayName: u.DisplayName, Email: u.Email, Role: m.Role, JoinedAt: m.JoinedAt})
		}
		if len(out.LastEvaluatedKey) == 0 {
			break
		}
		start = out.LastEvaluatedKey
	}
	rank := map[string]int{RoleOwner: 0, RoleAdmin: 1, RoleMember: 2}
	sort.SliceStable(all, func(i, j int) bool {
		if ri, rj := rank[all[i].Role], rank[all[j].Role]; ri != rj {
			return ri < rj
		}
		return strings.ToLower(all[i].DisplayName) < strings.ToLower(all[j].DisplayName)
	})
	return all, nil
}

// SetRole moves a member to role and returns the role they had. The write also
// holds only while the actor still has the role the caller saw, and while the
// change leaves at least one owner.
func (st *Store) SetRole(ctx context.Context, tenantID string, actor Membership, userID, role string) (string, error) {
	if _, err := st.orgTenant(ctx, tenantID); err != nil {
		return "", err
	}
	target, err := st.GetMembership(ctx, tenantID, userID)
	if err != nil {
		return "", err
	}
	old := target.Role
	if old == role {
		return old, nil
	}
	delta := 0
	if role == RoleOwner {
		delta = 1
	} else if old == RoleOwner {
		delta = -1
	}
	setRole := func(key item) types.TransactWriteItem {
		return types.TransactWriteItem{Update: &types.Update{
			TableName: aws.String(st.Table), Key: key,
			UpdateExpression:          aws.String("SET #r = :new"),
			ConditionExpression:       aws.String("#r = :old"),
			ExpressionAttributeNames:  map[string]string{"#r": "role"},
			ExpressionAttributeValues: item{":new": s(role), ":old": s(old)},
		}}
	}
	err = txWrite(ctx, st, st.orgWrites(tenantID, actor, userID, delta, 0,
		setRole(memberKey(tenantID, userID)), setRole(userTenantKey(userID, tenantID)))...)
	if dynamo.IsConditionFailed(err) {
		return "", st.explainOrgConflict(ctx, tenantID, delta < 0)
	}
	if err != nil {
		return "", fmt.Errorf("set role: %w", err)
	}
	return old, nil
}

// RemoveMember deletes a membership and returns it as it was. Leaving is the
// actor removing themselves. Removing the last owner gives ErrLastOwner.
func (st *Store) RemoveMember(ctx context.Context, tenantID string, actor Membership, userID string) (Membership, error) {
	if _, err := st.orgTenant(ctx, tenantID); err != nil {
		return Membership{}, err
	}
	target, err := st.GetMembership(ctx, tenantID, userID)
	if err != nil {
		return Membership{}, err
	}
	delta := 0
	if target.Role == RoleOwner {
		delta = -1
	}
	del := func(key item) types.TransactWriteItem {
		return types.TransactWriteItem{Delete: &types.Delete{
			TableName: aws.String(st.Table), Key: key,
			ConditionExpression:       aws.String("#r = :old"),
			ExpressionAttributeNames:  map[string]string{"#r": "role"},
			ExpressionAttributeValues: item{":old": s(target.Role)},
		}}
	}
	err = txWrite(ctx, st, st.orgWrites(tenantID, actor, userID, delta, -1,
		del(memberKey(tenantID, userID)), del(userTenantKey(userID, tenantID)))...)
	if dynamo.IsConditionFailed(err) {
		return Membership{}, st.explainOrgConflict(ctx, tenantID, delta < 0)
	}
	if err != nil {
		return Membership{}, fmt.Errorf("remove member: %w", err)
	}
	return target, nil
}

// orgWrites is the transaction a member change shares: the two membership
// writes, the tenant's counters (owners by ownerDelta, members by memberDelta),
// and a check that the actor still has the role the caller saw. The check is
// left out when the actor is the target, as one transaction may not touch an
// item twice; the membership write's own role condition covers that case.
func (st *Store) orgWrites(tenantID string, actor Membership, userID string, ownerDelta, memberDelta int, pair ...types.TransactWriteItem) []types.TransactWriteItem {
	expr, cond := "ADD ownerCount :od, memberCount :md", "kind = :org AND attribute_not_exists(deletingAt)"
	vals := item{":od": n(int64(ownerDelta)), ":md": n(int64(memberDelta)), ":org": s(KindOrg)}
	if ownerDelta < 0 {
		cond += " AND ownerCount > :one"
		vals[":one"] = n(1)
	}
	items := append(pair, types.TransactWriteItem{Update: &types.Update{
		TableName: aws.String(st.Table), Key: tenantKey(tenantID),
		UpdateExpression: aws.String(expr), ConditionExpression: aws.String(cond),
		ExpressionAttributeValues: vals,
	}})
	if actor.UserID != userID {
		items = append(items, types.TransactWriteItem{ConditionCheck: &types.ConditionCheck{
			TableName: aws.String(st.Table), Key: memberKey(tenantID, actor.UserID),
			ConditionExpression:       aws.String("#r = :actor"),
			ExpressionAttributeNames:  map[string]string{"#r": "role"},
			ExpressionAttributeValues: item{":actor": s(actor.Role)},
		}})
	}
	return items
}
