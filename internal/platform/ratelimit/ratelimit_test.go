package ratelimit

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
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
	if d.Allowed || d.RetryAfterSec != 2 {
		t.Fatalf("decision = %+v, want denied, RetryAfterSec 2", d)
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

func TestClockBehindAddsNothing(t *testing.T) {
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
	if numAttr(t, after, "updatedAt") < numAttr(t, before, "updatedAt") {
		t.Fatalf("updatedAt moved backwards: before %v after %v", before["updatedAt"], after["updatedAt"])
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

// fakeReply is one canned DynamoDB answer: an HTTP status and a JSON body.
type fakeReply struct {
	status int
	body   string
}

func errReply(status int, code string) fakeReply {
	return fakeReply{status, fmt.Sprintf(`{"__type":"com.amazonaws.dynamodb.v20120810#%s","message":"fake"}`, code)}
}

// fakeLimiter points a Limiter at an httptest server that answers each operation by
// its X-Amz-Target header, taking the replies for that operation in order and
// repeating the last. DynamoDB Local cannot throttle, so this is the only way to
// reach those branches. Retries and response checksums are off to keep it fast and quiet.
func fakeLimiter(t *testing.T, replies map[string][]fakeReply) *Limiter {
	t.Helper()
	var mu sync.Mutex
	next := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		op := r.Header.Get("X-Amz-Target")
		mu.Lock()
		rs, ok := replies[op]
		if !ok {
			mu.Unlock()
			t.Errorf("unexpected operation %q", op)
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		rep := rs[min(next[op], len(rs)-1)]
		next[op]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-amz-json-1.0")
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	db := dynamodb.New(dynamodb.Options{
		Region:                          "ap-southeast-1",
		BaseEndpoint:                    aws.String(srv.URL),
		Credentials:                     credentials.NewStaticCredentialsProvider("x", "x", ""),
		Retryer:                         aws.NopRetryer{},
		DisableValidateResponseChecksum: true,
	})
	return &Limiter{DB: db, Table: "t", Now: func() time.Time { return t0 }}
}

const (
	getNothing = `{}`
	// a bucket with 5 tokens whose refill window started 10 s before t0
	getStale = `{"Item":{"pk":{"S":"RL#k"},"sk":{"S":"META"},"tokens":{"N":"5.000000"},"updatedAt":{"N":"%d"}}}`
)

func staleItem() fakeReply {
	return fakeReply{200, fmt.Sprintf(getStale, t0.Add(-10*time.Second).UnixMicro())}
}

// failingPaths maps each call site to the replies that reach it, with the failure
// reply for the call under test left to the caller.
func failingPaths(fail fakeReply) map[string]map[string][]fakeReply {
	lost := errReply(400, "ConditionalCheckFailedException")
	return map[string]map[string][]fakeReply{
		"read":   {"DynamoDB_20120810.GetItem": {fail}},
		"create": {"DynamoDB_20120810.GetItem": {{200, getNothing}}, "DynamoDB_20120810.PutItem": {fail}},
		"claim":  {"DynamoDB_20120810.GetItem": {staleItem()}, "DynamoDB_20120810.UpdateItem": {fail}},
		"spend":  {"DynamoDB_20120810.GetItem": {staleItem()}, "DynamoDB_20120810.UpdateItem": {lost, fail}},
	}
}

func TestThrottledCallsDeny(t *testing.T) {
	cases := []struct {
		site, code string
		why        string
	}{
		{"read", "ProvisionedThroughputExceededException", "the SDK's own throttle code for a hot partition"},
		{"read", "RequestLimitExceeded", "the on-demand table throttle code"},
		{"read", "ThrottlingException", "the generic throttle code"},
		{"create", "ProvisionedThroughputExceededException", "a flood can throttle the first write too"},
		{"claim", "RequestLimitExceeded", "the claim is a write on the hot item"},
		{"spend", "ThrottlingException", "the spend after a lost claim is the most common write under a flood"},
	}
	for _, c := range cases {
		t.Run(c.site+"/"+c.code, func(t *testing.T) {
			t.Log(c.why)
			l := fakeLimiter(t, failingPaths(errReply(400, c.code))[c.site])
			d := l.Allow(context.Background(), "k", Settings{Burst: 10, PerSecond: 1})
			if d.Allowed || d.RetryAfterSec != 1 {
				t.Fatalf("decision = %+v, want denied with RetryAfterSec 1", d)
			}
		})
	}
}

func TestOtherDynamoErrorsStillAllow(t *testing.T) {
	// A 500 is not a throttle: the limiter keeps failing open on it, and these
	// are the write-error branches that had no test.
	for _, site := range []string{"read", "create", "claim", "spend"} {
		t.Run(site, func(t *testing.T) {
			l := fakeLimiter(t, failingPaths(errReply(500, "InternalServerError"))[site])
			if d := l.Allow(context.Background(), "k", Settings{Burst: 10, PerSecond: 1}); !d.Allowed {
				t.Fatalf("denied on a non-throttle error: %+v", d)
			}
		})
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
		if allowed.Load() != burst {
			t.Fatalf("round %d: allowed %d, want exactly %d", round, allowed.Load(), burst)
		}
		if got := numAttr(t, item(t, l, key), "tokens"); got != 0 {
			t.Fatalf("round %d: stored tokens %v, want 0 (lost update)", round, got)
		}
	}
}

// TestSpikeAfterIdleDoesNotRefillAgain guards the hazard of a plain spend on a
// full bucket: it earns nothing, so it might not move updatedAt, and the next
// caller would then be credited the whole idle hour again. The bucket is seeded
// holding exactly burst, because that is the only state where earning nothing
// and being idle coincide.
func TestSpikeAfterIdleDoesNotRefillAgain(t *testing.T) {
	l, clk := newLimiter(t)
	const burst = 20
	s := Settings{Burst: burst, PerSecond: 1}
	k := dynamo.Key("RL#k", "META")
	if _, err := l.DB.PutItem(context.Background(), &dynamodb.PutItemInput{
		TableName: aws.String(l.Table),
		Item:      map[string]types.AttributeValue{"pk": k["pk"], "sk": k["sk"], "tokens": num(burst), "updatedAt": num(t0.UnixMicro())},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	clk.add(time.Hour)

	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Allow(context.Background(), "k", s).Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != burst {
		t.Fatalf("allowed %d, want exactly %d", allowed.Load(), burst)
	}
	if d := l.Allow(context.Background(), "k", s); d.Allowed {
		t.Fatalf("next call at the same instant allowed (idle time credited twice): tokens now %v",
			numAttr(t, item(t, l, "k"), "tokens"))
	}
}

// TestConcurrentCallersWithAdvancingClock exercises lost claims: every finished
// call moves the clock, so callers keep reading a window another caller is about
// to claim. Demand is one token per call and the refill 0.2 per call, so the
// bucket never reaches burst again and everything credited stays visible in
// updatedAt: credited = (last updatedAt - t0) * perSecond.
func TestConcurrentCallersWithAdvancingClock(t *testing.T) {
	l, clk := newLimiter(t)
	const burst, workers, calls = 20, 40, 25
	const step = 200 * time.Microsecond
	s := Settings{Burst: burst, PerSecond: 1000}

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < calls; j++ {
				if l.Allow(context.Background(), "k", s).Allowed {
					allowed.Add(1)
				}
				clk.add(step)
			}
		}()
	}

	// tokens < 0 is only visible while the run is on: later claims hide it.
	done := make(chan struct{})
	var lowest float64 // read only after sampler.Wait
	var sampler sync.WaitGroup
	sampler.Add(1)
	go func() {
		defer sampler.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			out, err := l.DB.GetItem(context.Background(), &dynamodb.GetItemInput{
				TableName: aws.String(l.Table), Key: dynamo.Key("RL#k", "META"), ConsistentRead: aws.Bool(true),
			})
			if err != nil {
				continue
			}
			if v, ok := out.Item["tokens"].(*types.AttributeValueMemberN); ok {
				if f, err := strconv.ParseFloat(v.Value, 64); err == nil {
					lowest = math.Min(lowest, f)
				}
			}
		}
	}()
	wg.Wait()
	close(done)
	sampler.Wait()

	it := item(t, l, "k")
	stored := numAttr(t, it, "tokens")
	if stored < 0 || lowest < 0 {
		t.Fatalf("tokens went negative: lowest sampled %v, stored %v", lowest, stored)
	}
	earned := clk.now().Sub(t0).Seconds() * s.PerSecond
	if got, limit := float64(allowed.Load()), burst+earned; got > limit {
		t.Fatalf("allowed %v > burst + earned %v", got, limit)
	}
	credited := (numAttr(t, it, "updatedAt") - float64(t0.UnixMicro())) / 1e6 * s.PerSecond
	if got, want := float64(allowed.Load())+stored, burst+credited; math.Abs(got-want) > 1 {
		t.Fatalf("allowed %d + stored %v = %v, want burst + credited = %v within 1 token", allowed.Load(), stored, got, want)
	}
}

func TestLoweredBurstClampsTheBucket(t *testing.T) {
	l, clk := newLimiter(t)
	high, low := Settings{Burst: 100, PerSecond: 1}, Settings{Burst: 1, PerSecond: 1}
	check := func(key string) {
		t.Helper()
		if d := l.Allow(context.Background(), key, low); !d.Allowed {
			t.Fatalf("%s: denied: %+v", key, d)
		}
		if got := numAttr(t, item(t, l, key), "tokens"); got > 0 {
			t.Fatalf("%s: stored tokens %v after the spend, want at most 0", key, got)
		}
		if d := l.Allow(context.Background(), key, low); d.Allowed {
			t.Fatalf("%s: next call at the same instant allowed", key)
		}
	}

	l.Allow(context.Background(), "later", high)
	clk.add(time.Second)
	check("later")

	// Same instant as the last write, so nothing else in Allow would claim: this
	// is what the "tokens > burst" half of the claim test is there for.
	l.Allow(context.Background(), "same", high)
	check("same")
}

// TestMeasureDefaults prints how the limiter behaves under load with the
// default settings and the real clock. It is a measurement, not an assertion,
// so it only runs on request:
//
//	RATELIMIT_MEASURE=1 go test -run TestMeasureDefaults -v ./internal/platform/ratelimit
//
// DynamoDB Local latency is not AWS latency: read the shape, not the numbers.
func TestMeasureDefaults(t *testing.T) {
	if os.Getenv("RATELIMIT_MEASURE") != "1" {
		t.Skip("set RATELIMIT_MEASURE=1 to run the measurement")
	}
	db, table := dynamotest.NewTable(t)
	l := &Limiter{DB: db, Table: table}
	s := Settings{Burst: 100, PerSecond: 50} // the defaults of a new client
	ctx := context.Background()

	t.Logf("%-8s %10s %10s", "workers", "allowed/s", "denied")
	const window = 10 * time.Second
	for _, workers := range []int{1, 5, 10, 20, 50} {
		key := "m" + strconv.Itoa(workers)
		var allowed, denied atomic.Int64
		deadline := time.Now().Add(window)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for time.Now().Before(deadline) {
					if l.Allow(ctx, key, s).Allowed {
						allowed.Add(1)
					} else {
						denied.Add(1)
					}
				}
			}()
		}
		wg.Wait()
		t.Logf("%-8d %10.1f %10d", workers, float64(allowed.Load())/window.Seconds(), denied.Load())
	}

	// A spike: 100 callers at once on a full bucket. The bucket is full again
	// after Burst/PerSecond = 2 s, so wait a little longer between spikes.
	for round := 1; round <= 5; round++ {
		var allowed, denied atomic.Int64
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if l.Allow(ctx, "spike", s).Allowed {
					allowed.Add(1)
				} else {
					denied.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		t.Logf("spike %d: allowed %d of 100, denied %d", round, allowed.Load(), denied.Load())
		time.Sleep(2500 * time.Millisecond)
	}
}
