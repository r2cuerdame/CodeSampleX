package serverstore

// Two migrators against one database is this suite's normal shape, not an
// exotic case: every PostgreSQL test here isolates itself in a fresh schema of
// the shared CSX_TEST_DSN database, and `go test ./...` runs packages in
// parallel, so internal/serverstore and internal/compatibility migrate at the
// same time. Two of the last three CI Test runs (2026-09-09, PR #273 run
// 34341129526 and main run 34342608645 on 399c487d) died in
// internal/compatibility with
//
//	migration 0034_samples_manifest_trgm_idx.sql failed on "CREATE EXTENSION
//	IF NOT EXISTS pg_trgm WITH SCHEMA public": duplicate key value violates
//	unique constraint "pg_extension_name_index" (SQLSTATE 23505)
//
// pg_extension is per database, not per schema, so one migrator's IF NOT
// EXISTS cannot see the other's uncommitted extension row: the loser waits on
// the unique index and is handed 23505 the moment the winner commits.
//
// The race needs a database where pg_trgm is not installed yet, which is why
// it is invisible on a warm laptop and shows up on a cold CI runner — after
// the first run the shared test database has the extension and every later
// migrator takes the IF NOT EXISTS path. These tests therefore build their own
// throwaway database rather than dropping the extension out from under the
// sibling packages migrating concurrently in the shared one.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const trgmMigration = "0034_samples_manifest_trgm_idx.sql"

// migrationRaceDatabase creates a throwaway database on the configured test
// server and returns the base DSN plus its name. It is dropped with FORCE on
// cleanup, so a migrator left connected by a failed assertion cannot keep it.
func migrationRaceDatabase(t *testing.T) (string, string) {
	t.Helper()
	dsn, err := integrationDSN(os.Getenv("CSX_TEST_DSN"), os.Getenv("CSX_REQUIRE_TEST_DSN"))
	if errors.Is(err, errIntegrationDSNUnset) {
		t.Skip(err.Error())
	}
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	name := fmt.Sprintf("csx_migrace_%d", time.Now().UnixNano())

	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close(ctx)
		t.Fatalf("create throwaway database %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close(context.Background())
	})
	return dsn, name
}

// openMigrationRaceConn connects to the throwaway database and creates the
// named schema, the way every integration test in this repository isolates
// itself.
func openMigrationRaceConn(t *testing.T, dsn, database, schema string) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	conn := openMigrationRaceAdmin(t, dsn, database)
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		conn.Close(ctx)
		t.Fatalf("create schema %s: %v", schema, err)
	}
	if _, err := conn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		conn.Close(ctx)
		t.Fatalf("set search_path to %s: %v", schema, err)
	}
	return conn
}

func openMigrationRaceAdmin(t *testing.T, dsn, database string) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.Database = database
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect to %s: %v", database, err)
	}
	return conn
}

// applyMigrationsBefore brings a schema up to the state right before the named
// migration, so the barrier below releases both migrators onto the contended
// one instead of onto thirty-three unrelated statements.
func applyMigrationsBefore(t *testing.T, conn *pgx.Conn, stop string) {
	t.Helper()
	ctx := context.Background()
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	for _, m := range migs {
		if m.Version >= stop {
			return
		}
		if err := applyMigration(ctx, conn, m); err != nil {
			t.Fatalf("apply %s: %v", m.Version, err)
		}
	}
	t.Fatalf("migration %s not found", stop)
}

// TestIntegrationConcurrentMigratorsSurviveExtensionCreation is the regression
// test for the CI failure above. Without serialisation in applyMigration the
// second migrator fails with SQLSTATE 23505 on pg_extension_name_index; with
// it, the loser waits, sees the committed extension and takes the IF NOT
// EXISTS path, so every iteration must have both migrators finish.
func TestIntegrationConcurrentMigratorsSurviveExtensionCreation(t *testing.T) {
	dsn, database := migrationRaceDatabase(t)
	ctx := context.Background()

	const iterations = 6
	for i := 0; i < iterations; i++ {
		schemas := [2]string{
			fmt.Sprintf("csx_race_a%d_%d", i, time.Now().UnixNano()),
			fmt.Sprintf("csx_race_b%d_%d", i, time.Now().UnixNano()),
		}
		var conns [2]*pgx.Conn
		for j, schema := range schemas {
			conns[j] = openMigrationRaceConn(t, dsn, database, schema)
			applyMigrationsBefore(t, conns[j], trgmMigration)
		}
		// A test that never reaches the contended statement reports the same
		// green as one that survived it. pg_trgm must be absent here or this
		// iteration proves nothing.
		assertTrgmExtensionAbsent(t, conns[0], i)

		start := make(chan struct{})
		var wg sync.WaitGroup
		var errs [2]error
		for j := range conns {
			wg.Add(1)
			go func(j int) {
				defer wg.Done()
				<-start
				errs[j] = Migrate(ctx, conns[j])
			}(j)
		}
		close(start)
		wg.Wait()

		for j, err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: migrator %d in %s failed: %v", i, j, schemas[j], err)
			}
		}
		// Both migrators returning nil is only meaningful if 0034 actually
		// ran: the extension and each schema's trigram index must exist.
		for j, conn := range conns {
			assertTrgmIndexBuilt(t, conn, schemas[j], i)
		}

		for j, conn := range conns {
			if _, err := conn.Exec(ctx, "DROP SCHEMA "+schemas[j]+" CASCADE"); err != nil {
				t.Fatalf("drop schema %s: %v", schemas[j], err)
			}
			conn.Close(ctx)
		}
		// Reset the database-global half of the state so the next iteration
		// races for the extension again instead of coasting on this one.
		admin := openMigrationRaceAdmin(t, dsn, database)
		_, err := admin.Exec(ctx, "DROP EXTENSION IF EXISTS pg_trgm")
		admin.Close(ctx)
		if err != nil {
			t.Fatalf("drop pg_trgm after iteration %d: %v", i, err)
		}
	}
}

func assertTrgmExtensionAbsent(t *testing.T, conn *pgx.Conn, iteration int) {
	t.Helper()
	if trgmInstalled(t, conn, iteration) {
		t.Fatalf("iteration %d: pg_trgm already installed, so the migrators cannot race for it", iteration)
	}
}

func trgmInstalled(t *testing.T, conn *pgx.Conn, iteration int) bool {
	t.Helper()
	var present bool
	if err := conn.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_trgm')`).Scan(&present); err != nil {
		t.Fatalf("iteration %d: read pg_extension: %v", iteration, err)
	}
	return present
}

func assertTrgmIndexBuilt(t *testing.T, conn *pgx.Conn, schema string, iteration int) {
	t.Helper()
	if !trgmInstalled(t, conn, iteration) {
		t.Fatalf("iteration %d: both migrators returned nil but pg_trgm is not installed", iteration)
	}
	var present bool
	if err := conn.QueryRow(context.Background(),
		`SELECT EXISTS(SELECT 1 FROM pg_indexes
		 WHERE schemaname=$1 AND indexname='samples_manifest_lower_trgm_idx')`,
		schema).Scan(&present); err != nil {
		t.Fatalf("iteration %d: read pg_indexes for %s: %v", iteration, schema, err)
	}
	if !present {
		t.Fatalf("iteration %d: %s has no trigram index, so %s never ran there",
			iteration, schema, trgmMigration)
	}
}

// embeddedMigration returns the named migration, so a test can drive
// applyMigration for exactly the contended one.
func embeddedMigration(t *testing.T, version string) Migration {
	t.Helper()
	migs, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	for _, m := range migs {
		if m.Version == version {
			return m
		}
	}
	t.Fatalf("%s not embedded", version)
	return Migration{}
}

// holdMigrationLock takes the runner's advisory lock for one migration version
// on its own connection and returns a release function. The lock is
// transaction-scoped, so an open transaction is what holds it.
func holdMigrationLock(t *testing.T, conn *pgx.Conn, version string) func() {
	t.Helper()
	ctx := context.Background()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		migrationLockNamespace+version); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("hold migration lock for %s: %v", version, err)
	}
	return func() { _ = tx.Rollback(ctx) }
}

// runApplyMigration starts applyMigration in the background and reports its
// error on the returned channel.
func runApplyMigration(conn *pgx.Conn, m Migration) <-chan error {
	done := make(chan error, 1)
	go func() { done <- applyMigration(context.Background(), conn, m) }()
	return done
}

// The regression test above proves the 23505 is gone; this one pins the
// mechanism, so it keeps holding if 0034 is one day rewritten and pg_trgm
// stops being the statement that exposes the overlap. Wall-clock spans around
// applyMigration cannot prove server-side exclusion — the lock is released by
// the commit, so the two clients' timings legitimately overlap — so this holds
// the lock from a third connection instead and watches the migrator wait.
func TestIntegrationApplyMigrationWaitsOnItsOwnVersionLock(t *testing.T) {
	dsn, database := migrationRaceDatabase(t)
	ctx := context.Background()
	schema := fmt.Sprintf("csx_lockwait_%d", time.Now().UnixNano())
	conn := openMigrationRaceConn(t, dsn, database, schema)
	defer conn.Close(ctx)
	applyMigrationsBefore(t, conn, trgmMigration)

	holder := openMigrationRaceAdmin(t, dsn, database)
	defer holder.Close(ctx)
	release := holdMigrationLock(t, holder, trgmMigration)

	done := runApplyMigration(conn, embeddedMigration(t, trgmMigration))
	select {
	case err := <-done:
		release()
		t.Fatalf("applyMigration finished while %s was locked (err=%v); it never took the lock",
			trgmMigration, err)
	case <-time.After(750 * time.Millisecond):
		// Blocked, as the lock intends. The same migration takes about 15ms
		// unobstructed, so this is not merely a slow run.
	}

	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("applyMigration after the lock was released: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("applyMigration never returned after the lock was released")
	}
}

// Serialisation must be per migration, not one global gate: an unrelated
// version's lock has to leave this migrator alone, or a stuck migrator in one
// schema would stall every other migration in the database.
func TestIntegrationApplyMigrationIgnoresAnotherVersionsLock(t *testing.T) {
	dsn, database := migrationRaceDatabase(t)
	ctx := context.Background()
	schema := fmt.Sprintf("csx_lockother_%d", time.Now().UnixNano())
	conn := openMigrationRaceConn(t, dsn, database, schema)
	defer conn.Close(ctx)
	applyMigrationsBefore(t, conn, trgmMigration)

	holder := openMigrationRaceAdmin(t, dsn, database)
	defer holder.Close(ctx)
	release := holdMigrationLock(t, holder, "0001_init.sql")
	defer release()

	done := runApplyMigration(conn, embeddedMigration(t, trgmMigration))
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("applyMigration under an unrelated version's lock: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("applyMigration for %s blocked on 0001_init.sql's lock; the key is not per migration",
			trgmMigration)
	}
}

// The lock must not swallow a genuine failure: a migration whose statement is
// invalid still has to surface its own error, naming the migration and the
// statement so the log says which file to open.
func TestIntegrationApplyMigrationStillReportsStatementErrors(t *testing.T) {
	dsn, database := migrationRaceDatabase(t)
	ctx := context.Background()
	schema := fmt.Sprintf("csx_badstmt_%d", time.Now().UnixNano())
	conn := openMigrationRaceConn(t, dsn, database, schema)
	defer conn.Close(ctx)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	err := applyMigration(ctx, conn, Migration{
		Version:    "9999_not_a_migration.sql",
		Statements: []string{"SELECT no_such_function_here()"},
	})
	if err == nil {
		t.Fatal("a broken statement produced no error")
	}
	if !strings.Contains(err.Error(), "9999_not_a_migration.sql") ||
		!strings.Contains(err.Error(), "no_such_function_here") {
		t.Fatalf("error names neither the migration nor the statement: %v", err)
	}
}
