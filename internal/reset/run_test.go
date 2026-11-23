package reset

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
)

type seedItem struct {
	pk, sk, clientID string
	keep             bool // expected to survive an apply
}

var testKeep = Keep{Clients: map[string]bool{"con": true, "por": true, "app": true}, Console: "con"}

// seeds has one item per rule line of Decide, kept and deleted variants.
var seeds = map[Table][]seedItem{
	AuthGate: {
		{"C#con", "META", "", true}, {"C#old", "META", "", false},
		{"CLIENTS", "C#por", "", true}, {"CLIENTS", "C#old", "", false},
		{"K#h1", "META", "app", true}, {"K#h2", "META", "old", false}, {"K#h3", "META", "old", false},
		{"S#con#admin1", "PIN", "", true}, {"S#old#u1", "PIN", "", false}, {"S#app#u2", "PASSKEY#1", "", false},
		{"RL#app", "META", "", true}, {"RL#old", "META", "", false},
		{"CH#app#c1", "META", "", true}, {"CH#old#c1", "META", "", false},
		{"R#por#r1", "META", "", true}, {"R#old#r1", "META", "", false},
		{"OWNER#someone", "C#old", "", false},
		{"WEIRD#1", "X", "", true}, {"S#nohash", "PIN", "", true},
	},
	Console: {
		{"ADMINS", "A#1", "", true}, {"SES#1", "META", "", false}, {"JTI#1", "META", "", false},
		{"RL#1", "META", "", false}, {"AUDIT#1", "T#1", "", false}, {"ODD#1", "X", "", true},
	},
	Portal:  {{"USER#1", "META", "", false}, {"USER#2", "META", "", false}, {"SES#1", "META", "", false}},
	AuthApp: {{"USER#1", "META", "", false}, {"NOTE#1", "N#1", "", false}},
}

func setup(t *testing.T) (*dynamodb.Client, Tables) {
	t.Helper()
	var db *dynamodb.Client
	tables := Tables{}
	for _, tb := range []Table{AuthGate, Console, Portal, AuthApp} {
		var name string
		db, name = dynamotest.NewTable(t)
		tables[tb] = name
	}
	ctx := context.Background()
	for tb, items := range seeds {
		for _, s := range items {
			it := dynamo.Key(s.pk, s.sk)
			if s.clientID != "" {
				it["clientId"] = &types.AttributeValueMemberS{Value: s.clientID}
			}
			if _, err := db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(tables[tb]), Item: it}); err != nil {
				t.Fatalf("seed %s: %v", tb, err)
			}
		}
	}
	return db, tables
}

func remaining(t *testing.T, db *dynamodb.Client, tables Tables) []string {
	t.Helper()
	var got []string
	for tb, name := range tables {
		p := dynamodb.NewScanPaginator(db, &dynamodb.ScanInput{TableName: aws.String(name), ConsistentRead: aws.Bool(true)})
		for p.HasMorePages() {
			page, err := p.NextPage(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				got = append(got, fmt.Sprintf("%s|%s|%s", tb, it["pk"].(*types.AttributeValueMemberS).Value, it["sk"].(*types.AttributeValueMemberS).Value))
			}
		}
	}
	sort.Strings(got)
	return got
}

func keptSet() []string {
	var want []string
	for tb, items := range seeds {
		for _, s := range items {
			if s.keep {
				want = append(want, fmt.Sprintf("%s|%s|%s", tb, s.pk, s.sk))
			}
		}
	}
	sort.Strings(want)
	return want
}

func seededAll() []string {
	var all []string
	for tb, items := range seeds {
		for _, s := range items {
			all = append(all, fmt.Sprintf("%s|%s|%s", tb, s.pk, s.sk))
		}
	}
	sort.Strings(all)
	return all
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRunApplyLeavesOnlyKept(t *testing.T) {
	db, tables := setup(t)
	ctx := context.Background()
	counts, err := Run(ctx, db, tables, testKeep, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := remaining(t, db, tables), keptSet(); !equal(got, want) {
		t.Fatalf("remaining\n got %v\nwant %v", got, want)
	}
	deleted := 0
	for _, c := range counts {
		deleted += c.Deleted
	}
	if want := len(seededAll()) - len(keptSet()); deleted != want {
		t.Fatalf("deleted = %d, want %d", deleted, want)
	}
	again, err := Run(ctx, db, tables, testKeep, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range again {
		if c.Deleted != 0 {
			t.Fatalf("second apply deleted %d %s %s", c.Deleted, c.Table, c.Label)
		}
	}
}

func TestRunDryRunDeletesNothing(t *testing.T) {
	db, tables := setup(t)
	ctx := context.Background()
	dry, err := Run(ctx, db, tables, testKeep, false)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := remaining(t, db, tables), seededAll(); !equal(got, want) {
		t.Fatalf("dry run changed data\n got %v\nwant %v", got, want)
	}
	applied, err := Run(ctx, db, tables, testKeep, true)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(dry) != fmt.Sprint(applied) {
		t.Fatalf("dry counts differ from apply counts\n%v\n%v", dry, applied)
	}
	// Ordered by table (AuthGate first), then label.
	if dry[0].Table != AuthGate || dry[len(dry)-1].Table != AuthApp {
		t.Fatalf("unexpected order: %v", dry)
	}
}

func TestRunRefusesEmptyKeep(t *testing.T) {
	if _, err := Run(context.Background(), nil, Tables{}, Keep{}, true); err == nil {
		t.Fatal("want error for empty keep set")
	}
}

func TestCheckKeepRefuses(t *testing.T) {
	db, name := dynamotest.NewTable(t)
	ctx := context.Background()
	put := func(id string, extra map[string]types.AttributeValue) {
		it := dynamo.Key("C#"+id, "META")
		it["name"] = &types.AttributeValueMemberS{Value: "name-" + id}
		it["rpId"] = &types.AttributeValueMemberS{Value: id + ".example"}
		for k, v := range extra {
			it[k] = v
		}
		if _, err := db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(name), Item: it}); err != nil {
			t.Fatal(err)
		}
	}
	ts := &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)}
	put("a", nil)
	put("b", nil)
	put("c", nil)
	put("gone", map[string]types.AttributeValue{"deletedAt": ts})
	put("off", map[string]types.AttributeValue{"disabledAt": ts})

	for _, ids := range [][]string{{"a", ""}, {"a", "missing"}, {"a", "gone"}, {"a", "off"}, nil} {
		if _, err := CheckKeep(ctx, db, name, ids); err == nil {
			t.Fatalf("CheckKeep(%q): want error", ids)
		}
	}
	got, err := CheckKeep(ctx, db, name, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] != (KeptClient{ID: "b", Name: "name-b", RPID: "b.example"}) {
		t.Fatalf("got %v", got)
	}
}

type fakeBW struct {
	failFirst int // calls that return everything unprocessed
	always    bool
	calls     int
	deleted   int
}

func (f *fakeBW) BatchWriteItem(_ context.Context, in *dynamodb.BatchWriteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	f.calls++
	if f.always || f.calls <= f.failFirst {
		return &dynamodb.BatchWriteItemOutput{UnprocessedItems: in.RequestItems}, nil
	}
	for _, r := range in.RequestItems {
		f.deleted += len(r)
	}
	return &dynamodb.BatchWriteItemOutput{}, nil
}

func keys(n int) []map[string]types.AttributeValue {
	out := make([]map[string]types.AttributeValue, n)
	for i := range out {
		out[i] = dynamo.Key(fmt.Sprintf("p%d", i), "s")
	}
	return out
}

func TestDeleteKeysRetriesUnprocessed(t *testing.T) {
	f := &fakeBW{failFirst: 2}
	if err := deleteKeys(context.Background(), f, "tbl", keys(30), time.Microsecond); err != nil {
		t.Fatal(err)
	}
	if f.deleted != 30 {
		t.Fatalf("deleted %d, want 30", f.deleted)
	}
}

func TestDeleteKeysGivesUpNamingTable(t *testing.T) {
	f := &fakeBW{always: true}
	err := deleteKeys(context.Background(), f, "auth-gate-stg", keys(3), time.Microsecond)
	if err == nil || !errors.Is(err, errUnprocessed) || !contains(err.Error(), "auth-gate-stg") {
		t.Fatalf("err = %v", err)
	}
	if f.calls != maxAttempts {
		t.Fatalf("calls = %d, want %d", f.calls, maxAttempts)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// failOn delegates to the real client except for one table, whose writes fail.
type failOn struct {
	db    *dynamodb.Client
	table string
}

func (f failOn) BatchWriteItem(ctx context.Context, in *dynamodb.BatchWriteItemInput, o ...func(*dynamodb.Options)) (*dynamodb.BatchWriteItemOutput, error) {
	if _, bad := in.RequestItems[f.table]; bad {
		return nil, errors.New("boom")
	}
	return f.db.BatchWriteItem(ctx, in, o...)
}

// A failure on the third table (Console) returns the counts of the first two
// (Portal, AuthApp), which were cleared before it, and not the failing one.
func TestRunErrorKeepsCountsSoFar(t *testing.T) {
	db, tables := setup(t)
	counts, err := run(context.Background(), db, failOn{db, tables[Console]}, tables, testKeep, true)
	if err == nil {
		t.Fatal("want error")
	}
	seen := map[Table]bool{}
	for _, c := range counts {
		seen[c.Table] = true
	}
	if len(seen) != 2 || !seen[Portal] || !seen[AuthApp] {
		t.Fatalf("counts tables = %v, want Portal and AuthApp only", seen)
	}
	// The user tables are already empty of doomed items, AuthGate untouched.
	if left := remaining(t, db, Tables{Portal: tables[Portal]}); len(left) != 0 {
		t.Fatalf("portal not cleared: %v", left)
	}
	if left := remaining(t, db, Tables{AuthGate: tables[AuthGate]}); len(left) != len(seeds[AuthGate]) {
		t.Fatalf("AuthGate changed: %v", left)
	}
}

func TestRunListsUnknownItems(t *testing.T) {
	db, tables := setup(t)
	counts, err := Run(context.Background(), db, tables, testKeep, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range counts {
		for _, u := range c.Unknown {
			got = append(got, fmt.Sprintf("%s %s", c.Table, u))
		}
	}
	sort.Strings(got)
	// WEIRD#1 and S#nohash in AuthGate, ODD#1 in Console are the seeded unknown shapes.
	want := map[string]bool{"authgate WEIRD#1 X": false, "authgate S#nohash PIN": false, "console ODD#1 X": false}
	for _, g := range got {
		if _, ok := want[g]; ok {
			want[g] = true
		}
	}
	for k, ok := range want {
		if !ok {
			t.Fatalf("missing %q in %v", k, got)
		}
	}
}
