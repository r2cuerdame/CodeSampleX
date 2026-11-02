package localdb

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPendingUploadOldestIsReadOnlyAndUsesExistingTimestamps(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, _ := db.Enqueue(t.Context(), "adoption", "private-report-a")
	b, _ := db.Enqueue(t.Context(), "receipt", "private-report-b")
	old := time.Now().UTC().Add(-3 * time.Hour)
	recent := old.Add(time.Hour)
	if _, err := db.sql.ExecContext(t.Context(), "UPDATE upload_queue SET created_at=? WHERE id=?", recent.Format(time.RFC3339Nano), a); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(t.Context(), "UPDATE upload_queue SET created_at=? WHERE id=?", old.Format(time.RFC3339Nano), b); err != nil {
		t.Fatal(err)
	}
	before, _ := db.QueuePending(t.Context(), 1000)
	when, n, valid, err := db.PendingUploadOldest(t.Context(), 1000)
	after, _ := db.QueuePending(t.Context(), 1000)
	if err != nil || n != 2 || !valid || !when.Equal(old) {
		t.Fatalf("oldest=%v n=%d valid=%v err=%v", when, n, valid, err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("age inspection changed queue")
	}
}
func TestPendingUploadOldestMissingTimeStaysUnknownAndExhaustedRowsStayExcluded(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, _ := db.Enqueue(t.Context(), "adoption", "private-a")
	b, _ := db.Enqueue(t.Context(), "receipt", "private-b")
	db.sql.ExecContext(t.Context(), "UPDATE upload_queue SET created_at=NULL WHERE id=?", a)
	db.sql.ExecContext(t.Context(), "UPDATE upload_queue SET attempts=8 WHERE id=?", b)
	_, n, valid, err := db.PendingUploadOldest(t.Context(), 1000)
	if err != nil || n != 1 || valid {
		t.Fatalf("unknown timestamp repaired/exhausted counted n=%d valid=%v err=%v", n, valid, err)
	}
}

func TestDiagnosticMetadataContextDeadlineBoundsWriterWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "csx.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	reader.sql.SetMaxOpenConns(1)
	var original int
	if err := reader.sql.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&original); err != nil {
		t.Fatal(err)
	}
	tx, err := writer.sql.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(t.Context(), "INSERT INTO meta(key,value) VALUES('diag-lock','private')"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	start := time.Now()
	go func() {
		done <- reader.SetDeliveryDiagnostics(ctx, map[string]string{StatEvidenceAttemptDiagnostic: "safe"})
	}()
	select {
	case err := <-done:
		if err == nil || time.Since(start) > time.Second {
			t.Fatal("writer wait ignored deadline")
		}
	case <-time.After(250 * time.Millisecond):
		tx.Rollback()
		<-done
		t.Fatal("writer wait exceeded context budget")
	}
	var restored int
	if err := reader.sql.QueryRowContext(t.Context(), "PRAGMA busy_timeout").Scan(&restored); err != nil || restored != original {
		t.Fatalf("normal policy changed before=%d after=%d err=%v", original, restored, err)
	}
}

func TestDeliveryDiagnosticStorageHasFixedKeysAndBounds(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "csx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 0; i < 30; i++ {
		if err := db.SetDeliveryDiagnostics(t.Context(), map[string]string{StatEvidenceAttemptDiagnostic: "attempt", StatEvidenceACKDiagnostic: "ack"}); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := db.sql.QueryRowContext(t.Context(), "SELECT count(*) FROM meta WHERE key IN (?,?)", statPrefix+StatEvidenceAttemptDiagnostic, statPrefix+StatEvidenceACKDiagnostic).Scan(&n); err != nil || n != 2 {
		t.Fatalf("unbounded receipt rows n=%d err=%v", n, err)
	}
	if err := db.SetDeliveryDiagnostics(t.Context(), map[string]string{"lastUpload": "private"}); err == nil {
		t.Fatal("diagnostic writer accepted another meta key")
	}
	if err := db.SetDeliveryDiagnostics(t.Context(), map[string]string{StatEvidenceACKDiagnostic: strings.Repeat("x", 513)}); err == nil {
		t.Fatal("oversize diagnostic accepted")
	}
}
