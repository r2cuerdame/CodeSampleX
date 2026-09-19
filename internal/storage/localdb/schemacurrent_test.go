package localdb

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// Opening an already-migrated store used to take SQLite's write reservation
// (BEGIN IMMEDIATE) before looking at the schema, so every CLI start waited
// behind whatever evidence writer held it — up to the 30 s busy timeout,
// past the Farm's 20 s stats budget (#377). A store that is already current
// is read, recognised, and never written on open.
func TestOpenOfACurrentStoreDoesNotWaitForTheWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "csx.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.StampFirst(t.Context(), StatFirstRunAt, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	writer, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	if _, err := writer.Exec(`BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	defer writer.Exec(`ROLLBACK`)

	type result struct {
		db  *DB
		err error
	}
	done := make(chan result, 1)
	go func() {
		db, err := Open(path)
		done <- result{db, err}
	}()
	var got result
	select {
	case got = <-done:
	case <-time.After(3 * time.Second):
		_, _ = writer.Exec(`ROLLBACK`)
		got = <-done
		t.Fatal("Open of a current store waited for the evidence writer's lock")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer got.db.Close()
	if v, ok, err := got.db.GetStat(t.Context(), StatFirstRunAt); err != nil || !ok || v == "" {
		t.Fatalf("the store opened under a writer cannot read: value=%q ok=%v err=%v", v, ok, err)
	}
}

// schemaCurrent may only say "current" when migrate would do nothing. This
// pins it to the migration in both directions: everything migrate creates
// is something schemaCurrent looks for, and everything schemaCurrent looks
// for is something a fresh migration created.
func TestSchemaCurrentTracksEveryMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "csx.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if ok, err := db.schemaCurrent(ctx); err != nil || !ok {
		t.Fatalf("a freshly migrated store is not current: ok=%v err=%v", ok, err)
	}

	rows, err := db.sql.QueryContext(ctx, `
		SELECT type, name FROM sqlite_master
		WHERE type IN ('table','index','trigger')
		  AND name NOT LIKE 'sqlite\_%' ESCAPE '\'
		  AND name NOT LIKE 'search\_fts\_%' ESCAPE '\'`)
	if err != nil {
		t.Fatal(err)
	}
	var have []string
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			t.Fatal(err)
		}
		have = append(have, kind+":"+name)
	}
	rows.Close()
	var want []string
	for _, o := range expectedSchemaObjects {
		want = append(want, o.kind+":"+o.name)
	}
	sort.Strings(have)
	sort.Strings(want)
	if len(have) != len(want) {
		t.Fatalf("migrate created %d objects, schemaCurrent expects %d:\nhave %v\nwant %v", len(have), len(want), have, want)
	}
	for i := range have {
		if have[i] != want[i] {
			t.Fatalf("object %d: migrate created %s, schemaCurrent expects %s", i, have[i], want[i])
		}
	}

	// Each of these is one thing an older store can lack. Every one must
	// make the store not-current, and one Open must repair it.
	for _, tc := range []struct {
		name   string
		damage []string
	}{
		{"missing index", []string{`DROP INDEX observations_pending`}},
		{"missing migration index", []string{`DROP INDEX cli_execution_evidence_subject`}},
		{"missing trigger", []string{`DROP TRIGGER samples_corpus_generation_INSERT`}},
		{"missing additive column", []string{`ALTER TABLE observations DROP COLUMN depends_on_none`}},
		{"missing interventions column", []string{
			`DROP INDEX interventions_hit_sample_unique`,
			`ALTER TABLE interventions DROP COLUMN hit_id`}},
		{"legacy unique index still present", []string{`CREATE UNIQUE INDEX interventions_offer_id_unique ON interventions(offer_id)`}},
		{"missing schema version", []string{`DELETE FROM meta WHERE key = 'schema_version'`}},
		{"unbackfilled CLI subject", []string{`INSERT INTO cli_execution_evidence(evidence_id, coordinate_id, tool, env_hash, provenance, result, evidence_quality)
			VALUES('ev-1', 'coord', 'npm', 'env', 'cli', 'FAIL', 'complete')`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "csx.db")
			seed, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer seed.Close()
			for _, stmt := range tc.damage {
				if _, err := seed.sql.ExecContext(ctx, stmt); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := seed.schemaCurrent(ctx); err != nil || ok {
				t.Fatalf("damage was not noticed: ok=%v err=%v", ok, err)
			}
			if err := seed.Close(); err != nil {
				t.Fatal(err)
			}
			repaired, err := Open(p)
			if err != nil {
				t.Fatal(err)
			}
			defer repaired.Close()
			if ok, err := repaired.schemaCurrent(ctx); err != nil || !ok {
				t.Fatalf("Open did not repair the store: ok=%v err=%v", ok, err)
			}
		})
	}
}
