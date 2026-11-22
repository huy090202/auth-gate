package dynamo

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// TSLayout is RFC 3339 with a fixed nine fractional digits, so timestamps sort
// as strings (RFC3339Nano drops trailing zeros and would not).
const TSLayout = "2006-01-02T15:04:05.000000000Z"

// TS formats t as a sortable UTC timestamp string.
func TS(t time.Time) string { return t.UTC().Format(TSLayout) }

// Item is one DynamoDB item.
type Item = map[string]types.AttributeValue
