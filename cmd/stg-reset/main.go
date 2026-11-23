// Command stg-reset deletes the user data of stg from the four DynamoDB tables,
// keeping the three app clients and the Console admins. Without -apply it only
// prints what it would delete. It needs Scan, so run it under an operator
// profile (the wrapper script does).
//
//	go run ./cmd/stg-reset -stage stg -console ID -portal ID -authapp ID [-apply]
//
// $DYNAMO_ENDPOINT targets DynamoDB Local instead of AWS.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/reset"
)

// maxUnknownLines caps the unknown items listed.
const maxUnknownLines = 20

func main() {
	stage := flag.String("stage", "", "stage to reset; only stg is allowed")
	consoleID := flag.String("console", "", "client id the Console uses")
	portalID := flag.String("portal", "", "client id the portal uses")
	appID := flag.String("authapp", "", "client id auth-app uses")
	apply := flag.Bool("apply", false, "delete the items; without it only count them")
	flag.Parse()
	if *stage != "stg" {
		fmt.Fprintln(os.Stderr, "stg-reset: -stage must be stg")
		os.Exit(2)
	}
	if err := run(*stage, *consoleID, *portalID, *appID, *apply); err != nil {
		fmt.Fprintln(os.Stderr, "FAIL", err)
		os.Exit(1)
	}
}

func run(stage, consoleID, portalID, appID string, apply bool) error {
	ctx := context.Background()
	db, err := dynamo.New(ctx, os.Getenv("DYNAMO_ENDPOINT"))
	if err != nil {
		return err
	}
	tables := reset.Tables{
		reset.AuthGate: "auth-gate-" + stage,
		reset.Console:  "auth-gate-console-" + stage,
		reset.Portal:   "auth-gate-portal-" + stage,
		reset.AuthApp:  "auth-app-" + stage,
	}
	ids := []string{consoleID, portalID, appID}
	kept, err := reset.CheckKeep(ctx, db, tables[reset.AuthGate], ids)
	if err != nil {
		return fmt.Errorf("keep set: %w", err)
	}
	k := reset.Keep{Clients: map[string]bool{}, Console: consoleID}
	for _, id := range ids {
		k.Clients[id] = true
	}
	fmt.Println("keeping clients:")
	for _, c := range kept {
		fmt.Printf("  %s name=%q rpId=%q\n", c.ID, c.Name, c.RPID)
	}
	counts, err := reset.Run(ctx, db, tables, k, apply)
	verb := "would delete"
	if apply {
		verb = "deleted"
	}
	var keptN, delN int
	var unknown []string
	for _, c := range counts {
		fmt.Printf("%s %s kept=%d %s=%d\n", c.Table, c.Label, c.Kept, verb, c.Deleted)
		keptN += c.Kept
		delN += c.Deleted
		for _, u := range c.Unknown {
			unknown = append(unknown, fmt.Sprintf("%s %s", c.Table, u))
		}
	}
	if err != nil {
		fmt.Printf("stopped early; the lines above are the tables finished before the failure\n")
		return fmt.Errorf("%w\nrerun to finish; it is safe to run again", err)
	}
	fmt.Printf("total kept=%d %s=%d\n", keptN, verb, delN)
	if len(unknown) > 0 {
		fmt.Println("unknown items (kept):")
		for i, u := range unknown {
			if i == maxUnknownLines {
				fmt.Printf("… and %d more\n", len(unknown)-maxUnknownLines)
				break
			}
			fmt.Println(" ", u)
		}
	}
	if !apply && delN > 0 {
		fmt.Println("rerun with --apply to delete them")
	}
	return nil
}
