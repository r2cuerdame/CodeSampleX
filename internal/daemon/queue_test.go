package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/config"
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

// Local-only mode uploads nothing, ever.
func TestLocalOnlyNeverDrainsTheQueue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("local-only mode contacted the server")
	}))
	defer srv.Close()
	d := queueDaemon(t, "local-only", srv.URL)
	ctx := context.Background()
	if _, err := d.DB.Enqueue(ctx, "adoption", `{"schemaVersion":1}`); err != nil {
		t.Fatal(err)
	}
	if n, err := d.drainQueue(ctx); n != 0 || err != nil {
		t.Fatalf("drained %d err=%v", n, err)
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
	t.Cleanup(func() { _ = d.Close() })
	return d
}
