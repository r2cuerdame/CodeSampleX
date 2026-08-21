package localdb

import (
	"context"
	"strings"
	"testing"
)

// queryPlan returns SQLite's own account of how it intends to answer a query.
func queryPlan(t *testing.T, db *DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.sql.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, "\n")
}

// One local search reads the receipts of every candidate it scores — 2,788 of
// them on the machine this was measured on. With no index on
// receipts.sample_id, each of those reads is a full table scan of the whole
// receipts table, so a single search visited about 8.5 million rows and spent
// 10.4 of its 11.3 seconds inside this one query. A CPU profile put it at
// 89.6% of Engine.Search.
//
// The test is on the query PLAN rather than on the index by name, because
// what must stay true is that this lookup never scans the table again —
// whichever index makes that so.
func TestReceiptsOfOneSampleAreNotFoundByScanningThemAll(t *testing.T) {
	db := openTemp(t)
	plan := queryPlan(t,
		db,
		`SELECT json FROM receipts WHERE sample_id = ? ORDER BY created_at, receipt_id`,
		"sha256:whatever")
	if strings.Contains(plan, "SCAN receipts") {
		t.Errorf("reading one sample's receipts scans the whole table:\n%s", plan)
	}
	if !strings.Contains(plan, "receipts") {
		t.Fatalf("plan does not mention receipts at all:\n%s", plan)
	}
}
