package portal

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
)

// suspendInput suspends a user directly, as the operator's tooling will.
func suspendInput(st *Store, userID string, at time.Time) *dynamodb.UpdateItemInput {
	return &dynamodb.UpdateItemInput{
		TableName: aws.String(st.Table), Key: userKey(userID),
		UpdateExpression:          aws.String("SET suspendedAt = :t"),
		ExpressionAttributeValues: item{":t": s(ts(at))},
	}
}

// memberPut writes the tenant side of a membership, as the invite flow will.
func memberPut(st *Store, tenantID, userID, role string, at time.Time) *dynamodb.PutItemInput {
	it := memberKey(tenantID, userID)
	it["role"], it["joinedAt"] = s(role), s(ts(at))
	return &dynamodb.PutItemInput{TableName: aws.String(st.Table), Item: it}
}
