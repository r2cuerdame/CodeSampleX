package localdb

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// Farm polls stats on homes whose database had grown to ~95MB. The previous
// queue-depth implementation selected every wide observation column and every
// queued JSON payload, up to 1,000 of each, merely to call len. Pin both the
// classification and the query plan so diagnostics stay on narrow indexes.
func TestPendingQueueCountsAreClassifiedAndIndexOnly(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	if _, err := db.sql.ExecContext(ctx, `
		INSERT INTO observations(epoch,purl,symbol,env_hash,stage,result,count,error_fp,uploaded)
		VALUES
		 ('2026-09-05','pkg:npm/pending@1.0.0','','env','TEST','PASS',1,'',0),
		 ('2026-09-05','pkg:npm/sent@1.0.0','','env','TEST','PASS',1,'',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Enqueue(ctx, "adoption", `{"sampleId":"one"}`); err != nil {
		t.Fatal(err)
	}
	setAside, err := db.Enqueue(ctx, "wanted", `{"package":"pkg:npm/two@1.0.0"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueueSetAside(ctx, setAside, "terminal"); err != nil {
		t.Fatal(err)
	}

	observations, err := db.PendingObservationCount(ctx, 1000)
	if err != nil || observations != 1 {
		t.Fatalf("pending observations = %d, err=%v; want 1", observations, err)
	}
	uploads, err := db.QueuePendingCounts(ctx, 1000)
	if err != nil || uploads.Total != 1 || uploads.ByKind["adoption"] != 1 || uploads.ByKind["wanted"] != 0 {
		t.Fatalf("pending uploads = %+v, err=%v; want one retryable adoption", uploads, err)
	}

	assertPlanUses(t, db, "observations_pending", `
		SELECT COUNT(*) FROM (
		  SELECT 1 FROM observations INDEXED BY observations_pending
		  WHERE uploaded = 0 LIMIT 1000
		)`)
	assertPlanUses(t, db, "observations_legacy_windows", `
		SELECT COUNT(*) FROM (
		  SELECT 1 FROM observations INDEXED BY observations_legacy_windows
		  WHERE exit_code > 2147483647 AND exit_code <= 4294967295 LIMIT 1
		)`)
	assertPlanUses(t, db, "upload_queue_pending", `
		SELECT kind, COUNT(*) FROM (
		  SELECT kind FROM upload_queue INDEXED BY upload_queue_pending
		  WHERE attempts < 8 ORDER BY id LIMIT 1000
		) GROUP BY kind ORDER BY kind`)
}

func assertPlanUses(t *testing.T, db *DB, index, query string) {
	t.Helper()
	rows, err := db.sql.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), index) {
		t.Fatalf("query plan does not use %s: %v", index, plan)
	}
}

// BenchmarkEmptyQueueCountsWithLargeHistory reproduces the production shape:
// a roughly 95MB history and no pending work. The old status path selected up
// to 1,000 wide rows; the indexed count should remain near-empty-queue work.
//
//	Run with: go test ./internal/storage/localdb -run '^$' \
//	  -bench BenchmarkEmptyQueueCountsWithLargeHistory -benchtime=100x
func BenchmarkEmptyQueueCountsWithLargeHistory(b *testing.B) {
	db, err := Open(filepath.Join(b.TempDir(), "csx.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO observations(epoch,purl,symbol,env_hash,stage,result,count,error_fp,error_summary,uploaded)
		VALUES('2026-09-05',?,?,?,?,'PASS',1,'',?,1)`)
	if err != nil {
		b.Fatal(err)
	}
	wide := strings.Repeat("x", 1536)
	for i := 0; i < 60000; i++ {
		if _, err := stmt.ExecContext(ctx, "pkg:npm/history@1.0.0", fmt.Sprintf("symbol-%05d", i), "env", "TEST", wide); err != nil {
			b.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	var pages, pageSize int64
	if err := db.sql.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		b.Fatal(err)
	}
	if err := db.sql.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		b.Fatal(err)
	}
	dbMB := float64(pages*pageSize) / (1 << 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if n, err := db.PendingObservationCount(ctx, 1000); err != nil || n != 0 {
			b.Fatalf("pending observations = %d, err=%v", n, err)
		}
		if q, err := db.QueuePendingCounts(ctx, 1000); err != nil || q.Total != 0 {
			b.Fatalf("pending uploads = %+v, err=%v", q, err)
		}
	}
	b.ReportMetric(dbMB, "DB-MB")
}
