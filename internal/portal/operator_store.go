package portal

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// tenantsPage is how many workspaces the operator list returns at once.
const tenantsPage = 50

// tenantCursorRE is the sort key of a TENANTS index item: T# and a ULID.
var tenantCursorRE = regexp.MustCompile(`^T#[0-9A-HJKMNP-TV-Z]{26}$`)

// ListTenants returns up to 50 workspaces, newest first (the sort key is a ULID),
// each read with GetTenant. An index entry whose tenant is gone is skipped. The
// returned cursor is empty on the last page. A cursor that is not an index sort
// key is ErrInvalidCursor.
func (st *Store) ListTenants(ctx context.Context, cursor string) ([]Tenant, string, error) {
	if cursor != "" && !tenantCursorRE.MatchString(cursor) {
		return nil, "", ErrInvalidCursor
	}
	in := &dynamodb.QueryInput{
		TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
		KeyConditionExpression:    aws.String("pk = :p"),
		ExpressionAttributeValues: item{":p": s("TENANTS")},
		ScanIndexForward:          aws.Bool(false),
		Limit:                     aws.Int32(tenantsPage),
	}
	if cursor != "" {
		in.ExclusiveStartKey = item{"pk": s("TENANTS"), "sk": s(cursor)}
	}
	out, err := st.DB.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("list tenants: %w", err)
	}
	all := make([]Tenant, 0, len(out.Items))
	for _, it := range out.Items {
		tid := getS(it, "sk")[len("T#"):]
		t, err := st.GetTenant(ctx, tid)
		if errors.Is(err, ErrTenantNotFound) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		all = append(all, t)
	}
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		next = getS(out.LastEvaluatedKey, "sk")
	}
	return all, next, nil
}

// SetLastSignIn records the time of a completed sign-in. An unknown user is ErrUserNotFound.
func (st *Store) SetLastSignIn(ctx context.Context, userID string, at time.Time) error {
	return st.mapNotFound(st.update(ctx, userID, "SET lastSignInAt = :t", exists, nil, item{":t": s(ts(at))}), "set last sign-in")
}
