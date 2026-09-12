package localdb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func evidenceStatsFixture(t *testing.T) (*DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "csx.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO observations
		(epoch,purl,symbol,env_hash,stage,result,count,error_fp,uploaded)
		VALUES('2026-09-12','pkg:npm/private@1','','env','TEST','PASS',1,'',0),
		('2026-09-12','pkg:npm/uploaded@1','','env','TEST','PASS',1,'',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Enqueue(ctx, "adoption", `{"private":"payload"}`); err != nil {
		t.Fatal(err)
	}
	aside, err := db.Enqueue(ctx, "receipt", `{"private":"terminal"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueueSetAside(ctx, aside, "terminal"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(ctx, `INSERT INTO refused_evidence
		(epoch,purl,symbol,env_hash,stage,result,code,reason,refused_at)
		VALUES('2026-09-12','pkg:npm/refused@1','','env','TEST','PASS','invalid','private reason','2026-09-12T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"firstRunAt": "2026-09-11T00:00:00Z", "lastUpload": "2026-09-12T00:01:00Z",
		"lastUploadAttempt": "2026-09-12T00:02:00Z",
		"lastUploadError":   "evidence: the server refused 2 batches, first: CANARY_PRIVATE_REASON",
	} {
		if err := db.SetStat(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	return db, path
}

func TestEvidenceStatsReadOnlyCountsDuringCompetingWALWriter(t *testing.T) {
	db, path := evidenceStatsFixture(t)
	ctx := context.Background()
	conn, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, "ROLLBACK")
	if _, err := conn.ExecContext(ctx, `INSERT INTO upload_queue(kind,payload,created_at,attempts,last_error)
		VALUES('evidence','uncommitted','2026-09-12T00:00:00Z',0,'')`); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	st, err := ReadEvidenceStats(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(before); elapsed > 2*time.Second {
		t.Fatalf("WAL reader waited behind writer for %s", elapsed)
	}
	if st.QueueDepth != 2 || st.Queue.EvidenceBatches != 1 || st.Queue.Uploads != 1 || st.EvidenceRefusedTerminal != 1 {
		t.Fatalf("wrong committed counts: %+v", st)
	}
	if st.LastUpload != "2026-09-12T00:01:00Z" || st.LastUploadAttempt != "2026-09-12T00:02:00Z" ||
		st.LastUploadError != "evidence: the server refused 2 batches" {
		t.Fatalf("wrong metadata: %+v", st)
	}
	var first string
	if err := conn.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='stat:firstRunAt'`).Scan(&first); err != nil || first != "2026-09-11T00:00:00Z" {
		t.Fatalf("activation changed: %q, %v", first, err)
	}
}

func TestEvidenceStatsRequiredReadFailuresNeverReturnPartialZero(t *testing.T) {
	for _, ddl := range []string{
		"DROP INDEX observations_pending", "DROP INDEX upload_queue_pending",
		"DROP TABLE refused_evidence", "DROP TABLE meta",
	} {
		t.Run(ddl, func(t *testing.T) {
			db, path := evidenceStatsFixture(t)
			if _, err := db.sql.Exec(ddl); err != nil {
				t.Fatal(err)
			}
			if st, err := ReadEvidenceStats(context.Background(), path); err == nil || st != nil {
				t.Fatalf("failed read became successful/partial stats: %+v, %v", st, err)
			}
			var n int
			if err := db.sql.QueryRow("SELECT COUNT(*) FROM observations WHERE uploaded=0").Scan(&n); err != nil || n != 1 {
				t.Fatalf("pending evidence was changed: %d, %v", n, err)
			}
		})
	}
}

func TestEvidenceStatsMissingLegacyAndCorruptStoresAreNotInitialized(t *testing.T) {
	for _, kind := range []string{"missing", "legacy", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "missing-parent", "csx.db")
			if kind != "missing" {
				path = filepath.Join(dir, "csx.db")
				if kind == "corrupt" {
					if err := os.WriteFile(path, []byte("CORRUPT_PRIVATE_CANARY"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					sdb, err := sql.Open("sqlite", path)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := sdb.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
						INSERT INTO meta VALUES('schema_version','1')`); err != nil {
						t.Fatal(err)
					}
					sdb.Close()
				}
			}
			before, _ := os.ReadFile(path)
			if st, err := ReadEvidenceStats(context.Background(), path); err == nil || st != nil {
				t.Fatalf("unsupported store became stats: %+v, %v", st, err)
			}
			after, _ := os.ReadFile(path)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("read-only diagnostic changed database bytes")
			}
			if kind == "missing" {
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatalf("created missing profile directory: %v", err)
				}
			}
		})
	}
}

func TestEvidenceStatsClosedWALStoreKeepsDataAndOnlyAllowsSQLiteSidecars(t *testing.T) {
	db, path := evidenceStatsFixture(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := ReadEvidenceStats(context.Background(), path); err != nil || st.QueueDepth != 2 {
		t.Fatalf("closed WAL read: %+v, %v", st, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only diagnostic changed database bytes: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || (entry.Name() != "csx.db" && entry.Name() != "csx.db-wal" && entry.Name() != "csx.db-shm") {
			t.Fatalf("unexpected diagnostic-created path: %s", entry.Name())
		}
	}
}

func TestEvidenceStatsEmptyQueueAndMissingMetadataAreMeasuredNormally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "csx.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st, err := ReadEvidenceStats(context.Background(), path)
	if err != nil || st == nil || st.QueueDepth != 0 || st.Queue.EvidenceBatches != 0 || st.Queue.Uploads != 0 ||
		st.LastUpload != "" || st.LastUploadAttempt != "" || st.LastUploadError != "" {
		t.Fatalf("empty initialized store: %+v, %v", st, err)
	}
}

func TestEvidenceStatsMetadataValidationAndPrivateReasonProjection(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"lastUpload", "not a timestamp"}, {"lastUploadAttempt", "2026-99-99T00:00:00Z"},
		{"lastUploadError", strings.Repeat("x", 513)}, {"lastUploadError", "bad\xffutf8"},
		{"lastUploadError", "evidence: the server refused zero batches, first: private"},
		{"lastUploadError", "evidence: the server refused 2 batch: private"},
		{"lastUploadError", "evidence: the server refused 999999999999999 batches, first: private"},
	} {
		t.Run(tc.key+fmt.Sprint(len(tc.value)), func(t *testing.T) {
			db, path := evidenceStatsFixture(t)
			if err := db.SetStat(context.Background(), tc.key, tc.value); err != nil {
				t.Fatal(err)
			}
			if st, err := ReadEvidenceStats(context.Background(), path); err == nil || st != nil {
				t.Fatalf("malformed metadata became stats: %+v, %v", st, err)
			}
		})
	}
	// Review blocker 5184582285: SQLite TEXT substr/length stop at embedded
	// NUL, so the byte-level validation must inspect complete bounded bytes
	// and reject NUL/malformed/truncated values fail-closed. These three
	// controls inject NUL bytes via CAST(? AS TEXT) to bypass Go driver
	// NUL-truncation and prove the SQL+Go validation rejects them together.
	for _, tc := range []struct {
		name, key string
		raw       []byte
	}{
		// Timestamp with NUL suffix: TEXT length() returns 20, hiding the
		// appended payload that BLOB length reveals.
		{"timestamp+NUL_suffix", "lastUpload",
			append([]byte("2026-09-12T00:01:00Z\x00hidden-payload"), []byte{}...)},
		// NUL-leading error: TEXT length() returns 0, hiding the entire body.
		{"NUL-leading_error", "lastUploadError",
			append([]byte("\x00evidence: the server refused 1 batch: private"), []byte{}...)},
		// >512-rune error with embedded NUL: TEXT length() returns 256
		// (stops at NUL), hiding 257 more runes. Total 514 runes but
		// TEXT says 256; without the BLOB cross-check the size gate passes.
		{">512-rune_error_embedded_NUL", "lastUploadError",
			append(append([]byte(strings.Repeat("A", 256)), 0), []byte(strings.Repeat("B", 257))...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, path := evidenceStatsFixture(t)
			// Insert via CAST(? AS TEXT) so the NUL bytes survive into the
			// TEXT column; the Go sqlite driver may truncate string args at NUL.
			if _, err := db.sql.ExecContext(context.Background(),
				`INSERT INTO meta(key, value) VALUES(?, CAST(? AS TEXT))
				ON CONFLICT(key) DO UPDATE SET value = CAST(excluded.value AS TEXT)`,
				"stat:"+tc.key, tc.raw); err != nil {
				t.Fatal(err)
			}
			if st, err := ReadEvidenceStats(context.Background(), path); err == nil || st != nil {
				t.Fatalf("NUL-embedded metadata became stats: %+v, %v", st, err)
			}
		})
	}
	for raw, want := range map[string]string{
		"evidence: the server refused 1 batch: CANARY_PRIVATE":           "evidence: the server refused 1 batch",
		"evidence: the server refused 23 batches, first: CANARY_PRIVATE": "evidence: the server refused 23 batches",
		"network error /CANARY_PRIVATE/path?token=secret":                "upload failed", "": "",
	} {
		got, err := publicEvidenceUploadError(raw)
		if err != nil || got != want {
			t.Fatalf("projection = %q, %v; want %q", got, err, want)
		}
	}
}

func TestEvidenceStatsUsesReadonlyEscapedURIAndContext(t *testing.T) {
	name := "profile space#mark"
	if runtime.GOOS != "windows" {
		name += "?mode=rwc"
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "csx.db")
	// Setup uses the same escaped URI as production with only its mode
	// changed; this fixture must not inherit Open's unescaped-path defect.
	dsn, err := evidenceStatsDSN(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Query().Get("mode") != "ro" || u.Fragment != "" {
		t.Fatalf("not a safe file URI: %q, %v", dsn, err)
	}
	q := u.Query()
	q.Set("mode", "rwc")
	q.Del("_pragma")
	u.RawQuery = q.Encode()
	sdb, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	db := &DB{sql: sdb}
	if err := db.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	if st, err := ReadEvidenceStats(context.Background(), path); err != nil || st.QueueDepth != 0 {
		t.Fatalf("escaped path failed: %+v, %v", st, err)
	}
	ro, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Exec("INSERT INTO meta VALUES('forbidden','write')"); err == nil {
		t.Fatal("read-only connection accepted a data write")
	}
	var queryOnly, busy int
	if err := ro.QueryRow("PRAGMA query_only").Scan(&queryOnly); err != nil || queryOnly != 1 {
		t.Fatalf("query_only=%d, %v", queryOnly, err)
	}
	if err := ro.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 250 {
		t.Fatalf("busy_timeout=%d, %v", busy, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if st, err := ReadEvidenceStats(ctx, path); err == nil || st != nil {
		t.Fatalf("canceled caller became stats: %+v, %v", st, err)
	}
}

func TestEvidenceStatsPreservesExistingPerSourceCaps(t *testing.T) {
	db, path := evidenceStatsFixture(t)
	if _, err := db.sql.Exec(`WITH RECURSIVE n(v) AS (VALUES(1) UNION ALL SELECT v+1 FROM n WHERE v<1005)
		INSERT INTO upload_queue(kind,payload,created_at,attempts,last_error)
		SELECT 'receipt','private','2026-09-12T00:00:00Z',0,'' FROM n`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.Exec(`WITH RECURSIVE n(v) AS (VALUES(1) UNION ALL SELECT v+1 FROM n WHERE v<1005)
		INSERT INTO observations(epoch,purl,symbol,env_hash,stage,result,count,error_fp,uploaded)
		SELECT '2026-09-12','pkg:npm/pending@1','symbol'||v,'env','TEST','PASS',1,'',0 FROM n`); err != nil {
		t.Fatal(err)
	}
	st, err := ReadEvidenceStats(context.Background(), path)
	if err != nil || st.QueueDepth != 2000 || st.Queue.EvidenceBatches != 1000 || st.Queue.Uploads != 1000 {
		t.Fatalf("caps changed: %+v, %v", st, err)
	}
}

func TestEvidenceStatsExclusiveLockReturnsBoundedUnavailable(t *testing.T) {
	db, path := evidenceStatsFixture(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	conn, err := raw.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "PRAGMA journal_mode=DELETE"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), "BEGIN EXCLUSIVE"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	start := time.Now()
	st, err := ReadEvidenceStats(context.Background(), path)
	if err == nil || st != nil {
		t.Fatalf("locked store became stats: %+v, %v", st, err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("short busy timeout was not applied: %s", elapsed)
	}
}
