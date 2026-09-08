package serverstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// These run in the same disposable PostgreSQL 17 schema and mandatory-DSN CI
// lane as the existing store integration tests. They deliberately exercise the
// production SQL and writers instead of a fake that cannot reveal index scans.
func openBuilderReadPG(t *testing.T) *PG {
	t.Helper()
	pg := openTestPG(t)
	builderSQL(t, pg, func(c *pgx.Conn) error {
		var version int
		if err := c.QueryRow(context.Background(), "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil {
			return err
		}
		if version/10000 != 17 {
			t.Fatalf("builder regression requires PostgreSQL 17; got %d", version)
		}
		return nil
	})
	return pg
}

func builderSQL(t *testing.T, pg *PG, f func(*pgx.Conn) error) {
	t.Helper()
	if err := pg.withConn(context.Background(), f); err != nil {
		t.Fatal(err)
	}
}

func builderFixtureSample(t *testing.T, pg *PG, id string, packages, symbols []string, subject string) {
	t.Helper()
	raw, err := json.Marshal(domain.SampleManifest{Packages: packages, Symbols: symbols, Subject: subject})
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveSample(context.Background(), SampleRow{SampleID: id, ManifestJSON: string(raw)}); err != nil {
		t.Fatal(err)
	}
}

func builderFixtureReceipt(t *testing.T, pg *PG, id, sample string, packages []string) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"schemaVersion": 2, "stages": map[string]string{"resolve": "PASS", "contract": "PASS"},
		"resolvedPackages": packages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pg.SaveReceipt(context.Background(), ReceiptRow{
		ReceiptID: id, SampleID: sample, PeerID: "builder-peer", EnvHash: "builder-env",
		ReceiptJSON: string(raw), ContractResult: "PASS",
	}); err != nil {
		t.Fatal(err)
	}
}

func builderTargetsForPackage(t *testing.T, rows []SnapshotTarget, pkg BuilderPackage) []SnapshotTarget {
	t.Helper()
	out := []SnapshotTarget{}
	for _, row := range rows {
		p, err := domain.ParsePURL(row.PURL)
		if err == nil && strings.EqualFold(p.Ecosystem, pkg.Ecosystem) && strings.EqualFold(p.Name, pkg.Name) {
			out = append(out, row)
		}
	}
	return out
}

func TestIntegrationBuilderSamplesIncludeUndeclaredReceiptPackagesAndAllVersions(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	pkg := []BuilderPackage{{Ecosystem: "NPM", Name: "@Scope/Widget"}}
	builderFixtureSample(t, pg, "undeclared", []string{"pkg:npm/harness@1.0.0"}, []string{"widget.call"}, "")
	builderFixtureReceipt(t, pg, "undeclared-r1", "undeclared", []string{"pkg:npm/%40scope/widget@1.0.0"})
	builderFixtureReceipt(t, pg, "undeclared-r2", "undeclared", []string{"pkg:npm/%40scope/widget@9.0.0"})
	builderFixtureSample(t, pg, "declared", []string{"pkg:npm/@scope/widget@2.0.0"}, nil, "")
	builderFixtureSample(t, pg, "subject-only", nil, []string{"widget.subject"}, "pkg:npm/%40scope/widget@3.0.0")
	builderFixtureSample(t, pg, "other", []string{"pkg:npm/unrelated@1.0.0"}, nil, "")
	builderFixtureSample(t, pg, "quarantined", []string{"pkg:npm/@scope/widget@1.0.0"}, nil, "")
	builderSQL(t, pg, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, "UPDATE samples SET created_at='2026-01-01', quarantined=(sample_id='quarantined')")
		return err
	})
	all, err := pg.ListSamplesPage(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var want []SampleRow
	for _, row := range all {
		if row.SampleID != "other" {
			want = append(want, row)
		}
	}
	var got []SampleRow
	before := classStat(t, pg.PoolStats(), "background").Acquired
	for offset := 0; ; offset++ {
		page, err := pg.ListBuilderSamplesPage(ctx, pkg, 1, offset)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		got = append(got, page...)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selected rows/order differ from full rows: got=%+v want=%+v", got, want)
	}
	if checkouts := classStat(t, pg.PoolStats(), "background").Acquired - before; checkouts != 4 {
		t.Fatalf("3 selected samples + terminal page used %d checkouts, want 4", checkouts)
	}
	histories, err := pg.ReceiptsForSamples(ctx, []string{"undeclared"})
	if err != nil || len(histories["undeclared"]) != 2 {
		t.Fatalf("complete receipt history = %+v, err=%v", histories, err)
	}
	if _, err := pg.ListBuilderSamplesPage(ctx, pkg, 0, 0); err == nil {
		t.Fatal("invalid page size succeeded")
	}
}

func TestIntegrationBuilderTargetsMatchFullGlobalAttributionAndCoordinates(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	builderFixtureSample(t, pg, "broad", []string{"pkg:npm/selected@1.0.0"}, []string{"shared", "subject-shared", "broad-only"}, "")
	builderFixtureReceipt(t, pg, "broad-r", "broad", []string{"pkg:npm/helper@1.0.0", "pkg:npm/selected@1.0.0"})
	// A narrower claim lives outside the selected package AND ecosystem.
	builderFixtureSample(t, pg, "narrow", []string{"pkg:pypi/other@2.0.0"}, []string{"shared"}, "")
	builderFixtureReceipt(t, pg, "narrow-r", "narrow", []string{"pkg:pypi/other@2.0.0"})
	builderFixtureSample(t, pg, "subject", nil, []string{"subject-shared"}, "pkg:gem/subject@3.0.0")
	builderFixtureReceipt(t, pg, "subject-r", "subject", []string{"pkg:gem/helper@1.0.0", "pkg:gem/subject@3.0.0"})
	builderFixtureSample(t, pg, "matrix", []string{"pkg:npm/selected@1.0.0"}, []string{"matrix"}, "")
	builderFixtureReceipt(t, pg, "matrix-r", "matrix", []string{"pkg:npm/selected@9.0.0"})
	builderFixtureSample(t, pg, "raw-subject", nil, []string{"scoped.subject"}, "pkg:npm/@Scope/Widget@2.0.0")
	builderFixtureReceipt(t, pg, "raw-subject-r", "raw-subject", []string{"pkg:npm/%40Scope/Widget@2.0.0"})
	// Resolved-list validation must reject the entire list, but preserve the
	// full path's explicit subject semantics even when no package list survives.
	builderFixtureSample(t, pg, "invalid-list", []string{"pkg:npm/selected@1.0.0"}, []string{"invalid"}, "")
	builderFixtureReceipt(t, pg, "invalid-list-r", "invalid-list", []string{"pkg:npm/selected@4.0.0", "not-a-purl"})
	builderFixtureSample(t, pg, "invalid-subject-list", nil, []string{"subject-invalid-list"}, "pkg:npm/selected@5.0.0")
	builderFixtureReceipt(t, pg, "invalid-subject-list-r", "invalid-subject-list", []string{"pkg:npm/selected@4.0.0", "not-a-purl"})
	for _, purl := range []string{
		"pkg:npm/selected@7.0.0", "pkg:npm/@scope/widget@6.0.0",
		"pkg:NPM/%40scope/widget@7.0.0", "pkg:npm/percent%2Bplus@1.0.0",
		"pkg:golang/example.com/module@1.2.0",
	} {
		builderSQL(t, pg, func(c *pgx.Conn) error {
			_, err := c.Exec(ctx, `INSERT INTO evidence_agg(purl,symbol,env_hash,env_json,stage,result)
				VALUES($1,'observed','env','{}','run','PASS')`, purl)
			return err
		})
		if err := pg.PutSnapshot(ctx, purl, "retired", "{}"); err != nil {
			t.Fatal(err)
		}
	}
	full, err := pg.ListSnapshotTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := pg.SnapshotKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range []BuilderPackage{
		{"npm", "selected"}, {"npm", "helper"}, {"pypi", "other"}, {"gem", "subject"},
		{"npm", "@scope/widget"}, {"npm", "percent+plus"}, {"golang", "example.com/module"},
	} {
		t.Run(pkg.Ecosystem+"/"+pkg.Name, func(t *testing.T) {
			selected, metrics, err := pg.ListBuilderSnapshotTargets(ctx, []BuilderPackage{pkg})
			want := builderTargetsForPackage(t, full, pkg)
			if err != nil || !reflect.DeepEqual(selected, want) {
				t.Fatalf("scoped targets=%+v want full subset=%+v err=%v", selected, want, err)
			}
			if len(selected) > 0 && (metrics.Rows == 0 || metrics.Bytes == 0) {
				t.Fatalf("missing read metrics: %+v", metrics)
			}
			gotKeys, err := pg.BuilderSnapshotKeys(ctx, []BuilderPackage{pkg})
			if err != nil || !reflect.DeepEqual(targetSet(gotKeys), targetSet(builderTargetsForPackage(t, keys, pkg))) {
				t.Fatalf("snapshot retirement keys=%+v full=%+v err=%v", gotKeys, keys, err)
			}
		})
	}
	set := targetSet(full)
	if set[SnapshotTarget{"pkg:npm/selected@1.0.0", "shared"}] ||
		set[SnapshotTarget{"pkg:npm/selected@1.0.0", "subject-shared"}] ||
		set[SnapshotTarget{"pkg:npm/selected@4.0.0", ""}] {
		t.Fatalf("invalid/global competing claim was incorrectly attributed: %+v", full)
	}
	if !set[SnapshotTarget{"pkg:npm/selected@5.0.0", "subject-invalid-list"}] ||
		!set[SnapshotTarget{"pkg:npm/selected@9.0.0", "matrix"}] {
		t.Fatalf("subject or cross-major matrix target lost: %+v", full)
	}
}

func TestIntegrationBuilderMalformedResolvedListsRemainAllOrNothing(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	lists := [][]string{
		{"pkg:npm/selected@1.0.0", "not-a-purl"},
		{"pkg:npm/selected@2.0.0", "pkg:npm/selected@1.0.0"},
		{"pkg:npm/selected@1.0.0", "pkg:npm/selected@1.0.0"},
		{"pkg:npm/selected@1.0.0", "pkg:npm/selected@latest"},
		{"pkg:npm/@scope/widget@1.0.0"},
	}
	for i, list := range lists {
		id := fmt.Sprintf("invalid-%d", i)
		builderFixtureSample(t, pg, id, []string{"pkg:npm/harness@1.0.0"}, []string{"invalid"}, "")
		builderFixtureReceipt(t, pg, id+"-r", id, list)
	}
	targets, _, err := pg.ListBuilderSnapshotTargets(ctx, []BuilderPackage{{"npm", "selected"}})
	if err != nil || len(targets) != 0 {
		t.Fatalf("malformed signed lists partially accepted: targets=%+v err=%v", targets, err)
	}
	selected, err := pg.ListBuilderSamplesPage(ctx, []BuilderPackage{{"npm", "selected"}}, 100, 0)
	if err != nil || len(selected) != 0 {
		t.Fatalf("invalid partial package selected samples: %+v err=%v", selected, err)
	}
}

func assertBuilderReadsClosed(t *testing.T, pg *PG) {
	t.Helper()
	ctx, packages := context.Background(), []BuilderPackage{{"npm", "selected"}}
	if rows, err := pg.ListBuilderSamplesPage(ctx, packages, 100, 0); err == nil || len(rows) != 0 {
		t.Fatalf("unsafe sample projection read: rows=%+v err=%v", rows, err)
	}
	if rows, _, err := pg.ListBuilderSnapshotTargets(ctx, packages); err == nil || len(rows) != 0 {
		t.Fatalf("unsafe target projection read: rows=%+v err=%v", rows, err)
	}
	if rows, err := pg.BuilderSnapshotKeys(ctx, packages); err == nil || len(rows) != 0 {
		t.Fatalf("unsafe snapshot projection read: rows=%+v err=%v", rows, err)
	}
	if changes, err := pg.BuilderChangesSince(ctx, time.Time{}); err == nil || len(changes.SamplePURLs) != 0 || len(changes.Targets) != 0 {
		t.Fatalf("unsafe change projection read: changes=%+v err=%v", changes, err)
	}
}

func TestIntegrationBuilderLegacyWritesFailClosedAndBackfillResumes(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	builderFixtureSample(t, pg, "legacy", []string{"pkg:npm/old@1.0.0"}, []string{"old.symbol"}, "")
	builderSQL(t, pg, func(c *pgx.Conn) error {
		// Simulate a rollback binary that replaces a manifest without knowing
		// the projection columns or advancing its updated_at timestamp.
		_, err := c.Exec(ctx, `UPDATE samples SET manifest='{"packages":["pkg:npm/selected@2.0.0"],"symbols":["new.symbol"]}',
			created_at='2000-01-01',updated_at='2000-01-01' WHERE sample_id='legacy'`)
		return err
	})
	assertBuilderReadsClosed(t, pg)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("repair stale sample: %v", err)
	}
	rows, err := pg.ListBuilderSamplesPage(ctx, []BuilderPackage{{"npm", "selected"}}, 100, 0)
	if err != nil || len(rows) != 1 || rows[0].SampleID != "legacy" {
		t.Fatalf("backfilled sample selection=%+v err=%v", rows, err)
	}
	changes, err := pg.BuilderChangesSince(ctx, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil || !reflect.DeepEqual(changes.SamplePURLs, []string{"pkg:npm/old@1.0.0", "pkg:npm/selected@2.0.0"}) {
		t.Fatalf("backfill did not dirty old and new packages: %+v err=%v", changes, err)
	}
	builderSQL(t, pg, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `INSERT INTO receipts(receipt_id,sample_id,peer_id,env_hash,receipt,contract_result)
			VALUES('legacy-receipt','legacy','peer','env',
			'{"schemaVersion":2,"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/undeclared@3.0.0"]}','PASS')`)
		return err
	})
	assertBuilderReadsClosed(t, pg)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("repair legacy receipt: %v", err)
	}
	rows, err = pg.ListBuilderSamplesPage(ctx, []BuilderPackage{{"npm", "undeclared"}}, 100, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("backfilled receipt attribution=%+v err=%v", rows, err)
	}
	// A changed immutable receipt cannot safely erase its previous package
	// history. Repair refuses it instead of reblessing the new source hash.
	builderSQL(t, pg, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `UPDATE receipts SET receipt='{"schemaVersion":2,"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/changed@4.0.0"]}'
			WHERE receipt_id='legacy-receipt'`)
		return err
	})
	assertBuilderReadsClosed(t, pg)
	if err := pg.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "immutable receipt") {
		t.Fatalf("changed immutable receipt migration=%v", err)
	}
}

func TestIntegrationBuilderAmbiguousLegacyRowsBlockReadsAndRepair(t *testing.T) {
	for _, kind := range []string{"sample", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			pg, ctx := openBuilderReadPG(t), context.Background()
			builderFixtureSample(t, pg, "valid", []string{"pkg:npm/selected@1.0.0"}, nil, "")
			if kind == "sample" {
				if err := pg.SaveSample(ctx, SampleRow{SampleID: "ambiguous", ManifestJSON: `{"packages":42}`}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := pg.SaveReceipt(ctx, ReceiptRow{ReceiptID: "ambiguous", SampleID: "valid",
					ReceiptJSON: `{"schemaVersion":2,"resolvedPackages":42}`}); err != nil {
					t.Fatal(err)
				}
			}
			assertBuilderReadsClosed(t, pg)
			if err := pg.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "ambiguous builder") {
				t.Fatalf("ambiguous legacy row silently repaired: %v", err)
			}
		})
	}
}

func TestIntegrationBuilderProjectionWriterFailureRollsBackSourceAndJob(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	builderFixtureSample(t, pg, "valid", []string{"pkg:npm/selected@1.0.0"}, nil, "")
	builderSQL(t, pg, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `ALTER TABLE samples ADD CONSTRAINT injected_projection_error
			CHECK (sample_id <> 'rollback-sample' OR builder_source_hash IS NULL)`); err != nil {
			return err
		}
		_, err := c.Exec(ctx, `ALTER TABLE receipts ADD CONSTRAINT injected_projection_error
			CHECK (receipt_id NOT LIKE 'rollback-%' OR builder_source_hash IS NULL)`)
		return err
	})
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "rollback-sample", ManifestJSON: "{}"}); err == nil {
		t.Fatal("injected sample projection failure succeeded")
	}
	if _, exists, err := pg.GetSample(ctx, "rollback-sample"); err != nil || exists {
		t.Fatalf("source escaped failed projection transaction: exists=%v err=%v", exists, err)
	}
	receipt := ReceiptRow{ReceiptID: "rollback-receipt", SampleID: "valid", PeerID: "peer", ReceiptJSON: "{}"}
	if err := pg.SaveReceipt(ctx, receipt); err == nil {
		t.Fatal("injected receipt projection failure succeeded")
	}
	jobID, err := pg.CreateJob(ctx, JobRow{SampleID: "valid", Reason: "cross"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.ClaimJob(ctx, jobID, "peer"); err != nil || !ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	receipt.ReceiptID = "rollback-job"
	if ok, err := pg.SaveReceiptForJob(ctx, receipt, jobID); err == nil || ok {
		t.Fatalf("injected job projection failure succeeded: saved=%v err=%v", ok, err)
	}
	builderSQL(t, pg, func(c *pgx.Conn) error {
		var count int
		if err := c.QueryRow(ctx, "SELECT count(*) FROM receipts WHERE receipt_id LIKE 'rollback-%'").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatalf("%d receipt sources escaped rollback", count)
		}
		var status string
		if err := c.QueryRow(ctx, "SELECT status FROM verification_jobs WHERE id=$1", jobID).Scan(&status); err != nil {
			return err
		}
		if status != "claimed" {
			t.Fatalf("failed receipt consumed job: status=%s", status)
		}
		return nil
	})
	if _, _, err := pg.ListBuilderSnapshotTargets(ctx, []BuilderPackage{{"npm", "selected"}}); err != nil {
		t.Fatalf("rolled-back writes left stale projection residue: %v", err)
	}
}

func TestIntegrationBuilderCancellationReleasesReadTransaction(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	builderFixtureSample(t, pg, "selected", []string{"pkg:npm/selected@1.0.0"}, []string{"call"}, "")
	builderFixtureReceipt(t, pg, "selected-r", "selected", []string{"pkg:npm/selected@1.0.0"})
	builderSQL(t, pg, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "LOCK receipts IN ACCESS EXCLUSIVE MODE"); err != nil {
			return err
		}
		timed, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		rows, _, err := pg.ListBuilderSnapshotTargets(timed, []BuilderPackage{{"npm", "selected"}})
		if err == nil || len(rows) != 0 || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("blocked read cancellation: rows=%+v err=%v", rows, err)
		}
		return nil
	})
	if rows, _, err := pg.ListBuilderSnapshotTargets(ctx, []BuilderPackage{{"npm", "selected"}}); err != nil || len(rows) != 2 {
		t.Fatalf("read after cancelled transaction: rows=%+v err=%v", rows, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := pg.Migrate(cancelled); err == nil {
		t.Fatal("cancelled backfill/migration succeeded")
	}
	if _, err := pg.BuilderSnapshotKeys(ctx, []BuilderPackage{{"npm", "selected"}}); err != nil {
		t.Fatalf("cancelled migration poisoned store: %v", err)
	}
}

type builderPlan struct {
	NodeType    string        `json:"Node Type"`
	IndexName   string        `json:"Index Name"`
	ActualRows  float64       `json:"Actual Rows"`
	ActualLoops float64       `json:"Actual Loops"`
	RowsRemoved float64       `json:"Rows Removed by Filter"`
	SharedHits  int64         `json:"Shared Hit Blocks"`
	SharedReads int64         `json:"Shared Read Blocks"`
	Plans       []builderPlan `json:"Plans"`
}

type builderPlanEvidence struct {
	Rows    float64
	Buffers int64
	Indexes []string
}

func explainBuilderRead(t *testing.T, pg *PG, sql string, args ...any) builderPlanEvidence {
	t.Helper()
	var raw []byte
	builderSQL(t, pg, func(c *pgx.Conn) error {
		return c.QueryRow(context.Background(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql,
			append([]any{pgx.QueryExecModeExec}, args...)...).Scan(&raw)
	})
	var plans []struct{ Plan builderPlan }
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode EXPLAIN: err=%v plan=%s", err, raw)
	}
	out := builderPlanEvidence{Buffers: plans[0].Plan.SharedHits + plans[0].Plan.SharedReads}
	var walk func(builderPlan)
	walk = func(p builderPlan) {
		out.Rows += (p.ActualRows + p.RowsRemoved) * p.ActualLoops
		if p.NodeType == "Seq Scan" {
			t.Fatalf("scoped read scanned a corpus table: %s", raw)
		}
		if p.IndexName != "" {
			out.Indexes = append(out.Indexes, p.IndexName)
		}
		for _, child := range p.Plans {
			walk(child)
		}
	}
	walk(plans[0].Plan)
	sort.Strings(out.Indexes)
	return out
}

func builderSeedIrrelevantCorpus(t *testing.T, pg *PG, first, last int) {
	t.Helper()
	ctx := context.Background()
	// Exercise normal transactional writers. A NULL source hash inserted and
	// repaired in a second statement can leave corpus-sized dead index history
	// even though both statements commit atomically. Cold plans must catch it.
	for i := first; i <= last; i++ {
		id := fmt.Sprintf("noise-%d", i)
		purl := fmt.Sprintf("pkg:npm/noise-%d@1.0.0", i)
		manifest, err := json.Marshal(map[string]any{
			"packages": []string{purl}, "symbols": []string{fmt.Sprintf("noise.symbol.%d", i)},
			"goal": strings.Repeat("irrelevant payload ", 128),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := pg.SaveSample(ctx, SampleRow{SampleID: id, ManifestJSON: string(manifest), SizeBytes: 4096}); err != nil {
			t.Fatal(err)
		}
		receipt, err := json.Marshal(map[string]any{
			"schemaVersion": 2, "stages": map[string]string{"resolve": "PASS", "contract": "PASS"},
			"resolvedPackages": []string{purl}, "detail": strings.Repeat("irrelevant receipt ", 128),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := pg.SaveReceipt(ctx, ReceiptRow{ReceiptID: "noise-r-" + id, SampleID: id,
			PeerID: "peer", EnvHash: "env", ReceiptJSON: string(receipt), ContractResult: "PASS"}); err != nil {
			t.Fatal(err)
		}
	}
	builderSQL(t, pg, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `INSERT INTO evidence_agg(purl,symbol,env_hash,env_json,stage,result,first_seen,last_seen)
			SELECT 'pkg:npm/noise-'||i||'@1.0.0','noise.symbol.'||i,'env','{}','run','PASS','2000-01-01','2000-01-01'
			FROM generate_series($1::int,$2::int) i`, first, last); err != nil {
			return err
		}
		if _, err := c.Exec(ctx, `INSERT INTO compatibility_snapshots(purl,symbol,snapshot)
			SELECT 'pkg:npm/noise-'||i||'@1.0.0','noise.symbol.'||i,jsonb_build_object('payload',repeat('snapshot ',256))
			FROM generate_series($1::int,$2::int) i`, first, last); err != nil {
			return err
		}
		// Preserve pending/dead index entries. No vacuum and no read warmup.
		for _, table := range []string{"samples", "receipts", "evidence_agg", "compatibility_snapshots"} {
			if _, err := c.Exec(ctx, "ANALYZE "+table); err != nil {
				return err
			}
		}
		return nil
	})
}

func TestIntegrationBuilderReadsStayBoundedAcrossTenfoldIrrelevantCorpus(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	packages := []BuilderPackage{{"npm", "selected"}}
	builderFixtureSample(t, pg, "selected", []string{"pkg:npm/selected@1.0.0"}, []string{"selected.call"}, "")
	builderFixtureReceipt(t, pg, "selected-r", "selected", []string{"pkg:npm/selected@1.0.0"})
	if err := pg.PutSnapshot(ctx, "pkg:npm/selected@1.0.0", "selected.call", "{}"); err != nil {
		t.Fatal(err)
	}
	builderSQL(t, pg, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `INSERT INTO evidence_agg(purl,symbol,env_hash,env_json,stage,result)
			VALUES('pkg:npm/selected@1.0.0','selected.call','env','{}','run','PASS')`)
		if err != nil {
			return err
		}
		// An explicit future cutoff keeps ordinary noise writes irrelevant
		// without rewriting their indexed clocks or vacuuming their history.
		for _, sql := range []string{
			"UPDATE samples SET created_at='2101-01-01',updated_at='2101-01-01' WHERE sample_id='selected'",
			"UPDATE receipts SET created_at='2101-01-01' WHERE sample_id='selected'",
			"UPDATE evidence_agg SET last_seen='2101-01-01' WHERE purl='pkg:npm/selected@1.0.0'",
		} {
			if _, err := c.Exec(ctx, sql); err != nil {
				return err
			}
		}
		return nil
	})
	type result struct {
		Samples       []SampleRow
		Receipts      map[string][]ReceiptRow
		Targets, Keys []SnapshotTarget
		Changes       Changes
		Metrics       BuilderReadMetrics
		Bytes         int64
		Checkouts     uint64
	}
	read := func() result {
		out := result{}
		before := classStat(t, pg.PoolStats(), "background").Acquired
		var err error
		if out.Samples, err = pg.ListBuilderSamplesPage(ctx, packages, 1000, 0); err != nil {
			t.Fatal(err)
		}
		if out.Receipts, err = pg.ReceiptsForSamples(ctx, []string{"selected"}); err != nil {
			t.Fatal(err)
		}
		if out.Targets, out.Metrics, err = pg.ListBuilderSnapshotTargets(ctx, packages); err != nil {
			t.Fatal(err)
		}
		if out.Keys, err = pg.BuilderSnapshotKeys(ctx, packages); err != nil {
			t.Fatal(err)
		}
		if out.Changes, err = pg.BuilderChangesSince(ctx, time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
		out.Checkouts = classStat(t, pg.PoolStats(), "background").Acquired - before
		out.Bytes = out.Metrics.Bytes
		for _, sample := range out.Samples {
			out.Bytes += int64(len(sample.ManifestJSON))
			for _, receipt := range out.Receipts[sample.SampleID] {
				out.Bytes += int64(len(receipt.ReceiptJSON))
			}
		}
		return out
	}
	queries := []struct {
		name, sql, index string
		args             []any
	}{
		{"sample selection", builderSelectedSamplesSQL, "samples_builder_coords_idx", []any{builderCoords(packages)}},
		{"complete sample page", builderSamplesPageSQL, "samples_builder_coords_idx", []any{builderCoords(packages), 1000, 0}},
		{"complete target claims", builderTargetClaimsSQL, "receipts_sample_idx", []any{builderCoords(packages)}},
		{"complete changed samples", builderChangesSamplesSQL, "samples_updated_at_idx", []any{time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"complete changed contenders", builderChangesContendersSQL, "samples_builder_symbols_idx", []any{[]string{"selected.call"}}},
		{"complete changed receipts", builderChangesReceiptsSQL, "receipts_sample_idx", []any{[]string{"selected"}}},
		{"complete changed evidence", builderChangesEvidenceSQL, "evidence_agg_builder_changed_idx", []any{time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}},
		{"receipt selection", "SELECT sample_id FROM receipts WHERE builder_coords && $1::text[]", "receipts_builder_coords_idx", []any{builderCoords(packages)}},
		{"symbol contenders", "SELECT sample_id FROM samples WHERE builder_symbols && $1::text[]", "samples_builder_symbols_idx", []any{[]string{"selected.call"}}},
		{"evidence targets", builderTargetEvidenceSQL, "evidence_agg_builder_coord_idx", []any{builderCoords(packages)}},
		{"retirement keys", builderSnapshotKeysSQL, "snapshots_builder_coord_idx", []any{builderCoords(packages)}},
		{"complete readiness", builderProjectionReadinessSQL, "samples_builder_stale_idx", nil},
		{"sample readiness", builderSampleStaleSQL, "samples_builder_stale_idx", nil},
		{"receipt readiness", builderReceiptStaleSQL, "receipts_builder_stale_idx", nil},
	}
	var baseline result
	plans := map[string]builderPlanEvidence{}
	for _, scale := range []int{1000, 10000} {
		first := 1
		if scale == 10000 {
			first = 1001
		}
		builderSeedIrrelevantCorpus(t, pg, first, scale)
		// Before any readiness probe/API warms dead index entries, measure the
		// actual first execution after normal source+projection writer growth.
		for _, q := range queries {
			plan := explainBuilderRead(t, pg, q.sql, q.args...)
			if !strings.Contains(strings.Join(plan.Indexes, ","), q.index) {
				t.Fatalf("cold %s did not use %s: %+v", q.name, q.index, plan)
			}
			key := "cold/" + q.name
			if scale == 1000 {
				plans[key] = plan
			} else if before := plans[key]; plan.Rows > before.Rows+4 || plan.Buffers > before.Buffers+24 {
				t.Fatalf("cold first-probe %s IO scaled with ordinary unrelated inserts: before=%+v after=%+v", q.name, before, plan)
			}
			t.Logf("cold irrelevant=%d query=%s actual_plan_rows=%.0f buffers=%d indexes=%v", scale, q.name, plan.Rows, plan.Buffers, plan.Indexes)
		}
		// Cross PostgreSQL's five-execution custom/generic decision boundary on
		// reused pool connections before measuring the production execution path.
		got := read()
		for repeat := 0; repeat < 6; repeat++ {
			next := read()
			if !reflect.DeepEqual(next, got) {
				t.Fatalf("reused production query changed output/metrics at repeat %d: first=%+v next=%+v", repeat, got, next)
			}
			got = next
		}
		if len(got.Samples) != 1 || len(got.Receipts["selected"]) != 1 || len(got.Targets) != 2 || got.Checkouts != 5 {
			t.Fatalf("unexpected bounded read result at %d: %+v", scale, got)
		}
		if scale == 1000 {
			baseline = got
		} else if !reflect.DeepEqual(got, baseline) {
			t.Fatalf("10x unrelated corpus changed rows/bytes/checkouts/output:\nbefore=%+v\nafter=%+v", baseline, got)
		}
		t.Logf("irrelevant=%d selected_samples=%d selected_receipts=%d target_projection_rows=%d target_projection_bytes=%d selected_payload_bytes=%d checkouts=%d",
			scale, len(got.Samples), len(got.Receipts["selected"]), got.Metrics.Rows, got.Metrics.Bytes, got.Bytes, got.Checkouts)
		for _, q := range queries {
			plan := explainBuilderRead(t, pg, q.sql, q.args...)
			if !strings.Contains(strings.Join(plan.Indexes, ","), q.index) {
				t.Fatalf("%s did not use %s: %+v", q.name, q.index, plan)
			}
			if scale == 1000 {
				plans[q.name] = plan
			} else {
				before := plans[q.name]
				if plan.Rows > before.Rows+4 || plan.Buffers > before.Buffers+24 {
					t.Fatalf("%s IO scaled with unrelated corpus: before=%+v after=%+v", q.name, before, plan)
				}
			}
			t.Logf("irrelevant=%d query=%s actual_plan_rows=%.0f buffers=%d indexes=%v", scale, q.name, plan.Rows, plan.Buffers, plan.Indexes)
		}
	}
}

func TestIntegrationBuilderJSONKeyAliasesFailClosedBeforeAttribution(t *testing.T) {
	cases := []struct {
		name, kind, raw string
	}{
		{"sample-exact-and-alias", "sample", `{"packages":["pkg:npm/selected@1.0.0"],"Packages":["pkg:npm/other@1.0.0"]}`},
		{"sample-uppercase-only", "sample", `{"Packages":["pkg:npm/selected@1.0.0"],"Symbols":["call"]}`},
		{"receipt-exact-and-alias", "receipt", `{"schemaVersion":2,"SchemaVersion":1,"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/selected@1.0.0"]}`},
		{"receipt-uppercase-only", "receipt", `{"SchemaVersion":2,"Stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/selected@1.0.0"]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pg, ctx := openBuilderReadPG(t), context.Background()
			builderFixtureSample(t, pg, "valid", []string{"pkg:npm/selected@1.0.0"}, []string{"call"}, "")
			query := "SELECT builder_source_hash FROM samples WHERE sample_id='ambiguous'"
			if tc.kind == "sample" {
				if err := pg.SaveSample(ctx, SampleRow{SampleID: "ambiguous", ManifestJSON: tc.raw}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := pg.SaveReceipt(ctx, ReceiptRow{ReceiptID: "ambiguous", SampleID: "valid", ReceiptJSON: tc.raw}); err != nil {
					t.Fatal(err)
				}
				query = "SELECT builder_source_hash FROM receipts WHERE receipt_id='ambiguous'"
			}
			// Go's case-insensitive decoder and JSONB's key ordering must not
			// accidentally bless different interpretations of this same source.
			builderSQL(t, pg, func(c *pgx.Conn) error {
				var hash *string
				if err := c.QueryRow(ctx, query).Scan(&hash); err != nil {
					return err
				}
				if hash != nil {
					t.Fatalf("ambiguous key aliases received a trusted source hash: %s", *hash)
				}
				return nil
			})
			assertBuilderReadsClosed(t, pg)
			if err := pg.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "ambiguous builder") {
				t.Fatalf("key alias ambiguity silently backfilled: %v", err)
			}
		})
	}
}
