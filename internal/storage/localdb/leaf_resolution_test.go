package localdb

import (
	"path/filepath"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestLeafResolutionMigrationAndLatestObservation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "csx.db")
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	key := ObsKey{Epoch: "2026-09-13", PURL: "pkg:npm/hono@4.13.5", EnvHash: "env", Stage: domain.StageUsed, Result: domain.ResultPass}
	if err := db.RecordObservation(t.Context(), key, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.sql.ExecContext(t.Context(), `ALTER TABLE observations DROP COLUMN depends_on_none`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check := func(want bool, count int) {
		t.Helper()
		rows, err := db.PendingObservations(t.Context(), 10)
		if err != nil || len(rows) != 1 || rows[0].DependsOnNone != want || rows[0].Count != count {
			t.Fatalf("rows=%+v err=%v", rows, err)
		}
	}
	check(false, 1) // an old absence never becomes proof on upgrade
	key.DependsOnNone = true
	if err := db.RecordObservation(t.Context(), key, 1); err != nil {
		t.Fatal(err)
	}
	check(true, 2)
	key.DependsOn = []string{"pkg:npm/child@1.0.0"}
	if err := db.RecordObservation(t.Context(), key, 1); err != nil {
		t.Fatal(err)
	}
	check(false, 3) // never upload contradictory edge and empty declarations
	key.DependsOn, key.DependsOnNone = nil, false
	if err := db.RecordObservation(t.Context(), key, 1); err != nil {
		t.Fatal(err)
	}
	check(false, 4)
}
