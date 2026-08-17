package serverstore

// PostgreSQL integration tests. They are skipped unless CSX_TEST_DSN points
// at a reachable database, so the default `go test ./...` never needs a real
// PostgreSQL. Each run works inside its own throwaway schema and drops it
// afterwards.
//
//	$env:CSX_TEST_DSN = "postgres://csx:csx@localhost:5432/csx"
//	go test ./internal/serverstore/ -run TestIntegration -v

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// openTestPG connects to CSX_TEST_DSN inside a fresh schema, migrates it,
// and registers cleanup. Skips when no DSN is configured.
func openTestPG(t *testing.T) *PG {
	t.Helper()
	dsn := os.Getenv("CSX_TEST_DSN")
	if dsn == "" {
		t.Skip("CSX_TEST_DSN not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	schema := fmt.Sprintf("csx_test_%d", time.Now().UnixNano())

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["search_path"] = schema

	pg := newPG(cfg)
	t.Cleanup(pg.Close)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pg
}

func TestIntegrationWantedClosesOnlyExactVersionAndSymbol(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	requests := []WantedRow{
		{Ecosystem: "npm", Name: "three", Version: "0.180.0", Symbol: "Texture.transformUv"},
		{Ecosystem: "npm", Name: "three", Version: "0.180.0", Symbol: "CanvasTexture"},
		// Rows received before the version migration remain visible: their
		// exact requested release can no longer be reconstructed honestly.
		{Ecosystem: "npm", Name: "three", Symbol: "LegacySymbol"},
		{Ecosystem: "npm", Name: "@scope/pkg", Version: "2.0.0", Symbol: "scoped.call"},
	}
	if err := pg.RecordWanted(ctx, "2026-08-17", "anon-a", requests); err != nil {
		t.Fatal(err)
	}

	// The website collection keeps the same exact-answer policy while adding
	// search, total counts and stable offset pagination. Legacy versionless
	// rows remain searchable and are not silently discarded.
	page, total, err := pg.ListWanted(ctx, "npm three canvas", 0, 1)
	if err != nil || total != 1 || len(page) != 1 || page[0].Symbol != "CanvasTexture" {
		t.Fatalf("searched wanted page = %+v total=%d err=%v", page, total, err)
	}
	page, total, err = pg.ListWanted(ctx, "npm three", 1, 2)
	if err != nil || total != 3 || len(page) != 2 ||
		page[0].Symbol != "CanvasTexture" || page[1].Symbol != "Texture.transformUv" {
		t.Fatalf("paged wanted rows = %+v total=%d err=%v", page, total, err)
	}
	page, total, err = pg.ListWanted(ctx, "legacy", 0, 20)
	if err != nil || total != 1 || len(page) != 1 || page[0].Version != "" {
		t.Fatalf("versionless wanted row = %+v total=%d err=%v", page, total, err)
	}

	manifest := `{"packages":["pkg:npm/three@0.179.0"],"symbols":["Texture.transformUv"]}`
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:other", ManifestJSON: manifest}); err != nil {
		t.Fatal(err)
	}
	rows, err := pg.TopWanted(ctx, 20)
	if err != nil || len(rows) != 4 {
		t.Fatalf("different version closed wanted row: rows=%+v err=%v", rows, err)
	}

	manifest = `{"packages":["pkg:npm/three@0.180.0"],"symbols":["Texture.transformUv"]}`
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:exact", ManifestJSON: manifest}); err != nil {
		t.Fatal(err)
	}
	// A PASS attached to an author-declared 0.180.0 manifest does not
	// answer 0.180.0 when the v2 resolver says the run actually used
	// 0.179.0.
	if err := pg.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "receipt-wrong-resolve", SampleID: "sha256:exact", ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS","contract":"PASS"},` +
			`"resolvedPackages":["pkg:npm/three@0.179.0"]}`,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err = pg.TopWanted(ctx, 20)
	if err != nil || len(rows) != 4 {
		t.Fatalf("manifest version closed request despite a different resolved release: rows=%+v err=%v", rows, err)
	}

	// Matrix verification is the inverse: the manifest may name the base
	// release, while resolvedPackages proves the requested release really
	// ran and therefore answers it.
	manifest = `{"packages":["pkg:npm/three@0.179.0"],"symbols":["Texture.transformUv"]}`
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:matrix", ManifestJSON: manifest}); err != nil {
		t.Fatal(err)
	}
	manifest = `{"packages":["pkg:npm/%40scope/pkg@2.0.0"],"symbols":["scoped.call"]}`
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:scoped", ManifestJSON: manifest}); err != nil {
		t.Fatal(err)
	}
	for _, receipt := range []ReceiptRow{
		{ReceiptID: "receipt-matrix", SampleID: "sha256:matrix", ContractResult: "PASS",
			ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS","contract":"PASS"},` +
				`"resolvedPackages":["pkg:npm/three@0.180.0"]}`},
		{ReceiptID: "receipt-scoped", SampleID: "sha256:scoped", ContractResult: "PASS",
			ReceiptJSON: `{"schemaVersion":2,"stages":{"resolve":"PASS","contract":"PASS"},` +
				`"resolvedPackages":["pkg:npm/%40scope/pkg@2.0.0"]}`},
	} {
		if err := pg.SaveReceipt(ctx, receipt); err != nil {
			t.Fatal(err)
		}
	}
	rows, err = pg.TopWanted(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("wanted rows after exact answers = %+v, want CanvasTexture + legacy", rows)
	}
	remaining := map[string]bool{}
	for _, row := range rows {
		remaining[row.Symbol] = true
	}
	if !remaining["CanvasTexture"] || !remaining["LegacySymbol"] {
		t.Fatalf("wrong rows remain: %+v", rows)
	}

	// Rows migrated from the old schema have no version. A v1 receipt also
	// has no resolvedPackages, so the only honest recoverable policy is a
	// contract pass for the same package and symbol at any release.
	manifest = `{"packages":["pkg:npm/three@0.170.0"],"symbols":["LegacySymbol"]}`
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:legacy", ManifestJSON: manifest}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "receipt-legacy", SampleID: "sha256:legacy", ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":1,"stages":{"contract":"PASS"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err = pg.TopWanted(ctx, 20)
	if err != nil || len(rows) != 1 || rows[0].Symbol != "CanvasTexture" {
		t.Fatalf("legacy answer policy left wrong rows: rows=%+v err=%v", rows, err)
	}
}

func TestIntegrationVerifiedSampleReadsRequireContractPass(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	manifest := `{"packages":["pkg:npm/axios@1.12.0"],"symbols":["axios.get"]}`
	for _, id := range []string{"sha256:source-only", "sha256:proved"} {
		if err := pg.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: manifest}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "receipt-proved", SampleID: "sha256:proved", ContractResult: "PASS", ReceiptJSON: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := pg.VerifiedSamplesForPackages(ctx, []string{"pkg:npm/axios@%"}, 10)
	if err != nil || len(rows) != 1 || rows[0].SampleID != "sha256:proved" {
		t.Fatalf("verified package rows = %+v, err=%v", rows, err)
	}
	rows, err = pg.ListVerifiedSamples(ctx, 10)
	if err != nil || len(rows) != 1 || rows[0].SampleID != "sha256:proved" {
		t.Fatalf("verified sample rows = %+v, err=%v", rows, err)
	}
}

func TestIntegrationIngestDeltaMerge(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	evidence := func() EvidenceRow {
		t.Helper()
		rows, err := pg.EvidenceForTarget(ctx, "pkg:npm/axios@1.12.0", "axios.post")
		if err != nil {
			t.Fatalf("EvidenceForTarget: %v", err)
		}
		if len(rows) != 1 {
			t.Fatalf("evidence rows = %d, want 1", len(rows))
		}
		return rows[0]
	}

	// First send: full count lands.
	acc, rej, err := pg.IngestBatches(ctx, []domain.ObservationBatch{obsBatch("anonaaaa", "projaaaa", 5)})
	if err != nil || acc != 1 || len(rej) != 0 {
		t.Fatalf("first ingest: acc=%d rej=%v err=%v", acc, rej, err)
	}
	e := evidence()
	if e.ObservationCount != 5 || e.UniquePeerBuckets != 1 || e.UniqueProjectBuckets != 1 {
		t.Fatalf("after first send: %+v", e)
	}

	// Re-sent identical batch adds 0 (BINDING).
	if acc, rej, err = pg.IngestBatches(ctx, []domain.ObservationBatch{obsBatch("anonaaaa", "projaaaa", 5)}); err != nil || acc != 1 || len(rej) != 0 {
		t.Fatalf("duplicate ingest: acc=%d rej=%v err=%v", acc, rej, err)
	}
	if e = evidence(); e.ObservationCount != 5 {
		t.Fatalf("re-sent identical batch inflated count: %+v", e)
	}
	if e.UniquePeerBuckets != 1 || e.UniqueProjectBuckets != 1 {
		t.Fatalf("re-sent identical batch inflated buckets: %+v", e)
	}

	// Same peer's epoch total grew 5→8: only the delta 3 lands.
	if _, _, err = pg.IngestBatches(ctx, []domain.ObservationBatch{obsBatch("anonaaaa", "projaaaa", 8)}); err != nil {
		t.Fatalf("grown ingest: %v", err)
	}
	if e = evidence(); e.ObservationCount != 8 {
		t.Fatalf("grown count = %d, want 8", e.ObservationCount)
	}

	// A second peer adds its own count and a new dedup bucket.
	if _, _, err = pg.IngestBatches(ctx, []domain.ObservationBatch{obsBatch("anonbbbb", "projbbbb", 2)}); err != nil {
		t.Fatalf("second peer ingest: %v", err)
	}
	e = evidence()
	if e.ObservationCount != 10 || e.UniquePeerBuckets != 2 || e.UniqueProjectBuckets != 2 {
		t.Fatalf("after second peer: %+v", e)
	}

	// Mixed batch: invalid rows rejected with reasons, valid ones land.
	bad := obsBatch("anoncccc", "projcccc", 1)
	bad.Stage = domain.StageSymbolExecuted
	acc, rej, err = pg.IngestBatches(ctx, []domain.ObservationBatch{bad, obsBatch("anoncccc", "projcccc", 1)})
	if err != nil || acc != 1 || len(rej) != 1 || rej[0].Index != 0 {
		t.Fatalf("mixed ingest: acc=%d rej=%v err=%v", acc, rej, err)
	}
	if e = evidence(); e.ObservationCount != 11 {
		t.Fatalf("count after mixed = %d, want 11", e.ObservationCount)
	}

	// Purge: today's buckets survive a 30-day purge; a 0-day purge drops
	// them all while the aggregate keeps its counts.
	if removed, err := pg.PurgeDedupOlderThan(ctx, 30); err != nil || removed != 0 {
		t.Fatalf("30d purge: removed=%d err=%v", removed, err)
	}
	if removed, err := pg.PurgeDedupOlderThan(ctx, -1); err != nil || removed == 0 {
		t.Fatalf("-1d purge: removed=%d err=%v", removed, err)
	}
	if e = evidence(); e.ObservationCount != 11 {
		t.Fatalf("purge changed aggregate count: %+v", e)
	}

	// Snapshot targets reflect the evidence rows.
	targets, err := pg.ListSnapshotTargets(ctx)
	if err != nil || len(targets) != 1 || targets[0].PURL != "pkg:npm/axios@1.12.0" || targets[0].Symbol != "axios.post" {
		t.Fatalf("targets = %v err=%v", targets, err)
	}

	// Migrate is idempotent.
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestIntegrationResolvedReceiptTargetsAndChanges(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	mark := time.Now().UTC().Add(-time.Second)
	declared := "pkg:npm/axios@1.0.0"
	resolved := "pkg:npm/axios@2.1.3"
	cse := domain.Case{
		SchemaVersion: 1, Kind: "HOW", Goal: "post JSON",
		Packages: []string{declared}, Contract: []string{"posts JSON"},
	}
	caseID := cse.ComputeID()
	if err := pg.SaveCase(ctx, cse); err != nil {
		t.Fatal(err)
	}
	manifest := domain.SampleManifest{
		SchemaVersion: 1, Case: cse, Packages: []string{declared},
		Symbols: []string{"axios.post"},
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		},
		License: "MIT-0", ContractCommand: []string{"node", "test.mjs"},
		VerifierAdapter: "node-typescript@1",
	}
	sampleID := "sha256:" + fmt.Sprintf("%064d", 91)
	if err := pg.SaveSample(ctx, SampleRow{
		SampleID: sampleID, CaseID: caseID,
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)),
	}); err != nil {
		t.Fatal(err)
	}
	receipt := domain.VerificationReceipt{
		SchemaVersion: 2, SampleID: sampleID, CaseID: caseID,
		Stages:           map[string]string{"resolve": "PASS", "compile": "PASS", "contract": "PASS"},
		ResolvedPackages: []string{resolved},
	}
	if err := pg.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "sha256:" + fmt.Sprintf("%064d", 92), SampleID: sampleID,
		PeerID: "ed25519:0011223344556677", EnvHash: "sha256:" + fmt.Sprintf("%064d", 93),
		ReceiptJSON: string(domain.MustCanonicalJSON(receipt)), ContractResult: "PASS",
	}); err != nil {
		t.Fatal(err)
	}

	targets, err := pg.ListSnapshotTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantTargets := map[SnapshotTarget]bool{
		{PURL: resolved, Symbol: ""}:           true,
		{PURL: resolved, Symbol: "axios.post"}: true,
	}
	for _, target := range targets {
		delete(wantTargets, target)
	}
	if len(wantTargets) != 0 {
		t.Fatalf("receipt targets missing: %v (got %v)", wantTargets, targets)
	}
	changes, err := pg.ChangedSince(ctx, mark)
	if err != nil {
		t.Fatal(err)
	}
	wantDirty := map[string]bool{declared: true, resolved: true}
	for _, purl := range changes.SamplePURLs {
		delete(wantDirty, purl)
	}
	if len(wantDirty) != 0 {
		t.Fatalf("receipt dirty keys missing: %v (got %v)", wantDirty, changes.SamplePURLs)
	}

	if err := pg.SetSampleQuarantine(ctx, sampleID, true, "test"); err != nil {
		t.Fatal(err)
	}
	targets, err = pg.ListSnapshotTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.PURL == resolved {
			t.Fatalf("quarantined sample still created receipt target: %+v", target)
		}
	}
	changes, err = pg.ChangedSince(ctx, mark)
	if err != nil {
		t.Fatal(err)
	}
	wantDirty = map[string]bool{declared: true, resolved: true}
	for _, purl := range changes.SamplePURLs {
		delete(wantDirty, purl)
	}
	if len(wantDirty) != 0 {
		t.Fatalf("quarantine did not dirty historical resolved keys: %v", wantDirty)
	}
}

func TestIntegrationCRUD(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	t.Run("packages", func(t *testing.T) {
		row := PackageRow{PURL: "pkg:npm/axios@1.12.0", Ecosystem: "npm", Name: "axios",
			Version: "1.12.0", Major: "1", Publicness: "PUBLIC", CheckedAt: time.Now().UTC()}
		if err := pg.UpsertPackage(ctx, row); err != nil {
			t.Fatalf("UpsertPackage: %v", err)
		}
		got, ok, err := pg.GetPackage(ctx, row.PURL)
		if err != nil || !ok || got.Publicness != "PUBLIC" || got.CheckedAt.IsZero() {
			t.Fatalf("GetPackage: %+v ok=%v err=%v", got, ok, err)
		}
		if _, ok, _ := pg.GetPackage(ctx, "pkg:npm/absent@0.0.1"); ok {
			t.Fatal("GetPackage found absent purl")
		}
		vs, err := pg.ListPackageVersions(ctx, "npm", "axios")
		if err != nil || len(vs) != 1 {
			t.Fatalf("ListPackageVersions: %v err=%v", vs, err)
		}
	})

	t.Run("snapshots", func(t *testing.T) {
		first := SnapshotTarget{PURL: "pkg:npm/axios@1.12.0", Symbol: "axios.post"}
		second := SnapshotTarget{PURL: "pkg:npm/axios@1.12.0", Symbol: "axios.request"}
		if err := pg.PutSnapshot(ctx, first.PURL, first.Symbol, `{"schemaVersion":1}`); err != nil {
			t.Fatalf("PutSnapshot: %v", err)
		}
		if err := pg.PutSnapshot(ctx, second.PURL, second.Symbol, `{"schemaVersion":1}`); err != nil {
			t.Fatalf("PutSnapshot second: %v", err)
		}
		js, ok, err := pg.GetSnapshot(ctx, first.PURL, first.Symbol)
		if err != nil || !ok || js == "" {
			t.Fatalf("GetSnapshot: %q ok=%v err=%v", js, ok, err)
		}
		if _, ok, _ := pg.GetSnapshot(ctx, "pkg:npm/axios@1.12.0", "axios.get"); ok {
			t.Fatal("GetSnapshot found absent symbol")
		}
		keys, err := pg.SnapshotKeys(ctx)
		if err != nil || len(keys) != 2 || keys[0] != first || keys[1] != second {
			t.Fatalf("SnapshotKeys: %+v err=%v", keys, err)
		}
		if err := pg.DeleteSnapshots(ctx, []SnapshotTarget{first}); err != nil {
			t.Fatalf("DeleteSnapshots: %v", err)
		}
		if _, ok, _ := pg.GetSnapshot(ctx, first.PURL, first.Symbol); ok {
			t.Fatal("deleted snapshot survived")
		}
		if _, ok, _ := pg.GetSnapshot(ctx, second.PURL, second.Symbol); !ok {
			t.Fatal("deleting one snapshot removed another")
		}
	})

	caseID := ""
	t.Run("cases and samples", func(t *testing.T) {
		cse := domain.Case{SchemaVersion: 1, Kind: "HOW", Goal: "post JSON with axios",
			Packages: []string{"pkg:npm/axios@1.12.0"}, Contract: []string{"posts JSON body"}}
		caseID = cse.ComputeID()
		if err := pg.SaveCase(ctx, cse); err != nil {
			t.Fatalf("SaveCase: %v", err)
		}
		s := SampleRow{SampleID: "sha256:" + fmt.Sprintf("%064d", 1), CaseID: caseID,
			ManifestJSON: `{"schemaVersion":1}`, OriginSeeder: "anonymous", SizeBytes: 1234}
		if err := pg.SaveSample(ctx, s); err != nil {
			t.Fatalf("SaveSample: %v", err)
		}
		got, ok, err := pg.GetSample(ctx, s.SampleID)
		if err != nil || !ok || got.Status != "PUBLISHED" || got.License != "MIT-0" || got.CaseID != caseID {
			t.Fatalf("GetSample: %+v ok=%v err=%v", got, ok, err)
		}
		if err := pg.SetSampleStatus(ctx, s.SampleID, "CROSS_PASS"); err != nil {
			t.Fatalf("SetSampleStatus: %v", err)
		}
		if got, _, _ := pg.GetSample(ctx, s.SampleID); got.Status != "CROSS_PASS" {
			t.Fatalf("status = %q, want CROSS_PASS", got.Status)
		}
		if err := pg.SetSampleStatus(ctx, "sha256:absent", "STABLE"); err == nil {
			t.Fatal("SetSampleStatus on absent sample succeeded")
		}
		list, err := pg.ListSamples(ctx, 10)
		if err != nil || len(list) != 1 {
			t.Fatalf("ListSamples: %v err=%v", list, err)
		}
	})

	sampleID := "sha256:" + fmt.Sprintf("%064d", 1)
	t.Run("receipts", func(t *testing.T) {
		r := ReceiptRow{ReceiptID: "sha256:" + fmt.Sprintf("%064d", 2), SampleID: sampleID,
			PeerID: "ed25519:0011223344556677", EnvHash: "sha256:" + fmt.Sprintf("%064d", 3),
			ReceiptJSON: `{"schemaVersion":1}`, ContractResult: "PASS"}
		if err := pg.SaveReceipt(ctx, r); err != nil {
			t.Fatalf("SaveReceipt: %v", err)
		}
		if err := pg.SaveReceipt(ctx, r); err != nil { // idempotent
			t.Fatalf("SaveReceipt duplicate: %v", err)
		}
		rs, err := pg.ReceiptsForSample(ctx, sampleID)
		if err != nil || len(rs) != 1 || rs[0].ContractResult != "PASS" {
			t.Fatalf("ReceiptsForSample: %v err=%v", rs, err)
		}
	})

	t.Run("jobs", func(t *testing.T) {
		id, err := pg.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "cross"})
		if err != nil || id == 0 {
			t.Fatalf("CreateJob: id=%d err=%v", id, err)
		}
		pinned, err := pg.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "matrix",
			WantEnvJSON: `{"sandboxCapability":"CONTAINER_RUN"}`})
		if err != nil {
			t.Fatalf("CreateJob pinned: %v", err)
		}
		open, err := pg.OpenJobs(ctx, "", "", 10)
		if err != nil || len(open) != 2 {
			t.Fatalf("OpenJobs any: %v err=%v", open, err)
		}
		open, err = pg.OpenJobs(ctx, "COMPILE_ONLY", "", 10)
		if err != nil || len(open) != 1 || open[0].ID != id {
			t.Fatalf("OpenJobs COMPILE_ONLY: %v err=%v", open, err)
		}
		ok, err := pg.ClaimJob(ctx, id, "ed25519:0011223344556677")
		if err != nil || !ok {
			t.Fatalf("ClaimJob: ok=%v err=%v", ok, err)
		}
		if ok, _ := pg.ClaimJob(ctx, id, "ed25519:8899aabbccddeeff"); ok {
			t.Fatal("second claim of same job succeeded")
		}
		if err := pg.CompleteJob(ctx, id); err != nil {
			t.Fatalf("CompleteJob: %v", err)
		}
		open, err = pg.OpenJobs(ctx, "", "", 10)
		if err != nil || len(open) != 1 || open[0].ID != pinned {
			t.Fatalf("OpenJobs after claim+complete: %v err=%v", open, err)
		}
	})

	t.Run("peers", func(t *testing.T) {
		peer := PeerRow{PeerID: "ed25519:0011223344556677", Addr: "203.0.113.9", Port: 48620,
			CapabilitiesJSON: `["CONTAINER_RUN"]`,
			SampleIDsJSON:    fmt.Sprintf(`[%q]`, sampleID),
			ExpiresAt:        time.Now().Add(30 * time.Minute)}
		if err := pg.AnnouncePeer(ctx, peer); err != nil {
			t.Fatalf("AnnouncePeer: %v", err)
		}
		got, err := pg.PeersForSample(ctx, sampleID)
		if err != nil || len(got) != 1 || got[0].Addr != "203.0.113.9" {
			t.Fatalf("PeersForSample: %v err=%v", got, err)
		}
		if got, _ := pg.PeersForSample(ctx, "sha256:absent"); len(got) != 0 {
			t.Fatalf("PeersForSample absent: %v", got)
		}
		removed, err := pg.ExpirePeers(ctx, time.Now().Add(time.Hour))
		if err != nil || removed != 1 {
			t.Fatalf("ExpirePeers: removed=%d err=%v", removed, err)
		}
	})

	t.Run("shards", func(t *testing.T) {
		if err := pg.PutShard(ctx, "npm/axios/1", "etag1", `{"schemaVersion":1}`); err != nil {
			t.Fatalf("PutShard: %v", err)
		}
		if err := pg.PutShard(ctx, "npm/axios/1", "etag2", `{"schemaVersion":1,"v":2}`); err != nil {
			t.Fatalf("PutShard update: %v", err)
		}
		etag, js, ok, err := pg.GetShard(ctx, "npm/axios/1")
		if err != nil || !ok || etag != "etag2" || js == "" {
			t.Fatalf("GetShard: etag=%q ok=%v err=%v", etag, ok, err)
		}
		if _, _, ok, _ := pg.GetShard(ctx, "npm/axios/2"); ok {
			t.Fatal("GetShard found absent key")
		}
	})

	t.Run("identities", func(t *testing.T) {
		if err := pg.SaveIdentity(ctx, "octocat", 583231, "th", "ath"); err != nil {
			t.Fatalf("SaveIdentity: %v", err)
		}
		id, ok, err := pg.IdentityByAPIToken(ctx, "ath")
		if err != nil || !ok || id.Login != "octocat" || id.GithubID != 583231 {
			t.Fatalf("IdentityByAPIToken: %+v ok=%v err=%v", id, ok, err)
		}
		if _, ok, _ := pg.IdentityByAPIToken(ctx, "nope"); ok {
			t.Fatal("IdentityByAPIToken matched wrong hash")
		}
		if _, ok, _ := pg.IdentityByAPIToken(ctx, ""); ok {
			t.Fatal("IdentityByAPIToken matched empty hash")
		}
	})

	t.Run("failure clusters", func(t *testing.T) {
		cl := ClusterRow{Ecosystem: "npm", PackageName: "axios", Symbol: "axios.post",
			Stage: "PROJECT_COMPILE", ErrorFingerprint: "sha256:" + fmt.Sprintf("%064d", 4),
			ErrorCode: "ERR_REQUIRE_ESM", ObservationCount: 7,
			HypothesesJSON: `[{"domain":"CONFIGURATION","confidence":0.72}]`}
		if err := pg.UpsertFailureCluster(ctx, cl); err != nil {
			t.Fatalf("UpsertFailureCluster: %v", err)
		}
		cl.ObservationCount = 9
		if err := pg.UpsertFailureCluster(ctx, cl); err != nil {
			t.Fatalf("UpsertFailureCluster update: %v", err)
		}
		got, err := pg.ListFailureClusters(ctx, "axios")
		if err != nil || len(got) != 1 || got[0].ObservationCount != 9 {
			t.Fatalf("ListFailureClusters: %v err=%v", got, err)
		}
	})

	t.Run("stats", func(t *testing.T) {
		if _, ok, err := pg.GetLatestStats(ctx); ok || err != nil {
			t.Fatalf("GetLatestStats on empty table: ok=%v err=%v", ok, err)
		}
		if err := pg.SetStatsDaily(ctx, "2026-08-12", `{"peers":1}`); err != nil {
			t.Fatalf("SetStatsDaily: %v", err)
		}
		if err := pg.SetStatsDaily(ctx, "2026-08-13", `{"peers":2}`); err != nil {
			t.Fatalf("SetStatsDaily 2: %v", err)
		}
		js, ok, err := pg.GetLatestStats(ctx)
		if err != nil || !ok || js == "" {
			t.Fatalf("GetLatestStats: %q ok=%v err=%v", js, ok, err)
		}
		if js != `{"peers": 2}` && js != `{"peers":2}` { // jsonb may reformat
			t.Fatalf("latest stats = %q, want the 2026-08-13 row", js)
		}
	})
}

// TestIntegrationHotShardKeysPrefersSamples pins the cold-start ranking: a
// fresh install warms this list, so a shard that carries a verified sample
// must outrank a shard that only carries observation counts, however large
// those counts are. Ranking by volume alone filled the warm set with
// transitive dependencies nobody asks about.
func TestIntegrationHotShardKeysPrefersSamples(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	// A busy package with no sample, and a quiet one with a sample.
	busy := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-14", AnonID: "peerA", ProjectBucket: "projA",
		Package: "pkg:npm/ms@2.1.3", Symbol: "ms", SymbolConfidence: domain.SymbolProbable,
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm",
			OS: "linux", Arch: "amd64", Runtime: "node", RuntimeVersion: "22", ModuleSystem: "esm"},
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 9999,
	}
	if _, _, err := pg.IngestBatches(ctx, []domain.ObservationBatch{busy}); err != nil {
		t.Fatalf("IngestBatches: %v", err)
	}
	for _, key := range []string{"npm/ms/2", "npm/chalk/6"} {
		if err := pg.PutShard(ctx, key, "etag-"+key, `{"key":"`+key+`"}`); err != nil {
			t.Fatalf("PutShard %s: %v", key, err)
		}
	}
	s := SampleRow{
		SampleID:     "sha256:" + fmt.Sprintf("%064d", 77),
		ManifestJSON: `{"schemaVersion":1,"packages":["pkg:npm/chalk@6.0.0"]}`,
		OriginSeeder: "anonymous", SizeBytes: 512,
	}
	if err := pg.SaveSample(ctx, s); err != nil {
		t.Fatalf("SaveSample: %v", err)
	}

	keys, err := pg.HotShardKeys(ctx, 10)
	if err != nil {
		t.Fatalf("HotShardKeys: %v", err)
	}
	if len(keys) < 2 {
		t.Fatalf("keys = %v, want both shards", keys)
	}
	if keys[0] != "npm/chalk/6" {
		t.Fatalf("keys = %v, want the sample-bearing shard first", keys)
	}
}

// TestIntegrationChangedSinceSeesOutOfBandStatusChanges pins the gap that
// let a corrected status keep serving the old value. Incremental
// aggregation rebuilds only what ChangedSince reports; a quarantine or a
// recompute-status correction touches no evidence row, no new sample and
// no receipt, so without updated_at the materialized shard advertises the
// superseded state indefinitely. Observed live: 25 samples downgraded from
// MATRIX_PASS in the database while their shards still said MATRIX_PASS.
func TestIntegrationChangedSinceSeesOutOfBandStatusChanges(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	purl := "pkg:npm/axios@1.12.0"
	id := "sha256:" + fmt.Sprintf("%064d", 42)
	manifest := `{"schemaVersion":1,"packages":["` + purl + `"]}`
	if err := pg.SaveSample(ctx, SampleRow{
		SampleID: id, ManifestJSON: manifest, Status: "PUBLISHED",
		License: "MIT-0", SizeBytes: 128,
	}); err != nil {
		t.Fatal(err)
	}

	// A moment after creation: nothing has changed since.
	time.Sleep(1100 * time.Millisecond)
	mark := time.Now().UTC()
	changes, err := pg.ChangedSince(ctx, mark)
	if err != nil {
		t.Fatal(err)
	}
	if !changes.Empty() {
		t.Fatalf("expected no changes, got %+v", changes)
	}

	// A status correction must make the package dirty.
	if err := pg.SetSampleStatus(ctx, id, "CROSS_PASS"); err != nil {
		t.Fatal(err)
	}
	changes, err = pg.ChangedSince(ctx, mark)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(changes.SamplePURLs, purl) {
		t.Errorf("a status change outside the request path was not reported: %+v", changes)
	}

	// So must a quarantine — otherwise a taken-down sample keeps being
	// served from the shard it is already in.
	mark2 := time.Now().UTC()
	time.Sleep(1100 * time.Millisecond)
	if err := pg.SetSampleQuarantine(ctx, id, true, "abuse"); err != nil {
		t.Fatal(err)
	}
	changes, err = pg.ChangedSince(ctx, mark2)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(changes.SamplePURLs, purl) {
		t.Errorf("a quarantine was not reported as a change: %+v", changes)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
