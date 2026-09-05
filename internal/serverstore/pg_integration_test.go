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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
)

// errIntegrationDSNUnset marks the one absence that is allowed to skip: a
// developer machine with no PostgreSQL. Everything else is a failure.
var errIntegrationDSNUnset = errors.New("CSX_TEST_DSN not set; skipping PostgreSQL integration test")

// integrationDSN decides whether a PostgreSQL integration test may run, from
// the two environment values that control it. CI sets CSX_REQUIRE_TEST_DSN so
// that a lost DSN fails the run instead of quietly skipping every test in this
// package — a skipped guard reports the same green as a passing one, and that
// is precisely how the /wanted regression test could not have caught anything.
//
// Anything that is not plainly false requires: a guard that reads a typo as
// "off" disarms itself in the situation it exists for.
func integrationDSN(dsn, require string) (string, error) {
	if dsn != "" {
		return dsn, nil
	}
	if off, err := strconv.ParseBool(require); require == "" || (err == nil && !off) {
		return "", errIntegrationDSNUnset
	}
	return "", fmt.Errorf("CSX_TEST_DSN is empty while CSX_REQUIRE_TEST_DSN=%s demands the "+
		"PostgreSQL integration suite; point CSX_TEST_DSN at a disposable database", require)
}

// openTestPG connects to CSX_TEST_DSN inside a fresh schema, migrates it,
// and registers cleanup. Skips when no DSN is configured, unless
// CSX_REQUIRE_TEST_DSN forbids that skip.
func openTestPG(t *testing.T) *PG {
	t.Helper()
	return openTestPGWithPolicy(t, DefaultPoolPolicy())
}

func TestIntegrationSampleSearchKeepsTotalWithOneInRangeQueryAndPastLastPage(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	for i, goal := range []string{"connect pgx pool", "close pgx pool", "unrelated axios request"} {
		manifest := fmt.Sprintf(`{"goal":%q,"packages":["pkg:golang/github.com/jackc/pgx/v5@v5.10.0"],"symbols":[]}`, goal)
		if i == 2 {
			manifest = fmt.Sprintf(`{"goal":%q,"packages":["pkg:npm/axios@1.0.0"],"symbols":[]}`, goal)
		}
		if err := pg.SaveSample(ctx, SampleRow{SampleID: fmt.Sprintf("sha256:search-%d", i), ManifestJSON: manifest}); err != nil {
			t.Fatal(err)
		}
	}

	rows, total, err := pg.SearchSamplesPage(ctx, "PGX", 1, 0)
	if err != nil || len(rows) != 1 || total != 2 {
		t.Fatalf("first search page = %d rows, total=%d, err=%v", len(rows), total, err)
	}
	rows, total, err = pg.SearchSamplesPage(ctx, "pgx", 1, 99)
	if err != nil || len(rows) != 0 || total != 2 {
		t.Fatalf("past-last search page = %d rows, total=%d, err=%v", len(rows), total, err)
	}
}

// This is the safe production-scale reproduction for R2C-190. Production had
// 7,012 public samples when /samples?q=pgx measured 4.92s p50. The production
// query was not EXPLAIN ANALYZE'd because it was already slow and contending
// with the builder; this disposable schema uses the same cardinality,
// predicate, ordering and representative multi-kilobyte manifests instead.
func TestIntegrationProductionScaleSampleSearchPlan(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `
			INSERT INTO samples(sample_id, manifest, size_bytes, created_at)
			SELECT 'sha256:scale-' || n,
			       jsonb_build_object(
			         'goal', CASE WHEN n % 70 = 0 THEN 'connect pgx pool ' ELSE 'ordinary sample ' END || repeat('x', 2048),
			         'packages', jsonb_build_array(CASE WHEN n % 70 = 0
			           THEN 'pkg:golang/github.com/jackc/pgx/v5@v5.10.0'
			           ELSE 'pkg:npm/example@1.0.0' END),
			         'symbols', jsonb_build_array('example.call')),
			       2048, now() - make_interval(secs => n)
			FROM generate_series(1, 7012) AS n`); err != nil {
			return err
		}
		if _, err := c.Exec(ctx, `ANALYZE samples`); err != nil {
			return err
		}
		if _, err := c.Exec(ctx, `DROP INDEX samples_manifest_lower_trgm_idx`); err != nil {
			return err
		}
		beforeCount, err := explainLines(ctx, c, `
			EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
			SELECT count(*) FROM samples
			WHERE NOT quarantined AND lower(manifest::text) LIKE '%pgx%'`)
		if err != nil {
			return err
		}
		beforePage, err := explainLines(ctx, c, `
			EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
			SELECT `+sampleCols+` FROM samples
			WHERE NOT quarantined AND lower(manifest::text) LIKE '%pgx%'
			ORDER BY created_at DESC, sample_id LIMIT 24 OFFSET 0`)
		if err != nil {
			return err
		}
		t.Logf("production-scale old count plan:\n%s", strings.Join(beforeCount, "\n"))
		t.Logf("production-scale old page plan:\n%s", strings.Join(beforePage, "\n"))

		if _, err := c.Exec(ctx, `
			CREATE INDEX samples_manifest_lower_trgm_idx
			ON samples USING gin ((lower(manifest::text)) public.gin_trgm_ops)
			WHERE NOT quarantined`); err != nil {
			return err
		}
		if _, err := c.Exec(ctx, `ANALYZE samples`); err != nil {
			return err
		}
		after, err := explainLines(ctx, c, `
			EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
			SELECT `+sampleCols+`, count(*) OVER() FROM samples
			WHERE NOT quarantined AND lower(manifest::text) LIKE '%pgx%'
			ORDER BY created_at DESC, sample_id LIMIT 24 OFFSET 0`)
		if err != nil {
			return err
		}
		afterText := strings.Join(after, "\n")
		t.Logf("production-scale indexed one-query plan:\n%s", afterText)
		if !strings.Contains(afterText, "samples_manifest_lower_trgm_idx") {
			return fmt.Errorf("indexed plan did not use samples_manifest_lower_trgm_idx:\n%s", afterText)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func explainLines(ctx context.Context, c *pgx.Conn, query string) ([]string, error) {
	rows, err := c.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

// openTestPGWithPolicy is openTestPG with the pool policy named, so a
// test about timeouts and admission can use budgets small enough to run
// in seconds instead of waiting out the shipped ones.
func openTestPGWithPolicy(t *testing.T, pol PoolPolicy) *PG {
	t.Helper()
	dsn, err := integrationDSN(os.Getenv("CSX_TEST_DSN"), os.Getenv("CSX_REQUIRE_TEST_DSN"))
	if errors.Is(err, errIntegrationDSNUnset) {
		t.Skip(err.Error())
	}
	if err != nil {
		t.Fatal(err)
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

	pg := newPGWithPolicy(cfg, pol)
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

func TestIntegrationFailureClusterBatchIsAtomicAndSkipsNoopUpdates(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	firstSeen := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	lastSeen := firstSeen.Add(time.Hour)
	rows := []ClusterRow{
		{Ecosystem: "npm", PackageName: "batch-boundary", Symbol: "parse", Stage: "PROJECT_TEST", ErrorFingerprint: "fp-a", ObservationCount: 7, FirstSeen: firstSeen, LastSeen: lastSeen},
		{Ecosystem: "npm", PackageName: "batch-boundary", Symbol: "render", Stage: "PROJECT_TEST", ErrorFingerprint: "fp-b", ObservationCount: 9, FirstSeen: firstSeen, LastSeen: lastSeen},
	}
	if err := pg.UpsertFailureClusters(ctx, rows); err != nil {
		t.Fatal(err)
	}

	xmin := func(symbol string) string {
		t.Helper()
		var got string
		if err := pg.withConn(ctx, func(c *pgx.Conn) error {
			return c.QueryRow(ctx, `SELECT xmin::text FROM failure_clusters
				WHERE ecosystem='npm' AND package_name='batch-boundary' AND symbol=$1`, symbol).Scan(&got)
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	before := xmin("parse")
	if err := pg.UpsertFailureClusters(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if after := xmin("parse"); after != before {
		t.Fatalf("unchanged cluster was rewritten: xmin %s -> %s", before, after)
	}

	rows[0].ObservationCount = 8
	if err := pg.UpsertFailureClusters(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if after := xmin("parse"); after == before {
		t.Fatalf("changed cluster was not rewritten: xmin stayed %s", before)
	}
	got, err := pg.ListFailureClusters(ctx, "batch-boundary")
	if err != nil || len(got) != 2 {
		t.Fatalf("clusters = %d, err=%v", len(got), err)
	}
}

func TestIntegrationFailureClusterBatchPipelining(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	firstSeen := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	lastSeen := firstSeen.Add(time.Hour)

	const n = 100
	rows := make([]ClusterRow, n)
	for i := 0; i < n; i++ {
		rows[i] = ClusterRow{
			Ecosystem:        "npm",
			PackageName:      "pipelined-batch",
			Symbol:           fmt.Sprintf("sym-%d", i),
			Stage:            "PROJECT_TEST",
			ErrorFingerprint: fmt.Sprintf("fp-%d", i),
			ObservationCount: int64(i + 1),
			FirstSeen:        firstSeen,
			LastSeen:         lastSeen,
		}
	}

	if err := pg.UpsertFailureClusters(ctx, rows); err != nil {
		t.Fatalf("upsert batch of %d: %v", n, err)
	}

	got, err := pg.ListFailureClusters(ctx, "pipelined-batch")
	if err != nil || len(got) != n {
		t.Fatalf("ListFailureClusters = %d rows (err=%v), want %d", len(got), err, n)
	}

	// Re-upsert with no changes should succeed cleanly as no-ops.
	if err := pg.UpsertFailureClusters(ctx, rows); err != nil {
		t.Fatalf("re-upsert unchanged batch: %v", err)
	}

	// Update half of the rows and re-upsert.
	for i := 0; i < n/2; i++ {
		rows[i].ObservationCount += 10
	}
	if err := pg.UpsertFailureClusters(ctx, rows); err != nil {
		t.Fatalf("re-upsert partially updated batch: %v", err)
	}

	gotAfter, err := pg.ListFailureClusters(ctx, "pipelined-batch")
	if err != nil || len(gotAfter) != n {
		t.Fatalf("ListFailureClusters after partial update = %d, want %d", len(gotAfter), n)
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
	candidates = sampleAxisRows(candidates)
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
	remaining = sampleAxisRows(remaining)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("remaining = %+v err=%v", remaining, err)
	}
	next, ok, err := pg.ClaimAuthoringWork(ctx, "pg-expansion-writer-2", remaining, now, now.Add(24*time.Hour))
	if err != nil || !ok || next.Kind != "EXPANSION" || next.Symbol != "request" {
		t.Fatalf("second claim = %+v ok=%v err=%v", next, ok, err)
	}
}

func TestIntegrationAuthoringExpansionTimeoutIsDatabaseOwnedAndReusable(t *testing.T) {
	pg := openTestPG(t)
	locked := make(chan struct{})
	release := make(chan struct{})
	lockResult := make(chan error, 1)
	go func() {
		lockResult <- pg.withConn(context.Background(), func(c *pgx.Conn) error {
			tx, err := c.Begin(context.Background())
			if err != nil {
				return err
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := tx.Exec(context.Background(), `LOCK TABLE packages IN ACCESS EXCLUSIVE MODE`); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	select {
	case <-locked:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("could not establish the blocking authoring fixture")
	}

	started := time.Now()
	_, err := pg.listAuthoringExpansionCandidates(context.Background(), 10, 75*time.Millisecond, false)
	elapsed := time.Since(started)
	close(release)
	if lockErr := <-lockResult; lockErr != nil {
		t.Fatalf("release authoring fixture: %v", lockErr)
	}
	if !IsQueryTimeout(err) {
		t.Fatalf("blocked authoring query error = %v, want PostgreSQL statement timeout", err)
	}
	if elapsed >= time.Second {
		t.Fatalf("75ms statement timeout returned after %v", elapsed)
	}

	// PostgreSQL canceled the statement inside a read-only transaction. The
	// pool must be able to serve the next candidate read without reconnecting
	// or carrying the failed transaction forward.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := pg.listAuthoringExpansionCandidates(ctx, 1, time.Second, false); err != nil {
		t.Fatalf("candidate query after timeout: %v", err)
	}
}

// Production's candidate scan crossed the JIT cost threshold. The HTTP poll
// returned its bounded 503, but PostgreSQL stayed in LLVM compilation for
// roughly another minute and later polls accumulated behind it. A timeout is
// not a useful ceiling if the executor cannot observe it, so prove the actual
// candidate transaction disables JIT locally and gives the connection back
// with its session setting intact.
func TestIntegrationAuthoringExpansionDisablesJITAndRestoresTheConnection(t *testing.T) {
	pol := DefaultPoolPolicy()
	pol.MaxConns = 1
	pol.ProbeReserve = 0
	pol.InteractiveConns = 1
	pol.BackgroundConns = 1
	pg := openTestPGWithPolicy(t, pol)
	ctx := context.Background()
	const purl = "pkg:npm/jit-boundary@1.0.0"
	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
	}
	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-27", AnonID: "jitboundarypeer",
		ProjectBucket: "jitboundaryproject", Package: purl, Symbol: "compile",
		SymbolConfidence: domain.SymbolProbable, Environment: env,
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
	}
	if accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{batch}); err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	if err := pg.UpsertPackage(ctx, PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "jit-boundary", Version: "1.0.0",
		Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}

	// The view is an executable assertion inside the candidate statement:
	// without transaction-local jit=off it hides the only package and this
	// test gets no candidate. Rename is safe because each integration test
	// owns and drops its own schema.
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `SET jit=on`); err != nil {
			return err
		}
		if _, err := c.Exec(ctx, `ALTER TABLE packages RENAME TO packages_jit_source`); err != nil {
			return err
		}
		_, err := c.Exec(ctx, `CREATE VIEW packages AS
			SELECT * FROM packages_jit_source WHERE current_setting('jit')='off'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	candidates, err := pg.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range candidates {
		if candidate.Ecosystem == "npm" && candidate.Name == "jit-boundary" && candidate.Version == "1.0.0" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("candidate query did not execute with transaction-local jit=off: %+v", candidates)
	}

	var jit string
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SHOW jit`).Scan(&jit)
	}); err != nil {
		t.Fatal(err)
	}
	if jit != "on" {
		t.Fatalf("pooled connection jit = %q after rollback, want on", jit)
	}
}

// A symbol-scoped draft holds the whole package coordinate. PG's in_flight
// used to yield the package-level (purl,”) pair only when the draft named no
// symbols — while the fake has always marked it for every draft — so a purl
// whose symbol draft was awaiting verification was re-offered as
// package-level EXPANSION work minutes later: exactly the duplicate-sample
// loop in_flight exists to stop, surviving because every test ran on the
// fake.
func TestIntegrationSymbolDraftHoldsThePackageCoordinate(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	const purl = "pkg:npm/three@0.185.1"
	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
	}
	usage := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-08-19", AnonID: "threepeer", ProjectBucket: "threeproject",
		Package: purl, Environment: env,
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 40,
	}
	if accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{usage}); err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	if err := pg.UpsertPackage(ctx, PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "three", Version: "0.185.1", Major: "0", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}

	candidates, err := pg.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	offered := false
	for _, c := range candidates {
		if c.Name == "three" && c.Symbol == "" {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("no package-level candidate to begin with: %+v", candidates)
	}

	// A worker answers it with a SYMBOL-scoped draft, now awaiting its cross
	// verification.
	now := time.Date(2026, 8, 19, 13, 0, 0, 0, time.UTC)
	session := AuthoringSessionRow{
		TokenHash:     "0a5ad1d2e47e11bd71619acda03e7c2fe6f0f80ea5f35d70f27097c12808e6d7",
		SessionID:     "worker-three-01",
		Label:         "three-lab",
		IssuedAt:      now,
		IdleExpiresAt: now.Add(time.Hour),
	}
	if err := pg.IssueAuthoringSessions(ctx, []AuthoringSessionRow{session}, now); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schemaVersion":1,"packages":["` + purl + `"],"symbols":["three.WebGLRenderer"]}`
	if err := pg.SaveAuthoringDraft(ctx, AuthoringDraftRow{
		SampleID: "sha256:three-draft", SessionID: session.SessionID, WorkerLabel: session.Label,
		ManifestJSON: manifest, LocalStatus: "LOCAL_PASS", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:three-draft", ManifestJSON: manifest,
		Status: "DRAFT", Quarantined: true, QuarantineReason: "private authoring draft"}); err != nil {
		t.Fatal(err)
	}

	after, err := pg.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range after {
		if c.Name == "three" && normalizeAuthoringAxis(c.Axis) == AuthoringAxisSample {
			t.Fatalf("a purl with a draft in flight was re-offered: %+v", c)
		}
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

	// A mixed-version component can be smaller than the combined full total.
	// It must neither add now nor lower the durable high-water so restoring the
	// same total later cannot add the already-counted delta a second time.
	if _, _, err = pg.IngestBatches(ctx, []domain.ObservationBatch{obsBatch("anonaaaa", "projaaaa", 3)}); err != nil {
		t.Fatalf("shrunk component ingest: %v", err)
	}
	if _, _, err = pg.IngestBatches(ctx, []domain.ObservationBatch{obsBatch("anonaaaa", "projaaaa", 8)}); err != nil {
		t.Fatalf("restored full total ingest: %v", err)
	}
	if e = evidence(); e.ObservationCount != 8 {
		t.Fatalf("shrunk/restored report re-inflated count: %+v", e)
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

func TestIntegrationConcurrentIngestKeepsDedupHighWaterMonotone(t *testing.T) {
	pg := openTestPG(t)
	ctx := t.Context()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, count := range []int{8, 3} {
		count := count
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{obsBatch("anon-race", "project-race", count)})
			if err == nil && (accepted != 1 || len(rejected) != 0) {
				err = fmt.Errorf("count %d: accepted %d rejected %+v", count, accepted, rejected)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Whichever transaction won first, the aggregate and dedup high-water are
	// both 8. Re-sending 8 proves a late smaller component did not lower the
	// ledger and make the same observations count twice.
	if _, _, err := pg.IngestBatches(ctx, []domain.ObservationBatch{obsBatch("anon-race", "project-race", 8)}); err != nil {
		t.Fatal(err)
	}
	rows, err := pg.EvidenceForTarget(ctx, "pkg:npm/axios@1.12.0", "axios.post")
	if err != nil || len(rows) != 1 || rows[0].ObservationCount != 8 {
		t.Fatalf("concurrent monotone evidence = %+v, err=%v; want count 8", rows, err)
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

	t.Run("preserved legacy failure clusters stay out of current reads", func(t *testing.T) {
		legacy := ClusterRow{
			Ecosystem: "npm", PackageName: "legacy-current-boundary", Symbol: "parse", Stage: "PROJECT_TEST",
			ErrorFingerprint: "sha256:" + strings.Repeat("a", 64), EvidenceQuality: "legacy-evidence-incomplete", ObservationCount: 9,
		}
		current := legacy
		current.ErrorFingerprint = ""
		current.ObservationCount = 11
		if err := pg.UpsertFailureCluster(ctx, legacy); err != nil {
			t.Fatalf("insert preserved legacy cluster: %v", err)
		}
		if err := pg.UpsertFailureCluster(ctx, current); err != nil {
			t.Fatalf("insert current evidence-gap cluster: %v", err)
		}
		got, err := pg.ListFailureClusters(ctx, legacy.PackageName)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ErrorFingerprint != "" || got[0].ObservationCount != current.ObservationCount {
			t.Fatalf("current clusters = %+v, want only the rebuilt evidence-gap row", got)
		}
		// The ledger the deploy transaction checks counts exactly what this
		// read serves: the rebuilt row once, never beside the preserved one.
		var ledger int64
		for _, c := range got {
			ledger += c.ObservationCount
		}
		if ledger != current.ObservationCount {
			t.Fatalf("cluster-observation ledger = %d, want %d", ledger, current.ObservationCount)
		}
		// Exact failure matching still needs the preserved fingerprint: no
		// released client computes anything else.
		recorded, err := pg.ListFailureClustersIncludingPreserved(ctx, legacy.PackageName)
		if err != nil {
			t.Fatal(err)
		}
		if len(recorded) != 2 {
			t.Fatalf("recorded clusters = %+v, want the preserved row served beside the rebuilt one", recorded)
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

	// Every mark here comes from the row, never from the clock.
	//
	// This test used to sleep 1.1s and read now(), which assumes the database
	// clock only moves forward. It does not: measured against the container
	// this suite runs, clock_timestamp() stepped BACKWARD four times in 300
	// samples, by as much as 0.497s. One step is smaller than the sleep and
	// hides; two in a row, or one plus scheduling delay, is not — and then the
	// test failed in whichever direction the step happened to fall, either
	// "expected no changes" for a row written before the mark or "a quarantine
	// was not reported" for one written after it. Both readings were of the
	// clock, not of the code.
	//
	// ChangedSince asks `> $1`, so a mark equal to a row's own stamp excludes
	// it and a mark one nanosecond below it includes it. Taking the marks from
	// updated_at makes both assertions exact whichever way the clock moves,
	// and drops 2.2 seconds of sleeping.
	afterSave := sampleUpdatedAt(t, pg, id)
	changes, err := pg.ChangedSince(ctx, afterSave)
	if err != nil {
		t.Fatal(err)
	}
	if !changes.Empty() {
		t.Fatalf("a sample reported itself as changed since its own stamp: %+v", changes)
	}

	// A status correction must make the package dirty.
	if err := pg.SetSampleStatus(ctx, id, "CROSS_PASS"); err != nil {
		t.Fatal(err)
	}
	changes, err = pg.ChangedSince(ctx, sampleUpdatedAt(t, pg, id).Add(-time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(changes.SamplePURLs, purl) {
		t.Errorf("a status change outside the request path was not reported: %+v", changes)
	}

	// So must a quarantine — otherwise a taken-down sample keeps being
	// served from the shard it is already in.
	if err := pg.SetSampleQuarantine(ctx, id, true, "abuse"); err != nil {
		t.Fatal(err)
	}
	quarantined := sampleUpdatedAt(t, pg, id)
	changes, err = pg.ChangedSince(ctx, quarantined.Add(-time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if !contains(changes.SamplePURLs, purl) {
		t.Errorf("a quarantine was not reported as a change: %+v", changes)
	}
	// And the same write is invisible to a reader that has already seen it.
	changes, err = pg.ChangedSince(ctx, quarantined)
	if err != nil {
		t.Fatal(err)
	}
	if contains(changes.SamplePURLs, purl) {
		t.Errorf("a quarantine was reported again to a reader already at its stamp: %+v", changes)
	}
}

// sampleUpdatedAt reads the stamp ChangedSince compares against, so a test can
// mark a point in the data instead of guessing one from a clock.
func sampleUpdatedAt(t *testing.T, pg *PG, sampleID string) time.Time {
	t.Helper()
	ctx := context.Background()
	var updated time.Time
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT updated_at FROM samples WHERE sample_id=$1`, sampleID).Scan(&updated)
	}); err != nil {
		t.Fatalf("reading the sample stamp: %v", err)
	}
	return updated.UTC()
}

// dbNow reads the clock that stamps created_at and updated_at.
//
// It is a clock, so it is only safe where a test needs "roughly now" and not
// an ordering: the container clock steps backward (measured at up to 0.497s).
// A test that needs to be on one side of a write marks the row instead — see
// sampleUpdatedAt.
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
		if c.Name != "spreadpkg" || normalizeAuthoringAxis(c.Axis) != AuthoringAxisSample {
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
		if c.Name == "bigpkg" && c.Version != "1.0.0" && normalizeAuthoringAxis(c.Axis) == AuthoringAxisSample {
			siblings++
		}
		if c.Name == "smallpkg" && normalizeAuthoringAxis(c.Axis) == AuthoringAxisSample {
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

// resolvedPackages is credited only from a v2 receipt whose resolve stage
// passed — the rule the Fake's resolvedPackageStrings has always applied.
// The SQL credited any receipt's list, so a v1 (or failed-resolve) receipt
// carrying a resolvedPackages array put coverage on a package the run never
// resolved, while the Fake fell back to the manifest.
func TestIntegrationFarmCoverageCreditsOnlyResolvedV2Lists(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	const declared = "pkg:golang/example.com/declared@v1.0.0"
	const claimed = "pkg:golang/example.com/claimed@v9.9.9"
	for name, purl := range map[string]string{"declared": declared, "claimed": claimed} {
		if err := pg.UpsertPackage(ctx, PackageRow{PURL: purl, Ecosystem: "golang",
			Name: "example.com/" + name, Version: "v1.0.0", Publicness: "PUBLIC"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "sha256:v1credit",
		ManifestJSON: `{"packages":["` + declared + `"],"symbols":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveReceipt(ctx, ReceiptRow{ReceiptID: "r-v1credit", SampleID: "sha256:v1credit",
		PeerID: "p", EnvHash: "e-v1credit", ContractResult: "PASS",
		ReceiptJSON: `{"schemaVersion":1,"environment":{"os":"linux"},"resolvedPackages":["` + claimed + `"]}`}); err != nil {
		t.Fatal(err)
	}
	rows, err := pg.FarmCoverage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.OS != "linux" || r.Ecosystem != "golang" {
			continue
		}
		if r.Proven != 1 {
			t.Errorf("linux/golang proven = %d, want only the manifest package credited (rows: %+v)", r.Proven, rows)
		}
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

// The same rule the fake enforces, against real SQL. A divergence here is a
// silent production bug: the queue that hid these jobs is PostgreSQL only,
// and the fake is what every other test runs against.
func TestIntegrationASkippedContractDoesNotLockAPeerOutOfCrossJobs(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	caseID := "case:sha256:lockout"
	if err := pg.SaveCase(ctx, domain.Case{SchemaVersion: 1, CaseID: caseID, Kind: "HOW",
		Goal: "lockout", Packages: []string{"pkg:npm/example@1"}, Contract: []string{"passes"}}); err != nil {
		t.Fatal(err)
	}
	sampleID := "sha256:" + fmt.Sprintf("%064d", 92)
	if err := pg.SaveSample(ctx, SampleRow{SampleID: sampleID, CaseID: caseID,
		ManifestJSON: `{"schemaVersion":1}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateJob(ctx, JobRow{SampleID: sampleID, Reason: "cross"}); err != nil {
		t.Fatal(err)
	}

	const stalled = "ed25519:stalled-verifier"
	const judged = "ed25519:judged-verifier"

	// An empty workspace: nothing was ever judged.
	if err := pg.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "receipt-skipped", SampleID: sampleID, PeerID: stalled,
		ContractResult: "SKIPPED", CreatedAt: time.Now(),
		ReceiptJSON: `{"stages":{"resolve":"FAIL","contract":"SKIPPED"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	jobs, err := pg.OpenJobsPage(ctx, "", stalled, "cross", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("open cross jobs for the stalled peer = %d, want 1 — its receipt judged nothing", len(jobs))
	}

	// A verdict still locks its peer out; independence is the one thing a
	// cross pass asserts that a publisher cannot manufacture alone.
	if err := pg.SaveReceipt(ctx, ReceiptRow{
		ReceiptID: "receipt-pass", SampleID: sampleID, PeerID: judged,
		ContractResult: "PASS", CreatedAt: time.Now(),
		ReceiptJSON: `{"stages":{"contract":"PASS"}}`,
	}); err != nil {
		t.Fatal(err)
	}
	jobs, err = pg.OpenJobsPage(ctx, "", judged, "cross", "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Errorf("the peer that judged this sample can still claim its cross job")
	}
}

// TestIntegrationListWantedUsesTheNormalizedPackageProjection pins the
// complexity class of the board's anti-join, which is what took
// /wanted down rather than any error in its answer.
//
// The correlated form asked "does any live sample close this?" once per
// wanted row and expanded every sample's manifest package array inside that
// subquery, so the expansion ran wanted × samples times. In production that
// was 692 × 2,362 = 823,594 executions for a page of 31 rows: 8.3s for one
// request, and with an 8-connection pool the ninth concurrent reader got a
// 502. Both factors grow with the corpus, so the page decayed as the network
// filled up.
//
// A wall-clock budget would only restate how fast the machine running the
// test is. The invariant is structural, so this asserts on the plan the
// shipped statement actually produced: request-time Wanted reads never parse
// manifest package arrays. SaveSample and migration 0028 own that one-time
// projection instead.
func TestIntegrationListWantedUsesTheNormalizedPackageProjection(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	const (
		wantedRows = 300
		samples    = 40
	)

	requests := make([]WantedRow, 0, wantedRows)
	for i := range wantedRows {
		// Half the board is package-level, as production's is. A request
		// that names a symbol lets the planner decide "no such symbol here"
		// without ever expanding the package array, so a suite of only
		// symbol-bearing rows would measure a path the outage never took.
		symbol := ""
		if i%2 == 1 {
			symbol = fmt.Sprintf("sym%04d", i)
		}
		requests = append(requests, WantedRow{
			Ecosystem: "npm",
			Name:      fmt.Sprintf("unanswered-pkg-%04d", i),
			Version:   "1.0.0",
			Symbol:    symbol,
		})
	}
	if err := pg.RecordWanted(ctx, "2026-08-22", "anon-scale", requests); err != nil {
		t.Fatal(err)
	}
	// None of these samples answers anything, which is the worst case: every
	// request has to be checked against the whole corpus before it can be
	// called unanswered. The scoped spelling keeps a package string with two
	// literal '@' in the corpus while the plan is measured.
	for i := range samples {
		id := fmt.Sprintf("sha256:%064d", i)
		if err := pg.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: fmt.Sprintf(
			`{"packages":["pkg:npm/answered-%04d@2.3.4","pkg:npm/@scope/side-%04d@1.0.0"],`+
				`"symbols":["a%04d","b%04d"]}`, i, i, i, i)}); err != nil {
			t.Fatal(err)
		}
		if err := pg.SaveReceipt(ctx, ReceiptRow{
			ReceiptID: fmt.Sprintf("receipt-scale-%04d", i), SampleID: id, ContractResult: "PASS",
			ReceiptJSON: fmt.Sprintf(`{"schemaVersion":2,"stages":{"resolve":"PASS","contract":"PASS"},`+
				`"resolvedPackages":["pkg:npm/answered-%04d@2.3.4"]}`, i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	page, total, err := pg.ListWanted(ctx, "", 0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if total != wantedRows || len(page) != wantedRows {
		t.Fatalf("ListWanted returned %d rows of %d total; none of these requests is answered",
			len(page), total)
	}

	var projected int
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT count(*) FROM sample_packages`).Scan(&projected)
	}); err != nil {
		t.Fatalf("count sample package projection: %v", err)
	}
	if want := 2 * samples; projected != want {
		t.Fatalf("sample package projection rows = %d, want %d", projected, want)
	}

	loops := listWantedExpansionLoops(t, pg)
	if loops != 0 {
		t.Fatalf("Wanted reparsed manifest package arrays %.0f times for %d requests over %d samples, want 0",
			loops, wantedRows, samples)
	}
	t.Logf("request-time manifest expansions: %.0f for %d requests over %d projected samples",
		loops, wantedRows, samples)

	// Package pages ask only for their own wanted rows. A crawler can visit
	// thousands of real package pages that have no open request; those pages
	// must not expand the entire sample corpus merely to prove the filtered
	// wanted set is empty.
	absentLoops := listWantedExpansionLoopsFor(t, pg, "npm", "package-with-no-wanted-row")
	if absentLoops != 0 {
		t.Fatalf("wanted-free package page expanded manifests %.0f times, want 0", absentLoops)
	}
}

func TestIntegrationMigrateRepairsSamplesWrittenDuringOldBinaryRollback(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	const sampleID = "sha256:rollback-gap"
	const manifest = `{"packages":["pkg:npm/react@19.1.1","pkg:npm/%40scope/pkg@2.0.0"],"symbols":[]}`

	// A pre-0028 binary writes only samples. Keep this direct SQL in the test:
	// calling current SaveSample would populate the projection and fail to
	// reproduce the rollback gap.
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `INSERT INTO samples(sample_id,manifest,size_bytes) VALUES($1,$2,0)`,
			sampleID, manifest)
		return err
	}); err != nil {
		t.Fatalf("simulate old binary sample write: %v", err)
	}

	var before int
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SELECT count(*) FROM sample_packages WHERE sample_id=$1`, sampleID).Scan(&before)
	}); err != nil {
		t.Fatal(err)
	}
	if before != 0 {
		t.Fatalf("rollback gap was not reproduced: projection rows before re-upgrade = %d", before)
	}

	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("re-upgrade migrate: %v", err)
	}
	var got []string
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT purl || '|' || coord FROM sample_packages WHERE sample_id=$1 ORDER BY purl`, sampleID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				return err
			}
			got = append(got, value)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"pkg:npm/%40scope/pkg@2.0.0|pkg:npm/%40scope/pkg@",
		"pkg:npm/react@19.1.1|pkg:npm/react@",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection after re-upgrade = %v, want %v", got, want)
	}
}

// listWantedExpansionLoops runs the shipped Wanted statement under EXPLAIN
// ANALYZE and reports how many times it expanded a manifest package array.
// It reads listWantedSQL itself so the guard cannot drift away from the
// statement the server sends.
func listWantedExpansionLoops(t *testing.T, pg *PG) float64 {
	t.Helper()
	return listWantedExpansionLoopsFor(t, pg, "", "")
}

func listWantedExpansionLoopsFor(t *testing.T, pg *PG, ecosystem, name string) float64 {
	t.Helper()
	return jsonbExpansionLoopsForAlias(t, pg, "", listWantedSQL,
		2000, 0, ecosystem, name, []string{})
}

// jsonbExpansionLoopsForAlias runs the exact shipped statement and counts
// manifest JSON array expansions in its actual plan. alias may be empty to
// count every jsonb_array_elements_text node.
func jsonbExpansionLoopsForAlias(t *testing.T, pg *PG, alias, query string, args ...any) float64 {
	t.Helper()
	ctx := context.Background()
	var raw []byte
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query, args...).Scan(&raw)
	}); err != nil {
		t.Fatalf("explain statement: %v", err)
	}
	var plans []struct {
		Plan json.RawMessage `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	total := 0.0
	var walk func(json.RawMessage)
	walk = func(n json.RawMessage) {
		var node map[string]json.RawMessage
		if err := json.Unmarshal(n, &node); err != nil {
			return
		}
		var fn string
		if v, ok := node["Function Name"]; ok {
			_ = json.Unmarshal(v, &fn)
		}
		var nodeAlias string
		if v, ok := node["Alias"]; ok {
			_ = json.Unmarshal(v, &nodeAlias)
		}
		if fn == "jsonb_array_elements_text" && (alias == "" || nodeAlias == alias) {
			var loops float64
			if v, ok := node["Actual Loops"]; ok {
				_ = json.Unmarshal(v, &loops)
			}
			total += loops
		}
		if v, ok := node["Plans"]; ok {
			var kids []json.RawMessage
			if err := json.Unmarshal(v, &kids); err == nil {
				for _, k := range kids {
					walk(k)
				}
			}
		}
	}
	for _, p := range plans {
		walk(p.Plan)
	}
	return total
}

func TestIntegrationEvidenceAggregateRetainsEveryOuterCommand(t *testing.T) {
	pg := openTestPG(t)
	exitCode := 1
	term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode}
	classified := func(outer string) domain.ObservationBatch {
		f := sanitizer.SanitizeClassifiedFailure("src/index.ts(12,5): error TS2352: bad conversion",
			domain.StageProjectCompile, term, nil, outer, domain.StageProjectTest,
			"typescript/tsc", domain.FailureStageCompilerDiagnostic, "")
		b := reviewBatch(f)
		b.Stage = domain.StageProjectCompile
		return b
	}
	first := classified("go test")
	second := classified("npm test")
	second.AnonID = "anon-integration-two"
	second.ProjectBucket = "project-integration-two"

	if accepted, rejected, err := pg.IngestBatches(t.Context(), []domain.ObservationBatch{first, second}); err != nil || accepted != 2 || len(rejected) != 0 {
		t.Fatalf("ingest = accepted %d, rejected %+v, err %v", accepted, rejected, err)
	}
	rows, err := pg.EvidenceForTarget(t.Context(), first.Package, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || strings.Join(rows[0].OuterCommands, ",") != "go test,npm test" {
		t.Fatalf("PostgreSQL aggregate outer commands = %+v", rows)
	}
}

// A pre-fix Windows client can have this exact durable row: os/exec returned
// the DWORD representation of -1 (4294967295), the client fingerprinted it,
// and PostgreSQL INTEGER then rejected it. Because ingest is atomic, one such
// row rolled back every otherwise-valid batch in the request.
func TestIntegrationIngestCanonicalizesLegacyWindowsUnsignedExitCode(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("legacy uint32 exit status does not fit in int on this architecture")
	}
	pg := openTestPG(t)
	legacyWire := uint64(1<<32 - 1)
	legacyCode := int(legacyWire)
	legacyTerm := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &legacyCode}
	legacy := sanitizer.SanitizeClassifiedFailure("process exited without a conventional status",
		domain.StageProjectTest, legacyTerm, nil, "go test", domain.StageProjectTest,
		"go/test", domain.FailureStageTestRunnerDiagnostic, "")
	poison := reviewBatch(legacy)
	// The current sanitizer is itself fixed and emits -1. Restore the exact
	// pre-fix signedness and matching pre-fix digest at the wire boundary; this
	// is the value that used to overflow PostgreSQL INTEGER.
	poison.ExitCode = &legacyCode
	poison.ErrorFingerprint = domain.ClassifiedFailureFingerprint(poison.Stage, poison.ActualToolchain,
		legacyTerm, poison.ErrorCode, poison.ErrorSummary)
	good := obsBatch("anon-after-poison", "project-after-poison", 1)

	accepted, rejected, err := pg.IngestBatches(t.Context(), []domain.ObservationBatch{poison, good})
	if err != nil || accepted != 2 || len(rejected) != 0 {
		t.Fatalf("atomic ingest with legacy Windows status = accepted %d, rejected %+v, err %v", accepted, rejected, err)
	}
	rows, err := pg.EvidenceForTarget(t.Context(), poison.Package, "")
	if err != nil || len(rows) != 1 {
		t.Fatalf("stored poison row = %+v, err=%v", rows, err)
	}
	wantCode := -1
	if rows[0].ExitCode == nil || *rows[0].ExitCode != wantCode {
		t.Fatalf("stored exitCode = %v, want %d", rows[0].ExitCode, wantCode)
	}
	wantFingerprint := domain.ClassifiedFailureFingerprint(poison.Stage, poison.ActualToolchain,
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &wantCode},
		poison.ErrorCode, poison.ErrorSummary)
	if rows[0].ErrorFingerprint != wantFingerprint {
		t.Fatalf("stored fingerprint = %q, want canonical %q", rows[0].ErrorFingerprint, wantFingerprint)
	}
}
