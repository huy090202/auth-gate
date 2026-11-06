// Package dynamo builds the DynamoDB client and manages the single table.
package dynamo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const localRegion = "ap-southeast-1"

// localIdleConns replaces the SDK's pool of 10 idle connections per host, for
// DynamoDB Local only. Tests run up to 200 callers at once and the default pool
// closes the surplus, so back-to-back test runs use up this machine's local ports
// and connections start to reset. 512 is well above that fan-out; it was measured
// to delay that point, not to remove it.
const localIdleConns = 512

// New returns a DynamoDB client. An empty endpoint targets real AWS using the
// default config chain; a non-empty endpoint targets that URL (DynamoDB Local)
// with static dummy credentials.
func New(ctx context.Context, endpoint string) (*dynamodb.Client, error) {
	var opts []func(*config.LoadOptions) error
	if endpoint != "" {
		opts = append(opts, config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("local", "local", "")))
	}
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	if endpoint != "" && cfg.Region == "" {
		cfg.Region = localRegion
	}
	return dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
			o.HTTPClient = awshttp.NewBuildableClient().WithTransportOptions(func(t *http.Transport) {
				t.MaxIdleConns = localIdleConns
				t.MaxIdleConnsPerHost = localIdleConns
			})
		}
	}), nil
}

// EnsureTable creates the pk/sk on-demand table with TTL on "ttl". It is a
// no-op when the table already exists.
func EnsureTable(ctx context.Context, c *dynamodb.Client, name string) error {
	_, err := c.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(name),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("pk"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("sk"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
	})
	if err != nil {
		var inUse *types.ResourceInUseException
		if errors.As(err, &inUse) {
			return nil
		}
		return fmt.Errorf("create table %s: %w", name, err)
	}
	if err := dynamodb.NewTableExistsWaiter(c).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}, 2*time.Minute); err != nil {
		return fmt.Errorf("wait for table %s: %w", name, err)
	}
	_, err = c.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: aws.String(name),
		TimeToLiveSpecification: &types.TimeToLiveSpecification{
			AttributeName: aws.String("ttl"),
			Enabled:       aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("enable ttl on %s: %w", name, err)
	}
	return nil
}

// Key builds the primary key of an item.
func Key(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk": &types.AttributeValueMemberS{Value: pk},
		"sk": &types.AttributeValueMemberS{Value: sk},
	}
}
