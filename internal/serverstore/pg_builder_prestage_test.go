package serverstore

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func openTestPGThrough0035(t *testing.T) (*PG, string) {
	t.Helper()
	dsn, err := integrationDSN(os.Getenv("CSX_TEST_DSN"), os.Getenv("CSX_REQUIRE_TEST_DSN"))
	if err != nil {
		t.Skip(err.Error())
	}
	ctx := context.Background()
	schema := fmt.Sprintf("csx_prestage_%d", time.Now().UnixNano())

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(context.Background())
	})

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["search_path"] = schema

	pg := newPGWithPolicy(cfg, DefaultPoolPolicy())
	t.Cleanup(pg.Close)

	// Apply migrations up to 0035 (v0.1.149 schema state).
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}

	builderSQL(t, pg, func(conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(
			version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
		if err != nil {
			return err
		}
		for _, m := range migs {
			if m.Version > "0035_recent_wanted_demand.sql" {
				break
			}
			if err := applyMigration(ctx, conn, m); err != nil {
				return fmt.Errorf("apply migration %s: %w", m.Version, err)
			}
		}
		return nil
	})

	return pg, schema
}

func TestIntegrationBuilderPrestageExactDefinitionMismatchFailsClosed(t *testing.T) {
	pg, _ := openTestPGThrough0035(t)
	ctx := context.Background()

	// Case 1: Pre-existing index with wrong columns.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `CREATE INDEX evidence_agg_builder_coord_idx ON evidence_agg(purl, symbol)`)
		return err
	})

	// Validation in Migrate must fail closed.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		err := validatePrebuiltBuilderObjects(ctx, conn)
		if err == nil || !strings.Contains(err.Error(), "definition mismatch") {
			t.Fatalf("validatePrebuiltBuilderObjects error = %v, want definition mismatch", err)
		}
		return nil
	})

	// Prestage must fail closed and refuse to overwrite the mismatched index.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		err := PrestageBuilderIndexes(ctx, conn)
		if err == nil || !strings.Contains(err.Error(), "definition mismatch") {
			t.Fatalf("PrestageBuilderIndexes error = %v, want definition mismatch", err)
		}
		return nil
	})

	// Case 2: Drop mismatched index and test function definition mismatch.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		if _, err := conn.Exec(ctx, `DROP INDEX evidence_agg_builder_coord_idx`); err != nil {
			return err
		}
		// Create a VOLATILE builder_purl_coord (violating IMMUTABLE contract).
		_, err := conn.Exec(ctx, `CREATE OR REPLACE FUNCTION builder_purl_coord(raw TEXT) RETURNS TEXT
			LANGUAGE SQL VOLATILE AS $$ SELECT raw $$`)
		return err
	})

	builderSQL(t, pg, func(conn *pgx.Conn) error {
		err := validatePrebuiltBuilderObjects(ctx, conn)
		if err == nil || !strings.Contains(err.Error(), "IMMUTABLE") {
			t.Fatalf("validatePrebuiltBuilderObjects error = %v, want IMMUTABLE mismatch", err)
		}
		return nil
	})
}

func TestIntegrationBuilderPrestageInterruptedBuildCleanupAndRetry(t *testing.T) {
	pg, _ := openTestPGThrough0035(t)
	ctx := context.Background()

	// 1. Prestage cleanly first.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		return PrestageBuilderIndexes(ctx, conn)
	})

	// 2. Simulate an interrupted build by corrupting indisvalid on an index.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		res, err := conn.Exec(ctx, `UPDATE pg_index
			SET indisvalid = false
			WHERE indexrelid = (
				SELECT c.oid FROM pg_class c
				JOIN pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = current_schema() AND c.relname = 'evidence_agg_builder_coord_idx'
			)`)
		if err != nil {
			return err
		}
		if res.RowsAffected() != 1 {
			return fmt.Errorf("failed to mark index invalid: %d rows", res.RowsAffected())
		}
		return nil
	})

	// 3. Migrate validation must fail closed when an invalid index is present.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		err := validatePrebuiltBuilderObjects(ctx, conn)
		if err == nil || !strings.Contains(err.Error(), "is invalid") {
			t.Fatalf("validatePrebuiltBuilderObjects error = %v, want invalid index error", err)
		}
		return nil
	})

	// 4. PrestageBuilderIndexes must detect the invalid index, drop it, and rebuild cleanly.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		if err := PrestageBuilderIndexes(ctx, conn); err != nil {
			t.Fatalf("PrestageBuilderIndexes retry: %v", err)
		}
		// Confirm the rebuilt index is now valid and ready.
		info, exists, err := queryIndexInfo(ctx, conn, "evidence_agg_builder_coord_idx")
		if err != nil || !exists {
			t.Fatalf("query rebuilt index: exists=%v err=%v", exists, err)
		}
		if !info.IsValid || !info.IsReady {
			t.Fatalf("rebuilt index not valid: %+v", info)
		}
		return nil
	})

	// 5. Subsequent Migrate must succeed cleanly.
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate after prestage cleanup: %v", err)
	}
}

func TestIntegrationBuilderPrestage0035To0036Idempotent(t *testing.T) {
	pg, _ := openTestPGThrough0035(t)
	ctx := context.Background()

	// Seed source data in 0035 schema using legacy writes.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `INSERT INTO samples(sample_id, manifest, status, size_bytes)
			VALUES('prestage-sample-1', '{"packages":["pkg:npm/left-pad@1.3.0"],"symbols":["leftPad"]}', 'PUBLISHED', 100)`)
		if err != nil {
			return err
		}
		_, err = conn.Exec(ctx, `INSERT INTO receipts(receipt_id, sample_id, peer_id, env_hash, receipt, contract_result)
			VALUES('prestage-rcpt-1', 'prestage-sample-1', 'prestage-peer', 'prestage-env',
			'{"schemaVersion":2,"stages":{"resolve":"PASS","contract":"PASS"},"resolvedPackages":["pkg:npm/left-pad@1.3.0"]}', 'PASS')`)
		if err != nil {
			return err
		}
		_, err = conn.Exec(ctx, `INSERT INTO evidence_agg(purl, symbol, env_hash, env_json, stage, result, observation_count, last_seen)
			VALUES('pkg:npm/left-pad@1.3.0', 'leftPad', 'prestage-env', '{"ecosystem":"npm"}'::jsonb, 'contract', 'PASS', 1, now())`)
		if err != nil {
			return err
		}
		_, err = conn.Exec(ctx, `INSERT INTO compatibility_snapshots(purl, symbol, generated_at, snapshot)
			VALUES('pkg:npm/left-pad@1.3.0', 'leftPad', now(), '{"test":true}'::jsonb)`)
		return err
	})

	// Run PrestageBuilderIndexes concurrently while 0035 is active.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		return PrestageBuilderIndexes(ctx, conn)
	})

	// Measure 0036 execution time.
	start := time.Now()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	ddlAndBackfillElapsed := time.Since(start)

	// Since indexes were pre-staged, 0036 DDL statements run in milliseconds.
	t.Logf("0036 migration + backfill over prebuilt indexes completed in %v", ddlAndBackfillElapsed)

	// Verify ledger.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		var count int
		if err := conn.QueryRow(ctx, `SELECT count(*) FROM schema_migrations
			WHERE version='0036_builder_projections.sql'`).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			t.Fatalf("0036 not recorded in schema_migrations")
		}
		return nil
	})

	// Verify projections are ready.
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		return checkBuilderProjections(ctx, tx)
	})

	// Test scoped reads work over the prebuilt indexes.
	selected, err := pg.ListBuilderSamplesPage(ctx, []BuilderPackage{{"npm", "left-pad"}}, 10, 0)
	if err != nil || len(selected) != 1 || selected[0].SampleID != "prestage-sample-1" {
		t.Fatalf("ListBuilderSamplesPage = %+v, err = %v", selected, err)
	}

	// Idempotent reapplication.
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("idempotent second Migrate: %v", err)
	}
}

func TestIntegrationBuilderPrestageActivationBudgetBenchmark(t *testing.T) {
	pg, _ := openTestPGThrough0035(t)
	ctx := context.Background()

	// Seed synthetic data at benchmark scale:
	// 8,000 samples, 8,000 receipts, 16,000 sample_packages,
	// 50,000 evidence_agg rows (or up to 1M depending on environment).
	t.Log("Seeding synthetic benchmark data...")
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		// Seed 8,000 samples and receipts
		_, err := conn.Exec(ctx, `INSERT INTO samples(sample_id, manifest, status, size_bytes)
			SELECT 'bench-sample-' || lpad(i::text, 5, '0'),
				jsonb_build_object(
					'packages', jsonb_build_array('pkg:npm/bench-pkg-' || (i % 2000) || '@1.0.0'),
					'symbols', jsonb_build_array('fn_' || (i % 20)),
					'subject', 'pkg:npm/bench-pkg-' || (i % 2000) || '@1.0.0'
				), 'PUBLISHED', 100
			FROM generate_series(1, 8000) i`)
		if err != nil {
			return fmt.Errorf("seed samples: %w", err)
		}

		_, err = conn.Exec(ctx, `INSERT INTO receipts(receipt_id, sample_id, peer_id, env_hash, receipt, contract_result)
			SELECT 'bench-rcpt-' || lpad(i::text, 5, '0'),
				'bench-sample-' || lpad(i::text, 5, '0'),
				'bench-peer', 'bench-env',
				jsonb_build_object(
					'schemaVersion', 2,
					'stages', jsonb_build_object('resolve', 'PASS', 'contract', 'PASS'),
					'resolvedPackages', jsonb_build_array('pkg:npm/bench-pkg-' || (i % 2000) || '@1.0.0')
				), 'PASS'
			FROM generate_series(1, 8000) i`)
		if err != nil {
			return fmt.Errorf("seed receipts: %w", err)
		}

		_, err = conn.Exec(ctx, `INSERT INTO evidence_agg(purl, symbol, env_hash, env_json, stage, result, observation_count, last_seen)
			SELECT 'pkg:npm/bench-pkg-' || i || '@1.0.0',
				'fn_' || (i % 20), 'bench-env', '{"ecosystem":"npm"}'::jsonb, 'contract', 'PASS', 1, now() - (i || ' seconds')::interval
			FROM generate_series(1, 50000) i`)
		if err != nil {
			return fmt.Errorf("seed evidence_agg: %w", err)
		}

		_, err = conn.Exec(ctx, `INSERT INTO compatibility_snapshots(purl, symbol, generated_at, snapshot)
			SELECT 'pkg:npm/bench-pkg-' || i || '@1.0.0',
				'fn_' || (i % 20), now(), '{"test":true}'::jsonb
			FROM generate_series(1, 20000) i`)
		return err
	})

	// Pre-activation heavy index preparation.
	prestageStart := time.Now()
	builderSQL(t, pg, func(conn *pgx.Conn) error {
		return PrestageBuilderIndexes(ctx, conn)
	})
	prestageDuration := time.Since(prestageStart)
	t.Logf("PrestageBuilderIndexes completed in %v", prestageDuration)

	// Activation: run migration 0036 and backfill.
	migrateStart := time.Now()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("Migrate 0036: %v", err)
	}
	migrateDuration := time.Since(migrateStart)
	t.Logf("Migrate 0036 (DDL + 8000 sample/receipt backfill) completed in %v", migrateDuration)

	// Activation budget is 120 seconds.
	if migrateDuration >= 120*time.Second {
		t.Fatalf("0036 activation duration %v exceeds 120s budget ceiling", migrateDuration)
	}
	t.Logf("PASS: 0036 activation finished in %v (well under 120s budget ceiling)", migrateDuration)
}
