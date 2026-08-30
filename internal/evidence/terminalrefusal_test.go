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

// refusingServer answers every batch with one refusal, using the reply the
// caller supplies. calls counts how many uploads it saw, which is how a test
// tells "stopped being retried" from "kept coming back".
func refusingServer(t *testing.T, reply string, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batches []map[string]any `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"accepted":0,"rejected":[%s]}`, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A refusal the server calls terminal stops being sent, and leaves a
// tombstone saying what was refused and why.
//
// Restoring it to pending is what pinned production's queue at its 1,000 cap:
// 7,432 batches sent, 852 refused, and the same refusals coming back on every
// sync — crowding out the batches that would have been accepted and making a
// real delivery failure indistinguishable from a server decision.
func TestATerminalRefusalStopsBeingRetriedAndLeavesATombstone(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()
	var calls atomic.Int64
	srv := refusingServer(t,
		`{"index":0,"reason":"package is not public (PRIVATE)","code":"not-public","terminal":true}`, &calls)

	cfg := communityCfg(srv.URL)
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	if err := rec.RecordRun(ctx, t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatal(err)
	}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	if _, err := b.Upload(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("a refusal was not reported to the caller")
	}
	first := calls.Load()

	n, err := db.RefusedEvidenceCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("a terminal refusal left no tombstone; the evidence is simply gone")
	}
	rows, err := db.RefusedEvidenceRows(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Code != "not-public" || rows[0].Key.PURL == "" || rows[0].RefusedAt == "" {
		t.Errorf("tombstone = %+v, want the reason, the coordinate and the time", rows[0])
	}

	// The next drain must not carry it again.
	_, _ = b.Upload(ctx, srv.Client(), srv.URL)
	if got := calls.Load(); got > first {
		t.Errorf("the refused batch was posted again (%d uploads, was %d); "+
			"this is the loop that pinned the queue at its cap", got, first)
	}
}

// And the distinction the issue turns on: UNKNOWN is the server saying it
// could not check, not that the package is private. Treating it as final
// would discard evidence about a public package nobody had budget to confirm
// — and the budget is per request, so on a deep backlog that is most of them.
func TestPublicnessUnknownIsRetriedNotDiscarded(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()
	var calls atomic.Int64
	srv := refusingServer(t,
		`{"index":0,"reason":"package publicness not determined (UNKNOWN)","code":"publicness-unknown","terminal":false}`, &calls)

	cfg := communityCfg(srv.URL)
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	if err := rec.RecordRun(ctx, t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatal(err)
	}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	if _, err := b.Upload(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("a refusal was not reported")
	}
	if n, err := db.RefusedEvidenceCount(ctx); err != nil || n != 0 {
		t.Fatalf("tombstones = %d (err %v), want none: the server never decided", n, err)
	}
	first := calls.Load()
	_, _ = b.Upload(ctx, srv.Client(), srv.URL)
	if calls.Load() <= first {
		t.Error("an undetermined package was not retried; evidence about it is lost")
	}
}

// An older server sends no terminal field at all. That must read as
// retryable, so a client talking to one behaves exactly as it did before.
func TestARefusalWithoutTheFieldIsRetryable(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()
	var calls atomic.Int64
	srv := refusingServer(t, `{"index":0,"reason":"unsupported symbol"}`, &calls)

	cfg := communityCfg(srv.URL)
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	if err := rec.RecordRun(ctx, t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatal(err)
	}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	if _, err := b.Upload(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("a refusal was not reported")
	}
	if n, _ := db.RefusedEvidenceCount(ctx); n != 0 {
		t.Errorf("tombstones = %d from a server that never said terminal", n)
	}
	first := calls.Load()
	_, _ = b.Upload(ctx, srv.Client(), srv.URL)
	if calls.Load() <= first {
		t.Error("an unclassified refusal stopped being retried")
	}
}
