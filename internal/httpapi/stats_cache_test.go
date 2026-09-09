package httpapi

// GET /v1/stats served the daily rollup by reading it from PostgreSQL on
// every single request, and answered 503 "database busy" whenever that read
// was refused. Both halves are wrong for this document.
//
// The rollup changes at most once per builder pass -- in the 2026-09-09 #174
// incident production's carried generatedAt 2026-09-08T19:49:36Z for over a
// day -- so the per-request read spends a starved database to be told the
// same thing every time, and it is the ONLY thing on the endpoint that can
// fail: withHotShards is already bounded and omits its hint rather than
// failing the request. Live probes during that incident measured /v1/stats at
// 5 of 6 requests 503 while the endpoint had a perfectly good answer from
// seconds earlier.
//
// So the last rollup this process read is remembered, and backpressure serves
// it rather than refusing. Staleness is visible in the document's own
// generatedAt; a 503 is not more honest than a rollup a few minutes old, it
// is just less useful. Nothing is fabricated: with nothing yet cached the
// endpoint still reports the pressure.

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// flakyStatsStore answers GetLatestStats from its own canned document so a
// test controls both the value and the failure independently of the fake.
type flakyStatsStore struct {
	serverstore.Store
	mu    sync.Mutex
	calls int
	doc   string
	fail  error
}

func (s *flakyStatsStore) GetLatestStats(context.Context) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.fail != nil {
		return "", false, s.fail
	}
	return s.doc, true, nil
}

func (s *flakyStatsStore) set(doc string, fail error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doc, s.fail = doc, fail
}

func (s *flakyStatsStore) readCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func statsRollup(packages int) string {
	return fmt.Sprintf(`{"day":"2026-09-08","packages":%d,"generatedAt":"2026-09-08T19:49:36Z"}`, packages)
}

func poolBusy() error { return fmt.Errorf("%w (test)", serverstore.ErrPoolBusy) }

// newStatsCacheServer mounts the API over a flakyStatsStore with a known
// rollup interval, so the cache's lifetime is the builder cadence rather than
// the package default.
func newStatsCacheServer(t *testing.T, interval time.Duration) (string, *flakyStatsStore, *clock) {
	t.Helper()
	store := &flakyStatsStore{doc: statsRollup(3148)}
	srv, fake, ck := newTestServer(t, func(d *Deps) {
		store.Store = d.Store
		d.Store = store
		d.Cfg.SnapshotInterval = interval
	})
	_ = fake
	return srv.URL, store, ck
}

// The incident behaviour: the endpoint had an answer and refused anyway.
func TestStatsServesTheCachedRollupWhilePressureRefusesTheRead(t *testing.T) {
	url, store, ck := newStatsCacheServer(t, 5*time.Minute)

	doc, status, _ := statsDoc(t, url+"/v1/stats")
	if status != http.StatusOK {
		t.Fatalf("first status = %d, want 200", status)
	}
	if doc["packages"] != float64(3148) {
		t.Fatalf("first packages = %v, want 3148", doc["packages"])
	}

	// Expire the entry first, so these requests genuinely attempt the read and
	// are refused. A still-fresh cache would pass this test without the
	// pressure path ever running.
	ck.t = ck.t.Add(6 * time.Minute)
	store.set("", poolBusy())

	for i := range 3 {
		doc, status, _ = statsDoc(t, url+"/v1/stats")
		if status != http.StatusOK {
			t.Fatalf("request %d under pressure: status = %d, want the cached rollup with 200", i, status)
		}
		if doc["packages"] != float64(3148) {
			t.Fatalf("request %d under pressure: packages = %v, want the last rollup 3148", i, doc["packages"])
		}
		if doc["generatedAt"] != "2026-09-08T19:49:36Z" {
			t.Fatalf("request %d: generatedAt = %v; the served document must carry its own age",
				i, doc["generatedAt"])
		}
	}
}

// The read this endpoint makes is per request, and the fleet polls it. Inside
// one builder cadence there is nothing new to read.
func TestStatsDoesNotRereadTheRollupForEveryRequest(t *testing.T) {
	url, store, _ := newStatsCacheServer(t, 5*time.Minute)

	for range 5 {
		if _, status, _ := statsDoc(t, url+"/v1/stats"); status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
	}
	if got := store.readCount(); got != 1 {
		t.Fatalf("GetLatestStats calls = %d for 5 requests inside one interval, want 1", got)
	}
}

// Cached is not frozen: past the builder cadence the next request picks up a
// pass that has since completed.
func TestStatsRefreshesAfterTheBuilderCadence(t *testing.T) {
	url, store, ck := newStatsCacheServer(t, 5*time.Minute)

	if doc, _, _ := statsDoc(t, url+"/v1/stats"); doc["packages"] != float64(3148) {
		t.Fatalf("packages = %v, want 3148", doc["packages"])
	}
	store.set(statsRollup(3200), nil)

	// Still inside the interval: the endpoint is entitled to the cached value.
	if doc, _, _ := statsDoc(t, url+"/v1/stats"); doc["packages"] != float64(3148) {
		t.Fatalf("packages = %v inside the interval, want the cached 3148", doc["packages"])
	}

	ck.t = ck.t.Add(6 * time.Minute)
	doc, status, _ := statsDoc(t, url+"/v1/stats")
	if status != http.StatusOK {
		t.Fatalf("status = %d after the interval, want 200", status)
	}
	if doc["packages"] != float64(3200) {
		t.Fatalf("packages = %v after the interval, want the refreshed 3200", doc["packages"])
	}
}

// Serving what was read is not the same as inventing something to serve. With
// nothing cached the pressure is still the answer.
func TestStatsStillReportsPressureWithNothingCached(t *testing.T) {
	url, store, _ := newStatsCacheServer(t, 5*time.Minute)
	store.set("", poolBusy())

	resp, err := http.Get(url + "/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d with nothing cached, want 503", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("a 503 from backpressure must keep its Retry-After")
	}
}

// A failing read must not be repeated once per caller: that is the stampede
// that makes a starved database worse at exactly the wrong moment.
func TestStatsBoundsRereadsWhileTheReadKeepsFailing(t *testing.T) {
	url, store, _ := newStatsCacheServer(t, 5*time.Minute)
	store.set("", poolBusy())

	for range 6 {
		resp, err := http.Get(url + "/v1/stats")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if got := store.readCount(); got > 2 {
		t.Fatalf("GetLatestStats calls = %d for 6 failing requests, want the retries bounded", got)
	}
}

// Backpressure is served from cache; a genuine store fault is not laundered
// into a stale 200. The two have different meanings and different statuses.
func TestStatsDoesNotServeCacheForANonPressureFault(t *testing.T) {
	url, store, ck := newStatsCacheServer(t, 5*time.Minute)
	if _, status, _ := statsDoc(t, url+"/v1/stats"); status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}

	ck.t = ck.t.Add(6 * time.Minute)
	store.set("", fmt.Errorf("relation \"stats_daily\" does not exist"))
	resp, err := http.Get(url + "/v1/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d for a genuine fault, want 500 rather than a cached 200", resp.StatusCode)
	}
}

// hangupStatsStore holds its read open until released, and records whether the
// context it was handed was cancelled underneath it.
type hangupStatsStore struct {
	serverstore.Store
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
	mu        sync.Mutex
	sawCancel bool
	doc       string
}

func (s *hangupStatsStore) GetLatestStats(ctx context.Context) (string, bool, error) {
	s.once.Do(func() { close(s.entered) })
	select {
	case <-s.release:
		return s.doc, true, nil
	case <-ctx.Done():
		s.mu.Lock()
		s.sawCancel = true
		s.mu.Unlock()
		return "", false, ctx.Err()
	}
}

func (s *hangupStatsStore) cancelled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sawCancel
}

// A client hanging up is not a fault in the server, and a starved box produces
// it constantly: the #174 operator probes gave up at 12s while TTFB ran to
// 6.8s. This read is SHARED -- one caller performs it while every other caller
// waits behind the same lock -- so running it on that one caller's request
// context let a single disconnect cancel the read for all of them. The
// cancellation is then remembered as a fault, and deliberately is not
// backpressure (IsQueryTimeout matches the timeout message so that an operator
// reading "query timeout" is reading about a ceiling this code set), so the
// callers still waiting were answered 500 "stats lookup failed" with a
// perfectly good rollup in hand. The client that left cannot be served; the
// ones still here must not be told the server is broken.
func TestStatsReadSurvivesTheClientThatHangsUp(t *testing.T) {
	store := &hangupStatsStore{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		doc:     statsRollup(3148),
	}
	srv, _, _ := newTestServer(t, func(d *Deps) {
		store.Store = d.Store
		d.Store = store
		d.Cfg.SnapshotInterval = 5 * time.Minute
	})

	ctx, cancel := context.WithCancel(context.Background())
	hungUp := make(chan struct{})
	go func() {
		defer close(hungUp)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/stats", nil)
		if err != nil {
			return
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	}()

	<-store.entered // the shared read is in flight
	cancel()        // ...and the caller that started it hangs up
	<-hungUp
	close(store.release) // the database answers, as it was always going to

	doc, status, _ := statsDoc(t, srv.URL+"/v1/stats")
	if store.cancelled() {
		t.Error("one client hanging up cancelled the read every other caller was waiting on")
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d after another client hung up, want 200 with the rollup", status)
	}
	if doc["packages"] != float64(3148) {
		t.Fatalf("packages = %v, want the rollup 3148", doc["packages"])
	}
}
