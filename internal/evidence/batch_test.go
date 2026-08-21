package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/config"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

func communityCfg(serverURL string) *config.Config {
	cfg := config.Default()
	cfg.Mode = config.ModeCommunity
	cfg.ServerURL = serverURL
	return cfg
}

func TestDrainBuildsBatchesAndMarksUploadedAtomically(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	cfg := config.Default()
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	ctx := context.Background()
	dir := t.TempDir()

	if err := rec.RecordRun(ctx, dir, fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	batches, err := b.Drain(ctx)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("want 2 batches, got %d", len(batches))
	}

	epoch := time.Now().UTC().Format("2006-01-02")
	month := time.Now().UTC().Format("2006-01")
	abs, _ := filepath.Abs(dir)
	wantBucket := ident.ProjectBucket(abs, month)
	wantEnvHash := testEnvFP().Hash()
	for _, batch := range batches {
		if batch.SchemaVersion != 1 {
			t.Errorf("schemaVersion = %d", batch.SchemaVersion)
		}
		if batch.AnonID != ident.AnonID(epoch) {
			t.Errorf("anonId = %q, want %q", batch.AnonID, ident.AnonID(epoch))
		}
		if batch.ProjectBucket != wantBucket {
			t.Errorf("projectBucket = %q, want %q", batch.ProjectBucket, wantBucket)
		}
		if batch.Package != "pkg:npm/axios@1.12.0" {
			t.Errorf("package = %q", batch.Package)
		}
		if batch.Environment.Hash() != wantEnvHash {
			t.Errorf("environment hash mismatch: %q != %q", batch.Environment.Hash(), wantEnvHash)
		}
		if batch.ObservationCount != 1 {
			t.Errorf("observationCount = %d, want 1", batch.ObservationCount)
		}
		if batch.Symbol == "" && batch.SymbolConfidence != "" {
			t.Errorf("package-level batch carries symbolConfidence %q", batch.SymbolConfidence)
		}
	}

	// Re-drain: nothing new until new observations arrive.
	again, err := b.Drain(ctx)
	if err != nil {
		t.Fatalf("re-Drain: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("re-drain returned %d batches, want 0", len(again))
	}

	// A later increment re-pends the rows carrying the FULL epoch count.
	if err := rec.RecordRun(ctx, dir, fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun again: %v", err)
	}
	batches, err = b.Drain(ctx)
	if err != nil {
		t.Fatalf("Drain after increment: %v", err)
	}
	if len(batches) != 2 {
		t.Fatalf("want 2 re-pended batches, got %d", len(batches))
	}
	for _, batch := range batches {
		if batch.ObservationCount != 2 {
			t.Errorf("re-send observationCount = %d, want full epoch count 2", batch.ObservationCount)
		}
	}
}

func TestUploadPostsBatchesWithoutAnyPathLikeStrings(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	dir := t.TempDir()

	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		if r.URL.Path != "/v1/evidence/batches" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		// Ack what was actually sent, as the real server does: it replies
		// {accepted, rejected:[{index,reason}]} and the client now counts
		// what the server took rather than what it handed over.
		var body struct {
			Batches []json.RawMessage `json:"batches"`
		}
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
	}))
	defer srv.Close()

	cfg := communityCfg(srv.URL)
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	ctx := context.Background()

	// A failing run whose stderr is full of identifying material.
	stderrTail := `C:\Users\someone\secret-project\src\app.ts(3,7): error TS2345: boom` + "\n" +
		`    at /home/someone/secret-project/node_modules/corp-secret-lib/index.js:10`
	profile := knownProfile()
	if err := rec.RecordRun(ctx, dir, fakeScanResult(), profile, 1, stderrTail); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}

	n, err := b.Upload(ctx, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if n != 2 {
		t.Fatalf("uploaded %d batches, want 2", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(bodies))
	}
	body := bodies[0]

	var payload struct {
		Batches []domain.ObservationBatch `json:"batches"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if len(payload.Batches) != 2 {
		t.Fatalf("payload has %d batches, want 2", len(payload.Batches))
	}
	for _, batch := range payload.Batches {
		if batch.Result != domain.ResultFail || batch.ErrorCode != "TS2345" {
			t.Errorf("batch result/code = %s/%s", batch.Result, batch.ErrorCode)
		}
		if !strings.HasPrefix(batch.ErrorFingerprint, "sha256:") {
			t.Errorf("errorFingerprint = %q", batch.ErrorFingerprint)
		}
	}

	// Privacy: no path-like strings, usernames, or private package names.
	pathLike := regexp.MustCompile(`[A-Za-z]:[\\/]|/home/|/Users/|node_modules`)
	if pathLike.MatchString(body) {
		t.Errorf("payload contains path-like string:\n%s", body)
	}
	for _, banned := range []string{"corp-secret-lib", "maybe-internal", "secret-project", "someone", dir, abs2(dir)} {
		if banned != "" && strings.Contains(body, banned) {
			t.Errorf("payload contains %q:\n%s", banned, body)
		}
	}

	// Everything got marked: nothing pending, second upload posts nothing.
	if rows := pendingRows(t, db); len(rows) != 0 {
		t.Fatalf("rows still pending after successful upload: %+v", rows)
	}
	n, err = b.Upload(ctx, srv.Client(), srv.URL)
	if err != nil || n != 0 {
		t.Fatalf("second upload = (%d, %v), want (0, nil)", n, err)
	}
}

func abs2(dir string) string {
	a, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	return a
}

func TestUploadFailureKeepsRowsPending(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := communityCfg(srv.URL)
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	ctx := context.Background()

	if err := rec.RecordRun(ctx, t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	if _, err := b.Upload(ctx, srv.Client(), srv.URL); err == nil {
		t.Fatal("Upload succeeded against a 500 server")
	}
	if rows := pendingRows(t, db); len(rows) != 2 {
		t.Fatalf("want 2 rows still pending after failed upload, got %d", len(rows))
	}

	// Counts must be untouched by the pending restore.
	for _, r := range pendingRows(t, db) {
		if r.Count != 1 {
			t.Errorf("count changed by restore: %+v", r)
		}
	}
}

// cancelMidFlight is a transport standing in for the commonest failed
// delivery there is: the caller's context dying while the request is on the
// wire (the daemon shutting down mid-sync).
type cancelMidFlight struct{ cancel context.CancelFunc }

func (c cancelMidFlight) RoundTrip(*http.Request) (*http.Response, error) {
	c.cancel()
	return nil, context.Canceled
}

// A canceled sync must not lose the chunk. The pending restore ran on the
// SAME dead context that killed the delivery, so it silently did nothing:
// the rows stayed marked uploaded and the evidence was gone.
func TestACanceledUploadDoesNotLoseTheChunk(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := communityCfg("http://server.invalid")
	rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	if err := rec.RecordRun(context.Background(), t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	client := &http.Client{Transport: cancelMidFlight{cancel}}
	if _, err := b.Upload(ctx, client, "http://server.invalid"); err == nil {
		t.Fatal("Upload succeeded against a dead delivery")
	}
	if rows := pendingRows(t, db); len(rows) != 2 {
		t.Fatalf("want 2 rows still pending after a canceled upload, got %d", len(rows))
	}
}

// TestUploadChunksToServerLimit pins the fix for a backlog that could
// never drain: the client posted a whole 1000-row drain in one request
// while the server rejects anything over 500, so a first sync after
// scanning a machine's projects failed with 400 and retried forever.
func TestUploadChunksToServerLimit(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)

	const serverCap = 500
	var mu sync.Mutex
	var sizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batches []json.RawMessage `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
			return
		}
		mu.Lock()
		sizes = append(sizes, len(body.Batches))
		mu.Unlock()
		if len(body.Batches) > serverCap {
			http.Error(w, `{"error":"too many batches in one request (max 500)"}`, http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
	}))
	defer srv.Close()

	// A backlog several server-caps deep, as a first scan of many projects
	// produces.
	const backlog = 1200
	ctx := context.Background()
	env := testEnvFP()
	if err := db.SaveEnvironment(ctx, env); err != nil {
		t.Fatal(err)
	}
	envHash := env.Hash()
	for i := 0; i < backlog; i++ {
		key := localdb.ObsKey{
			Epoch:   "2026-08-13",
			PURL:    fmt.Sprintf("pkg:npm/pkg-%04d@1.0.0", i),
			EnvHash: envHash,
			Stage:   domain.StageUsed,
			Result:  domain.ResultPass,
		}
		if err := db.RecordObservation(ctx, key, 1); err != nil {
			t.Fatal(err)
		}
	}

	cfg := communityCfg(srv.URL)
	b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	sent, err := b.Upload(ctx, srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if sent != backlog {
		t.Errorf("uploaded %d of %d rows", sent, backlog)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, n := range sizes {
		if n > serverCap {
			t.Errorf("request %d carried %d batches, over the server's %d cap", i, n, serverCap)
		}
	}
	if rows := pendingRows(t, db); len(rows) != 0 {
		t.Errorf("%d rows still pending after a successful drain", len(rows))
	}
}

func TestUploadOnlyRunsInCommunityMode(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	for _, mode := range []string{config.ModeUninitialized, config.ModeLocalOnly} {
		cfg := config.Default()
		cfg.Mode = mode
		rec := &Recorder{DB: db, Ident: ident, Cfg: cfg}
		b := &Batcher{DB: db, Ident: ident, Cfg: cfg}
		ctx := context.Background()
		if err := rec.RecordRun(ctx, t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
			t.Fatalf("RecordRun: %v", err)
		}
		n, err := b.Upload(ctx, srv.Client(), srv.URL)
		if err != nil || n != 0 {
			t.Fatalf("mode %q: Upload = (%d, %v), want no-op", mode, n, err)
		}
	}
	if requests != 0 {
		t.Fatalf("server saw %d requests from non-community modes", requests)
	}
	// Rows stay for local stats.
	if rows := pendingRows(t, db); len(rows) == 0 {
		t.Fatal("local rows were lost in non-community mode")
	}
}
