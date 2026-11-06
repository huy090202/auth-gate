package ratelimit

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/huy090202/auth-gate/internal/platform/dynamo"
	"github.com/huy090202/auth-gate/internal/platform/dynamo/dynamotest"
)

var t0 = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newLimiter(t *testing.T) (*Limiter, *clock) {
	t.Helper()
	db, table := dynamotest.NewTable(t)
	clk := &clock{t: t0}
	return &Limiter{DB: db, Table: table, Now: clk.now}, clk
}

func item(t *testing.T, l *Limiter, key string) map[string]types.AttributeValue {
	t.Helper()
	out, err := l.DB.GetItem(context.Background(), &dynamodb.GetItemInput{
		TableName: aws.String(l.Table), Key: dynamo.Key("RL#"+key, "META"), ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	return out.Item
}

func numAttr(t *testing.T, it map[string]types.AttributeValue, name string) float64 {
	t.Helper()
	v, ok := it[name].(*types.AttributeValueMemberN)
	if !ok {
		t.Fatalf("attribute %q missing or not a number: %v", name, it[name])
	}
	f, err := strconv.ParseFloat(v.Value, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", name, err)
	}
	return f
}

func count(l *Limiter, key string, s Settings, n int) int {
	allowed := 0
	for i := 0; i < n; i++ {
		if l.Allow(context.Background(), key, s).Allowed {
			allowed++
		}
	}
	return allowed
}

func TestFirstRequestStartsFullAndSpendsOne(t *testing.T) {
	l, _ := newLimiter(t)
	if d := l.Allow(context.Background(), "k", Settings{Burst: 3, PerSecond: 1}); !d.Allowed {
		t.Fatalf("first request denied: %+v", d)
	}
	if got := numAttr(t, item(t, l, "k"), "tokens"); got != 2 {
		t.Fatalf("tokens = %v, want 2", got)
	}
}

func TestDeniesWhenEmptyWithRetryAfter(t *testing.T) {
	l, _ := newLimiter(t)
	s := Settings{Burst: 2, PerSecond: 0.5}
	if got := count(l, "k", s, 2); got != 2 {
		t.Fatalf("allowed = %d, want 2", got)
	}
	d := l.Allow(context.Background(), "k", s)
	if d.Allowed || d.Conflict || d.RetryAfterSec != 2 {
		t.Fatalf("decision = %+v, want denied, RetryAfterSec 2, no conflict", d)
	}
}

func TestRetryAfterIsAtLeastOneWholeSecond(t *testing.T) {
	l, _ := newLimiter(t)
	s := Settings{Burst: 1, PerSecond: 50}
	l.Allow(context.Background(), "k", s)
	d := l.Allow(context.Background(), "k", s)
	if d.Allowed || d.RetryAfterSec != 1 {
		t.Fatalf("decision = %+v, want denied, RetryAfterSec 1", d)
	}
}

func TestRefillsOverTime(t *testing.T) {
	l, clk := newLimiter(t)
	s := Settings{Burst: 2, PerSecond: 0.5}
	count(l, "k", s, 2)
	clk.add(4 * time.Second)
	if got := count(l, "k", s, 5); got != 2 {
		t.Fatalf("allowed after refill = %d, want 2", got)
	}
}

func TestRefillNeverExceedsBurst(t *testing.T) {
	l, clk := newLimiter(t)
	s := Settings{Burst: 3, PerSecond: 1}
	count(l, "k", s, 3)
	clk.add(time.Hour)
	if got := count(l, "k", s, 10); got != 3 {
		t.Fatalf("allowed = %d, want 3", got)
	}
}

func TestClockBehindAddsNothingAndStillAdvances(t *testing.T) {
	l, clk := newLimiter(t)
	s := Settings{Burst: 5, PerSecond: 1}
	l.Allow(context.Background(), "k", s)
	before := item(t, l, "k")
	clk.add(-10 * time.Second)
	if d := l.Allow(context.Background(), "k", s); !d.Allowed {
		t.Fatalf("denied: %+v", d)
	}
	after := item(t, l, "k")
	if got := numAttr(t, after, "tokens"); got != 3 {
		t.Fatalf("tokens = %v, want 3 (no refill from a clock that went back)", got)
	}
	if numAttr(t, after, "updatedAt") <= numAttr(t, before, "updatedAt") {
		t.Fatalf("updatedAt did not advance: before %v after %v", before["updatedAt"], after["updatedAt"])
	}
}

func TestDeniedRequestDoesNotWrite(t *testing.T) {
	l, clk := newLimiter(t)
	s := Settings{Burst: 1, PerSecond: 0.1}
	l.Allow(context.Background(), "k", s)
	before := item(t, l, "k")
	clk.add(time.Second)
	if d := l.Allow(context.Background(), "k", s); d.Allowed {
		t.Fatalf("allowed: %+v", d)
	}
	after := item(t, l, "k")
	for _, name := range []string{"tokens", "updatedAt"} {
		if numAttr(t, before, name) != numAttr(t, after, name) {
			t.Fatalf("%s changed on denial: %v -> %v", name, before[name], after[name])
		}
	}
}

func TestKeysDoNotTouchEachOther(t *testing.T) {
	l, _ := newLimiter(t)
	s := Settings{Burst: 2, PerSecond: 0.1}
	count(l, "a", s, 2)
	if l.Allow(context.Background(), "a", s).Allowed {
		t.Fatal("a should be empty")
	}
	if got := count(l, "b", s, 2); got != 2 {
		t.Fatalf("b allowed = %d, want 2", got)
	}
}

func TestTTLOnlyWhenSet(t *testing.T) {
	l, _ := newLimiter(t)
	s := Settings{Burst: 2, PerSecond: 1}
	l.Allow(context.Background(), "none", s)
	if _, ok := item(t, l, "none")["ttl"]; ok {
		t.Fatal("ttl written with TTL 0")
	}
	l.TTL = time.Hour
	l.Allow(context.Background(), "with", s)
	if got, want := numAttr(t, item(t, l, "with"), "ttl"), float64(t0.Add(time.Hour).Unix()); got != want {
		t.Fatalf("ttl = %v, want %v", got, want)
	}
}

func TestDynamoErrorLetsRequestThrough(t *testing.T) {
	l, _ := newLimiter(t)
	l.Table = "missing"
	if d := l.Allow(context.Background(), "k", Settings{Burst: 1, PerSecond: 1}); !d.Allowed {
		t.Fatalf("denied on dynamo error: %+v", d)
	}
}

func TestConcurrentCallersNeverExceedBurst(t *testing.T) {
	l, _ := newLimiter(t) // fixed clock: nothing refills
	const burst, callers = 20, 200
	s := Settings{Burst: burst, PerSecond: 1}
	for round := 0; round < 3; round++ {
		key := "c" + strconv.Itoa(round)
		var allowed atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if l.Allow(context.Background(), key, s).Allowed {
					allowed.Add(1)
				}
			}()
		}
		wg.Wait()
		if allowed.Load() > burst {
			t.Fatalf("round %d: allowed %d > burst %d", round, allowed.Load(), burst)
		}
		if got, want := numAttr(t, item(t, l, key), "tokens"), float64(burst-int(allowed.Load())); got != want {
			t.Fatalf("round %d: stored tokens %v, want %v (lost update)", round, got, want)
		}
	}
}
