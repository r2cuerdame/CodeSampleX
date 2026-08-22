package serverstore

// CI's database preflight. It runs as its own step before the suite so that a
// database that never came up, a DSN that names the wrong credentials, and a
// migration that no longer applies each fail under a step called "preflight"
// — separately from a step called "tests", which is then only ever about the
// code. Without that split every one of those failures arrives as "the
// serverstore tests failed" and costs a bisect to tell apart.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestIntegrationDatabaseIsReadyForTheSuite(t *testing.T) {
	// openTestPG labels its three preparation stages — connect, create
	// schema, migrate — so a failure here already names which one.
	pg := openTestPG(t)
	ctx := context.Background()

	migs, err := LoadMigrations()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}

	applied := map[string]bool{}
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		rows, err := c.Query(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			applied[v] = true
		}
		return rows.Err()
	}); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	for _, m := range migs {
		if !applied[m.Version] {
			t.Errorf("migration %s is embedded but was not applied to the test schema", m.Version)
		}
	}
	if t.Failed() {
		t.Fatalf("%d of %d migrations reached the database", len(applied), len(migs))
	}

	// Reaching the database is not the same as being able to serve from it.
	// One real query through the store proves the search_path the tests run
	// under actually sees the tables the migrations just created.
	if _, _, err := pg.ListWanted(ctx, "", 0, 1); err != nil {
		t.Fatalf("the migrated schema cannot serve a query: %v", err)
	}
	t.Logf("database ready: %d migrations applied", len(applied))
}
