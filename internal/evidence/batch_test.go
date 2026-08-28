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
	"strconv"
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
		if batch.SchemaVersion != 2 {
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

func TestUploadToV1ServerLeavesV2RowsPending(t *testing.T) {
	db := testDB(t)
	ident := testIdentity(t)
	var versions []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batches []domain.ObservationBatch `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upload: %v", err)
		}
		rejected := make([]map[string]any, len(body.Batches))
		for i, batch := range body.Batches {
			versions = append(versions, batch.SchemaVersion)
			rejected[i] = map[string]any{"index": i, "reason": "schemaVersion must be 1"}
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"accepted": 0, "rejected": rejected})
	}))
	defer srv.Close()

	cfg := communityCfg(srv.URL)
	recorder := &Recorder{DB: db, Ident: ident, Cfg: cfg}
	batcher := &Batcher{DB: db, Ident: ident, Cfg: cfg}
	if err := recorder.RecordRun(t.Context(), t.TempDir(), fakeScanResult(), knownProfile(), 0, ""); err != nil {
		t.Fatalf("RecordRun: %v", err)
	}
	if _, err := batcher.Upload(t.Context(), srv.Client(), srv.URL); err == nil || !strings.Contains(err.Error(), "schemaVersion must be 1") {
		t.Fatalf("v1 refusal = %v, want explicit schema-version error", err)
	}
	if len(versions) == 0 {
		t.Fatal("old server saw no batches")
	}
	for _, version := range versions {
		if version != 2 {
			t.Fatalf("uploaded schemaVersion = %d, want 2", version)
		}
	}
	if rows := pendingRows(t, db); len(rows) != len(versions) {
		t.Fatalf("pending rows = %d, want all %d refused rows preserved", len(rows), len(versions))
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
		t.Errorf("payload contains path-like string %q:\n%s", pathLike.FindString(body), body)
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

// Windows exposes process status as a DWORD. Go therefore reports the native
// -1 sentinel as 4294967295 on 64-bit Windows, and clients released before the
// signed wire contract persisted that value in their durable local queue.
// Upload must repair those already-pending rows; fixing only new recordings
// leaves the deterministic queue pinned on the same poison batch forever.
func TestUploadCanonicalizesPendingWindowsUnsignedExitCode(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("legacy uint32 exit status does not fit in int on this architecture")
	}
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()
	env := testEnvFP()
	if err := db.SaveEnvironment(ctx, env); err != nil {
		t.Fatal(err)
	}

	legacyWire := uint64(1<<32 - 1)
	legacyCode := int(legacyWire)
	legacyTerm := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &legacyCode}
	summary := "process exited without a conventional status"
	legacyFingerprint := domain.ClassifiedFailureFingerprint(domain.StageProjectTest, "go/test", legacyTerm, "", summary)
	if err := db.RecordObservation(ctx, localdb.ObsKey{
		Epoch: "2026-08-27", PURL: "pkg:golang/github.com/Microsoft/go-winio@v0.6.2",
		EnvHash: env.Hash(), Stage: domain.StageProjectTest, Result: domain.ResultFail,
		ErrorFP: legacyFingerprint, TerminationKind: domain.TerminationExit,
		ExitCode: &legacyCode, ErrorSummary: summary, EvidenceQuality: domain.EvidenceComplete,
		OuterCommand: "go test", OuterStage: domain.StageProjectTest, ActualToolchain: "go/test",
		StageEvidence: domain.FailureStageTestRunnerDiagnostic,
	}, 1); err != nil {
		t.Fatal(err)
	}
	canonicalCode := -1
	canonicalTerm := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &canonicalCode}
	canonicalFingerprint := domain.ClassifiedFailureFingerprint(domain.StageProjectTest, "go/test",
		canonicalTerm, "", summary)
	if err := db.RecordObservation(ctx, localdb.ObsKey{
		Epoch: "2026-08-27", PURL: "pkg:golang/github.com/Microsoft/go-winio@v0.6.2",
		EnvHash: env.Hash(), Stage: domain.StageProjectTest, Result: domain.ResultFail,
		ErrorFP: canonicalFingerprint, TerminationKind: domain.TerminationExit,
		ExitCode: &canonicalCode, ErrorSummary: summary, EvidenceQuality: domain.EvidenceComplete,
		OuterCommand: "go test", OuterStage: domain.StageProjectTest, ActualToolchain: "go/test",
		StageEvidence: domain.FailureStageTestRunnerDiagnostic,
	}, 2); err != nil {
		t.Fatal(err)
	}

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
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
	}))
	defer srv.Close()

	b := &Batcher{DB: db, Ident: ident, Cfg: communityCfg(srv.URL)}
	if sent, err := b.Upload(ctx, srv.Client(), srv.URL); err != nil || sent != 2 {
		t.Fatalf("Upload = (%d, %v), want (2, nil)", sent, err)
	}
	wantCode := -1
	wantFingerprint := domain.ClassifiedFailureFingerprint(domain.StageProjectTest, "go/test",
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &wantCode}, "", summary)
	if len(posted) != 2 {
		t.Fatalf("posted batches = %d, want both durable rows", len(posted))
	}
	for i, batch := range posted {
		if batch.ExitCode == nil || *batch.ExitCode != wantCode {
			t.Errorf("batch %d exitCode = %v, want signed int32 %d", i, batch.ExitCode, wantCode)
		}
		if batch.ErrorFingerprint != wantFingerprint {
			t.Errorf("batch %d fingerprint = %q, want canonical %q", i, batch.ErrorFingerprint, wantFingerprint)
		}
		if batch.ObservationCount != 3 {
			t.Errorf("batch %d observationCount = %d, want lossless combined total 3", i, batch.ObservationCount)
		}
	}
	if pending := pendingRows(t, db); len(pending) != 0 {
		t.Fatalf("canonicalized backlog left %d row(s) pending", len(pending))
	}
}

// During a rolling self-update an old daemon can upload both the unsigned and
// signed spellings before the new daemon starts. The new server correctly
// canonicalizes both identities, but its full-epoch dedup then observes max(a,
// b), not the intended a+b. The upgraded client keeps a durable acknowledged
// high-water so an old uploader cannot steal the pending flag or a later mixed
// increment without the next upgraded upload detecting it again.
func TestUploadReconcilesAlreadyUploadedMixedWindowsSpellingsAfterOldUploaderSteal(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("legacy uint32 exit status does not fit in int on this architecture")
	}
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()
	env := testEnvFP()
	if err := db.SaveEnvironment(ctx, env); err != nil {
		t.Fatal(err)
	}

	legacyCode := int(uint64(1<<32 - 1))
	canonicalCode := -1
	summary := "process exited without a conventional status"
	base := localdb.ObsKey{
		Epoch: "2026-08-27", PURL: "pkg:golang/github.com/Microsoft/go-winio@v0.6.2",
		EnvHash: env.Hash(), Stage: domain.StageProjectTest, Result: domain.ResultFail,
		TerminationKind: domain.TerminationExit, ErrorSummary: summary,
		EvidenceQuality: domain.EvidenceComplete, OuterCommand: "go test",
		OuterStage: domain.StageProjectTest, ActualToolchain: "go/test",
		StageEvidence: domain.FailureStageTestRunnerDiagnostic,
	}
	legacy := base
	legacy.ExitCode = &legacyCode
	legacy.ErrorFP = domain.ClassifiedFailureFingerprint(legacy.Stage, legacy.ActualToolchain,
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &legacyCode}, "", summary)
	canonical := base
	canonical.ExitCode = &canonicalCode
	canonical.ErrorFP = domain.ClassifiedFailureFingerprint(canonical.Stage, canonical.ActualToolchain,
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &canonicalCode}, "", summary)
	if err := db.RecordObservation(ctx, legacy, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordObservation(ctx, canonical, 2); err != nil {
		t.Fatal(err)
	}
	// This is the exact post-old-uploader state: neither row is pending, while
	// the canonical server aggregate contains only max(1,2).
	if err := db.MarkObservationsUploaded(ctx, []localdb.ObsKey{legacy, canonical}); err != nil {
		t.Fatal(err)
	}

	var posted []domain.ObservationBatch
	rejectNext := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batches []domain.ObservationBatch `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		posted = append(posted, body.Batches...)
		w.WriteHeader(http.StatusAccepted)
		if rejectNext {
			rejectNext = false
			fmt.Fprint(w, `{"accepted":0,"rejected":[{"index":0,"reason":"retry fixture"}]}`)
			return
		}
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
	}))
	defer srv.Close()

	b := &Batcher{DB: db, Ident: ident, Cfg: communityCfg(srv.URL)}
	// Force the narrow prepare→build gap, then let the old uploader steal the
	// pending flag. Upload calls prepare again and must recover it. The first
	// server reply refuses the repaired batch: rejected evidence must remain
	// pending and must not advance the acknowledgement high-water.
	if err := b.prepareLegacyWindowsReconciliation(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkObservationsUploaded(ctx, []localdb.ObsKey{legacy, canonical}); err != nil {
		t.Fatal(err)
	}
	if sent, err := b.Upload(ctx, srv.Client(), srv.URL); err == nil || sent != 0 {
		t.Fatalf("refused reconciliation Upload = (%d, %v), want retained refusal", sent, err)
	}
	legacyRows, err := db.LegacyWindowsObservations(ctx)
	if err != nil || len(legacyRows) != 1 || legacyRows[0].LegacyReconciledCount != 0 {
		t.Fatalf("refused reconciliation high-water = %+v, err=%v; want 0", legacyRows, err)
	}
	posted = nil
	// The old uploader can steal the restored pending flag too; no marker was
	// consumed, so the next current-client pass must rediscover it again.
	if err := db.MarkObservationsUploaded(ctx, []localdb.ObsKey{legacy, canonical}); err != nil {
		t.Fatal(err)
	}
	if sent, err := b.Upload(ctx, srv.Client(), srv.URL); err != nil || sent != 1 {
		t.Fatalf("first upgraded Upload = (%d, %v), want one reconciliation batch", sent, err)
	}
	if len(posted) != 1 || posted[0].ExitCode == nil || *posted[0].ExitCode != -1 || posted[0].ObservationCount != 3 {
		t.Fatalf("reconciliation payload = %+v, want signed exit -1 with combined count 3", posted)
	}
	legacyRows, err = db.LegacyWindowsObservations(ctx)
	if err != nil || len(legacyRows) != 1 || legacyRows[0].LegacyReconciledCount != 3 {
		t.Fatalf("accepted reconciliation high-water = %+v, err=%v; want 3", legacyRows, err)
	}
	if sent, err := b.Upload(ctx, srv.Client(), srv.URL); err != nil || sent != 0 {
		t.Fatalf("unchanged second Upload = (%d, %v), want durable high-water no-op", sent, err)
	}
	if len(posted) != 1 {
		t.Fatalf("unchanged reconciliation posted %d batches", len(posted))
	}

	// A still-running old command records one canonical observation and its
	// old uploader marks both component rows uploaded before the new daemon's
	// next tick. It cannot update legacy_reconciled_count, so the combined
	// durable total 4 must be rediscovered and sent despite pending=0.
	if err := db.RecordObservation(ctx, canonical, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkObservationsUploaded(ctx, []localdb.ObsKey{legacy, canonical}); err != nil {
		t.Fatal(err)
	}
	if sent, err := b.Upload(ctx, srv.Client(), srv.URL); err != nil || sent != 1 {
		t.Fatalf("Upload after old-uploader steal = (%d, %v), want repaired combined batch", sent, err)
	}
	if len(posted) != 2 || posted[1].ObservationCount != 4 {
		t.Fatalf("post-steal reconciliation payloads = %+v, want final combined count 4", posted)
	}
	if sent, err := b.Upload(ctx, srv.Client(), srv.URL); err != nil || sent != 0 {
		t.Fatalf("post-repair Upload = (%d, %v), want high-water no-op", sent, err)
	}
}

func TestUploadAdvancesLegacyReconciliationHighWaterOnlyForAcceptedIndexes(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("legacy uint32 exit status does not fit in int on this architecture")
	}
	db := testDB(t)
	ident := testIdentity(t)
	ctx := context.Background()
	env := testEnvFP()
	if err := db.SaveEnvironment(ctx, env); err != nil {
		t.Fatal(err)
	}
	legacyCode := int(uint64(1<<32 - 1))
	term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &legacyCode}
	summary := "process exited without a conventional status"
	fingerprint := domain.FailureFingerprint(domain.StageProjectTest, term, "", summary)
	keys := make([]localdb.ObsKey, 0, 2)
	for _, symbol := range []string{"one", "two"} {
		key := localdb.ObsKey{
			Epoch: "2026-08-27", PURL: "pkg:npm/axios@1.12.0", Symbol: symbol,
			EnvHash: env.Hash(), Stage: domain.StageProjectTest, Result: domain.ResultFail,
			ErrorFP: fingerprint, TerminationKind: domain.TerminationExit, ExitCode: &legacyCode,
			ErrorSummary: summary, EvidenceQuality: domain.EvidenceComplete,
		}
		if err := db.RecordObservation(ctx, key, 1); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
	}
	if err := db.MarkObservationsUploaded(ctx, keys); err != nil {
		t.Fatal(err)
	}

	rejectSecond := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Batches []domain.ObservationBatch `json:"batches"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		if rejectSecond {
			rejectSecond = false
			fmt.Fprint(w, `{"accepted":1,"rejected":[{"index":1,"reason":"second refused"}]}`)
			return
		}
		fmt.Fprintf(w, `{"accepted":%d,"rejected":[]}`, len(body.Batches))
	}))
	defer srv.Close()

	b := &Batcher{DB: db, Ident: ident, Cfg: communityCfg(srv.URL)}
	if sent, err := b.Upload(ctx, srv.Client(), srv.URL); err == nil || sent != 1 {
		t.Fatalf("partial reconciliation Upload = (%d, %v), want one accepted plus refusal", sent, err)
	}
	rows, err := db.LegacyWindowsObservations(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("legacy rows = %+v, err=%v", rows, err)
	}
	bySymbol := map[string]localdb.ObsRow{}
	for _, row := range rows {
		bySymbol[row.Symbol] = row
	}
	if bySymbol["one"].LegacyReconciledCount != 1 || bySymbol["two"].LegacyReconciledCount != 0 {
		t.Fatalf("partial ack high-waters = one:%d two:%d, want 1/0",
			bySymbol["one"].LegacyReconciledCount, bySymbol["two"].LegacyReconciledCount)
	}
	pending := pendingRows(t, db)
	if len(pending) != 1 || pending[0].Symbol != "two" {
		t.Fatalf("partial ack pending rows = %+v, want only rejected symbol two", pending)
	}
	if sent, err := b.Upload(ctx, srv.Client(), srv.URL); err != nil || sent != 1 {
		t.Fatalf("rejected reconciliation retry = (%d, %v), want one accepted", sent, err)
	}
	rows, err = db.LegacyWindowsObservations(ctx)
	if err != nil || len(rows) != 2 || rows[0].LegacyReconciledCount != 1 || rows[1].LegacyReconciledCount != 1 {
		t.Fatalf("retried high-waters = %+v, err=%v; want both 1", rows, err)
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
