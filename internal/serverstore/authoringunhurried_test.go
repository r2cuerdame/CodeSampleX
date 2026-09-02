package serverstore

// The background refresh of the candidate snapshot takes one core.
//
// The production host has two. The expansion read is ~700MB from disk and
// takes about four minutes there; its parallel plan uses both cores for
// that whole window, and the website shares the box with nothing. Measured
// 2026-09-02: parallelism off made the read a fifth slower (300s against
// 249s) and left the site a core. A refresh answers no caller, so it has no
// claim on the second one. The poll-bounded read keeps its parallel plan:
// it is capped at ten seconds and a caller is waiting on it.
//
// The assertion is executable inside the statement, like the JIT test's:
// `packages` becomes a view that yields rows only when the session is on
// one core, so the unhurried read finds the candidate and the ordinary one
// does not -- and afterwards the pooled connection is back on the shipped
// setting, because the change is transaction-local.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestIntegrationUnhurriedExpansionTakesOneCoreAndRestoresTheConnection(t *testing.T) {
	pol := DefaultPoolPolicy()
	pol.MaxConns = 1
	pol.ProbeReserve = 0
	pol.InteractiveConns = 1
	pol.BackgroundConns = 1
	pg := openTestPGWithPolicy(t, pol)
	ctx := context.Background()
	const purl = "pkg:npm/one-core@1.0.0"
	env := domain.EnvironmentFingerprint{
		SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64",
		Runtime: "node", RuntimeVersion: "22.18", ModuleSystem: "esm",
	}
	batch := domain.ObservationBatch{
		SchemaVersion: 1, Epoch: "2026-09-02", AnonID: "onecorepeer",
		ProjectBucket: "onecoreproject", Package: purl, Symbol: "compile",
		SymbolConfidence: domain.SymbolProbable, Environment: env,
		Stage: domain.StageProjectCompile, Result: domain.ResultPass, ObservationCount: 1,
	}
	if accepted, rejected, err := pg.IngestBatches(ctx, []domain.ObservationBatch{batch}); err != nil || accepted != 1 || len(rejected) != 0 {
		t.Fatalf("ingest = %d rejected=%v err=%v", accepted, rejected, err)
	}
	if err := pg.UpsertPackage(ctx, PackageRow{
		PURL: purl, Ecosystem: "npm", Name: "one-core", Version: "1.0.0",
		Major: "1", Publicness: "PUBLIC",
	}); err != nil {
		t.Fatal(err)
	}

	// The only package is visible only to a session on one core.
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		if _, err := c.Exec(ctx, `ALTER TABLE packages RENAME TO packages_core_source`); err != nil {
			return err
		}
		_, err := c.Exec(ctx, `CREATE VIEW packages AS
			SELECT * FROM packages_core_source
			WHERE current_setting('max_parallel_workers_per_gather') = '0'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	// The poll-bounded read keeps its parallel plan and so sees nothing.
	hurried, err := pg.ListAuthoringExpansionCandidates(ctx, 10)
	if err != nil {
		t.Fatalf("hurried read: %v", err)
	}
	if len(hurried) != 0 {
		t.Fatalf("the poll-bounded read ran on one core; it is capped at ten seconds with a caller waiting and keeps its parallel plan (got %+v)", hurried)
	}

	// The refresh's read runs on one core and finds it.
	unhurried, err := pg.ListAuthoringExpansionCandidatesUnhurried(ctx, 10)
	if err != nil {
		t.Fatalf("unhurried read: %v", err)
	}
	// The read may legitimately return the package-level row and the
	// symbol-level row for the same package; what matters is that it saw the
	// package at all, which only a one-core session can.
	if len(unhurried) == 0 {
		t.Fatalf("the unhurried read did not run on one core: the only package was invisible to it")
	}
	for _, row := range unhurried {
		if row.Name != "one-core" {
			t.Fatalf("unexpected candidate %+v", row)
		}
	}

	// And the single pooled connection is back on the shipped setting.
	var setting string
	if err := pg.withConn(ctx, func(c *pgx.Conn) error {
		return c.QueryRow(ctx, `SHOW max_parallel_workers_per_gather`).Scan(&setting)
	}); err != nil {
		t.Fatal(err)
	}
	if setting == "0" {
		t.Fatal("max_parallel_workers_per_gather stayed 0 on the pooled connection after the unhurried read; the change must be transaction-local")
	}
}
