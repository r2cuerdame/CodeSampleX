package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// A 202 response is allowed to accept some aggregates and refuse others.
// The accepted count is real progress, but the refused aggregate is still
// locally owned evidence and must remain pending for a later retry.
func TestUploadPartialAcceptanceKeepsOnlyRefusedRowsPending(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()
	var calls atomic.Int64
	var firstPosted []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batches []map[string]any `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upload: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if calls.Add(1) == 1 {
			firstPosted = body.Batches
			fmt.Fprintf(w, `{"accepted":%d,"rejected":[{"index":1,"reason":"unsupported symbol"}]}`,
				len(body.Batches)-1)
			return
		}
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
	}))
	defer srv.Close()

	cfg := communityCfg(srv.URL)
	recorder := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	if err := recorder.RecordRun(ctx, t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	batcher := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	sent, err := batcher.Upload(ctx, srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("partial refusal was not reported")
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want the one accepted aggregate", sent)
	}
	if len(firstPosted) != 2 {
		t.Fatalf("first upload carried %d aggregates, want 2", len(firstPosted))
	}
	pending := pendingRows(t, db)
	if len(pending) != 1 {
		t.Fatalf("pending rows = %d, want only the refused aggregate", len(pending))
	}
	if got, want := pending[0].Symbol, fmt.Sprint(firstPosted[1]["symbol"]); got != want {
		t.Fatalf("pending symbol = %q, want refused posted symbol %q", got, want)
	}

	sent, err = batcher.Upload(ctx, srv.Client(), srv.URL)
	if err != nil || sent != 1 {
		t.Fatalf("retry Upload = (%d, %v), want refused aggregate accepted", sent, err)
	}
	if pending := pendingRows(t, db); len(pending) != 0 {
		t.Fatalf("retry left %d aggregate(s) pending", len(pending))
	}
}
