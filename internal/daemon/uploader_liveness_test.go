package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func TestDaemonLivingLessThanUploadEveryStillFlushesEvidence(t *testing.T) {
	uploaded := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/evidence/batches" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Batches []json.RawMessage `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		select {
		case uploaded <- len(body.Batches):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
	}))
	defer srv.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = srv.URL
	})
	d, err := New(home)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	seedPendingObservation(t, d.DB, "pkg:npm/first-upload@1.0.0", "first_upload")
	d.uploadFirstDelay = 25 * time.Millisecond
	d.uploadEvery = time.Hour

	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	started := time.Now()
	go func() { runDone <- d.Run(runCtx) }()
	select {
	case <-d.Ready():
	case err := <-runDone:
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not become ready")
	}

	select {
	case n := <-uploaded:
		if n != 1 {
			t.Fatalf("first upload carried %d aggregates, want 1", n)
		}
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("daemon waited for uploadEvery instead of its bounded first delay")
	}
	// The flush includes READING the ack. Cancelling the moment the server
	// saw the request races the response read — and the uploader marks rows
	// uploaded BEFORE the post, so a zero pending count says only that a
	// request is in flight. A delivery whose ack was never read is
	// ambiguous, and the uploader now restores it to pending on purpose
	// (the dedup ledger makes the re-send safe), where it used to lose it
	// because the restore ran on the already-dead context. statLastUpload
	// is stamped after the ack is processed; that is the settle signal.
	settle := time.Now().Add(2 * time.Second)
	for {
		if _, ok, _ := d.DB.GetStat(t.Context(), statLastUpload); ok {
			break
		}
		if time.Now().After(settle) {
			cancel()
			t.Fatal("the upload never stamped statLastUpload, so its ack was never processed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("daemon shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon shutdown was not bounded")
	}
	if elapsed := time.Since(started); elapsed >= d.uploadEvery {
		t.Fatalf("daemon lived %s, not less than uploadEvery %s", elapsed, d.uploadEvery)
	}
	if pending, err := d.DB.PendingObservations(t.Context(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("pending after first-delay upload = %d, err=%v", len(pending), err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPartialEvidenceUploadStampsOnlyAcceptedBatches(t *testing.T) {
	var posted []domain.ObservationBatch
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batches []domain.ObservationBatch `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		posted = body.Batches
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"accepted":1,"rejected":[{"index":1,"reason":"unsupported symbol"}]}`)
	}))
	defer srv.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = srv.URL
	})
	d, err := New(home)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	seedPendingObservation(t, d.DB, "pkg:npm/accepted@1.0.0", "accepted")
	seedPendingObservation(t, d.DB, "pkg:npm/refused@1.0.0", "refused")

	sent, err := d.uploadNow(t.Context())
	if err == nil {
		t.Fatal("partial refusal was not reported")
	}
	if sent != 1 {
		t.Fatalf("sent = %d, want only the accepted batch", sent)
	}
	if len(posted) != 2 {
		t.Fatalf("posted batches = %d, want 2", len(posted))
	}
	last, ok, statErr := d.DB.GetStat(t.Context(), statLastUpload)
	if statErr != nil || !ok || last == "" {
		t.Fatalf("lastUpload = %q, ok=%v, err=%v", last, ok, statErr)
	}
	evidenceSent, ok, statErr := d.DB.GetStat(t.Context(), statEvidenceSent)
	if statErr != nil || !ok || evidenceSent != strconv.Itoa(sent) {
		t.Fatalf("evidenceSent = %q, ok=%v, err=%v; want %d", evidenceSent, ok, statErr, sent)
	}
	pending, pendingErr := d.DB.PendingObservations(t.Context(), 10)
	if pendingErr != nil || len(pending) != 1 {
		t.Fatalf("pending rows = %d, err=%v; want refused row only", len(pending), pendingErr)
	}
	if got, want := pending[0].PURL, posted[1].Package; got != want {
		t.Fatalf("pending purl = %q, want refused purl %q", got, want)
	}
}

// A permanent 500 used to be invisible: the automatic loop discarded its
// error, status showed only an old successful upload, and the same durable row
// sat at the front of the deterministic queue. The daemon must expose the
// failure locally and stay alive long enough for a later tick to recover.
func TestAutomaticEvidenceUploadFailureIsVisibleAndNextTickRecovers(t *testing.T) {
	var calls atomic.Int32
	firstFailed := make(chan struct{})
	recovered := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			http.Error(w, `{"error":"ingest failed"}`, http.StatusInternalServerError)
			close(firstFailed)
			return
		}
		var body struct {
			Batches []json.RawMessage `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
		if call == 2 {
			close(recovered)
		}
	}))
	defer srv.Close()

	home := newTestHome(t, func(cfg *config.Config) {
		cfg.Mode = config.ModeCommunity
		cfg.ServerURL = srv.URL
	})
	d, err := New(home)
	if err != nil {
		t.Fatal(err)
	}
	seedPendingObservation(t, d.DB, "pkg:npm/upload-recovery@1.0.0", "retry")
	d.uploadFirstDelay = 20 * time.Millisecond
	d.uploadEvery = 250 * time.Millisecond

	runCtx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- d.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop")
		}
		_ = d.Close()
	})
	select {
	case <-d.Ready():
	case err := <-runDone:
		t.Fatalf("daemon exited before ready: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not become ready")
	}
	select {
	case <-firstFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("first upload was not attempted")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		lastErr, ok, statErr := d.DB.GetStat(t.Context(), statLastUploadError)
		if statErr == nil && ok && strings.Contains(lastErr, "500 Internal Server Error") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("upload failure not visible: value=%q ok=%v err=%v", lastErr, ok, statErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-runDone:
		t.Fatalf("daemon exited after non-fatal upload error: %v", err)
	default:
	}

	select {
	case <-recovered:
	case <-time.After(3 * time.Second):
		t.Fatal("next automatic upload tick did not retry")
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		last, uploaded, _ := d.DB.GetStat(t.Context(), statLastUpload)
		lastErr, _, _ := d.DB.GetStat(t.Context(), statLastUploadError)
		attempt, attempted, _ := d.DB.GetStat(t.Context(), statLastUploadAttempt)
		if uploaded && last != "" && attempted && attempt != "" && lastErr == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovery not reflected: last=%q attempt=%q error=%q", last, attempt, lastErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pending, err := d.DB.PendingObservations(t.Context(), 10); err != nil || len(pending) != 0 {
		t.Fatalf("pending after recovered tick = %d, err=%v", len(pending), err)
	}
}

func seedPendingObservation(t *testing.T, db *localdb.DB, purl, symbol string) {
	t.Helper()
	env := domain.EnvironmentFingerprint{
		SchemaVersion:  1,
		Ecosystem:      "npm",
		OS:             "windows",
		Arch:           "amd64",
		Runtime:        "node",
		RuntimeVersion: "24",
		Language:       "javascript",
	}.Normalize()
	if err := db.SaveEnvironment(t.Context(), env); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordObservation(t.Context(), localdb.ObsKey{
		Epoch:   "2026-08-18",
		PURL:    purl,
		Symbol:  symbol,
		EnvHash: env.Hash(),
		Stage:   domain.StageUsed,
		Result:  domain.ResultPass,
	}, 1); err != nil {
		t.Fatal(err)
	}
}
