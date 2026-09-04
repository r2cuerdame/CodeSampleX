package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Farm convergence is interested in delivery, not cache warming. Even when
// the server advertises a large hot-shard set, an empty upload-only sync must
// do no network work and finish without walking that set.
func TestUploadOnlySyncWithEmptyQueueDoesNotWarmOrContactServer(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()
	d := queueDaemon(t, "community", srv.URL)

	res := d.FlushNow(t.Context())
	if res.WarmedKeys != 0 || res.UploadedBatches != 0 || res.UploadedReports != 0 || len(res.Errors) != 0 {
		t.Fatalf("empty upload-only sync = %+v", res)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("empty upload-only sync made %d network request(s), want 0", got)
	}
}

// One evidence aggregate and one typed report are real upload backlog. A
// failed shard pull is not. This synthetic fixture fixes both the counter
// meaning and durable delivery: the two pending counts fall to zero after the
// server acknowledges them while the shard failure remains only an error.
func TestQueueCountsSeparateDurableUploadsFromShardFailure(t *testing.T) {
	var evidenceAccepted, reportsAccepted atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/stats":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"hotShards":["npm/missing/1"]}`)
		case strings.HasPrefix(r.URL.Path, "/v1/shards/"):
			http.Error(w, "shard unavailable", http.StatusInternalServerError)
		case r.URL.Path == "/v1/evidence/batches":
			var body struct {
				Batches []json.RawMessage `json:"batches"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			evidenceAccepted.Add(int64(len(body.Batches)))
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
		case r.URL.Path == "/v1/adoptions":
			reportsAccepted.Add(1)
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	d := queueDaemon(t, "community", srv.URL)
	seedPendingObservation(t, d.DB, "pkg:npm/evidence@1.0.0", "fixture")
	if _, err := d.DB.Enqueue(t.Context(), "adoption", `{"schemaVersion":1}`); err != nil {
		t.Fatal(err)
	}

	before, err := d.queueCounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if before.EvidenceBatches != 1 || before.Uploads != 1 || before.UploadsByKind["adoption"] != 1 {
		t.Fatalf("before queue counts = %+v, want one evidence + one adoption", before)
	}

	res := d.SyncNow(t.Context())
	if evidenceAccepted.Load() != 1 || reportsAccepted.Load() != 1 {
		t.Fatalf("server accepted evidence=%d reports=%d, want 1 each", evidenceAccepted.Load(), reportsAccepted.Load())
	}
	if len(res.Errors) == 0 || !strings.Contains(strings.Join(res.Errors, "\n"), "shard") {
		t.Fatalf("failed shard pull was not reported: %+v", res)
	}
	after, err := d.queueCounts(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.EvidenceBatches != 0 || after.Uploads != 0 {
		t.Fatalf("durably accepted uploads stayed pending: %+v", after)
	}
	if _, exists := after.UploadsByKind["shard"]; exists {
		t.Fatalf("shard pull failure appeared in upload queue: %+v", after)
	}
}
