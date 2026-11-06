// Command backfill-clients writes the list-index item for every client that
// predates the index. It needs Scan, which no Lambda role has, so run it under
// an operator profile (scripts/setup-and-deploy/05-backfill-clients.sh does).
// It is safe to rerun and prints counts only.
//
//	AWS_PROFILE=... AWS_REGION=... go run ./cmd/backfill-clients -table auth-gate-stg
//
// $DYNAMO_ENDPOINT targets DynamoDB Local instead of AWS.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/huy090202/auth-gate/internal/auth/client"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
)

func main() {
	table := flag.String("table", "", "DynamoDB table name")
	flag.Parse()
	if *table == "" {
		fmt.Fprintln(os.Stderr, "usage: backfill-clients -table NAME")
		os.Exit(2)
	}
	ctx := context.Background()
	db, err := dynamo.New(ctx, os.Getenv("DYNAMO_ENDPOINT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL", err)
		os.Exit(1)
	}
	written, skipped, err := client.Backfill(ctx, db, *table)
	fmt.Printf("written %d, skipped %d\n", written, skipped)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL", err)
		os.Exit(1)
	}
}
