package serverstore

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Keep the old multiplicity as a differential oracle without copying the
// receipt, quarantine, dependency or coverage predicates into a second query.
// PostgreSQL does not execute the now-unreferenced wanted_coord CTE.
func farmStocksBeforeCoordinateDedup(t *testing.T) string {
	t.Helper()
	const join = "FROM wanted_coord wk"
	if strings.Count(farmBacklogStocksSQL, join) != 1 {
		t.Fatal("cannot construct the pre-dedup stock query")
	}
	return strings.Replace(farmBacklogStocksSQL, join, "FROM wanted_key wk", 1)
}

func readFarmStocks(t *testing.T, pg *PG, query string) [6]int {
	t.Helper()
	ctx, cancel := farmAggregateContext(t.Context())
	defer cancel()
	var got [6]int
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := beginFarmAggregate(ctx, c, farmAggregateTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		return tx.QueryRow(ctx, query).Scan(&got[0], &got[1], &got[2], &got[3], &got[4], &got[5])
	}); err != nil {
		t.Fatalf("read Farm stocks: %v", err)
	}
	return got
}

func TestIntegrationFarmBacklogWantedCoordinateParity(t *testing.T) {
	pg := openTestPG(t)
	fake := NewFake()
	ctx := t.Context()
	now := time.Now().UTC()
	fake.NowFn = func() time.Time { return now }
	stores := []interface {
		SaveSample(context.Context, SampleRow) error
		SaveReceipt(context.Context, ReceiptRow) error
		RecordWanted(context.Context, string, string, []WantedRow) error
	}{fake, pg}

	for _, fixture := range []struct {
		id, manifest string
		quarantined  bool
		receipts     []string
	}{
		{"modern", `{"packages":["pkg:npm/modern@0.1.0"],"symbols":["run"]}`, false, []string{
			`{"schemaVersion":2,"environment":{"os":"linux"},"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/modern@1.0.0"]}`,
			`{"schemaVersion":2,"environment":{"os":"windows"},"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/modern@2.0.0"]}`,
			`{"schemaVersion":2,"environment":{"os":"linux"},"stages":{"resolve":"FAIL"},"resolvedPackages":["pkg:npm/modern@3.0.0"]}`,
		}},
		// Both historical spellings can carry the same sample. They must not
		// multiply the answer, nor may either spelling be dropped by dedup.
		{"scoped", `{"packages":["pkg:npm/@scope/tool@0.1.0","pkg:npm/%40scope/tool@0.1.0"],"symbols":["run"]}`, false, []string{
			`{"schemaVersion":2,"environment":{"os":"linux"},"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/%40scope/tool@1.0.0"]}`,
		}},
		{"scoped-raw", `{"packages":["pkg:npm/@scope/raw@0.1.0"],"symbols":["run"]}`, false, []string{
			`{"schemaVersion":2,"environment":{"os":"linux"},"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/%40scope/raw@1.0.0"]}`,
		}},
		{"scoped-encoded", `{"packages":["pkg:npm/%40scope/encoded@0.1.0"],"symbols":["run"]}`, false, []string{
			`{"schemaVersion":2,"environment":{"os":"linux"},"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/%40scope/encoded@1.0.0"]}`,
		}},
		{"quarantined", `{"packages":["pkg:npm/quarantined@1.0.0"],"symbols":["run"]}`, true, []string{
			`{"schemaVersion":2,"environment":{"os":"linux"},"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/quarantined@1.0.0"]}`,
		}},
		{"legacy", `{"packages":["pkg:npm/legacy@1.0.0"],"symbols":["run"]}`, false, []string{
			`{"schemaVersion":1,"environment":{"os":"windows"}}`,
		}},
	} {
		for _, store := range stores {
			if err := store.SaveSample(ctx, SampleRow{SampleID: fixture.id, ManifestJSON: fixture.manifest, Quarantined: fixture.quarantined}); err != nil {
				t.Fatal(err)
			}
			for i, receipt := range fixture.receipts {
				if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: fmt.Sprintf("%s-%d", fixture.id, i), SampleID: fixture.id,
					ContractResult: "PASS", ReceiptJSON: receipt, CreatedAt: now.Add(-time.Duration(i+1) * time.Minute)}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	for _, store := range stores {
		if err := store.SaveReceipt(ctx, ReceiptRow{ReceiptID: "modern-fail", SampleID: "modern", ContractResult: "FAIL",
			ReceiptJSON: `{"schemaVersion":2,"environment":{"os":"linux"},"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/modern@4.0.0"]}`,
			CreatedAt:   now.Add(-time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}

	assertStocks := func(want [6]int) {
		t.Helper()
		for _, query := range []string{farmStocksBeforeCoordinateDedup(t), farmBacklogStocksSQL} {
			if got := readFarmStocks(t, pg, query); got != want {
				t.Fatalf("stock counters = %v, want %v", got, want)
			}
		}
		// PG stamps receipt persistence with its own clock; the Fake retains
		// CreatedAt. The wide window includes the same seeded receipts on both.
		before, err := fake.FarmBacklogNow(ctx, now.Add(-time.Hour), now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		after, err := pg.FarmBacklogNow(ctx, now.Add(-time.Hour), now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("Fake/PG full backlog differs: fake=%+v pg=%+v", before, after)
		}
	}
	assertStocks([6]int{}) // Samples alone must not manufacture Wanted rows.

	var want [6]int
	for i, request := range []struct {
		name, version, symbol, os string
		answered                  bool
	}{
		{"modern", "1.0.0", "run", "linux", true},
		{"modern", "1.0.0", "run", "windows", false},
		{"modern", "2.0.0", "run", "windows", true},
		{"modern", "2.0.0", "run", "linux", false},
		{"modern", "0.1.0", "run", "linux", false},
		{"modern", "3.0.0", "run", "linux", false},
		{"modern", "4.0.0", "run", "linux", false},
		{"modern", "1.0.0", "missing", "linux", false},
		{"modern", "", "run", "linux", true},
		{"modern", "1.0.0", "", "", true},
		{"@scope/tool", "1.0.0", "run", "linux", true},
		{"@scope/tool", "1.0.0", "run", "windows", false},
		{"@scope/tool", "2.0.0", "run", "linux", false},
		{"@scope/raw", "1.0.0", "run", "linux", true},
		{"@scope/encoded", "1.0.0", "run", "linux", true},
		{"quarantined", "1.0.0", "run", "linux", false},
		{"legacy", "", "run", "windows", true},
		{"legacy", "", "run", "linux", false},
		{"absent", "1.0.0", "run", "linux", false},
	} {
		row := WantedRow{Ecosystem: "npm", Name: request.name, Version: request.version, Symbol: request.symbol, TargetOS: request.os}
		for _, store := range stores {
			if err := store.RecordWanted(ctx, "2026-09-11", "reader-1", []WantedRow{row}); err != nil {
				t.Fatal(err)
			}
			if i%2 == 0 {
				if err := store.RecordWanted(ctx, "2026-09-11", "reader-2", []WantedRow{row}); err != nil {
					t.Fatal(err)
				}
			}
		}
		want[5]++
		if request.answered {
			want[4]++
		} else {
			want[2]++
			if i%2 == 0 {
				want[3]++
			}
		}
	}
	assertStocks(want)

	// The existing PG backlog accepts manifest versions for v1 receipts;
	// Fake's newer exact-version policy does not. Preserve the PG predicate
	// here without disguising that pre-existing difference as Fake parity.
	if err := pg.RecordWanted(ctx, "2026-09-11", "legacy-reader", []WantedRow{
		{Ecosystem: "npm", Name: "legacy", Version: "1.0.0", Symbol: "run", TargetOS: "windows"},
		{Ecosystem: "npm", Name: "legacy", Version: "2.0.0", Symbol: "run", TargetOS: "windows"},
	}); err != nil {
		t.Fatal(err)
	}
	want[2]++
	want[4]++
	want[5] += 2
	for _, query := range []string{farmStocksBeforeCoordinateDedup(t), farmBacklogStocksSQL} {
		if got := readFarmStocks(t, pg, query); got != want {
			t.Fatalf("legacy PG counters changed: got %v, want %v", got, want)
		}
	}
}

// The performance change does not change FarmBacklog's existing newest-ten
// PASS receipt window. This is PG old/new parity, not a claim that this
// window matches TopWanted's newer full-history semantics.
func TestIntegrationFarmBacklogWantedReceiptWindowUnchanged(t *testing.T) {
	pg := openTestPG(t)
	ctx := t.Context()
	if err := pg.SaveSample(ctx, SampleRow{SampleID: "history", ManifestJSON: `{"packages":["pkg:npm/history@0.1.0"],"symbols":["run"]}`}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		version, os := "2.0.0", "linux"
		if i == 0 {
			version, os = "1.0.0", "windows"
		}
		if err := pg.SaveReceipt(ctx, ReceiptRow{ReceiptID: fmt.Sprintf("history-%d", i), SampleID: "history", ContractResult: "PASS",
			ReceiptJSON: fmt.Sprintf(`{"schemaVersion":2,"environment":{"os":%q},"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/history@%s"]}`, os, version)}); err != nil {
			t.Fatal(err)
		}
	}
	// Set an explicit total order instead of relying on persistence clock
	// resolution to decide which receipt falls outside the preserved limit.
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `UPDATE receipts SET created_at='2026-09-11 12:00:00+00'::timestamptz + make_interval(secs => substring(receipt_id from 9)::int)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := pg.RecordWanted(ctx, "2026-09-11", "reader", []WantedRow{
		{Ecosystem: "npm", Name: "history", Version: "1.0.0", Symbol: "run", TargetOS: "windows"},
		{Ecosystem: "npm", Name: "history", Version: "2.0.0", Symbol: "run", TargetOS: "linux"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{farmStocksBeforeCoordinateDedup(t), farmBacklogStocksSQL} {
		if got, want := readFarmStocks(t, pg, query), [6]int{0, 0, 1, 0, 1, 2}; got != want {
			t.Fatalf("PG receipt window changed: got %v, want %v", got, want)
		}
	}
}

// Measure intermediate rows, not runner speed. Ten times as many Wanted
// symbols on one package may enlarge wanted_key, but must not multiply the
// package's samples before candidate_samples deduplicates them.
func TestIntegrationFarmBacklogWantedJoinDoesNotMultiplySamples(t *testing.T) {
	pg := openTestPG(t)
	const samples = 128
	if err := pg.withConn(t.Context(), func(c *pgx.Conn) error {
		_, err := c.Exec(t.Context(), `
			INSERT INTO samples(sample_id,manifest,size_bytes)
			SELECT 'fanout-'||g, '{"packages":["pkg:npm/fanout@1.0.0"],"symbols":["run"]}'::jsonb,0
			FROM generate_series(1,$1::int) g;
			INSERT INTO sample_packages(sample_id,purl,coord)
			SELECT sample_id,'pkg:npm/fanout@1.0.0','pkg:npm/fanout@' FROM samples`, pgx.QueryExecModeSimpleProtocol, samples)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, count := range []int{100, 1000} {
		if err := pg.withConn(t.Context(), func(c *pgx.Conn) error {
			// Bare ANALYZE reaches other tests' schemas in the same database.
			// Only refresh this fixture's populated tables through its search_path.
			_, err := c.Exec(t.Context(), `TRUNCATE wanted;
				INSERT INTO wanted(ecosystem,name,version,symbol,target_os,asks,first_seen,last_seen)
				SELECT 'npm','fanout','1.0.0','sym-'||g,'linux',2,now(),now()
				FROM generate_series(1,$1::int) g;
				ANALYZE wanted, samples, sample_packages`, pgx.QueryExecModeSimpleProtocol, count)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		want := [6]int{0, 0, count, count, 0, count}
		fake := NewFake()
		rows := make([]WantedRow, count)
		for i := range rows {
			rows[i] = WantedRow{Ecosystem: "npm", Name: "fanout", Version: "1.0.0", Symbol: fmt.Sprintf("sym-%d", i+1), TargetOS: "linux"}
		}
		for _, reader := range []string{"first", "second"} {
			if err := fake.RecordWanted(t.Context(), "2026-09-11", reader, rows); err != nil {
				t.Fatal(err)
			}
		}
		fakeBacklog, err := fake.FarmBacklogNow(t.Context(), time.Now().Add(-time.Hour), time.Now())
		if err != nil || fakeBacklog.RequestBacklog != count || fakeBacklog.RepeatedMisses != count || fakeBacklog.TotalRequests != count || fakeBacklog.ResolvedRequests != 0 {
			t.Fatalf("skewed Fake counters = %+v, err=%v", fakeBacklog, err)
		}
		for _, query := range []string{farmStocksBeforeCoordinateDedup(t), farmBacklogStocksSQL} {
			if got := readFarmStocks(t, pg, query); got != want {
				t.Fatalf("skewed Wanted counters = %v, want %v", got, want)
			}
		}
		got := farmCandidateIntermediateRows(t, pg, farmBacklogStocksSQL)
		old := farmCandidateIntermediateRows(t, pg, farmStocksBeforeCoordinateDedup(t))
		t.Logf("wanted=%d sample rows=%d candidate intermediate rows: old=%.0f current=%.0f", count, samples, old, got)
		if got > samples || got < 1 {
			t.Fatalf("candidate join emitted %.0f intermediate rows for %d samples", got, samples)
		}
		if old < float64(count*samples) {
			t.Fatalf("fixture did not exercise the original multiplication: %.0f rows", old)
		}
	}
}

func farmCandidateIntermediateRows(t *testing.T, pg *PG, query string) float64 {
	t.Helper()
	ctx, cancel := farmAggregateContext(t.Context())
	defer cancel()
	var raw []byte
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := beginFarmAggregate(ctx, c, farmAggregateTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		return tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query).Scan(&raw)
	}); err != nil {
		t.Fatal(err)
	}
	type planNode struct {
		Subplan string     `json:"Subplan Name"`
		Rows    float64    `json:"Actual Rows"`
		Loops   float64    `json:"Actual Loops"`
		Plans   []planNode `json:"Plans"`
	}
	var plans []struct{ Plan planNode }
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("invalid Farm stock plan: %v", err)
	}
	found, maximum := false, float64(0)
	var walk func(planNode, bool)
	walk = func(node planNode, candidate bool) {
		if node.Subplan == "CTE candidate_samples" {
			found, candidate = true, true
		}
		if candidate {
			maximum = max(maximum, node.Rows*node.Loops)
		}
		for _, child := range node.Plans {
			walk(child, candidate)
		}
	}
	walk(plans[0].Plan, false)
	if !found {
		t.Fatal("Farm stock plan omitted candidate_samples")
	}
	return maximum
}
