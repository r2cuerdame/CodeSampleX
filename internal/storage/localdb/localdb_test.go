package localdb

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func openTemp(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestRemoveSampleRollsBackAllEvidenceOnFailure(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	const sampleID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := db.SaveSample(ctx, SampleRow{
		SampleID: sampleID, ManifestJSON: `{}`, Status: "LOCAL_PASS", HasArtifact: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO receipts(receipt_id, sample_id, json, created_at) VALUES('receipt:test', ?, '{}', '2026-01-01T00:00:00Z')`,
		sampleID); err != nil {
		t.Fatal(err)
	}
	if err := db.IndexDoc(ctx, sampleID, "sample", "rollback sentinel", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `
		CREATE TRIGGER reject_sample_removal BEFORE DELETE ON samples
		BEGIN SELECT RAISE(ABORT, 'blocked'); END`); err != nil {
		t.Fatal(err)
	}

	removed, err := db.RemoveSample(ctx, sampleID)
	if err == nil || removed {
		t.Fatalf("RemoveSample = (%v, %v), want false and trigger error", removed, err)
	}
	if _, ok, err := db.GetSample(ctx, sampleID); err != nil || !ok {
		t.Fatalf("sample after rollback: found=%v err=%v", ok, err)
	}
	receipts, err := db.ReceiptsForSample(ctx, sampleID)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipts after rollback = %d, err=%v", len(receipts), err)
	}
	hits, err := db.FTSQuery(ctx, "rollback sentinel", 10)
	if err != nil || len(hits) != 1 || hits[0].DocID != sampleID {
		t.Fatalf("FTS after rollback = %+v, err=%v", hits, err)
	}
}

func TestOpenMigrateIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "csx.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	p := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	if err := db.UpsertPackage(ctx, p, "UNKNOWN"); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (migrate must be idempotent): %v", err)
	}
	defer db2.Close()
	status, _, ok := db2.GetPublicness(ctx, p)
	if !ok || status != "UNKNOWN" {
		t.Fatalf("row lost across reopen: status=%q ok=%v", status, ok)
	}
	// Third migration on a live handle must also be harmless.
	if err := db2.migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
}

func TestLegacyObservationMigrationAddsAndPreservesReconciliationHighWater(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range ddl {
		legacy := strings.Replace(stmt, "legacy_reconciled_count INTEGER NOT NULL DEFAULT 0,", "", 1)
		if _, err := raw.ExecContext(ctx, legacy); err != nil {
			raw.Close()
			t.Fatalf("create legacy schema: %v", err)
		}
	}
	legacyCode := int64(4294967295)
	if _, err := raw.ExecContext(ctx, `
		INSERT INTO observations(epoch,purl,symbol,env_hash,stage,result,count,error_fp,exit_code,uploaded)
		VALUES('2026-08-27','pkg:npm/axios@1.12.0','','sha256:env','PROJECT_TEST','FAIL',1,'sha256:legacy',?,1)`,
		legacyCode); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy database: %v", err)
	}
	defer db.Close()
	rows, err := db.LegacyWindowsObservations(ctx)
	if err != nil || len(rows) != 1 || rows[0].LegacyReconciledCount != 0 {
		t.Fatalf("migrated legacy rows = %+v, err=%v; want default high-water 0", rows, err)
	}
	key := rows[0].ObsKey
	if err := db.MarkLegacyWindowsObservationReconciled(ctx, key, 4); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkLegacyWindowsObservationReconciled(ctx, key, 3); err != nil {
		t.Fatal(err)
	}
	// RecordObservation is the same explicit-column UPSERT an older binary
	// uses. It must increment the row without resetting the new high-water.
	if err := db.RecordObservation(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	rows, err = db.LegacyWindowsObservations(ctx)
	if err != nil || len(rows) != 1 || rows[0].Count != 2 || rows[0].LegacyReconciledCount != 4 {
		t.Fatalf("legacy-compatible UPSERT rows = %+v, err=%v; want count 2/high-water 4", rows, err)
	}
}

func TestPublicnessRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	p := domain.PURL{Ecosystem: "npm", Name: "@scope/pkg", Version: "2.0.0"}

	if _, _, ok := db.GetPublicness(ctx, p); ok {
		t.Fatal("GetPublicness on missing row: ok must be false")
	}

	if err := db.UpsertPackage(ctx, p, "UNKNOWN"); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	status, checkedAt, ok := db.GetPublicness(ctx, p)
	if !ok || status != "UNKNOWN" {
		t.Fatalf("got status=%q ok=%v, want UNKNOWN true", status, ok)
	}
	if !checkedAt.IsZero() {
		t.Fatalf("never registry-checked, checkedAt must be zero, got %v", checkedAt)
	}

	before := time.Now().Add(-time.Second)
	if err := db.SetPublicness(ctx, p, "PUBLIC"); err != nil {
		t.Fatalf("SetPublicness: %v", err)
	}
	status, checkedAt, ok = db.GetPublicness(ctx, p)
	if !ok || status != "PUBLIC" {
		t.Fatalf("got status=%q ok=%v, want PUBLIC true", status, ok)
	}
	if checkedAt.Before(before) || checkedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("checkedAt not recent: %v", checkedAt)
	}

	// A later scan reporting UNKNOWN must not clobber the cached verdict.
	if err := db.UpsertPackage(ctx, p, "UNKNOWN"); err != nil {
		t.Fatalf("UpsertPackage: %v", err)
	}
	status, checkedAt2, _ := db.GetPublicness(ctx, p)
	if status != "PUBLIC" {
		t.Fatalf("UNKNOWN upsert clobbered cache: status=%q", status)
	}
	if !checkedAt2.Equal(checkedAt) {
		t.Fatalf("UNKNOWN upsert changed checkedAt: %v != %v", checkedAt2, checkedAt)
	}

	// SetPublicness on a purl with no prior row inserts it.
	q := domain.PURL{Ecosystem: "pypi", Name: "requests", Version: "2.32.0"}
	if err := db.SetPublicness(ctx, q, "PRIVATE"); err != nil {
		t.Fatalf("SetPublicness insert: %v", err)
	}
	status, _, ok = db.GetPublicness(ctx, q)
	if !ok || status != "PRIVATE" {
		t.Fatalf("got status=%q ok=%v, want PRIVATE true", status, ok)
	}
}

func TestListPackages(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	a := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	b := domain.PURL{Ecosystem: "golang", Name: "github.com/x/y", Version: "v1.2.0"}
	if err := db.UpsertPackage(ctx, a, "PUBLIC"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPackage(ctx, b, "PRIVATE"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListPackages(ctx)
	if err != nil {
		t.Fatalf("ListPackages: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	byPURL := map[string]PackageRow{}
	for _, r := range rows {
		byPURL[r.PURL.String()] = r
		if r.FirstSeen.IsZero() || r.LastSeen.IsZero() {
			t.Fatalf("seen timestamps missing for %v", r.PURL)
		}
	}
	if r := byPURL[a.String()]; r.Publicness != "PUBLIC" || !r.Public {
		t.Fatalf("axios: %+v", r)
	}
	if r := byPURL[b.String()]; r.Publicness != "PRIVATE" || r.Public {
		t.Fatalf("go module: %+v", r)
	}
}

func TestObservationUpsertIncrements(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	key := ObsKey{
		Epoch: "2026-08-13", PURL: "pkg:npm/axios@1.12.0",
		Symbol: "axios.post", SymbolConfidence: domain.SymbolProbable,
		EnvHash: "sha256:abc", Stage: domain.StageProjectCompile,
		Result: domain.ResultPass,
	}
	if err := db.RecordObservation(ctx, key, 1); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}
	if err := db.RecordObservation(ctx, key, 2); err != nil {
		t.Fatalf("RecordObservation upsert: %v", err)
	}
	rows, err := db.PendingObservations(ctx, 10)
	if err != nil {
		t.Fatalf("PendingObservations: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d pending rows, want 1", len(rows))
	}
	if rows[0].Count != 3 {
		t.Fatalf("count = %d, want 3", rows[0].Count)
	}
	if !reflect.DeepEqual(rows[0].ObsKey, key) {
		t.Fatalf("key round-trip: %+v != %+v", rows[0].ObsKey, key)
	}

	// A different result is a separate aggregate row.
	failKey := key
	failKey.Result = domain.ResultFail
	failKey.ErrorFP = "sha256:def"
	failKey.ErrorCode = "TS2345"
	if err := db.RecordObservation(ctx, failKey, 1); err != nil {
		t.Fatal(err)
	}
	rows, err = db.PendingObservations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d pending rows, want 2", len(rows))
	}
}

func TestObservationUploadLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	key := ObsKey{
		Epoch: "2026-08-13", PURL: "pkg:pypi/requests@2.32.0",
		SymbolConfidence: domain.SymbolUnknown, EnvHash: "sha256:e1",
		Stage: domain.StageProjectTest, Result: domain.ResultPass,
	}
	if err := db.RecordObservation(ctx, key, 5); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkObservationsUploaded(ctx, []ObsKey{key}); err != nil {
		t.Fatalf("MarkObservationsUploaded: %v", err)
	}
	rows, err := db.PendingObservations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("uploaded row still pending: %+v", rows)
	}
	// New evidence for the same aggregate re-queues the row.
	if err := db.RecordObservation(ctx, key, 1); err != nil {
		t.Fatal(err)
	}
	rows, err = db.PendingObservations(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Count != 6 {
		t.Fatalf("re-queue after upload: %+v", rows)
	}
}

func TestEnvironmentRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	fp := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "windows", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18.1", ModuleSystem: "esm",
	}
	if err := db.SaveEnvironment(ctx, fp); err != nil {
		t.Fatalf("SaveEnvironment: %v", err)
	}
	got, ok, err := db.GetEnvironment(ctx, fp.Hash())
	if err != nil || !ok {
		t.Fatalf("GetEnvironment: ok=%v err=%v", ok, err)
	}
	if got.Hash() != fp.Hash() {
		t.Fatalf("fingerprint mutated in storage: %+v", got)
	}
	if _, ok, err := db.GetEnvironment(ctx, "sha256:nope"); err != nil || ok {
		t.Fatalf("missing env: ok=%v err=%v", ok, err)
	}
	// Idempotent re-save.
	if err := db.SaveEnvironment(ctx, fp); err != nil {
		t.Fatal(err)
	}
}

func TestSymbolUsage(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	p := domain.PURL{Ecosystem: "npm", Name: "axios", Version: "1.12.0"}
	if err := db.RecordSymbolUsage(ctx, p, "axios.post", domain.SymbolProbable, "abc123def456"); err != nil {
		t.Fatalf("RecordSymbolUsage: %v", err)
	}
	// Upsert on same key upgrades confidence.
	if err := db.RecordSymbolUsage(ctx, p, "axios.post", domain.SymbolExact, "abc123def456"); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordSymbolUsage(ctx, p, "axios.get", domain.SymbolProbable, "abc123def456"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.SymbolUsages(ctx, p)
	if err != nil {
		t.Fatalf("SymbolUsages: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d usages, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Symbol == "axios.post" && r.Confidence != domain.SymbolExact {
			t.Fatalf("confidence not upserted: %+v", r)
		}
	}
}

func TestCaseRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	c := domain.Case{
		SchemaVersion: 1, Kind: "HOW", Goal: "post json with axios",
		Packages: []string{"pkg:npm/axios@1.12.0"}, Contract: []string{"response status is 200"},
	}
	c.CaseID = c.ComputeID()
	if err := db.SaveCase(ctx, c); err != nil {
		t.Fatalf("SaveCase: %v", err)
	}
	got, ok, err := db.GetCase(ctx, c.CaseID)
	if err != nil || !ok {
		t.Fatalf("GetCase: ok=%v err=%v", ok, err)
	}
	if got.Goal != c.Goal || got.CaseID != c.CaseID {
		t.Fatalf("case round-trip: %+v", got)
	}
}

func TestSampleRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	now := time.Now().UTC().Truncate(time.Second)
	s := SampleRow{
		SampleID: "sha256:aa11", CaseID: "case:sha256:bb22",
		ManifestJSON: `{"schemaVersion":1}`, Status: "LOCAL",
		OriginSeeder: "anonymous", License: "MIT-0", CreatedAt: now,
	}
	if err := db.SaveSample(ctx, s); err != nil {
		t.Fatalf("SaveSample: %v", err)
	}
	got, ok, err := db.GetSample(ctx, s.SampleID)
	if err != nil || !ok {
		t.Fatalf("GetSample: ok=%v err=%v", ok, err)
	}
	if got.Status != "LOCAL" || got.License != "MIT-0" || !got.CreatedAt.Equal(now) {
		t.Fatalf("sample round-trip: %+v", got)
	}
	if got.Pinned || got.HasArtifact || got.HotScore != 0 || !got.LastUsed.IsZero() {
		t.Fatalf("zero-value fields dirty: %+v", got)
	}

	if err := db.SetSampleStatus(ctx, s.SampleID, "LOCAL_PASS"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSamplePinned(ctx, s.SampleID, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSampleHot(ctx, s.SampleID, 4.5); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchSample(ctx, s.SampleID); err != nil {
		t.Fatal(err)
	}
	got, _, err = db.GetSample(ctx, s.SampleID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "LOCAL_PASS" || !got.Pinned || got.HotScore != 4.5 || got.LastUsed.IsZero() {
		t.Fatalf("updates lost: %+v", got)
	}

	list, err := db.ListSamples(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListSamples: n=%d err=%v", len(list), err)
	}

	if _, ok, err := db.GetSample(ctx, "sha256:none"); err != nil || ok {
		t.Fatalf("missing sample: ok=%v err=%v", ok, err)
	}
}

func TestFTSInsertAndRank(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.IndexDoc(ctx, "case:1", "case", "Post JSON with axios",
		"How to send a POST request using axios in an ESM project",
		"pkg:npm/axios@1.12.0", "axios.post", "ERR_REQUIRE_ESM"); err != nil {
		t.Fatalf("IndexDoc: %v", err)
	}
	if err := db.IndexDoc(ctx, "case:2", "case", "Read a file in Go",
		"os.ReadFile usage in a CLI", "pkg:golang/github.com/x/y@v1.0.0", "os.ReadFile", ""); err != nil {
		t.Fatal(err)
	}

	hits, err := db.FTSQuery(ctx, "axios", 10)
	if err != nil {
		t.Fatalf("FTSQuery: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1: %+v", len(hits), hits)
	}
	if hits[0].DocID != "case:1" || hits[0].Kind != "case" {
		t.Fatalf("wrong hit: %+v", hits[0])
	}
	if hits[0].Score <= 0 {
		t.Fatalf("score must be positive (higher = better): %v", hits[0].Score)
	}

	// Multi-term OR query ranks the doc matching more terms first.
	hits, err = db.FTSQuery(ctx, "axios post request", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].DocID != "case:1" {
		t.Fatalf("ranking: %+v", hits)
	}
}

func TestFTSIndexDocIdempotent(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	for i := 0; i < 3; i++ {
		if err := db.IndexDoc(ctx, "doc:x", "sample", "vitest setup", "configure vitest", "pkg:npm/vitest@2.0.0", "", ""); err != nil {
			t.Fatalf("IndexDoc #%d: %v", i, err)
		}
	}
	hits, err := db.FTSQuery(ctx, "vitest", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("delete+insert not idempotent, got %d hits", len(hits))
	}
}

func TestFTSQueryQuotesUserTerms(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if err := db.IndexDoc(ctx, "doc:1", "case", "esm import error", "fix ERR_REQUIRE_ESM", "", "", "ERR_REQUIRE_ESM"); err != nil {
		t.Fatal(err)
	}
	// FTS5 syntax metacharacters in user input must not cause query errors.
	for _, q := range []string{
		`esm OR ) AND ( NEAR "`,
		`esm*`,
		`"esm`,
		`col:esm`,
		`- ( ) *`,
		"",
		"   ",
	} {
		hits, err := db.FTSQuery(ctx, q, 5)
		if err != nil {
			t.Fatalf("FTSQuery(%q): %v", q, err)
		}
		_ = hits
	}
	hits, err := db.FTSQuery(ctx, `esm OR ) AND (`, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].DocID != "doc:1" {
		t.Fatalf("quoted term lost matching power: %+v", hits)
	}
}

func TestShardRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if _, ok, err := db.GetShard(ctx, "npm/axios/1"); err != nil || ok {
		t.Fatalf("missing shard: ok=%v err=%v", ok, err)
	}
	if err := db.SaveShard(ctx, "npm/axios/1", `W/"e1"`, `{"schemaVersion":1}`); err != nil {
		t.Fatalf("SaveShard: %v", err)
	}
	got, ok, err := db.GetShard(ctx, "npm/axios/1")
	if err != nil || !ok {
		t.Fatalf("GetShard: ok=%v err=%v", ok, err)
	}
	if got.ETag != `W/"e1"` || got.JSON != `{"schemaVersion":1}` || got.SyncedAt.IsZero() {
		t.Fatalf("shard round-trip: %+v", got)
	}
	// Upsert replaces.
	if err := db.SaveShard(ctx, "npm/axios/1", `W/"e2"`, `{"schemaVersion":1,"n":2}`); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListShards(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListShards: n=%d err=%v", len(list), err)
	}
	if list[0].ETag != `W/"e2"` {
		t.Fatalf("upsert did not replace: %+v", list[0])
	}
}

func TestQueueLifecycle(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	id1, err := db.Enqueue(ctx, "evidence", `{"batch":1}`)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	id2, err := db.Enqueue(ctx, "receipt", `{"r":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if id2 <= id1 {
		t.Fatalf("ids not monotonic: %d then %d", id1, id2)
	}

	items, err := db.QueuePending(ctx, 10)
	if err != nil {
		t.Fatalf("QueuePending: %v", err)
	}
	if len(items) != 2 || items[0].ID != id1 || items[1].ID != id2 {
		t.Fatalf("pending order: %+v", items)
	}
	if items[0].Kind != "evidence" || items[0].Payload != `{"batch":1}` || items[0].Attempts != 0 {
		t.Fatalf("item fields: %+v", items[0])
	}
	if items[0].CreatedAt.IsZero() {
		t.Fatalf("created_at missing: %+v", items[0])
	}

	if err := db.QueueMarkFailed(ctx, id1, "server unreachable"); err != nil {
		t.Fatalf("QueueMarkFailed: %v", err)
	}
	items, err = db.QueuePending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].Attempts != 1 || items[0].LastError != "server unreachable" {
		t.Fatalf("failure not recorded: %+v", items[0])
	}

	if err := db.QueueMarkDone(ctx, id2); err != nil {
		t.Fatalf("QueueMarkDone: %v", err)
	}
	items, err = db.QueuePending(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != id1 {
		t.Fatalf("done item still pending: %+v", items)
	}
}

func TestReceipts(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	r := domain.VerificationReceipt{
		SchemaVersion: 1, SampleID: "sha256:s1", CaseID: "case:sha256:c1",
		EnvironmentHash: "sha256:e1",
		Environment:     domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64"},
		Stages:          map[string]string{"resolve": "PASS", "contract": "PASS"},
		VerifierAdapter: "node-typescript@1", SandboxCapability: domain.CapContainerRun,
		LogsDigest: "sha256:l1", CreatedAt: "2026-08-13T00:00:00Z",
		PeerID: "ed25519:0011223344556677",
	}
	if err := db.SaveReceipt(ctx, r); err != nil {
		t.Fatalf("SaveReceipt: %v", err)
	}
	// Content-addressed: saving the identical receipt twice keeps one row.
	if err := db.SaveReceipt(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := db.ReceiptsForSample(ctx, "sha256:s1")
	if err != nil {
		t.Fatalf("ReceiptsForSample: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d receipts, want 1", len(got))
	}
	if got[0].ReceiptID() != r.ReceiptID() {
		t.Fatalf("receipt mutated in storage")
	}
}

func TestHits(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	n, err := db.CountHits(ctx)
	if err != nil || n != 0 {
		t.Fatalf("CountHits empty: n=%d err=%v", n, err)
	}
	ts := time.Now().UTC().Truncate(time.Second)
	if err := db.RecordHit(ctx, HitRow{TS: ts, Query: "axios post", Grade: domain.GradeExact, SampleID: "sha256:s1", Adopted: true, PostBuildPass: sql.NullBool{Bool: true, Valid: true}}); err != nil {
		t.Fatalf("RecordHit: %v", err)
	}
	if err := db.RecordHit(ctx, HitRow{TS: ts, Query: "unknown thing", Grade: domain.GradeNoSafeMatch}); err != nil {
		t.Fatal(err)
	}
	n, err = db.CountHits(ctx)
	if err != nil || n != 2 {
		t.Fatalf("CountHits: n=%d err=%v", n, err)
	}
	rows, err := db.ListHits(ctx, 10)
	if err != nil {
		t.Fatalf("ListHits: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Newest first.
	if rows[0].Grade != domain.GradeNoSafeMatch || rows[0].Adopted || rows[0].PostBuildPass.Valid {
		t.Fatalf("row 0: %+v", rows[0])
	}
	if rows[1].Grade != domain.GradeExact || !rows[1].Adopted ||
		!rows[1].PostBuildPass.Valid || !rows[1].PostBuildPass.Bool || !rows[1].TS.Equal(ts) {
		t.Fatalf("row 1: %+v", rows[1])
	}
}

func TestStats(t *testing.T) {
	ctx := context.Background()
	db := openTemp(t)
	if _, ok, err := db.GetStat(ctx, "observations_total"); err != nil || ok {
		t.Fatalf("missing stat: ok=%v err=%v", ok, err)
	}
	if err := db.SetStat(ctx, "observations_total", "42"); err != nil {
		t.Fatalf("SetStat: %v", err)
	}
	if err := db.SetStat(ctx, "observations_total", "43"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetStat(ctx, "last_sync", "2026-08-13T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := db.GetStat(ctx, "observations_total")
	if err != nil || !ok || v != "43" {
		t.Fatalf("GetStat: v=%q ok=%v err=%v", v, ok, err)
	}
	all, err := db.AllStats(ctx)
	if err != nil {
		t.Fatalf("AllStats: %v", err)
	}
	if len(all) != 2 || all["observations_total"] != "43" || all["last_sync"] != "2026-08-13T00:00:00Z" {
		t.Fatalf("AllStats: %+v", all)
	}
}

func TestRecordAndQueryCLIExperience(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	coord := domain.CLIExperienceCoordinate{
		Tool:        "docker",
		ToolVersion: "27.1.0",
		Subcommand:  "compose up",
		ArgsPattern: "-d",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1,
			OS:            "linux",
			Arch:          "amd64",
		},
	}

	exit0 := 0
	exit1 := 1

	// Record 5 field passes
	err := db.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
		Coordinate:  coord,
		Provenance:  domain.ProvenanceField,
		Result:      domain.ResultPass,
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit0},
		ObservedAt:  "2026-09-01T10:00:00Z",
		Count:       5,
	})
	if err != nil {
		t.Fatalf("Record field pass: %v", err)
	}

	// Record 1 field failure
	err = db.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
		Coordinate:   coord,
		Provenance:   domain.ProvenanceField,
		Result:       domain.ResultFail,
		Termination:  domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit1},
		ErrorCode:    "EADDRINUSE",
		ErrorSummary: "port 8080 already allocated",
		ObservedAt:   "2026-09-02T10:00:00Z",
		Count:        1,
	})
	if err != nil {
		t.Fatalf("Record field fail: %v", err)
	}

	// Record 1 farm verification pass
	err = db.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
		Coordinate:  coord,
		Provenance:  domain.ProvenanceFarm,
		Result:      domain.ResultPass,
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit0},
		ObservedAt:  "2026-09-03T10:00:00Z",
		Count:       1,
	})
	if err != nil {
		t.Fatalf("Record farm pass: %v", err)
	}

	summary, err := db.QueryCLIExperience(ctx, coord)
	if err != nil {
		t.Fatalf("QueryCLIExperience: %v", err)
	}

	if summary.Status != "COEXISTING_BOUNDARY" {
		t.Errorf("summary.Status = %q, want COEXISTING_BOUNDARY", summary.Status)
	}
	if summary.FieldPassCount != 5 {
		t.Errorf("summary.FieldPassCount = %d, want 5", summary.FieldPassCount)
	}
	if summary.FieldFailCount != 1 {
		t.Errorf("summary.FieldFailCount = %d, want 1", summary.FieldFailCount)
	}
	if summary.FarmPassCount != 1 {
		t.Errorf("summary.FarmPassCount = %d, want 1", summary.FarmPassCount)
	}
	if len(summary.RecentFailures) != 1 {
		t.Fatalf("RecentFailures count = %d, want 1", len(summary.RecentFailures))
	}
	if summary.RecentFailures[0].ErrorCode != "EADDRINUSE" {
		t.Errorf("Failure error code = %q, want EADDRINUSE", summary.RecentFailures[0].ErrorCode)
	}
	text := summary.TextSummary()
	if strings.Contains(strings.ToLower(text), "memory") {
		t.Errorf("User-facing text must not contain 'memory':\n%s", text)
	}
}

func TestStructuredCLIExecutionEvidenceAccumulatesComparableRunsWithoutCollapsingSignatures(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	exit := 1
	coord := domain.CLIExperienceCoordinate{
		Tool: "git", ToolVersion: "2.55.0", Subcommand: "status", ArgsPattern: "--short", Shell: "direct",
		Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, OS: "windows", Arch: "x64", Runtime: "go", RuntimeVersion: "1.26"},
	}
	base := domain.CLIExperienceObservation{
		Coordinate: coord, Provenance: domain.ProvenanceField, Result: domain.ResultFail,
		Termination:      domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit},
		ErrorFingerprint: "sha256:error-a", EvidenceQuality: domain.EvidenceComplete,
		Stdout:    domain.CLIStreamEvidence{Fingerprint: "sha256:stdout-a", Excerpt: "ordinary output"},
		Stderr:    domain.CLIStreamEvidence{Fingerprint: "sha256:stderr-a", Excerpt: "failure <path>"},
		StartedAt: "2026-09-07T01:00:00Z", FinishedAt: "2026-09-07T01:00:01Z", ObservedAt: "2026-09-07T01:00:01Z", Count: 1,
	}
	if err := db.RecordCLIExperienceObservation(ctx, base); err != nil {
		t.Fatalf("record first evidence: %v", err)
	}
	later := base
	later.StartedAt = "2026-09-07T02:00:00Z"
	later.FinishedAt = "2026-09-07T02:00:01Z"
	later.ObservedAt = later.FinishedAt
	if err := db.RecordCLIExperienceObservation(ctx, later); err != nil {
		t.Fatalf("record repeated evidence: %v", err)
	}
	different := later
	different.Stderr = domain.CLIStreamEvidence{Fingerprint: "sha256:stderr-b", Excerpt: "different failure"}
	if err := db.RecordCLIExperienceObservation(ctx, different); err != nil {
		t.Fatalf("record distinct evidence: %v", err)
	}
	clipped := later
	clipped.Stdout.Truncated = true
	if err := db.RecordCLIExperienceObservation(ctx, clipped); err != nil {
		t.Fatalf("record clipped evidence: %v", err)
	}

	rows, err := db.ListCLIExecutionEvidence(ctx, coord, 10)
	if err != nil {
		t.Fatalf("ListCLIExecutionEvidence: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("evidence rows = %d, want 3", len(rows))
	}
	counts := map[string]int64{}
	for _, row := range rows {
		if row.ID == "" {
			t.Fatal("ListCLIExecutionEvidence row has empty ID")
		}
		if !row.IsHighInformation {
			t.Fatal("failure execution evidence must be marked isHighInformation")
		}
		key := row.Stderr.Fingerprint
		if row.Stdout.Truncated {
			key += ":truncated"
		}
		counts[key] = row.Count
	}
	if counts["sha256:stderr-a"] != 2 || counts["sha256:stderr-a:truncated"] != 1 || counts["sha256:stderr-b"] != 1 {
		t.Fatalf("signature counts = %#v", counts)
	}
}

func TestCLIExperienceAggregateKeyCollisionPrevention(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	coord1 := domain.CLIExperienceCoordinate{
		Tool:        "docker",
		ToolVersion: "27.1.0",
		Subcommand:  "compose up",
		ArgsPattern: "-d",
		Environment: domain.EnvironmentFingerprint{
			SchemaVersion: 1,
			OS:            "linux",
			Arch:          "amd64",
		},
	}
	coord2 := coord1
	coord2.ArgsPattern = "--build"

	exit0 := 0
	sameDay := "2026-09-01T10:00:00Z"

	// 1. Field PASS for coord1 (-d) on sameDay
	if err := db.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
		Coordinate:  coord1,
		Provenance:  domain.ProvenanceField,
		Result:      domain.ResultPass,
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit0},
		ObservedAt:  sameDay,
		Count:       3,
	}); err != nil {
		t.Fatalf("Record field pass: %v", err)
	}

	// 2. Farm PASS for coord1 (-d) on the EXACT sameDay
	if err := db.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
		Coordinate:  coord1,
		Provenance:  domain.ProvenanceFarm,
		Result:      domain.ResultPass,
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit0},
		ObservedAt:  sameDay,
		Count:       2,
	}); err != nil {
		t.Fatalf("Record farm pass: %v", err)
	}

	// 3. Field PASS for coord2 (--build) on the EXACT sameDay
	if err := db.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
		Coordinate:  coord2,
		Provenance:  domain.ProvenanceField,
		Result:      domain.ResultPass,
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit0},
		ObservedAt:  sameDay,
		Count:       4,
	}); err != nil {
		t.Fatalf("Record field pass coord2: %v", err)
	}

	summary1, err := db.QueryCLIExperience(ctx, coord1)
	if err != nil {
		t.Fatalf("QueryCLIExperience coord1: %v", err)
	}
	if summary1.FieldPassCount != 3 {
		t.Errorf("summary1.FieldPassCount = %d, want 3", summary1.FieldPassCount)
	}
	if summary1.FarmPassCount != 2 {
		t.Errorf("summary1.FarmPassCount = %d, want 2", summary1.FarmPassCount)
	}

	summary2, err := db.QueryCLIExperience(ctx, coord2)
	if err != nil {
		t.Fatalf("QueryCLIExperience coord2: %v", err)
	}
	if summary2.FieldPassCount != 4 {
		t.Errorf("summary2.FieldPassCount = %d, want 4", summary2.FieldPassCount)
	}
	if summary2.FarmPassCount != 0 {
		t.Errorf("summary2.FarmPassCount = %d, want 0", summary2.FarmPassCount)
	}
}

func TestCLIExperienceExcludesDependencyEvidenceRows(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	coord := domain.CLIExperienceCoordinate{
		Tool:        "docker",
		ToolVersion: "27.1.0",
		Subcommand:  "compose up",
	}

	exit0 := 0
	// Record 1 real CLI experience observation
	if err := db.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
		Coordinate:  coord,
		Provenance:  domain.ProvenanceField,
		Result:      domain.ResultPass,
		Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exit0},
		ObservedAt:  "2026-09-01T10:00:00Z",
		Count:       1,
	}); err != nil {
		t.Fatalf("RecordCLIExperienceObservation: %v", err)
	}

	// Insert ordinary dependency package observations where outer_command is "docker compose up"
	envHash := coord.Environment.Hash()
	for i := 0; i < 10; i++ {
		err := db.RecordObservation(ctx, ObsKey{
			Epoch:        "2026-09-01",
			PURL:         fmt.Sprintf("pkg:golang/github.com/example/dep%d@v1.0.0", i),
			Symbol:       "SomeFunc",
			EnvHash:      envHash,
			Stage:        domain.StageProjectProcess,
			Result:       domain.ResultFail,
			OuterCommand: "docker compose up",
		}, 1)
		if err != nil {
			t.Fatalf("RecordObservation dep: %v", err)
		}
	}

	summary, err := db.QueryCLIExperience(ctx, coord)
	if err != nil {
		t.Fatalf("QueryCLIExperience: %v", err)
	}

	// Must NOT count dependency failure rows as CLI executions!
	if summary.FieldPassCount != 1 {
		t.Errorf("FieldPassCount = %d, want 1", summary.FieldPassCount)
	}
	if summary.FieldFailCount != 0 {
		t.Errorf("FieldFailCount = %d, want 0 (dependency rows must not be counted)", summary.FieldFailCount)
	}
}

func TestRecordAndQueryCLIExperiencePreservesArgumentPlaceholders(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1,
		OS:            "linux",
		Arch:          "amd64",
	}

	exit0 := 0
	exit1 := 1

	testCases := []struct {
		name        string
		coord       domain.CLIExperienceCoordinate
		result      domain.Result
		exitCode    *int
		wantStatus  string
		wantPassCnt int64
		wantFailCnt int64
	}{
		{
			name: "command with branch placeholder",
			coord: domain.CLIExperienceCoordinate{
				Tool:        "git",
				ToolVersion: "2.43.0",
				Subcommand:  "checkout",
				ArgsPattern: "-b <branch>",
				Environment: env,
			},
			result:      domain.ResultPass,
			exitCode:    &exit0,
			wantStatus:  "OBSERVED_PASS",
			wantPassCnt: 1,
			wantFailCnt: 0,
		},
		{
			name: "command with path placeholder",
			coord: domain.CLIExperienceCoordinate{
				Tool:        "git",
				ToolVersion: "2.43.0",
				Subcommand:  "add",
				ArgsPattern: "<path>",
				Environment: env,
			},
			result:      domain.ResultFail,
			exitCode:    &exit1,
			wantStatus:  "OBSERVED_FAIL",
			wantPassCnt: 0,
			wantFailCnt: 1,
		},
		{
			name: "command with url placeholder",
			coord: domain.CLIExperienceCoordinate{
				Tool:        "git",
				ToolVersion: "2.43.0",
				Subcommand:  "clone",
				ArgsPattern: "<url>",
				Environment: env,
			},
			result:      domain.ResultPass,
			exitCode:    &exit0,
			wantStatus:  "OBSERVED_PASS",
			wantPassCnt: 1,
			wantFailCnt: 0,
		},
		{
			name: "command with path and branch placeholders",
			coord: domain.CLIExperienceCoordinate{
				Tool:        "git",
				ToolVersion: "2.43.0",
				Subcommand:  "worktree add",
				ArgsPattern: "<path> <branch>",
				Environment: env,
			},
			result:      domain.ResultPass,
			exitCode:    &exit0,
			wantStatus:  "OBSERVED_PASS",
			wantPassCnt: 1,
			wantFailCnt: 0,
		},
		{
			name: "command with hash placeholder",
			coord: domain.CLIExperienceCoordinate{
				Tool:        "git",
				ToolVersion: "2.43.0",
				Subcommand:  "checkout",
				ArgsPattern: "<hash>",
				Environment: env,
			},
			result:      domain.ResultFail,
			exitCode:    &exit1,
			wantStatus:  "OBSERVED_FAIL",
			wantPassCnt: 0,
			wantFailCnt: 1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := db.RecordCLIExperienceObservation(ctx, domain.CLIExperienceObservation{
				Coordinate:  tc.coord,
				Provenance:  domain.ProvenanceField,
				Result:      tc.result,
				Termination: domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: tc.exitCode},
				ObservedAt:  "2026-09-01T10:00:00Z",
				Count:       1,
			})
			if err != nil {
				t.Fatalf("RecordCLIExperienceObservation failed: %v", err)
			}

			summary, err := db.QueryCLIExperience(ctx, tc.coord)
			if err != nil {
				t.Fatalf("QueryCLIExperience failed: %v", err)
			}

			if summary.Status == "UNOBSERVED" {
				t.Fatalf("QueryCLIExperience returned UNOBSERVED; argument placeholder pattern %q was not recalled", tc.coord.ArgsPattern)
			}
			if summary.Status != tc.wantStatus {
				t.Errorf("summary.Status = %q, want %q", summary.Status, tc.wantStatus)
			}
			if summary.FieldPassCount != tc.wantPassCnt {
				t.Errorf("summary.FieldPassCount = %d, want %d", summary.FieldPassCount, tc.wantPassCnt)
			}
			if summary.FieldFailCount != tc.wantFailCnt {
				t.Errorf("summary.FieldFailCount = %d, want %d", summary.FieldFailCount, tc.wantFailCnt)
			}
		})
	}
}

