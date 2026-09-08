package serverstore

import (
	"context"
	"github.com/jackc/pgx/v5"
	"reflect"
	"testing"
	"time"
)

func TestIntegrationBuilderScopeMigrationBackfillsAndRollsBackWithoutSourceChanges(t *testing.T) {
	pg := openTestPG(t)
	ctx := context.Background()
	// Recreate the previous schema in this disposable namespace, then add data
	// written by the previous binary before the new indexes exist.
	drop := func() {
		t.Helper()
		err := pg.withConn(ctx, func(c *pgx.Conn) error {
			for _, name := range []string{"builder_samples_packages_idx", "builder_receipts_packages_idx", "builder_samples_subject_idx", "builder_samples_symbols_idx", "builder_evidence_coord_idx", "builder_snapshots_coord_idx", "builder_samples_unsafe_keys_idx", "builder_receipts_unsafe_keys_idx", "builder_evidence_changed_idx"} {
				if _, err := c.Exec(ctx, "DROP INDEX "+name); err != nil {
					return err
				}
			}
			for _, name := range []string{"csx_builder_unsafe_keys(jsonb)", "csx_builder_coords(jsonb)", "csx_builder_coord(text)"} {
				if _, err := c.Exec(ctx, "DROP FUNCTION "+name); err != nil {
					return err
				}
			}
			_, err := c.Exec(ctx, "DELETE FROM schema_migrations WHERE version='0036_builder_scope_indexes.sql'")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sourceCounts := func() []int64 {
		var counts []int64
		err := pg.withConn(ctx, func(c *pgx.Conn) error {
			for _, table := range []string{"samples", "receipts", "evidence_agg", "compatibility_snapshots"} {
				var n int64
				if err := c.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
					return err
				}
				counts = append(counts, n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return counts
	}
	drop()
	seedBuilderIrrelevantCorpus(t, pg, 1, 1000)
	want, err := pg.ListSnapshotTargets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	counts := sourceCounts()
	for cycle := 0; cycle < 2; cycle++ {
		started := time.Now()
		if err := pg.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("migration cycle=%d existing samples/receipts/evidence/snapshots=%v elapsed=%s", cycle, counts, time.Since(started))
		got, err := pg.ListSnapshotTargetsForPackages(ctx, []string{"pkg:npm/irrelevant-999@"})
		if err != nil {
			t.Fatal(err)
		}
		var expected []SnapshotTarget
		for _, target := range want {
			if builderCoord(target.PURL) == "pkg:npm/irrelevant-999@" {
				expected = append(expected, target)
			}
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("historical index backfill got=%v want=%v", got, expected)
		}
		if after := sourceCounts(); !reflect.DeepEqual(after, counts) {
			t.Fatalf("source counts changed %v -> %v", counts, after)
		}
		if cycle == 0 {
			drop()
			rolledBack, err := pg.ListSnapshotTargets(ctx)
			if err != nil || !reflect.DeepEqual(rolledBack, want) {
				t.Fatalf("old binary read changed after schema rollback: %v", err)
			}
		}
	}
}
