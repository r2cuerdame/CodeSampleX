package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Housekeeping may not stop the upload.
//
// Reported from csx-farm-linux-1 (CodeSampleX-Farm#15): five consecutive
// converges flushed 0 items while the queue stayed full. One slot had a
// pending depth of ONE and still hit its 300-second limit every time, and
// `csx stats` named the cause:
//
//	Last upload error: evidence: reconcile legacy Windows exit codes: context canceled
//
// The reconciliation pass runs before every upload and its error returns
// straight out of Upload, so a pass that cannot finish inside the caller's
// budget stops every batch behind it -- forever, because the pass gets no
// faster and the budget does not grow. Measured on that node: one csx sync
// burned 1,245 seconds of CPU across 2,721 seconds and printed nothing.
//
// The reconciliation is a correction to old rows. The upload is the product.
// A correction that has not finished is a reason to try again later, never a
// reason to deliver nothing.
func TestAStalledReconciliationDoesNotStopTheUpload(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()

	posted := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batches []map[string]any `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		posted += len(body.Batches)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
	}))
	defer srv.Close()

	cfg := communityCfg(srv.URL)
	recorder := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	if err := recorder.RecordRun(ctx, t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	batcher := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	// A reconciliation that cannot finish, exactly as an expiring budget
	// makes it look.
	batcher.reconcile = func(context.Context) error { return context.Canceled }

	sent, err := batcher.Upload(ctx, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("a stalled reconciliation failed the whole upload: %v", err)
	}
	if sent == 0 || posted == 0 {
		t.Fatalf("nothing was uploaded behind the stalled reconciliation: sent=%d posted=%d", sent, posted)
	}
	// It must not go quiet about it either: a node that cannot see why its
	// queue is not draining cannot act on it.
	if note := batcher.LastReconcileNote(); note == "" || !strings.Contains(note, "reconcil") {
		t.Errorf("the stalled reconciliation left no note: %q", note)
	}
}

// And it is bounded, so it cannot spend the caller's whole window before the
// upload begins. The node measured 1,245 CPU-seconds inside one pass.
func TestTheReconciliationIsBounded(t *testing.T) {
	db := testDB(t)
	batcher := &Batcher{DB: db, Ident: testIdentity(t), Cfg: communityCfg("http://127.0.0.1:0")}
	started := time.Now()
	blocked := make(chan struct{})
	batcher.reconcile = func(ctx context.Context) error {
		<-ctx.Done() // never finishes on its own
		close(blocked)
		return ctx.Err()
	}
	_ = batcher.runReconcile(context.Background())
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("the reconciliation was not cancelled by its own budget")
	}
	if elapsed := time.Since(started); elapsed > reconcileBudget+2*time.Second {
		t.Errorf("the reconciliation ran %v against a %v budget", elapsed, reconcileBudget)
	}
}
