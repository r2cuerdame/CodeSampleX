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
)

func saveBuilderScopeFixture(t *testing.T, pg *PG, id, manifest string, receipts ...string) {
	t.Helper()
	ctx := context.Background()
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `INSERT INTO samples
			(sample_id,manifest,status,license,size_bytes,created_at,updated_at)
			VALUES($1,$2::jsonb,'PUBLISHED','MIT-0',1,'2001-01-01','2001-01-01')`,
			id, manifest); err != nil {
			return err
		}
		for i, receipt := range receipts {
			if _, err := c.Exec(ctx, `INSERT INTO receipts
				(receipt_id,sample_id,peer_id,env_hash,receipt,contract_result,created_at)
				VALUES($1,$2,'scope-peer','scope-env',$3::jsonb,'PASS','2001-01-01')`,
				fmt.Sprintf("%s-%d", id, i), id, receipt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func scopeReceipt(version int, packages ...string) string {
	raw, _ := json.Marshal(map[string]any{
		"schemaVersion": version, "stages": map[string]string{"resolve": "PASS", "contract": "PASS"},
		"resolvedPackages": packages,
	})
	return string(raw)
}

func assertScopedTargetsEqualReference(t *testing.T, pg *PG, coords []string) []SnapshotTarget {
	t.Helper()
	all, err := pg.ListSnapshotTargets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	inScope := map[string]bool{}
	for _, coord := range coords {
		inScope[coord] = true
	}
	want := make([]SnapshotTarget, 0)
	for _, target := range all {
		if inScope[builderCoord(target.PURL)] {
			want = append(want, target)
		}
	}
	got, err := pg.ListSnapshotTargetsForPackages(context.Background(), coords)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("scoped targets differ from full reference\nscope=%v\ngot=%+v\nwant=%+v", coords, got, want)
	}
	return got
}

func TestIntegrationBuilderScopePreservesReceiptAttributionAndGlobalSymbols(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	b, c := "pkg:npm/b@1.0.0", "pkg:npm/c@1.0.0"
	saveBuilderScopeFixture(t, pg, "wide",
		`{"packages":["pkg:npm/declared@1"],"symbols":["shared","subject.symbol"]}`,
		scopeReceipt(2, b, c), scopeReceipt(1, b))
	saveBuilderScopeFixture(t, pg, "narrow",
		`{"packages":["pkg:npm/narrow@1"],"symbols":["shared"]}`,
		scopeReceipt(2, "pkg:npm/narrow@1"))
	saveBuilderScopeFixture(t, pg, "subject",
		`{"packages":["pkg:npm/other@1"],"symbols":["subject.symbol"],"subject":"pkg:npm/subject@8"}`,
		scopeReceipt(2, "pkg:npm/subject@8", "pkg:npm/subject@^9"))
	saveBuilderScopeFixture(t, pg, "v1",
		`{"packages":["pkg:npm/v1declared@1"],"symbols":["v1.symbol"],"subject":"pkg:npm/v1subject@1"}`,
		scopeReceipt(1, b))
	saveBuilderScopeFixture(t, pg, "malformed",
		`{"packages":["pkg:npm/malformeddeclared@1"],"symbols":["bad.symbol"]}`,
		scopeReceipt(2, b, "pkg:npm/c@^1"))
	saveBuilderScopeFixture(t, pg, "old-major",
		`{"packages":["pkg:npm/olddeclared@1"],"symbols":["b.old"]}`,
		scopeReceipt(2, "pkg:npm/b@0.9.0"))
	saveBuilderScopeFixture(t, pg, "new-major",
		`{"packages":["pkg:npm/newdeclared@1"],"symbols":["b.new"]}`,
		scopeReceipt(2, "pkg:npm/b@2.0.0"))
	saveBuilderScopeFixture(t, pg, "scoped",
		`{"packages":["pkg:npm/@Scope/Name@1"],"symbols":["scoped.symbol"]}`,
		scopeReceipt(2, "pkg:npm/%40Scope/Name@2"))
	saveBuilderScopeFixture(t, pg, "invalid-subject",
		`{"packages":["pkg:npm/invalid-subject@1"],"symbols":["b.old"],"subject":123}`,
		scopeReceipt(2, "pkg:npm/invalid-subject@1"))

	for _, coords := range [][]string{
		{"pkg:npm/b@"}, {"pkg:npm/c@"}, {"pkg:npm/subject@"},
		{"pkg:npm/%40scope/name@"}, {"pkg:npm/v1subject@"},
	} {
		assertScopedTargetsEqualReference(t, pg, coords)
	}

	rows, err := pg.ListBuilderSamplesPage(ctx, []string{"pkg:npm/b@"}, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, row := range rows {
		ids[row.SampleID] = true
	}
	for _, id := range []string{"wide", "old-major", "new-major"} {
		if !ids[id] {
			t.Errorf("receipt-only package failed to select %s; got %v", id, ids)
		}
	}
	if ids["narrow"] || ids["scoped"] {
		t.Fatalf("unrelated sample rows escaped scope: %v", ids)
	}
	receipts, err := pg.ReceiptsForSamples(ctx, []string{"wide"})
	if err != nil || len(receipts["wide"]) != 2 {
		t.Fatalf("complete receipt history = %+v, err=%v", receipts, err)
	}
	page1, err := pg.ListBuilderSamplesPage(ctx, []string{"pkg:npm/b@"}, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	page2, err := pg.ListBuilderSamplesPage(ctx, []string{"pkg:npm/b@"}, 1000, 2)
	if err != nil || !reflect.DeepEqual(append(page1, page2...), rows) {
		t.Fatalf("scoped pagination changed row order: err=%v", err)
	}

	stored := []SnapshotTarget{
		{PURL: "pkg:npm/b@0.1.0", Symbol: "retired"},
		{PURL: b, Symbol: "shared"},
		{PURL: "pkg:npm/unrelated@1", Symbol: "untouched"},
	}
	for _, target := range stored {
		if err := pg.PutSnapshot(ctx, target.PURL, target.Symbol, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := pg.SnapshotKeysForPackages(ctx, []string{"pkg:npm/b@"})
	if err != nil || !reflect.DeepEqual(keys, stored[:2]) {
		t.Fatalf("stored-only old major lost from retirement scope: keys=%v err=%v", keys, err)
	}

	// Withdrawing the globally narrowest owner reactivates the wide receipt's
	// symbols, even though its packages received no new evidence or receipts.
	if err := pg.SetSampleQuarantine(ctx, "narrow", true, "scope test"); err != nil {
		t.Fatal(err)
	}
	changes, err := pg.ChangedSince(ctx, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	coords, err := pg.BuilderPackageScope(ctx, time.Now().Add(-time.Minute), changes)
	if err != nil {
		t.Fatal(err)
	}
	for _, coord := range []string{"pkg:npm/b@", "pkg:npm/c@", "pkg:npm/narrow@"} {
		if !containsScopeString(coords, coord) {
			t.Errorf("quarantine omitted affected %s: %v", coord, coords)
		}
	}
	targets := assertScopedTargetsEqualReference(t, pg, coords)
	if !containsScopeTarget(targets, SnapshotTarget{PURL: b, Symbol: "shared"}) {
		t.Fatal("quarantine did not reactivate the wider receipt's symbol")
	}
	if err := pg.SetSampleQuarantine(ctx, "narrow", false, ""); err != nil {
		t.Fatal(err)
	}
	assertScopedTargetsEqualReference(t, pg, coords)
}

func containsScopeString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func containsScopeTarget(values []SnapshotTarget, want SnapshotTarget) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

type builderScopeReadMetrics struct {
	Targets, Samples, Receipts, Snapshots, JSONBytes int
	Checkouts                                        uint64
}

func measureBuilderScopeReads(t *testing.T, pg *PG) builderScopeReadMetrics {
	t.Helper()
	ctx := context.Background()
	before := classStat(t, pg.PoolStats(), "background").Acquired
	coords, err := pg.BuilderPackageScope(ctx, time.Now().Add(-time.Minute),
		Changes{SamplePURLs: []string{"pkg:npm/relevant@1"}})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := pg.ListSnapshotTargetsForPackages(ctx, coords)
	if err != nil {
		t.Fatal(err)
	}
	samples, err := pg.ListBuilderSamplesPage(ctx, coords, 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(samples))
	var metric builderScopeReadMetrics
	for _, sample := range samples {
		ids = append(ids, sample.SampleID)
		metric.JSONBytes += len(sample.ManifestJSON)
	}
	receipts, err := pg.ReceiptsForSamples(ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range receipts {
		metric.Receipts += len(page)
		for _, receipt := range page {
			metric.JSONBytes += len(receipt.ReceiptJSON)
		}
	}
	keys, err := pg.SnapshotKeysForPackages(ctx, coords)
	if err != nil {
		t.Fatal(err)
	}
	metric.Targets, metric.Samples, metric.Snapshots = len(targets), len(samples), len(keys)
	metric.Checkouts = classStat(t, pg.PoolStats(), "background").Acquired - before
	return metric
}

func seedBuilderIrrelevantCorpus(t *testing.T, pg *PG, start, end int) {
	t.Helper()
	ctx := context.Background()
	for first := start; first <= end; first += 500 {
		last := first + 499
		if last > end {
			last = end
		}
		err := pg.withConn(ctx, func(c *pgx.Conn) error {
			for _, query := range []string{
				`INSERT INTO samples(sample_id,manifest,status,license,size_bytes,created_at,updated_at)
				 SELECT 'irrelevant-'||n, jsonb_build_object(
				  'packages',jsonb_build_array('pkg:npm/irrelevant-'||n||'@1'),
				  'symbols',jsonb_build_array('irrelevant.symbol.'||n),'padding',repeat('x',512)),
				  'PUBLISHED','MIT-0',1,'2001-01-01','2001-01-01'
				 FROM generate_series($1::int,$2::int) n`,
				`INSERT INTO receipts(receipt_id,sample_id,peer_id,env_hash,receipt,contract_result,created_at)
				 SELECT 'irrelevant-receipt-'||n,'irrelevant-'||n,'irrelevant-peer','irrelevant-env',
				  jsonb_build_object('schemaVersion',2,'stages',jsonb_build_object('resolve','PASS'),
				   'resolvedPackages',jsonb_build_array('pkg:npm/irrelevant-'||n||'@1'),'padding',repeat('r',512)),
				  'PASS','2001-01-01' FROM generate_series($1::int,$2::int) n`,
				`INSERT INTO evidence_agg(purl,symbol,env_hash,env_json,stage,result,last_seen)
				 SELECT 'pkg:npm/irrelevant-'||n||'@1','irrelevant.symbol.'||n,'irrelevant-env','{}',
				  'PROJECT_COMPILE','PASS','2001-01-01' FROM generate_series($1::int,$2::int) n`,
				`INSERT INTO compatibility_snapshots(purl,symbol,snapshot)
				 SELECT 'pkg:npm/irrelevant-'||n||'@1','irrelevant.symbol.'||n,'{}'
				 FROM generate_series($1::int,$2::int) n`,
			} {
				if _, err := c.Exec(ctx, query, first, last); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, "ANALYZE samples; ANALYZE receipts; ANALYZE evidence_agg; ANALYZE compatibility_snapshots")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

type builderScopePlan struct {
	NodeType   string             `json:"Node Type"`
	Relation   string             `json:"Relation Name"`
	Index      string             `json:"Index Name"`
	Rows       float64            `json:"Actual Rows"`
	Loops      float64            `json:"Actual Loops"`
	HitBlocks  int                `json:"Shared Hit Blocks"`
	ReadBlocks int                `json:"Shared Read Blocks"`
	Plans      []builderScopePlan `json:"Plans"`
}

func TestIntegrationBuilderScopeReadsStayBoundedAtTenfoldCorpus(t *testing.T) {
	pg := openTestPG(t)
	saveBuilderScopeFixture(t, pg, "relevant",
		`{"packages":["pkg:npm/declared@1"],"symbols":["relevant.symbol"]}`,
		scopeReceipt(2, "pkg:npm/relevant@1"), scopeReceipt(2, "pkg:npm/relevant@2"))
	if err := pg.PutSnapshot(context.Background(), "pkg:npm/relevant@0", "stale", "{}"); err != nil {
		t.Fatal(err)
	}
	seedBuilderIrrelevantCorpus(t, pg, 1, 1000)
	before := measureBuilderScopeReads(t, pg)
	seedBuilderIrrelevantCorpus(t, pg, 1001, 10000)
	after := measureBuilderScopeReads(t, pg)
	t.Logf("irrelevant corpus 1000 -> 10000; before=%+v after=%+v", before, after)
	if before != after || before.Samples != 1 || before.Receipts != 2 {
		t.Fatalf("incremental read rows/JSON bytes/checkouts grew with irrelevant corpus: before=%+v after=%+v", before, after)
	}

	queries := []struct {
		name, sql, index string
		arg              []string
	}{
		{"samples", `SELECT sample_id FROM samples WHERE csx_builder_coords(manifest->'packages') && $1`,
			"builder_samples_packages_idx", []string{"pkg:npm/declared@"}},
		{"receipts", `SELECT receipt_id FROM receipts WHERE csx_builder_coords(receipt->'resolvedPackages') && $1`,
			"builder_receipts_packages_idx", []string{"pkg:npm/relevant@"}},
		{"symbols", `SELECT sample_id FROM samples WHERE manifest->'symbols' ?| $1`,
			"builder_samples_symbols_idx", []string{"relevant.symbol"}},
		{"evidence", `SELECT DISTINCT purl,symbol FROM evidence_agg WHERE csx_builder_coord(purl)=ANY($1)`,
			"builder_evidence_coord_idx", []string{"pkg:npm/relevant@"}},
		{"snapshots", `SELECT purl,symbol FROM compatibility_snapshots WHERE csx_builder_coord(purl)=ANY($1) ORDER BY purl,symbol`,
			"builder_snapshots_coord_idx", []string{"pkg:npm/relevant@"}},
		{"unsafe", `SELECT sample_id FROM samples WHERE csx_builder_coords(manifest->'packages') @> $1 LIMIT 1`,
			"builder_samples_packages_idx", []string{"!"}},
	}
	for _, query := range queries {
		t.Run(query.name, func(t *testing.T) {
			var raw string
			if err := pg.withConn(context.Background(), func(c *pgx.Conn) error {
				return c.QueryRow(context.Background(), "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+query.sql, query.arg).Scan(&raw)
			}); err != nil {
				t.Fatal(err)
			}
			var top []struct{ Plan builderScopePlan }
			if err := json.Unmarshal([]byte(raw), &top); err != nil || len(top) != 1 {
				t.Fatalf("invalid explain %s: %v", raw, err)
			}
			indexes := []string{}
			var visit func(builderScopePlan)
			visit = func(node builderScopePlan) {
				if node.NodeType == "Seq Scan" && containsScopeString([]string{"samples", "receipts", "evidence_agg", "compatibility_snapshots"}, node.Relation) {
					t.Errorf("whole-corpus sequential scan: %s", raw)
				}
				if node.Index != "" {
					indexes = append(indexes, node.Index)
				}
				for _, child := range node.Plans {
					visit(child)
				}
			}
			visit(top[0].Plan)
			sort.Strings(indexes)
			if !containsScopeString(indexes, query.index) {
				t.Errorf("missing candidate index %s: %s", query.index, raw)
			}
			t.Logf("rows=%g loops=%g shared_hit=%d shared_read=%d indexes=%v",
				top[0].Plan.Rows, top[0].Plan.Loops, top[0].Plan.HitBlocks, top[0].Plan.ReadBlocks, indexes)
		})
	}
}

func TestIntegrationBuilderScopeRejectsAmbiguousLegacyAndCancellation(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	saveBuilderScopeFixture(t, pg, "unsafe-legacy",
		`{"packages":["pkg:npm/%61lias@1"],"symbols":[]}`, scopeReceipt(2, "pkg:npm/ordinary@1"))
	coords, err := pg.BuilderPackageScope(ctx, time.Now(), Changes{SamplePURLs: []string{"pkg:npm/unrelated@1"}})
	if err == nil || !strings.Contains(err.Error(), "unsafe-legacy") || len(coords) != 0 {
		t.Fatalf("ambiguous legacy silently omitted: coords=%v err=%v", coords, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	for name, call := range map[string]func() error{
		"scope": func() error { _, err := pg.BuilderPackageScope(cancelled, time.Now(), Changes{}); return err },
		"targets": func() error {
			_, err := pg.ListSnapshotTargetsForPackages(cancelled, []string{"pkg:npm/ordinary@"})
			return err
		},
		"samples": func() error {
			_, err := pg.ListBuilderSamplesPage(cancelled, []string{"pkg:npm/ordinary@"}, 1000, 0)
			return err
		},
		"snapshot keys": func() error {
			_, err := pg.SnapshotKeysForPackages(cancelled, []string{"pkg:npm/ordinary@"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled read returned %v", err)
			}
		})
	}
}

func TestIntegrationBuilderScopeQueryFailureDoesNotBecomeEmptySuccess(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	// openTestPG owns a disposable schema. Renaming its helper simulates an
	// unavailable required migration without touching source records.
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, "ALTER FUNCTION csx_builder_coord(text) RENAME TO test_unavailable_builder_coord")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"scope":         func() error { _, err := pg.BuilderPackageScope(ctx, time.Now(), Changes{}); return err },
		"targets":       func() error { _, err := pg.ListSnapshotTargetsForPackages(ctx, []string{"pkg:npm/a@"}); return err },
		"samples":       func() error { _, err := pg.ListBuilderSamplesPage(ctx, []string{"pkg:npm/a@"}, 1000, 0); return err },
		"snapshot keys": func() error { _, err := pg.SnapshotKeysForPackages(ctx, []string{"pkg:npm/a@"}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("missing required helper returned empty success")
			}
		})
	}
}
