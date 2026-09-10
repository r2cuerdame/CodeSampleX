package compatibility

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

func openBuilderTestPG(t *testing.T) (*serverstore.PG, *pgx.Conn) {
	t.Helper()
	dsn := os.Getenv("CSX_TEST_DSN")
	if dsn == "" {
		required := os.Getenv("CSX_REQUIRE_TEST_DSN")
		off, err := strconv.ParseBool(required)
		if required != "" && (err != nil || off) {
			t.Fatal("CSX_TEST_DSN is required for builder PostgreSQL tests")
		}
		t.Skip("CSX_TEST_DSN not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := conn.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version/10000 != 17 {
		t.Fatalf("builder acceptance requires PostgreSQL 17, got %d", version)
	}
	schema := fmt.Sprintf("builder_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		conn.Close(context.Background())
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	// pgx.Config.ConnString returns the original input string; it does not
	// serialize mutations to RuntimeParams. Set the actual Open DSN instead.
	scopedDSN := dsn + " search_path=" + schema
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		scopedDSN = u.String()
	}
	pg, err := serverstore.Open(ctx, scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return pg, conn
}

func seedBuilderPGSample(t *testing.T, pg *serverstore.PG, id string, declared []string, symbol, subject string, resolved ...[]string) {
	t.Helper()
	manifest := domain.SampleManifest{SchemaVersion: 1, Packages: declared, Symbols: []string{symbol}, Subject: subject,
		Case:        domain.Case{SchemaVersion: 1, CaseID: "case:" + id, Kind: "HOW", Goal: id, Contract: []string{"executes"}},
		Environment: envNode("esm"), License: "MIT-0", VerifierAdapter: "node-typescript@1",
		ContractCommand: []string{"node", "test/contract.mjs"}}
	if err := pg.SaveSample(context.Background(), serverstore.SampleRow{SampleID: id, ManifestJSON: string(domain.MustCanonicalJSON(manifest)), Status: "CROSS_PASS"}); err != nil {
		t.Fatal(err)
	}
	for i, purls := range resolved {
		result := "PASS"
		if i%2 == 1 {
			result = "FAIL"
		}
		rec := domain.VerificationReceipt{SchemaVersion: 2, SampleID: id, CaseID: "case:" + id, PeerID: fmt.Sprintf("peer:%s:%d", id, i), Environment: manifest.Environment,
			Stages: map[string]string{"resolve": "PASS", "compile": "PASS", "contract": result}, ResolvedPackages: purls, VerifierAdapter: manifest.VerifierAdapter, SandboxCapability: domain.CapContainerRun}
		if err := pg.SaveReceipt(context.Background(), serverstore.ReceiptRow{ReceiptID: fmt.Sprintf("receipt:%s:%d", id, i), SampleID: id, PeerID: rec.PeerID, ReceiptJSON: string(domain.MustCanonicalJSON(rec)), ContractResult: result}); err != nil {
			t.Fatal(err)
		}
	}
}

// Seed real nonempty derived outputs so parity cannot pass vacuously for the
// consumers most sensitive to loading incomplete package/sample histories.
func seedBuilderPGDerivedEvidence(t *testing.T, pg *serverstore.PG) {
	t.Helper()
	ctx := context.Background()
	seedBuilderPGSample(t, pg, "receipt-boundary", []string{"pkg:npm/actual@1.0.0"},
		"actual.receipt", "", []string{"pkg:npm/actual@1.0.0"}, []string{"pkg:npm/actual@2.0.0"})
	for i, version := range []string{"1.0.0", "2.0.0"} {
		batch := domain.ObservationBatch{
			SchemaVersion: 1, Epoch: "2026-09-08", AnonID: fmt.Sprintf("observed-peer-%d", i),
			ProjectBucket: fmt.Sprintf("observed-project-%d", i), Package: "pkg:npm/actual@" + version,
			Symbol: "actual.observed", SymbolConfidence: domain.SymbolProbable,
			Environment: envNode("esm"), Stage: domain.StageProjectCompile,
			Result: domain.ResultPass, ObservationCount: 10,
		}
		if i == 1 {
			exitCode := 1
			batch.Result, batch.ErrorCode = domain.ResultFail, "ERR_REQUIRE_ESM"
			batch.TerminationKind, batch.ExitCode = domain.TerminationExit, &exitCode
			batch.ErrorSummary, batch.EvidenceQuality = "ERR_REQUIRE_ESM normalized failure", domain.EvidenceComplete
			batch.ErrorFingerprint = domain.FailureFingerprint(batch.Stage,
				domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode},
				batch.ErrorCode, batch.ErrorSummary)
		}
		if accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{batch}); err != nil || accepted != 1 {
			t.Fatalf("seed observation: accepted=%d rejected=%v err=%v", accepted, rejected, err)
		}
	}
	manifest := jdkTestManifest("gradle-java@1", "8")
	if err := pg.SaveSample(ctx, serverstore.SampleRow{SampleID: "java-boundary",
		ManifestJSON: string(domain.MustCanonicalJSON(manifest)), Status: "CROSS_PASS"}); err != nil {
		t.Fatal(err)
	}
	for i, info := range []ReceiptInfo{jdkTestReceipt("8", "PASS", "PASS"), jdkTestReceipt("11", "PASS", "FAIL")} {
		receipt := domain.VerificationReceipt{
			SchemaVersion: 2, SampleID: "java-boundary", CaseID: jdkTestCase,
			PeerID: fmt.Sprintf("jdk-peer-%d", i), Environment: info.Env,
			Stages: info.Stages, ResolvedPackages: []string{jdkTestPURL},
			VerifierAdapter: info.VerifierAdapter, SandboxCapability: info.SandboxCapability,
		}
		if err := pg.SaveReceipt(ctx, serverstore.ReceiptRow{ReceiptID: fmt.Sprintf("java-receipt-%d", i),
			SampleID: receipt.SampleID, PeerID: receipt.PeerID,
			ReceiptJSON: string(domain.MustCanonicalJSON(receipt)), ContractResult: info.ContractResult}); err != nil {
			t.Fatal(err)
		}
	}
}

func assertBuilderPGDerivedEvidence(t *testing.T, pg *serverstore.PG, conn *pgx.Conn) {
	t.Helper()
	for _, check := range []struct{ purl, symbol, kind string }{
		{"pkg:npm/actual@2.0.0", "actual.receipt", "receipt"},
		{"pkg:npm/actual@2.0.0", "actual.observed", "observation"},
		{jdkTestPURL, "Library.call", "jdk"},
	} {
		raw, exists, err := pg.GetSnapshot(context.Background(), check.purl, check.symbol)
		if err != nil || !exists {
			t.Fatalf("%s snapshot: exists=%v err=%v", check.kind, exists, err)
		}
		var snapshot Snapshot
		if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
			t.Fatal(err)
		}
		if check.kind == "jdk" {
			if len(snapshot.JDKBoundaryCandidates) == 0 {
				t.Fatal("parity fixture has no JDK boundary")
			}
		} else {
			found := false
			for _, candidate := range snapshot.RegressionCandidates {
				if (candidate.CaseID != "") == (check.kind == "receipt") {
					found = true
				}
			}
			if !found {
				t.Fatalf("parity fixture has no %s regression: %+v", check.kind, snapshot)
			}
		}
	}
	var clusters, jobs int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM failure_clusters
		WHERE package_name='actual' AND regression_candidate`).Scan(&clusters); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM verification_jobs
		WHERE sample_id='java-boundary' AND reason='matrix'`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if clusters == 0 || jobs == 0 {
		t.Fatalf("vacuous parity fixture: regression clusters=%d matrix jobs=%d", clusters, jobs)
	}
}

// Exclude only generation clocks: incremental passes intentionally retain an
// untouched document's old generation time. All evidence, receipt order, shard
// totals, regressions, clusters, package rows and job contents must be equal.
func builderPGOutputs(t *testing.T, conn *pgx.Conn) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	queries := map[string]string{
		"snapshots": `SELECT jsonb_build_object('purl',purl,'symbol',symbol,'snapshot',snapshot)::text FROM compatibility_snapshots ORDER BY purl,symbol`,
		"shards":    `SELECT jsonb_build_object('key',key,'json',json)::text FROM shards ORDER BY key`,
		"clusters":  `SELECT (to_jsonb(c)-'id')::text FROM failure_clusters c ORDER BY ecosystem,package_name,symbol,stage,error_fp`,
		"jobs":      `SELECT (to_jsonb(j)-'id'-'created_at')::text FROM verification_jobs j ORDER BY sample_id,reason,want_env::text`,
		"packages":  `SELECT (to_jsonb(p)-'first_seen'-'last_seen')::text FROM packages p ORDER BY purl`,
	}
	var clearClocks func(any)
	clearClocks = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			delete(v, "generatedAt")
			for _, x := range v {
				clearClocks(x)
			}
		case []any:
			for _, x := range v {
				clearClocks(x)
			}
		}
	}
	for name, query := range queries {
		rows, err := conn.Query(context.Background(), query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var raw string
			if err := rows.Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var doc any
			if err := json.Unmarshal([]byte(raw), &doc); err != nil {
				t.Fatal(err)
			}
			clearClocks(doc)
			encoded, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}
			out[name] = append(out[name], string(encoded))
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func freezeBuilderPGSources(t *testing.T, conn *pgx.Conn, at time.Time) {
	t.Helper()
	for _, query := range []string{`UPDATE samples SET created_at=$1,updated_at=$1`, `UPDATE receipts SET created_at=$1`, `UPDATE evidence_agg SET first_seen=$1,last_seen=$1`} {
		if _, err := conn.Exec(context.Background(), query, at); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIntegrationBuilderScopedOutputParityAndBoundedPhases(t *testing.T) {
	pg, conn := openBuilderTestPG(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	// A versionless declaration and an undeclared resolved package, adjacent
	// majors, equal symbol claims in two ecosystems, and source-only coverage.
	seedBuilderPGSample(t, pg, "selected", []string{"pkg:npm/input@^1"}, "shared.call", "", []string{"pkg:npm/actual@1.0.0"}, []string{"pkg:npm/actual@2.0.0"})
	seedBuilderPGSample(t, pg, "broad", []string{"pkg:npm/actual@3.0.0"}, "shared.call", "", []string{"pkg:npm/actual@3.0.0", "pkg:npm/helper@1.0.0"})
	seedBuilderPGSample(t, pg, "global", []string{"pkg:pypi/global@1.0.0"}, "shared.call", "pkg:pypi/global@1.0.0", []string{"pkg:pypi/global@1.0.0"})
	seedBuilderPGSample(t, pg, "source-only", []string{"pkg:npm/actual@9.0.0"}, "actual.other", "")
	seedBuilderPGDerivedEvidence(t, pg)
	for i := 0; i < 10; i++ {
		purl := fmt.Sprintf("pkg:npm/irrelevant-%d@1.0.0", i)
		seedBuilderPGSample(t, pg, fmt.Sprintf("irrelevant-%d", i), []string{purl}, fmt.Sprintf("irrelevant%d.call", i), "", []string{purl})
	}
	freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
	full := &Builder{Store: pg, Now: func() time.Time { return now }, lastRun: now.Add(-time.Hour), passes: fullPassEvery}
	if err := full.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	assertBuilderPGDerivedEvidence(t, pg, conn)
	var baseline map[string]int64
	for _, growth := range []int{10, 100} {
		if growth == 100 {
			for i := 10; i < 100; i++ {
				purl := fmt.Sprintf("pkg:npm/irrelevant-%d@1.0.0", i)
				seedBuilderPGSample(t, pg, fmt.Sprintf("irrelevant-%d", i), []string{purl}, fmt.Sprintf("irrelevant%d.call", i), "", []string{purl})
			}
			freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
			full.passes = fullPassEvery
			if err := full.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := conn.Exec(ctx, `UPDATE samples SET updated_at=$1 WHERE sample_id IN ('selected','java-boundary')`, now); err != nil {
			t.Fatal(err)
		}
		sink := &phaseLogSink{}
		b := &Builder{Store: pg, Now: func() time.Time { return now }, lastRun: now.Add(-5 * time.Minute), passes: 1, phaseLogf: sink.logf}
		before := pg.PoolStats()
		if err := b.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		after := pg.PoolStats()
		assertBuilderPGDerivedEvidence(t, pg, conn)
		got := builderPGOutputs(t, conn)
		full.passes = fullPassEvery
		if err := full.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		want := builderPGOutputs(t, conn)
		for table := range want {
			if !reflect.DeepEqual(got[table], want[table]) {
				t.Fatalf("growth=%d %s incremental/full mismatch\ngot=%v\nwant=%v", growth, table, got[table], want[table])
			}
		}
		metrics := map[string]int64{}
		for _, line := range sink.lines {
			if !strings.Contains(line, "event=exit ") {
				continue
			}
			fields := map[string]string{}
			for _, field := range strings.Fields(line) {
				k, v, ok := strings.Cut(field, "=")
				if ok {
					fields[k] = v
				}
			}
			phase := fields["phase"]
			switch phase {
			case phaseSamplePageRead, phaseReceiptPageRead, phaseTargetProjectionRead, phaseSnapshotRetire:
				for _, field := range []string{"items", "json_bytes_examined_or_constructed_cumulative", "logical_calls"} {
					n, err := strconv.ParseInt(fields[field], 10, 64)
					if err != nil {
						t.Fatalf("missing phase metric %s: %s", field, line)
					}
					metrics[phase+"/"+field] = n
				}
				t.Logf("irrelevant_corpus=%d %s", growth, line)
			}
		}
		if len(metrics) == 0 {
			t.Fatal("no phase measurement")
		}
		// Acquired counters cover the whole pass, including source, target,
		// snapshots, stats and unchanged pool guards.
		metrics["checkouts"] = int64(builderPoolAcquired(after) - builderPoolAcquired(before))
		if baseline == nil {
			baseline = metrics
		} else if !reflect.DeepEqual(baseline, metrics) {
			t.Fatalf("10x irrelevant growth scaled incremental reads: baseline=%v grown=%v", baseline, metrics)
		}
	}
	// Removing the globally winning subject must restore the broader claims
	// and retire its own snapshots in the same incremental pass.
	freezeBuilderPGSources(t, conn, now.Add(-2*time.Hour))
	if _, err := conn.Exec(ctx, `UPDATE samples SET quarantined=true,updated_at=$1 WHERE sample_id='global'`, now); err != nil {
		t.Fatal(err)
	}
	b := &Builder{Store: pg, Now: func() time.Time { return now }, lastRun: now.Add(-5 * time.Minute), passes: 1}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	got := builderPGOutputs(t, conn)
	full.passes = fullPassEvery
	if err := full.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if want := builderPGOutputs(t, conn); !reflect.DeepEqual(got, want) {
		t.Fatalf("quarantine incremental/full mismatch: got=%v want=%v", got, want)
	}
}

func builderPoolAcquired(stats serverstore.PoolStats) uint64 {
	var total uint64
	for _, class := range stats.Classes {
		total += class.Acquired
	}
	return total
}

type failBuilderScopedStore struct {
	*serverstore.PG
	fail string
	err  error
}

func (s *failBuilderScopedStore) ListBuilderSnapshotTargets(ctx context.Context, p []serverstore.BuilderPackage) ([]serverstore.SnapshotTarget, serverstore.BuilderReadMetrics, error) {
	if s.fail == "targets" {
		return nil, serverstore.BuilderReadMetrics{}, s.err
	}
	return s.PG.ListBuilderSnapshotTargets(ctx, p)
}
func (s *failBuilderScopedStore) ListBuilderSamplesPage(ctx context.Context, p []serverstore.BuilderPackage, n, o int) ([]serverstore.SampleRow, error) {
	if s.fail == "samples" {
		return nil, s.err
	}
	return s.PG.ListBuilderSamplesPage(ctx, p, n, o)
}
func (s *failBuilderScopedStore) BuilderSnapshotKeys(ctx context.Context, p []serverstore.BuilderPackage) ([]serverstore.SnapshotTarget, error) {
	if s.fail == "retire" {
		return nil, s.err
	}
	return s.PG.BuilderSnapshotKeys(ctx, p)
}

func TestIntegrationBuilderScopedFailureDoesNotAdvanceCompletion(t *testing.T) {
	pg, conn := openBuilderTestPG(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	seedBuilderPGSample(t, pg, "selected", []string{"pkg:npm/actual@1.0.0"}, "actual.call", "", []string{"pkg:npm/actual@1.0.0"})
	freezeBuilderPGSources(t, conn, now)
	stamp := now.Add(-5 * time.Minute)
	if err := pg.SetStatsDaily(ctx, now.Format("2006-01-02"), `{"generatedAt":"`+stamp.Format(time.RFC3339)+`"}`); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"targets", "samples", "retire"} {
		for _, failure := range []error{errors.New("injected scoped read failure"), context.Canceled} {
			b := &Builder{Store: &failBuilderScopedStore{PG: pg, fail: phase, err: failure}, Now: func() time.Time { return now }, lastRun: stamp, passes: 1}
			if err := b.RunOnce(ctx); !errors.Is(err, failure) {
				t.Fatalf("phase=%s got=%v want=%v", phase, err, failure)
			}
			if b.passes != 1 || !b.lastRun.Equal(stamp) {
				t.Fatalf("phase=%s advanced completion", phase)
			}
			raw, _, err := pg.GetLatestStats(ctx)
			if err != nil || !strings.Contains(raw, stamp.Format(time.RFC3339)) {
				t.Fatalf("phase=%s stats advanced: %s %v", phase, raw, err)
			}
		}
	}
	// The persisted clock still resumes the failed work; retry is idempotent.
	b := &Builder{Store: pg, Now: func() time.Time { return now }}
	if err := b.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if !b.lastRun.Equal(now) {
		t.Fatal("retry failed to advance completion")
	}
}
