package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/evidence"
)

// report_sample_adoption enqueued a row, `csx sync` printed "uploaded
// batches: 0" and exited 0, and the row stayed in upload_queue forever —
// nothing in the codebase read that table, and the server had no route to
// receive one. The far end of the loop the product describes, ask →
// verified answer → report whether it worked, was never connected, which
// is why the site's post-hit success rate was a hardcoded zero.
func TestSyncDrainsTheAdoptionQueue(t *testing.T) {
	var got atomic.Int64
	var body atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/adoptions" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		body.Store(m)
		got.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := queueDaemon(t, "community", srv.URL)
	ctx := context.Background()

	payload := `{"schemaVersion":1,"evidenceClass":"ADOPTION_EVIDENCE","epoch":"2026-08-15",` +
		`"anonId":"anon1","sampleId":"sha256:` + strings.Repeat("aa", 32) + `","applied":true,"buildPass":true}`
	if _, err := d.DB.Enqueue(ctx, "adoption", payload); err != nil {
		t.Fatal(err)
	}

	n, err := d.drainQueue(ctx)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if n != 1 || got.Load() != 1 {
		t.Fatalf("drained %d, server saw %d — the queue is still not being uploaded", n, got.Load())
	}
	if m, _ := body.Load().(map[string]any); m["applied"] != true {
		t.Errorf("the report reached the server without its outcome: %v", m)
	}

	// Delivered items leave the queue, or every sync re-sends them.
	items, err := d.DB.QueuePending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("%d items still queued after a successful upload", len(items))
	}
}

// A failed delivery keeps the report. It is the one signal that cannot be
// recomputed from anything else, so a server being briefly down must not
// discard it.
func TestAFailedReportStaysQueued(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	d := queueDaemon(t, "community", srv.URL)
	ctx := context.Background()
	if _, err := d.DB.Enqueue(ctx, "adoption", `{"schemaVersion":1}`); err != nil {
		t.Fatal(err)
	}
	if n, err := d.drainQueue(ctx); n != 0 || err == nil {
		t.Fatalf("drained %d err=%v, want 0 and an error", n, err)
	}
	if items, _ := d.DB.QueuePending(ctx, 10); len(items) != 1 {
		t.Errorf("the report was dropped on a server error: %d left", len(items))
	}
}

// A shared write limiter throttles the endpoint after 60 requests.  A
// throttle is not a failed report and must stop the pass without consuming
// retries for this row or hammering every later row into the same window.
func TestRateLimitStopsDrainWithoutConsumingRetries(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	d := queueDaemon(t, "community", srv.URL)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := d.DB.Enqueue(ctx, "adoption", `{"schemaVersion":1}`); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := d.drainQueue(ctx); n != 0 || err == nil {
		t.Fatalf("drained=%d err=%v, want throttle", n, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls=%d, want one then stop", calls.Load())
	}
	items, err := d.DB.QueuePending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Attempts != 0 {
			t.Fatalf("throttle consumed retry: %+v", item)
		}
	}
}

func TestWantedBacklogUsesOneBoundedBatchPerDrain(t *testing.T) {
	var calls atomic.Int64
	var mu sync.Mutex
	var reportCounts []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/wanted/batches" {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		calls.Add(1)
		var batch struct {
			Reports []json.RawMessage `json:"reports"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Errorf("decode batch: %v", err)
		}
		mu.Lock()
		reportCounts = append(reportCounts, len(batch.Reports))
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := queueDaemon(t, "community", srv.URL)
	ctx := context.Background()
	for i := 0; i < 75; i++ {
		payload := fmt.Sprintf(`{"schemaVersion":1,"epoch":"2026-08-17","anonId":"%016x",`+
			`"packages":["pkg:npm/three@0.180.0"]}`, i)
		if _, err := d.DB.Enqueue(ctx, "wanted", payload); err != nil {
			t.Fatal(err)
		}
	}
	want := []int{20, 20, 20, 15}
	for i, count := range want {
		n, err := d.drainQueue(ctx)
		if err != nil || n != count {
			t.Fatalf("drain %d: drained=%d err=%v, want %d", i, n, err, count)
		}
	}
	if calls.Load() != int64(len(want)) {
		t.Fatalf("batch calls=%d, want %d", calls.Load(), len(want))
	}
	mu.Lock()
	gotCounts := append([]int(nil), reportCounts...)
	mu.Unlock()
	if fmt.Sprint(gotCounts) != fmt.Sprint(want) {
		t.Fatalf("reports per batch=%v, want %v", gotCounts, want)
	}
	if items, _ := d.DB.QueuePending(ctx, 100); len(items) != 0 {
		t.Fatalf("wanted backlog left=%d", len(items))
	}
}

func TestWantedBatchServerFailureDoesNotFanOutOrConsumeAttempts(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	d := queueDaemon(t, "community", srv.URL)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := d.DB.Enqueue(ctx, "wanted", `{"schemaVersion":1,"epoch":"2026-08-17",`+
			`"anonId":"0123456789abcdef","packages":["pkg:npm/three@0.180.0"]}`); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := d.drainQueue(ctx); n != 0 || err == nil {
		t.Fatalf("drained=%d err=%v, want channel failure", n, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("server calls=%d, want one batch only", calls.Load())
	}
	items, err := d.DB.QueuePending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Attempts != 0 {
			t.Fatalf("batch outage consumed report retry: %+v", item)
		}
	}
}

func TestWantedCandidateIsFilteredBeforeItCanLeaveTheMachine(t *testing.T) {
	var serverCalls atomic.Int64
	var uploaded atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalls.Add(1)
		var batch struct {
			Reports []map[string]any `json:"reports"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Errorf("decode batch: %v", err)
		} else if len(batch.Reports) != 1 {
			t.Errorf("reports=%d, want 1", len(batch.Reports))
		} else {
			uploaded.Store(batch.Reports[0])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := queueDaemon(t, "community", srv.URL)
	d.WantedPublic = func(_ context.Context, p domain.PURL) bool {
		return p.Ecosystem == "npm" && p.Name == "three" && p.Version == "0.180.0"
	}
	payload := `{"schemaVersion":1,"epoch":"2026-08-17","anonId":"0123456789abcdef",` +
		`"packages":["pkg:npm/three@0.180.0","pkg:npm/acme-private@1.0.0"],` +
		`"symbols":["CanvasTexture"],"query":"C:\\secret\\project"}`
	if _, err := d.DB.Enqueue(t.Context(), evidence.WantedCandidateQueueKind, payload); err != nil {
		t.Fatal(err)
	}

	if n, err := d.drainQueue(t.Context()); err != nil || n != 1 {
		t.Fatalf("drained=%d err=%v", n, err)
	}
	if serverCalls.Load() != 1 {
		t.Fatalf("server calls=%d, want 1", serverCalls.Load())
	}
	report, _ := uploaded.Load().(map[string]any)
	packages, _ := report["packages"].([]any)
	if len(packages) != 1 || packages[0] != "pkg:npm/three@0.180.0" {
		t.Fatalf("uploaded packages=%v", packages)
	}
	if _, leaked := report["query"]; leaked {
		t.Fatal("unknown local query field crossed the upload boundary")
	}
	if symbols, ok := report["symbols"].([]any); ok && len(symbols) != 0 {
		t.Fatalf("ambiguous multi-package symbols were uploaded: %v", symbols)
	}
}

func TestUnconfirmedWantedCandidateNeverContactsCSXServer(t *testing.T) {
	var serverCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := queueDaemon(t, "community", srv.URL)
	d.WantedPublic = func(context.Context, domain.PURL) bool { return false }
	payload := `{"schemaVersion":1,"epoch":"2026-08-17","anonId":"0123456789abcdef",` +
		`"packages":["pkg:npm/acme-private@1.0.0"]}`
	if _, err := d.DB.Enqueue(t.Context(), evidence.WantedCandidateQueueKind, payload); err != nil {
		t.Fatal(err)
	}

	if n, err := d.drainQueue(t.Context()); err != nil || n != 0 {
		t.Fatalf("drained=%d err=%v", n, err)
	}
	if serverCalls.Load() != 0 {
		t.Fatalf("unconfirmed candidate reached CSX server %d time(s)", serverCalls.Load())
	}
	items, err := d.DB.QueuePending(t.Context(), 10)
	if err != nil || len(items) != 1 || items[0].Attempts != 1 {
		t.Fatalf("candidate retry state=%+v err=%v", items, err)
	}
}

func TestWantedPublicnessCheckIsBoundedByDrainContext(t *testing.T) {
	var serverCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	d := queueDaemon(t, "community", srv.URL)
	d.WantedPublic = func(ctx context.Context, _ domain.PURL) bool {
		<-ctx.Done()
		return false
	}
	payload := `{"schemaVersion":1,"epoch":"2026-08-17","anonId":"0123456789abcdef",` +
		`"packages":["pkg:npm/three@0.180.0"]}`
	if _, err := d.DB.Enqueue(t.Context(), evidence.WantedCandidateQueueKind, payload); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	if n, err := d.drainQueue(ctx); n != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("drained=%d err=%v, want context deadline", n, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("publicness check ignored drain deadline: %s", elapsed)
	}
	if serverCalls.Load() != 0 {
		t.Fatalf("timed-out candidate reached CSX server %d time(s)", serverCalls.Load())
	}
}

// Local-only mode uploads nothing, ever.
func TestLocalOnlyNeverDrainsTheQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("local-only mode contacted the server")
	}))
	defer srv.Close()
	d := queueDaemon(t, "local-only", srv.URL)
	var checks atomic.Int64
	d.WantedPublic = func(context.Context, domain.PURL) bool {
		checks.Add(1)
		return true
	}
	ctx := context.Background()
	if _, err := d.DB.Enqueue(ctx, "adoption", `{"schemaVersion":1}`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Enqueue(ctx, evidence.WantedCandidateQueueKind,
		`{"schemaVersion":1,"epoch":"2026-08-17","anonId":"0123456789abcdef",`+
			`"packages":["pkg:npm/three@0.180.0"]}`); err != nil {
		t.Fatal(err)
	}
	if n, err := d.drainQueue(ctx); n != 0 || err != nil {
		t.Fatalf("drained %d err=%v", n, err)
	}
	if checks.Load() != 0 {
		t.Fatalf("local-only mode made %d registry check(s)", checks.Load())
	}
}

// queueDaemon builds a daemon over a throwaway home, without running it:
// drainQueue needs the stores and the config, not the listeners.
func queueDaemon(t *testing.T, mode, serverURL string) *Daemon {
	t.Helper()
	home := newTestHome(t, func(c *config.Config) {
		c.Mode = mode
		c.ServerURL = serverURL
	})
	d, err := New(home)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	// Queue tests opt in to a deterministic public registry. Individual
	// privacy tests override this seam; no test reaches the real network.
	d.WantedPublic = func(context.Context, domain.PURL) bool { return true }
	t.Cleanup(func() { _ = d.Close() })
	return d
}
