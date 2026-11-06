package console

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/huy090202/auth-gate/internal/platform/id"
)

// AuditEntry is one line of the audit log. It never holds a key, PIN, password or token.
type AuditEntry struct {
	AdminID, Username, Action, Target, Result, IP string
	At                                            time.Time
}

var monthRE = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)

// PutAudit appends one entry under its UTC month.
func (st *Store) PutAudit(ctx context.Context, e AuditEntry) error {
	at := e.At.UTC()
	it := item{
		"pk": s("AUDIT#" + at.Format("2006-01")), "sk": s(ts(at) + "#" + id.New()),
		"adminId": s(e.AdminID), "username": s(e.Username), "action": s(e.Action),
		"target": s(e.Target), "result": s(e.Result), "ip": s(e.IP),
	}
	if _, err := st.DB.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(st.Table), Item: it}); err != nil {
		return fmt.Errorf("put audit: %w", err)
	}
	return nil
}

// ListAudit returns up to 50 entries of a month (yyyy-mm), newest first. The
// returned cursor is empty on the last page; pass it back to read the next.
func (st *Store) ListAudit(ctx context.Context, month, cursor string) ([]AuditEntry, string, error) {
	if !monthRE.MatchString(month) {
		return nil, "", fmt.Errorf("list audit: bad month %q", month)
	}
	pk := "AUDIT#" + month
	in := &dynamodb.QueryInput{
		TableName: aws.String(st.Table), ConsistentRead: aws.Bool(true),
		KeyConditionExpression:    aws.String("pk = :p"),
		ExpressionAttributeValues: item{":p": s(pk)},
		ScanIndexForward:          aws.Bool(false),
		Limit:                     aws.Int32(auditPage),
	}
	if cursor != "" {
		in.ExclusiveStartKey = item{"pk": s(pk), "sk": s(cursor)}
	}
	out, err := st.DB.Query(ctx, in)
	if err != nil {
		return nil, "", fmt.Errorf("list audit: %w", err)
	}
	entries := make([]AuditEntry, 0, len(out.Items))
	for _, it := range out.Items {
		var t time.Time
		if stamp, _, ok := strings.Cut(getS(it, "sk"), "#"); ok {
			t, _ = time.Parse(time.RFC3339Nano, stamp)
		}
		entries = append(entries, AuditEntry{
			AdminID: getS(it, "adminId"), Username: getS(it, "username"), Action: getS(it, "action"),
			Target: getS(it, "target"), Result: getS(it, "result"), IP: getS(it, "ip"), At: t,
		})
	}
	next := ""
	if len(out.LastEvaluatedKey) > 0 {
		next = getS(out.LastEvaluatedKey, "sk")
	}
	return entries, next, nil
}
