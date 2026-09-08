package serverstore

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const builderMigrationSourceStateSQL = `
	SELECT jsonb_build_object(
		'samples', (SELECT jsonb_agg(to_jsonb(s)-ARRAY[
			'builder_coords','builder_purls','builder_symbols','builder_subject',
			'builder_source_hash','builder_previous_purls','builder_previous_symbols',
			'updated_at'] ORDER BY sample_id) FROM samples s),
		'receipts', (SELECT jsonb_agg(to_jsonb(r)-ARRAY[
			'builder_packages','builder_coords','builder_claim','builder_source_hash']
			ORDER BY receipt_id) FROM receipts r),
		'snapshots', (SELECT jsonb_agg(to_jsonb(s) ORDER BY purl,symbol) FROM compatibility_snapshots s),
		'shards', (SELECT jsonb_agg(to_jsonb(s) ORDER BY key) FROM shards s)
	)::text`

func builderMigrationSourceState(t *testing.T, pg *PG) string {
	t.Helper()
	var state string
	builderSQL(t, pg, func(c *pgx.Conn) error {
		return c.QueryRow(context.Background(), builderMigrationSourceStateSQL).Scan(&state)
	})
	return state
}

// Reversal is an operator action after restoring the old binary. Exercise the
// exact additive-object removal in a disposable schema; raw source JSON and
// materialized documents must survive rollback and a subsequent reapplication.
// updated_at alone is excluded because backfill deliberately marks old sources
// dirty for the next compatibility pass.
func TestIntegrationBuilderMigrationReversalAndReapplyPreservesSources(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	builderFixtureSample(t, pg, "original", []string{"pkg:npm/selected@1.0.0"}, []string{"selected.call"}, "")
	builderFixtureReceipt(t, pg, "original-receipt", "original", []string{"pkg:npm/selected@1.0.0"})
	if err := pg.PutSnapshot(ctx, "pkg:npm/selected@1.0.0", "selected.call", `{"original":true}`); err != nil {
		t.Fatal(err)
	}
	if err := pg.PutShard(ctx, "npm/selected/1", "original-etag", `{"original":true}`); err != nil {
		t.Fatal(err)
	}
	before := builderMigrationSourceState(t, pg)
	builderSQL(t, pg, func(c *pgx.Conn) error {
		tx, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		for _, statement := range []string{
			`DROP FUNCTION builder_purl_coord(text) CASCADE`,
			`DROP INDEX evidence_agg_builder_changed_idx`,
			`DROP INDEX samples_builder_created_idx`,
			`ALTER TABLE samples
				DROP COLUMN builder_coords CASCADE, DROP COLUMN builder_purls CASCADE,
				DROP COLUMN builder_symbols CASCADE, DROP COLUMN builder_subject CASCADE,
				DROP COLUMN builder_source_hash CASCADE, DROP COLUMN builder_previous_purls CASCADE,
				DROP COLUMN builder_previous_symbols CASCADE`,
			`ALTER TABLE receipts
				DROP COLUMN builder_packages CASCADE, DROP COLUMN builder_coords CASCADE,
				DROP COLUMN builder_claim CASCADE, DROP COLUMN builder_source_hash CASCADE`,
			`DELETE FROM schema_migrations WHERE version='0036_builder_projections.sql'`,
		} {
			if _, err := tx.Exec(ctx, statement); err != nil {
				return err
			}
		}
		return tx.Commit(ctx)
	})
	if got := builderMigrationSourceState(t, pg); got != before {
		t.Fatalf("rollback changed source/materialized rows: before=%s after=%s", before, got)
	}
	builderSQL(t, pg, func(c *pgx.Conn) error {
		var objects int
		if err := c.QueryRow(ctx, `SELECT
			(SELECT count(*) FROM information_schema.columns
				WHERE table_schema=current_schema() AND column_name LIKE 'builder_%')+
			(SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname LIKE '%builder%')+
			(SELECT count(*) FROM schema_migrations WHERE version='0036_builder_projections.sql')`).Scan(&objects); err != nil {
			return err
		}
		if objects != 0 {
			t.Fatalf("rollback left %d projection objects", objects)
		}
		// Simulate the restored old binary writing ordinary legacy rows.
		if _, err := c.Exec(ctx, `INSERT INTO samples(sample_id,manifest,status,size_bytes)
			VALUES('rollback-written','{"packages":["pkg:npm/harness@1.0.0"],"symbols":["rollback.call"]}','CROSS_PASS',0)`); err != nil {
			return err
		}
		_, err := c.Exec(ctx, `INSERT INTO receipts(receipt_id,sample_id,peer_id,env_hash,receipt,contract_result)
			VALUES('rollback-receipt','rollback-written','peer','env',
			'{"schemaVersion":2,"stages":{"resolve":"PASS"},"resolvedPackages":["pkg:npm/undeclared@2.0.0"]}','PASS')`)
		return err
	})
	withRollbackWrites := builderMigrationSourceState(t, pg)
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("reapply/backfill: %v", err)
	}
	if got := builderMigrationSourceState(t, pg); got != withRollbackWrites {
		t.Fatalf("reapplication changed retained source/materialized rows: before=%s after=%s", withRollbackWrites, got)
	}
	selected, err := pg.ListBuilderSamplesPage(ctx, []BuilderPackage{{"npm", "undeclared"}}, 100, 0)
	if err != nil || len(selected) != 1 || selected[0].SampleID != "rollback-written" {
		t.Fatalf("old-binary receipt did not regain scoped attribution: rows=%+v err=%v", selected, err)
	}
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("idempotent reapplication: %v", err)
	}
	t.Log("PASS: additive objects reversed; raw sources/materialized documents retained; old-binary writes backfilled on reapply")
}

func TestIntegrationBuilderBackfillCancellationResumesCompletedPages(t *testing.T) {
	pg, ctx := openBuilderReadPG(t), context.Background()
	builderSQL(t, pg, func(c *pgx.Conn) error {
		_, err := c.Exec(ctx, `INSERT INTO samples(sample_id,manifest,size_bytes)
			SELECT 'resume-'||lpad(i::text,3,'0'),
				jsonb_build_object('packages',jsonb_build_array('pkg:npm/resume-'||i||'@1.0.0')),0
			FROM generate_series(0,$1::int) i`, builderProjectionBatch)
		return err
	})
	builderSQL(t, pg, func(c *pgx.Conn) error {
		count, err := backfillBuilderProjectionPage(ctx, c, true)
		if err == nil && count != builderProjectionBatch {
			return fmt.Errorf("first committed page=%d", count)
		}
		return err
	})
	// Hold the remaining source row from another connection. Cancellation
	// must unwind the waiting backfill transaction without losing the already
	// committed page, and a later call must resume that one remaining row.
	builderSQL(t, pg, func(c *pgx.Conn) error {
		lock, err := c.Begin(ctx)
		if err != nil {
			return err
		}
		defer lock.Rollback(ctx)
		if _, err := lock.Exec(ctx, "SELECT sample_id FROM samples WHERE sample_id=$1 FOR UPDATE",
			fmt.Sprintf("resume-%03d", builderProjectionBatch)); err != nil {
			return err
		}
		timed, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		err = pg.withConn(timed, func(other *pgx.Conn) error { return backfillBuilderProjections(timed, other) })
		if !errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("blocked backfill cancellation=%w", err)
		}
		return nil
	})
	builderSQL(t, pg, func(c *pgx.Conn) error {
		var completed, pending int
		if err := c.QueryRow(ctx, `SELECT count(*) FILTER (WHERE builder_source_hash IS NOT NULL),
			count(*) FILTER (WHERE builder_source_hash IS NULL) FROM samples`).Scan(&completed, &pending); err != nil {
			return err
		}
		if completed != builderProjectionBatch || pending != 1 {
			return fmt.Errorf("cancelled backfill lost page boundary: completed=%d pending=%d", completed, pending)
		}
		if err := backfillBuilderProjections(ctx, c); err != nil {
			return err
		}
		if err := c.QueryRow(ctx, `SELECT count(*) FROM samples
			WHERE builder_source_hash IS DISTINCT FROM md5(manifest::text)`).Scan(&pending); err != nil {
			return err
		}
		if pending != 0 {
			return fmt.Errorf("resumed backfill left %d stale rows", pending)
		}
		return nil
	})
	selected, err := pg.ListBuilderSamplesPage(ctx, []BuilderPackage{{"npm", fmt.Sprintf("resume-%d", builderProjectionBatch)}}, 10, 0)
	if err != nil || len(selected) != 1 {
		t.Fatalf("resumed projection not ready: rows=%+v err=%v", selected, err)
	}
	t.Logf("PASS: %d-row committed page retained after blocked cancellation; remaining row repaired on resume", builderProjectionBatch)
}
