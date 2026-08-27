package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

const testShardKey = "npm/axios/1"
const testShardDoc = `{"key":"npm/axios/1"}`

// countingHotShards records how many whole-corpus hint reads reached the store.
type countingHotShards struct {
	*serverstore.Fake
	calls atomic.Int64
}

func (s *countingHotShards) HotShardKeys(ctx context.Context, limit int) ([]string, error) {
	s.calls.Add(1)
	return s.Fake.HotShardKeys(ctx, limit)
}

// blockingHotShards holds the hint read open until the test releases it, the
// way a database owned by a fresh server's first full builder pass does.
type blockingHotShards struct {
	*serverstore.Fake
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
}

func newBlockingHotShards() *blockingHotShards {
	return &blockingHotShards{
		Fake:    serverstore.NewFake(),
		started: make(chan struct{}, 16),
		release: make(chan struct{}),
	}
}

func (s *blockingHotShards) HotShardKeys(ctx context.Context, limit int) ([]string, error) {
	s.calls.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return s.Fake.HotShardKeys(ctx, limit)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func statsDoc(t *testing.T, url string) (map[string]any, int, time.Duration) {
	t.Helper()
	start := time.Now()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	elapsed := time.Since(start)
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("GET %s: bad JSON reply %q: %v", url, raw, err)
	}
	return doc, resp.StatusCode, elapsed
}

// TestStatsAnswersWithoutWaitingForASlowHotShardHint is the R2C-159
// regression. The production deploy's first request through the proxy is a
// GET /v1/stats with a ten-second ceiling, and it lands while the freshly
// started server's mandatory first full builder pass owns the database. The
// endpoint spent about ninety percent of its measured time on the optional
// shard-warming hint -- four whole-corpus reads, recomputed per caller -- and
// nothing bounded the request as a whole, so three of four unattended
// rollouts failed or barely survived here. A hint that cannot be produced in
// time is omitted; it never decides how long the endpoint takes.
func TestStatsAnswersWithoutWaitingForASlowHotShardHint(t *testing.T) {
	store := newBlockingHotShards()
	srv, _, _ := newTestServer(t, func(d *Deps) {
		d.Store = store
		d.hotShardWait = 50 * time.Millisecond
	})
	// A built shard exists, so a missing key can only be the budget.
	if err := store.PutShard(t.Context(), testShardKey, "etag", testShardDoc); err != nil {
		t.Fatal(err)
	}

	doc, status, elapsed := statsDoc(t, srv.URL+"/v1/stats")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when the hint is unavailable", status)
	}
	if _, ok := doc["hotShards"]; ok {
		t.Errorf("hotShards = %v, want the key omitted rather than waited for", doc["hotShards"])
	}
	if _, ok := doc["packages"]; !ok {
		t.Errorf("the stats document itself was dropped along with the hint: %v", doc)
	}
	if elapsed > 2*time.Second {
		t.Errorf("GET /v1/stats took %s with the hint blocked; the request was not bounded", elapsed)
	}

	// The read the request stopped waiting for is not abandoned: it finishes
	// on its own budget and a later poll carries the hint without paying for
	// a second whole-corpus scan.
	close(store.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		doc, status, _ = statsDoc(t, srv.URL+"/v1/stats")
		if status != http.StatusOK {
			t.Fatalf("status = %d after the hint became available", status)
		}
		if _, ok := doc["hotShards"]; ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the completed hint never reached a later poll: %v", doc)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := store.calls.Load(); got != 1 {
		t.Errorf("whole-corpus hint reads = %d, want the one abandoned read reused", got)
	}
}

// TestConcurrentStatsRequestsShareOneHotShardScan pins the other half: the
// fleet polls this endpoint, and each poll used to start its own four
// whole-corpus reads.
func TestConcurrentStatsRequestsShareOneHotShardScan(t *testing.T) {
	store := newBlockingHotShards()
	a := &api{d: Deps{Store: store, hotShardWait: time.Minute}}
	const callers = 8
	start := make(chan struct{})
	entered := make(chan struct{}, callers)
	returned := make(chan int, callers)
	for range callers {
		go func() {
			<-start
			entered <- struct{}{}
			returned <- len(a.hotShardKeys(context.Background()))
		}()
	}
	close(start)
	for range callers {
		<-entered
	}
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the hint read never started")
	}
	// Let every released caller reach the gate before the assertion, so a
	// second read cannot hide behind a quick completion.
	time.Sleep(25 * time.Millisecond)
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("concurrent hint reads = %d, want 1", got)
	}
	close(store.release)
	for range callers {
		select {
		case <-returned:
		case <-time.After(5 * time.Second):
			t.Fatal("a caller never returned after the hint read finished")
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Errorf("hint reads after release = %d, want 1", got)
	}
}

// TestHotShardHintIsReusedForItsTTLThenRefreshed: shards are built by the
// aggregation pass on CSX_SNAPSHOT_INTERVAL, so recomputing the hint per
// request bought nothing and cost four whole-corpus reads.
func TestHotShardHintIsReusedForItsTTLThenRefreshed(t *testing.T) {
	store := &countingHotShards{Fake: serverstore.NewFake()}
	ck := &clock{t: testNow}
	a := &api{d: Deps{Store: store, Now: ck.now}}
	if err := store.PutShard(t.Context(), testShardKey, "etag", testShardDoc); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if keys := a.hotShardKeys(t.Context()); len(keys) != 1 {
			t.Fatalf("call %d: hint = %v, want the one built shard key", i+1, keys)
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Errorf("hint reads inside the TTL = %d, want 1", got)
	}

	ck.t = ck.t.Add(hotShardTTL + time.Second)
	if keys := a.hotShardKeys(t.Context()); len(keys) != 1 {
		t.Fatalf("hint after the TTL = %v, want the built shard key", keys)
	}
	if got := store.calls.Load(); got != 2 {
		t.Errorf("hint reads after the TTL = %d, want a refresh", got)
	}
}

// TestACanceledStatsCallerDoesNotAbandonTheSharedHotShardRead: the read is
// shared, so a client that hangs up must not cancel it out from under the
// callers still waiting, nor make the next poll pay for it again.
func TestACanceledStatsCallerDoesNotAbandonTheSharedHotShardRead(t *testing.T) {
	store := newBlockingHotShards()
	a := &api{d: Deps{Store: store, hotShardWait: time.Minute}}
	if err := store.PutShard(t.Context(), testShardKey, "etag", testShardDoc); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	gone := make(chan int, 1)
	go func() { gone <- len(a.hotShardKeys(ctx)) }()
	select {
	case <-store.started:
	case <-time.After(2 * time.Second):
		t.Fatal("the hint read never started")
	}
	cancel()
	select {
	case got := <-gone:
		if got != 0 {
			t.Fatalf("a canceled caller returned %d keys", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a canceled caller kept waiting for the shared read")
	}

	close(store.release)
	if keys := a.hotShardKeys(t.Context()); len(keys) != 1 {
		t.Fatalf("hint after the abandoned read finished = %v, want the built shard key", keys)
	}
	if got := store.calls.Load(); got != 1 {
		t.Errorf("hint reads = %d, want the canceled caller's read reused", got)
	}
}
