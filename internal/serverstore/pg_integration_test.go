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
	"errors"
	"fmt"
	"os"
	"strings"
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

func TestIntegrationAuthoringSessionsPersistRefreshAndRevoke(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 18, 3, 0, 0, 0, time.UTC)
	row := AuthoringSessionRow{
		TokenHash:     "f65ad1d2e47e11bd71619acda03e7c2fe6f0f80ea5f35d70f27097c12808e6d7",
		SessionID:     "worker-persist-01",
		Label:         "spring-lab",
		Model:         "agy",
		Reasoning:     "auto",
		IssuedAt:      now,
		IdleExpiresAt: now.Add(time.Hour),
	}
	if err := pg.IssueAuthoringSessions(ctx, []AuthoringSessionRow{row}, now); err != nil {
		t.Fatalf("issue authoring session: %v", err)
	}

	listed, err := pg.ListAuthoringSessions(ctx, now.Add(time.Minute), MaxAuthoringSessions)
	if err != nil || len(listed) != 1 || listed[0].TokenHash != row.TokenHash {
		t.Fatalf("persisted sessions = %+v, err=%v", listed, err)
	}
	rotatedHash := "b8b34ad5632dc27fb9d2f6ef35b9ec73fbb6d09a4e476cc0d6cf32d9888bc3f0"
	rotated, err := pg.RotateAuthoringSession(ctx, row.SessionID, rotatedHash, now.Add(2*time.Minute), now.Add(62*time.Minute))
	if err != nil || rotated.TokenHash != rotatedHash {
		t.Fatalf("rotate authoring session = %+v, err=%v", rotated, err)
	}
	if _, err := pg.RefreshAuthoringSession(ctx, row.TokenHash, "", "", now.Add(3*time.Minute), now.Add(63*time.Minute)); !errors.Is(err, ErrAuthoringSessionMissing) {
		t.Fatalf("old token refresh after rotate err=%v", err)
	}
	row.TokenHash = rotatedHash
	refreshedAt := now.Add(45 * time.Minute)
	refreshed, err := pg.RefreshAuthoringSession(ctx, row.TokenHash, "198.51.100.24", "build-node-7", refreshedAt, refreshedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("refresh authoring session: %v", err)
	}
	if refreshed.LastRefreshIP != "198.51.100.24" || refreshed.ComputerName != "build-node-7" || !refreshed.IdleExpiresAt.Equal(refreshedAt.Add(time.Hour)) {
		t.Fatalf("refreshed session = %+v", refreshed)
	}
	draft := AuthoringDraftRow{
		SampleID: "sha256:private-draft", SessionID: row.SessionID, WorkerLabel: row.Label,
		ManifestJSON: `{"schemaVersion":1,"case":{"goal":"private"}}`, LocalStatus: "LOCAL_PASS",
		CreatedAt: refreshedAt, UpdatedAt: refreshedAt,
	}
	work, claimed, err := pg.ClaimAuthoringWork(ctx, row.SessionID, []WantedRow{{
		Ecosystem: "npm", Name: "private-lib", Version: "1.0.0", Symbol: "private.call", Asks: 3,
	}}, refreshedAt, refreshedAt.Add(24*time.Hour))
	if err != nil || !claimed {
		t.Fatalf("claim authoring work = %+v %v %v", work, claimed, err)
	}
	if attached, err := pg.AttachAuthoringWorkSample(ctx, row.SessionID, work, draft.SampleID, refreshedAt); err != nil || !attached {
		t.Fatalf("attach authoring sample = %v %v", attached, err)
	}
	if err := pg.SaveAuthoringDraft(ctx, draft); err != nil {
		t.Fatalf("save authoring draft: %v", err)
	}
	if err := pg.SaveSample(ctx, SampleRow{SampleID: draft.SampleID, ManifestJSON: draft.ManifestJSON,
		Status: "DRAFT", Quarantined: true, QuarantineReason: "private authoring draft"}); err != nil {
		t.Fatalf("save private draft sample: %v", err)
	}
	jobID, err := pg.CreateJob(ctx, JobRow{SampleID: draft.SampleID, Reason: "cross"})
	if err != nil {
		t.Fatal(err)
	}
	const verifierPeer = "ed25519:0123456789abcdef"
	if ok, err := pg.ClaimJob(ctx, jobID, verifierPeer); err != nil || !ok {
		t.Fatalf("claim private draft: %v, %v", ok, err)
	}
	if ok, err := pg.SaveReceiptForJob(ctx, ReceiptRow{ReceiptID: "sha256:private-cross-pass",
		SampleID: draft.SampleID, PeerID: verifierPeer, ContractResult: "PASS", ReceiptJSON: `{}`}, jobID); err != nil || !ok {
		t.Fatalf("cross private draft: %v, %v", ok, err)
	}
	drafts, err := pg.ListAuthoringDrafts(ctx, 100)
	if err != nil || len(drafts) != 1 || drafts[0].SampleID != draft.SampleID || drafts[0].WorkerLabel != row.Label || drafts[0].VerificationStatus != "CROSS_PASS" {
		t.Fatalf("persisted drafts = %+v, err=%v", drafts, err)
	}
	promoted, ok, err := pg.GetSample(ctx, draft.SampleID)
	if err != nil || !ok || promoted.Quarantined || promoted.Status != "CROSS_PASS" {
		t.Fatalf("promoted draft = %+v ok=%v err=%v", promoted, ok, err)
	}
	if ok, err := pg.RevokeAuthoringSession(ctx, row.SessionID, refreshedAt.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("revoke authoring session: ok=%v err=%v", ok, err)
	}
	if _, err := pg.RefreshAuthoringSession(ctx, row.TokenHash, "198.51.100.25", "other-node", refreshedAt.Add(2*time.Minute), refreshedAt.Add(62*time.Minute)); !errors.Is(err, ErrAuthoringSessionMissing) {
		t.Fatalf("refresh revoked session err=%v", err)
	}
}

func TestIntegrationAuthoringExpansionCandidates(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	const purl = "pkg:npm/undici@8.10.0"
	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
	}
	failing := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", AnonID: "expansionpeer", ProjectBucket: "expansionproject",
		Package: purl, Symbol: "MockAgent", SymbolConfidence: domain.SymbolProbable, Environment: env,
		Stage: domain.StageProjectCompile, Result: domain.ResultFail,
		ErrorFingerprint: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ErrorCode:        "ERR_MOCK_MATCH", ObservationCount: 23,
	}
	passing := failing
	passing.AnonID = "otherpeer"
	passing.ProjectBucket = "otherproject"
	passing.Symbol = "request"
	passing.Result = domain.ResultPass
	passing.ErrorFingerprint = ""
	passing.ErrorCode = ""
	passing.ObservationCount = 7
	if accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{failing, passing}); err != nil || accepted != 2 || len(rejected) != 0 {
		t.Fatalf("ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	if err := pg.UpsertPackage(ctx, PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "undici", Version: "8.10.0", Major: "8", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
	if err := pg.UpsertFailureCluster(ctx, ClusterRow{
		Ecosystem: "npm", PackageName: "undici", Symbol: "MockAgent", Stage: "PROJECT_COMPILE",
		ErrorFingerprint: failing.ErrorFingerprint, ErrorCode: failing.ErrorCode,
		ObservationCount: 23, VersionsJSON: `["8.10.0"]`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:linux-expansion-proof", ManifestJSON: `{"packages":["pkg:npm/undici@8.10.0"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveReceipt(ctx, ReceiptRow{ReceiptID: "receipt-linux-expansion-proof", SampleID: "sha256:linux-expansion-proof", PeerID: "linux-proof", EnvHash: "env-linux-proof", ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`}); err != nil {
		t.Fatal(err)
	}

	// The package-level row this used to expect is gone on purpose: the
	// fixture's sample already proves undici@8.10.0 on linux, and re-offering
	// a proven coordinate is what made 37% of the production corpus redundant.
	candidates, err := pg.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates = %+v err=%v", candidates, err)
	}
	if candidates[0].Kind != "FINDING" || candidates[0].Symbol != "MockAgent" || candidates[0].Score != 23 {
		t.Fatalf("finding candidate = %+v", candidates[0])
	}
	if candidates[1].Kind != "EXPANSION" || candidates[1].Symbol != "request" || candidates[1].Score != 7 {
		t.Fatalf("symbol expansion candidate = %+v", candidates[1])
	}
	for _, c := range candidates {
		if c.Symbol == "" {
			t.Fatalf("a package-level coordinate already proven on linux was offered: %+v", c)
		}
	}
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	claimed, ok, err := pg.ClaimAuthoringWork(ctx, "pg-expansion-writer", candidates, now, now.Add(24*time.Hour))
	if err != nil || !ok || claimed.Kind != "FINDING" || claimed.Score != 23 {
		t.Fatalf("claim = %+v ok=%v err=%v", claimed, ok, err)
	}
	remaining, err := pg.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("remaining = %+v err=%v", remaining, err)
	}
	next, ok, err := pg.ClaimAuthoringWork(ctx, "pg-expansion-writer-2", remaining, now, now.Add(24*time.Hour))
	if err != nil || !ok || next.Kind != "EXPANSION" || next.Symbol != "request" {
		t.Fatalf("second claim = %+v ok=%v err=%v", next, ok, err)
	}
}

func TestIntegrationAuthoringLeaseIsReassignedWhenEnvironmentBecomesIneligible(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	windows := []WantedRow{{Ecosystem: "golang", Name: "github.com/Microsoft/go-winio", Version: "0.6.2", Symbol: "ListenPipe", Kind: "FINDING", TargetOS: "windows"}}
	if claimed, ok, err := pg.ClaimAuthoringWork(ctx, "pg-env-writer", windows, now, now.Add(24*time.Hour)); err != nil || !ok || claimed.Name == "" {
		t.Fatalf("windows claim = %+v ok=%v err=%v", claimed, ok, err)
	}
	linux := []WantedRow{{Ecosystem: "npm", Name: "axios", Version: "1.12.0", Symbol: "axios.post", Kind: "EXPANSION", TargetOS: "linux"}}
	reassigned, ok, err := pg.ClaimAuthoringWork(ctx, "pg-env-writer", linux, now.Add(time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || reassigned.Name != "axios" {
		t.Fatalf("reassigned = %+v ok=%v err=%v", reassigned, ok, err)
	}
	other, ok, err := pg.ClaimAuthoringWork(ctx, "pg-other-writer", windows, now.Add(time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || other.Name != "github.com/Microsoft/go-winio" {
		t.Fatalf("released target unavailable = %+v ok=%v err=%v", other, ok, err)
	}
	packageExpansion := []WantedRow{{Ecosystem: "pypi", Name: "pandas", Version: "3.0.5", Kind: "EXPANSION", Score: 30, TargetOS: "linux"}}
	blank, ok, err := pg.ClaimAuthoringWork(ctx, "pg-package-writer", packageExpansion, now.Add(2*time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok {
		t.Fatalf("package claim = %+v ok=%v err=%v", blank, ok, err)
	}
	if attached, err := pg.AttachAuthoringWorkSample(ctx, "pg-package-writer", blank, "sha256:package-expansion", now.Add(3*time.Minute)); err != nil || !attached {
		t.Fatalf("package attach = %v err=%v", attached, err)
	}
	reopened, ok, err := pg.ClaimAuthoringWork(ctx, "pg-package-writer-2", packageExpansion, now.Add(4*time.Minute), now.Add(24*time.Hour))
	if err != nil || !ok || reopened.Name != "pandas" {
		t.Fatalf("package reopened = %+v ok=%v err=%v", reopened, ok, err)
	}
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
	// Upload validation accepts the human-readable scoped form while the v2
	// resolver records the canonical percent-encoded PURL. Both spellings
	// identify the same package and must close the same Wanted coordinate.
	manifest = `{"packages":["pkg:npm/@scope/pkg@2.0.0"],"symbols":["scoped.call"]}`
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

func TestIntegrationMatrixJobBindingAndAtomicCreation(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	caseID := "case:sha256:matrix-binding"
	if err := pg.SaveCase(ctx, domain.Case{SchemaVersion: 1, CaseID: caseID, Kind: "HOW", Goal: "matrix", Packages: []string{"pkg:maven/example/a@1"}, Contract: []string{"passes"}}); err != nil {
		t.Fatal(err)
	}
	sampleID := "sha256:" + fmt.Sprintf("%064d", 91)
	if err := pg.SaveSample(ctx, SampleRow{SampleID: sampleID, CaseID: caseID, ManifestJSON: `{"schemaVersion":1}`}); err != nil {
		t.Fatal(err)
	}
	want17 := `{"sandboxCapability":"CONTAINER_RUN","verifierAdapter":"maven-java@1","ecosystem":"maven","runtime":"java","runtimeVersion":"17","executionContext":"java"}`
	jobID, err := pg.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "matrix", WantEnvJSON: want17})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := pg.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "matrix", WantEnvJSON: want17})
	if err != nil || duplicate != jobID {
		t.Fatalf("atomic matrix creation: first=%d duplicate=%d err=%v", jobID, duplicate, err)
	}
	peer := "ed25519:0123456789abcdef"
	if ok, err := pg.ClaimJob(ctx, jobID, peer); err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	receipt := ReceiptRow{ReceiptID: "sha256:bound", SampleID: sampleID, PeerID: peer, ReceiptJSON: `{}`, ContractResult: "PASS"}
	if ok, err := pg.SaveReceiptForJob(ctx, receipt, jobID); err != nil || !ok {
		t.Fatalf("save bound receipt: ok=%v err=%v", ok, err)
	}

	want21 := strings.Replace(want17, `"17"`, `"21"`, 1)
	job21, err := pg.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "matrix", WantEnvJSON: want21})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.ClaimJob(ctx, job21, peer); err != nil || !ok {
		t.Fatalf("sequential claim: ok=%v err=%v", ok, err)
	}
	want25 := strings.Replace(want17, `"17"`, `"25"`, 1)
	job25, err := pg.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "matrix", WantEnvJSON: want25})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.ClaimJob(ctx, job25, peer); err != nil || ok {
		t.Fatalf("simultaneous same-peer/sample claim: ok=%v err=%v", ok, err)
	}
	if ok, err := pg.SaveReceiptForJob(ctx, receipt, job21); err != nil || ok {
		t.Fatalf("receipt replay consumed second job: ok=%v err=%v", ok, err)
	}
	job, _, err := pg.Job(ctx, job21)
	if err != nil || job.Status != "claimed" {
		t.Fatalf("replay target = %+v err=%v", job, err)
	}
	visible, err := pg.OpenJobs(ctx, string(domain.CapContainerRun), peer, "matrix", 10)
	if err != nil || !jobRowsContain(visible, job25) {
		t.Fatalf("prior receipt hid sequential matrix work: %+v err=%v", visible, err)
	}
}

func jobRowsContain(rows []JobRow, id int64) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
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
		open, err := pg.OpenJobs(ctx, "", "", "", 10)
		if err != nil || len(open) != 2 {
			t.Fatalf("OpenJobs any: %v err=%v", open, err)
		}
		open, err = pg.OpenJobs(ctx, "COMPILE_ONLY", "", "", 10)
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
		open, err = pg.OpenJobs(ctx, "", "", "", 10)
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
		etag, ok, err = pg.GetShardEtag(ctx, "npm/axios/1")
		if err != nil || !ok || etag != "etag2" {
			t.Fatalf("GetShardEtag: etag=%q ok=%v err=%v", etag, ok, err)
		}
		if _, ok, _ := pg.GetShardEtag(ctx, "npm/axios/2"); ok {
			t.Fatal("GetShardEtag found absent key")
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
	mark := dbNow(t, pg)
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
	mark2 := dbNow(t, pg)
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

// dbNow reads the clock that stamps created_at and updated_at.
//
// A mark taken from the test process's clock compares two different clocks:
// the tests run on the host while PostgreSQL runs in its own container, and a
// skew of tens of milliseconds is enough to hide a row written microseconds
// after the mark. Measured here at 81ms, which made
// TestIntegrationChangedSinceSeesOutOfBandStatusChanges fail four runs in six.
//
// No production caller compares this way: the compatibility builder asks
// ChangedSince for a full minute before its last pass (changeOverlap), and a
// full rebuild every twelfth pass repairs anything a gap still swallowed. The
// mismatch was the test's alone, so the fix belongs here.
func dbNow(t *testing.T, pg *PG) time.Time {
	t.Helper()
	ctx := context.Background()
	var now time.Time
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT now()`).Scan(&now)
	}); err != nil {
		t.Fatalf("reading the database clock: %v", err)
	}
	return now.UTC()
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// The PostgreSQL twin of TestAuthoringExpansionOffersUnmeasuredSiblingVersions
// and TestAuthoringExpansionSpreadsAcrossVersionsBeforeDeepening. The Fake
// mirrors this query, and a divergence between them is a silent production
// bug, so both properties are asserted against real SQL.
func TestIntegrationAuthoringExpansionSpreadsAcrossVersions(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
	}
	base := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", ProjectBucket: "spreadproject",
		Package: "pkg:npm/spreadpkg@3.0.0", SymbolConfidence: domain.SymbolProbable,
		Environment: env, Stage: domain.StageProjectCompile, Result: domain.ResultPass,
		ObservationCount: 40,
	}
	for i, symbol := range []string{"alpha", "beta", "gamma"} {
		b := base
		b.Symbol = symbol
		b.AnonID = fmt.Sprintf("spreadpeer%d", i)
		if accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{b}); err != nil || accepted != 1 || len(rejected) != 0 {
			t.Fatalf("ingest %s = %d rejected=%v err=%v", symbol, accepted, rejected, err)
		}
	}
	for _, v := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		if err := pg.UpsertPackage(ctx, PackageRow{
			PURL: "pkg:npm/spreadpkg@" + v, Ecosystem: "npm", Name: "spreadpkg",
			Version: v, Major: v[:1], Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.UpsertFailureCluster(ctx, ClusterRow{
		Ecosystem: "npm", PackageName: "spreadpkg", Symbol: "delta", Stage: "PROJECT_COMPILE",
		ErrorFingerprint: "sha256:" + strings.Repeat("d", 64), ErrorCode: "ERR_SPREAD",
		ObservationCount: 31, VersionsJSON: `["3.0.0"]`, EnvSummaryJSON: `{"os":"linux"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:spread-proof", ManifestJSON: `{"packages":["pkg:npm/spreadpkg@3.0.0"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveReceipt(ctx, ReceiptRow{
		SampleID: "sha256:spread-proof", ReceiptID: "receipt-spread-proof", PeerID: "spread-proof",
		EnvHash: "env-spread-proof", ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`,
	}); err != nil {
		t.Fatal(err)
	}

	candidates, err := pg.ListAuthoringExpansionCandidates(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	versions := []string{"1.0.0", "2.0.0", "3.0.0"}
	seen := map[string]int{}
	for i, c := range candidates {
		if c.Name != "spreadpkg" {
			continue
		}
		seen[c.Version]++
		if seen[c.Version] < 2 {
			continue
		}
		for _, v := range versions {
			if seen[v] == 0 {
				t.Fatalf("candidate %d is job #%d for %s while %s has none yet; order=%s",
					i, seen[c.Version], c.Version, v, formatCandidateOrder(candidates))
			}
		}
	}
	for _, v := range versions {
		if seen[v] == 0 {
			t.Errorf("version %s never offered; order=%s", v, formatCandidateOrder(candidates))
		}
	}
}

// The PostgreSQL twin of TestAuthoringExpansionCapsSiblingsPerPackage, seeded
// the way the review measured it: one package with a long release history must
// not fill the candidate window with score-0 first jobs and push a
// heavily-observed symbol past the LIMIT. Without the cap this returned 199
// bigpkg rows and no smallpkg work at all.
func TestIntegrationAuthoringExpansionCapsSiblingFlood(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
	}
	seed := func(purl, symbol, anon string, count int) {
		t.Helper()
		b := domain.ObservationBatch{
			SchemaVersion: 1, Epoch: "2026-08-19", AnonID: anon, ProjectBucket: anon + "proj",
			Package: purl, Symbol: symbol, SymbolConfidence: domain.SymbolProbable,
			Environment: env, Stage: domain.StageProjectCompile, Result: domain.ResultPass,
			ObservationCount: count,
		}
		if accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{b}); err != nil || accepted != 1 || len(rejected) != 0 {
			t.Fatalf("ingest %s = %d rejected=%v err=%v", purl, accepted, rejected, err)
		}
	}
	seed("pkg:npm/bigpkg@1.0.0", "big.call", "bigpeer", 3)
	seed("pkg:npm/smallpkg@1.0.0", "small.wanted", "smallpeer", 5000)

	for i := 1; i <= 60; i++ {
		v := fmt.Sprintf("%d.0.0", i)
		if err := pg.UpsertPackage(ctx, PackageRow{
			PURL: "pkg:npm/bigpkg@" + v, Ecosystem: "npm", Name: "bigpkg",
			Version: v, Major: fmt.Sprint(i), Publicness: "PUBLIC",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.UpsertPackage(ctx, PackageRow{
		PURL: "pkg:npm/smallpkg@1.0.0", Ecosystem: "npm", Name: "smallpkg",
		Version: "1.0.0", Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:big-proof", ManifestJSON: `{"packages":["pkg:npm/bigpkg@1.0.0"],"symbols":["big.call"]}`}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveReceipt(ctx, ReceiptRow{
		SampleID: "sha256:big-proof", ReceiptID: "receipt-big-proof", PeerID: "big-proof",
		EnvHash: "env-big-proof", ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`,
	}); err != nil {
		t.Fatal(err)
	}

	candidates, err := pg.ListAuthoringExpansionCandidates(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	siblings, smallReachable := 0, false
	for _, c := range candidates {
		if c.Name == "bigpkg" && c.Version != "1.0.0" {
			siblings++
		}
		if c.Name == "smallpkg" {
			smallReachable = true
		}
	}
	if siblings > authoringSiblingVersionsPerPackage {
		t.Errorf("bigpkg contributed %d sibling rows, cap is %d; order=%s",
			siblings, authoringSiblingVersionsPerPackage, formatCandidateOrder(candidates))
	}
	if !smallReachable {
		t.Errorf("smallpkg's score-5000 work never entered the window; order=%s",
			formatCandidateOrder(candidates))
	}
}

// The NULL-expiry column is the crux of the admin token table, and a Go zero
// time round-tripping through it is exactly the sort of thing the Fake cannot
// prove. Both halves are checked against real SQL.
func TestIntegrationAdminTokenExpiryRevokeAndUse(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	issued := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	if err := pg.IssueAdminTokens(ctx, []AdminTokenRow{
		{TokenHash: "hash-bounded", TokenID: "bounded", Label: "bounded", IssuedAt: issued, ExpiresAt: issued.Add(24 * time.Hour)},
		{TokenHash: "hash-forever", TokenID: "forever", Label: "farm", IssuedAt: issued},
	}); err != nil {
		t.Fatal(err)
	}

	if _, ok, err := pg.ResolveAdminToken(ctx, "hash-bounded", "10.0.0.1", issued.Add(time.Hour)); err != nil || !ok {
		t.Errorf("bounded inside window: ok=%v err=%v", ok, err)
	}
	if _, ok, err := pg.ResolveAdminToken(ctx, "hash-bounded", "10.0.0.1", issued.Add(48*time.Hour)); err != nil || ok {
		t.Errorf("bounded past expiry: ok=%v err=%v, want ok=false", ok, err)
	}
	distant := issued.Add(5 * 365 * 24 * time.Hour)
	if _, ok, err := pg.ResolveAdminToken(ctx, "hash-forever", "203.0.113.7", distant); err != nil || !ok {
		t.Errorf("unlimited five years on: ok=%v err=%v, want ok=true", ok, err)
	}

	rows, err := pg.ListAdminTokens(ctx, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list = %+v err=%v", rows, err)
	}
	var forever AdminTokenRow
	for _, r := range rows {
		if r.TokenID == "forever" {
			forever = r
		}
	}
	if !forever.ExpiresAt.IsZero() {
		t.Errorf("unlimited token came back with expiry %s, want zero", forever.ExpiresAt)
	}
	if !forever.LastUsedAt.UTC().Equal(distant) || forever.LastUsedIP != "203.0.113.7" {
		t.Errorf("use was not recorded: at=%s ip=%q", forever.LastUsedAt, forever.LastUsedIP)
	}

	if revoked, err := pg.RevokeAdminToken(ctx, "forever", distant); err != nil || !revoked {
		t.Fatalf("revoke = %v err=%v", revoked, err)
	}
	if _, ok, _ := pg.ResolveAdminToken(ctx, "hash-forever", "10.0.0.1", distant); ok {
		t.Error("a revoked unlimited token still resolves")
	}
	if revoked, err := pg.RevokeAdminToken(ctx, "forever", distant); err != nil || revoked {
		t.Errorf("revoking twice = %v err=%v, want false", revoked, err)
	}
}

// The farm panel's numbers come out of SQL the Fake only imitates, and the
// duplicate count in particular is a jsonb aggregate that no in-memory map
// can prove. Both are checked against real SQL on the same fixture.
func TestIntegrationFarmStats(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 17, 30, 0, 0, time.UTC)

	if err := pg.IssueAuthoringSessions(ctx, []AuthoringSessionRow{
		{TokenHash: "h-live", SessionID: "live", Label: "linux-slot1", Model: "agy",
			Reasoning: "auto", IssuedAt: now.Add(-2 * time.Hour), IdleExpiresAt: now.Add(time.Hour)},
		{TokenHash: "h-neverstarted", SessionID: "neverstarted", Label: "windows-slot1", Model: "agy",
			Reasoning: "auto", IssuedAt: now.Add(-25 * time.Minute), IdleExpiresAt: now.Add(35 * time.Minute)},
	}, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.RefreshAuthoringSession(ctx, "h-live", "10.0.0.1", "csx-farm-linux-1",
		now.Add(-time.Minute), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	for _, s := range []struct{ id, manifest, os string }{
		{"sha256:dupa", `{"packages":["pkg:npm/dup@1.0.0"],"symbols":["dup.call"]}`, "linux"},
		{"sha256:dupb", `{"packages":["pkg:npm/dup@1.0.0"],"symbols":["dup.call"]}`, "linux"},
		{"sha256:solo", `{"packages":["pkg:npm/solo@1.0.0"],"symbols":["solo.call"]}`, "windows"},
	} {
		if err := pg.SaveSample(ctx, SampleRow{SampleID: s.id, ManifestJSON: s.manifest}); err != nil {
			t.Fatal(err)
		}
		if err := pg.SaveReceipt(ctx, ReceiptRow{
			ReceiptID: "r-" + s.id, SampleID: s.id, PeerID: "p", EnvHash: "e-" + s.id,
			ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"` + s.os + `"}}`,
		}); err != nil {
			t.Fatal(err)
		}
	}

	workers, err := pg.FarmWorkers(ctx, now.Add(-time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 2 {
		t.Fatalf("workers = %+v, want 2", workers)
	}
	byLabel := map[string]FarmWorker{}
	for _, w := range workers {
		byLabel[w.Label] = w
	}
	if got := byLabel["linux-slot1"]; got.LastRefreshAt.IsZero() || got.ComputerName != "csx-farm-linux-1" {
		t.Errorf("live worker = %+v", got)
	}
	if got := byLabel["windows-slot1"]; !got.LastRefreshAt.IsZero() {
		t.Errorf("a worker that never refreshed reports one: %+v", got)
	}

	health, err := pg.FarmHealthNow(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if health.PublicSamples != 3 {
		t.Errorf("public samples = %d, want 3", health.PublicSamples)
	}
	if health.DuplicateCoords != 1 {
		t.Errorf("duplicate coordinates = %d, want 1", health.DuplicateCoords)
	}
	if health.ReceiptsByOS["linux"] != 2 || health.ReceiptsByOS["windows"] != 1 {
		t.Errorf("receipts by OS = %v", health.ReceiptsByOS)
	}
}

// The coverage query reads two differently-shaped jsonb documents -- evidence
// keeps the OS at the top level, receipts nest it under "environment" -- and
// picking the wrong path yields a plausible zero rather than an error. Only
// real PostgreSQL can prove the paths are right.
func TestIntegrationFarmCoverageSeparatesObservedFromProven(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	for _, tc := range []struct{ name, observedOS, provenOS string }{
		{"seen", "windows", "linux"},
		{"both", "windows", "windows"},
	} {
		purl := "pkg:golang/example.com/" + tc.name + "@v1.0.0"
		if accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{{
			SchemaVersion: 1, Epoch: "2026-08-20", AnonID: "peercov" + tc.name, ProjectBucket: "projcover",
			Package: purl, Symbol: tc.name + ".Call", SymbolConfidence: domain.SymbolProbable,
			Environment: domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "golang",
				OS: tc.observedOS, Arch: "amd64", Runtime: "go", RuntimeVersion: "1.26"},
			Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 3,
		}}); err != nil || accepted != 1 {
			t.Fatalf("ingest %s: accepted=%d rejected=%v err=%v", tc.name, accepted, rejected, err)
		}
		if err := pg.UpsertPackage(ctx, PackageRow{PURL: purl, Ecosystem: "golang",
			Name: "example.com/" + tc.name, Version: "v1.0.0", Publicness: "PUBLIC"}); err != nil {
			t.Fatal(err)
		}
		id := "sha256:cover" + tc.name
		if err := pg.SaveSample(ctx, SampleRow{SampleID: id,
			ManifestJSON: `{"packages":["` + purl + `"],"symbols":[]}`}); err != nil {
			t.Fatal(err)
		}
		if err := pg.SaveReceipt(ctx, ReceiptRow{ReceiptID: "r-cover-" + tc.name, SampleID: id,
			PeerID: "p", EnvHash: "e-cover-" + tc.name, ContractResult: "PASS",
			ReceiptJSON: `{"environment":{"os":"` + tc.provenOS + `"}}`}); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := pg.FarmCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var win FarmAxisCoverage
	for _, r := range rows {
		if r.OS == "windows" && r.Ecosystem == "golang" {
			win = r
		}
	}
	if win.Observed != 2 {
		t.Errorf("windows/golang observed = %d, want 2 (rows: %+v)", win.Observed, rows)
	}
	if win.ObservedProven != 1 {
		t.Errorf("windows/golang observed-and-proven = %d, want 1 — a linux proof is not a windows proof",
			win.ObservedProven)
	}
	if win.Measured != win.Proven {
		t.Errorf("no FAIL receipts were seeded, so measured (%d) must equal proven (%d)",
			win.Measured, win.Proven)
	}
}

// A manifest with no packages serializes to jsonb null, and expanding a
// scalar raises rather than yielding zero rows. One such sample must not
// take the whole coverage query down with it.
func TestIntegrationFarmCoverageSurvivesAnEmptyManifest(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:nullmanifest",
		ManifestJSON: `{"packages":null,"symbols":null}`}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveReceipt(ctx, ReceiptRow{ReceiptID: "r-nullmanifest",
		SampleID: "sha256:nullmanifest", PeerID: "p", EnvHash: "e-null",
		ContractResult: "PASS", ReceiptJSON: `{"environment":{"os":"linux"}}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.FarmCoverage(ctx); err != nil {
		t.Fatalf("FarmCoverage aborted on a scalar manifest: %v", err)
	}
	if _, err := pg.FarmHealthNow(ctx, dbNow(t, pg)); err != nil {
		t.Fatalf("FarmHealthNow aborted on a scalar manifest: %v", err)
	}
}
