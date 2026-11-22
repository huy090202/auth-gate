// Command prune-factors deletes the factor items (PINs and passkeys) of clients
// that were soft-deleted. Without -apply it only prints counts per client. It
// needs Scan, which no Lambda role has, so run it under an operator profile
// (scripts/setup-and-deploy/07-prune-factors.sh does).
//
//	AWS_PROFILE=... AWS_REGION=... go run ./cmd/prune-factors -table auth-gate-stg [-apply]
//
// $DYNAMO_ENDPOINT targets DynamoDB Local instead of AWS.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/huy090202/auth-gate/internal/auth/factor"
	"github.com/huy090202/auth-gate/internal/platform/dynamo"
)

func main() {
	table := flag.String("table", "", "DynamoDB table name")
	apply := flag.Bool("apply", false, "delete the items; without it only count them")
	flag.Parse()
	if *table == "" {
		fmt.Fprintln(os.Stderr, "usage: prune-factors -table NAME [-apply]")
		os.Exit(2)
	}
	ctx := context.Background()
	db, err := dynamo.New(ctx, os.Getenv("DYNAMO_ENDPOINT"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL", err)
		os.Exit(1)
	}
	counts, err := factor.PruneDeleted(ctx, db, *table, *apply)
	if err != nil {
		fmt.Fprintln(os.Stderr, "FAIL", err)
		os.Exit(1)
	}
	verb := "would delete"
	if *apply {
		verb = "deleted"
	}
	ids := make([]string, 0, len(counts))
	total := 0
	for id, n := range counts {
		ids = append(ids, id)
		total += n
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("%s %d factor items of deleted client %s\n", verb, counts[id], id)
	}
	fmt.Printf("%s %d factor items of %d deleted clients\n", verb, total, len(ids))
	if !*apply && total > 0 {
		fmt.Println("rerun with --apply to delete them")
	}
}
