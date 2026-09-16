package serverstore

// Corpus-scale proof for the read models CSX-452 added (package_symbols,
// migration 0044) and this milestone's Task 1 added (farm_coverage,
// migration 0045): that GetPackageSymbols and GetFarmCoverage stay bounded
// reads -- index lookups, never a scan that grows with corpus size -- as
// their backing tables grow well past anything a planner could mistake for
// a small table worth hashing whole.
//
// This is the same guard as dependencyaxis_plan_test.go and
// authoringexpansion_plan_test.go: a plan shape (and, secondarily, a
// generous wall-clock budget), not a bare stopwatch. planNode and
// scansRelationSequentially are defined once, in
// dependencyaxis_plan_test.go, and reused here.
//
//	$env:CSX_TEST_DSN = "postgres://csx:csx@localhost:5432/csx"
//	go test ./internal/serverstore/ -run TestIntegration.*StaysBoundedAtScale -v

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// packageSymbolsScaleBudget is deliberately looser than the 50ms a
// production primary-key read would need: local Docker Postgres on a
// developer machine carries connection and scheduling overhead production
// doesn't, and this budget only needs to catch the failure mode under
// test -- a scan that grows with corpus size, which would blow well past
// even this loosened number, not shave a few milliseconds off it.
const packageSymbolsScaleBudget = 200 * time.Millisecond

// explainAnalyze runs EXPLAIN (ANALYZE, FORMAT JSON) for query against pg's
// own connection and returns the decoded top-level plan node. Parallel
// workers are disabled first so the plan reads as a single executor's work,
// matching this package's other EXPLAIN-based proofs.
func explainAnalyze(t *testing.T, pg *PG, ctx context.Context, query string, args ...any) planNode {
	t.Helper()
	var raw string
	err := pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `SET max_parallel_workers_per_gather = 0`); err != nil {
			return err
		}
		return c.QueryRow(ctx, `EXPLAIN (ANALYZE, FORMAT JSON) `+query, args...).Scan(&raw)
	})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	var top []struct {
		Plan planNode `json:"Plan"`
	}
	if err := json.Unmarshal([]byte(raw), &top); err != nil || len(top) == 0 {
		t.Fatalf("unexpected EXPLAIN shape: %v\n%s", err, raw)
	}
	return top[0].Plan
}

// assertNoSeqScan fails the test if any node in plan, at any depth (a scan
// can be buried under a join or filter node, so the whole tree is walked,
// not just the root), is a Seq Scan on relation.
func assertNoSeqScan(t *testing.T, plan planNode, relation string) {
	t.Helper()
	if scansRelationSequentially(plan, relation) {
		t.Fatalf("query plan contains a Seq Scan on %s at corpus scale, want an index lookup:\n%+v", relation, plan)
	}
}

// syntheticPURL returns a deterministic, distinct purl for corpus-scale
// seeding. It is not a real package -- just a unique primary key at
// whatever cardinality the test chooses.
func syntheticPURL(i int) string {
	return fmt.Sprintf("pkg:npm/synthetic-scale-test-%06d", i)
}

// Proves internal/httpapi's read-model lookup stays a bounded primary-key
// read regardless of corpus size, satisfying #452's index/query-plan
// acceptance criterion. 5,000 purls is chosen to be well past anything a
// sequential scan could hide behind planner cost estimation noise.
func TestIntegrationPackageSymbolsLookupStaysBoundedAtScale(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	const corpusSize = 5000
	rows := make([]PackageSymbolsRow, corpusSize)
	for i := range rows {
		rows[i] = PackageSymbolsRow{
			PURL:    syntheticPURL(i),
			Symbols: []string{"Foo", "Bar", "Baz"},
		}
	}
	for start := 0; start < len(rows); start += 500 {
		end := start + 500
		if end > len(rows) {
			end = len(rows)
		}
		if err := pg.PutPackageSymbols(ctx, rows[start:end]); err != nil {
			t.Fatalf("seed PutPackageSymbols: %v", err)
		}
	}
	// Statistics collected deliberately: a planner without them can pick an
	// unrepresentative plan on a table it has never seen written this much
	// data, and the test would then pass or fail on the wrong SQL.
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `ANALYZE package_symbols`)
		return err
	}); err != nil {
		t.Fatalf("analyze package_symbols: %v", err)
	}

	target := syntheticPURL(corpusSize / 2)
	plan := explainAnalyze(t, pg, ctx,
		`SELECT symbols, generated_at FROM package_symbols WHERE purl=$1`, target)
	assertNoSeqScan(t, plan, "package_symbols")

	start := time.Now()
	_, _, found, err := pg.GetPackageSymbols(ctx, target)
	elapsed := time.Since(start)
	if err != nil || !found {
		t.Fatalf("GetPackageSymbols(%q): found=%v err=%v", target, found, err)
	}
	if elapsed > packageSymbolsScaleBudget {
		t.Fatalf("GetPackageSymbols took %v against a %d-row corpus, want a bounded primary-key read", elapsed, corpusSize)
	}
}

// Proves GetFarmCoverage stays fast at the upper bound of plausible axis
// growth. farm_coverage's cardinality is an (os, ecosystem) cross product,
// not per-purl -- naturally tens of rows, not thousands -- so 200 synthetic
// axis pairs is chosen to sit well above anything the network is likely to
// ever populate, not to approximate today's real count.
func TestIntegrationFarmCoverageReadStaysBoundedAtScale(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()

	const osCount, ecosystemCount = 20, 10 // 200 axis pairs
	rows := make([]FarmAxisCoverage, 0, osCount*ecosystemCount)
	for o := 0; o < osCount; o++ {
		for e := 0; e < ecosystemCount; e++ {
			rows = append(rows, FarmAxisCoverage{
				OS:             fmt.Sprintf("synthetic-os-%02d", o),
				Ecosystem:      fmt.Sprintf("synthetic-eco-%02d", e),
				Observed:       100,
				Measured:       80,
				Proven:         60,
				ObservedProven: 50,
			})
		}
	}
	generatedAt := time.Now().UTC().Truncate(time.Millisecond)
	if err := pg.PutFarmCoverage(ctx, rows, generatedAt); err != nil {
		t.Fatalf("seed PutFarmCoverage: %v", err)
	}
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `ANALYZE farm_coverage; ANALYZE farm_coverage_meta`)
		return err
	}); err != nil {
		t.Fatalf("analyze farm_coverage: %v", err)
	}

	// Unlike package_symbols, neither of GetFarmCoverage's two queries is a
	// keyed lookup this test can hold to "must use the index": the meta
	// table is a one-row singleton by construction, where a Seq Scan of
	// exactly one row is the planner's correct, cost-optimal choice (an
	// earlier version of this test asserted assertNoSeqScan on
	// farm_coverage_meta and it failed on exactly that plan, against
	// correctly-behaving code -- the assertion was the bug), and
	// farm_coverage itself is read whole on purpose (migration
	// 0045_farm_coverage.sql: "a small, bounded cardinality" -- an index
	// would only add overhead over reading every row). What corpus-scale
	// growth can hurt here is wall clock, not plan shape, so that is what
	// this test bounds: the whole-table read the admin panel is going to
	// wait on stays fast even at the upper bound of plausible axis growth.
	start := time.Now()
	coverage, gotGeneratedAt, found, err := pg.GetFarmCoverage(ctx)
	elapsed := time.Since(start)
	if err != nil || !found {
		t.Fatalf("GetFarmCoverage: found=%v err=%v", found, err)
	}
	if len(coverage) != len(rows) {
		t.Fatalf("coverage rows = %d, want %d", len(coverage), len(rows))
	}
	if !gotGeneratedAt.Equal(generatedAt) {
		t.Fatalf("generatedAt = %v, want %v", gotGeneratedAt, generatedAt)
	}
	// internal/admin/farm_http.go's farmRequestTimeout budgets the whole HTTP
	// route (including the live-section corpus read) at 5s; this one bounded
	// read should complete in a small fraction of that.
	const farmCoverageScaleBudget = 500 * time.Millisecond
	if elapsed > farmCoverageScaleBudget {
		t.Fatalf("GetFarmCoverage took %v against a %d-cell axis table, want well under the admin panel's request budget", elapsed, len(rows))
	}
}
