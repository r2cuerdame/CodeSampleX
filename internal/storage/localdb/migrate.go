package localdb

import (
	"context"
	"database/sql"
)

type migrationExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// schemaVersion is recorded in meta and bumped only with a migration path.
const schemaVersion = "1"

// ddl is contract C3 verbatim, plus packages.checked_at (when the registry
// publicness check last ran — C3 allows this one addition).
var ddl = []string{
	`CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT)`,
	`CREATE TABLE IF NOT EXISTS packages(
	  purl TEXT PRIMARY KEY, ecosystem TEXT NOT NULL, name TEXT NOT NULL, version TEXT NOT NULL,
	  public INTEGER NOT NULL DEFAULT 0,
	  publicness TEXT NOT NULL DEFAULT 'UNKNOWN',
	  first_seen TEXT, last_seen TEXT, checked_at TEXT)`,
	`CREATE TABLE IF NOT EXISTS symbol_usages(
	  purl TEXT NOT NULL, symbol TEXT NOT NULL, confidence TEXT NOT NULL,
	  project_bucket TEXT NOT NULL, last_seen TEXT,
	  PRIMARY KEY(purl,symbol,project_bucket))`,
	`CREATE TABLE IF NOT EXISTS observations(
	  epoch TEXT NOT NULL, purl TEXT NOT NULL, symbol TEXT NOT NULL DEFAULT '',
	  symbol_confidence TEXT NOT NULL DEFAULT 'UNKNOWN', env_hash TEXT NOT NULL,
	  stage TEXT NOT NULL, result TEXT NOT NULL, count INTEGER NOT NULL DEFAULT 0,
	  error_fp TEXT NOT NULL DEFAULT '', error_code TEXT NOT NULL DEFAULT '',
	  uploaded INTEGER NOT NULL DEFAULT 0,
	  PRIMARY KEY(epoch,purl,symbol,env_hash,stage,result,error_fp))`,
	`CREATE TABLE IF NOT EXISTS environments(hash TEXT PRIMARY KEY, json TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS cases(case_id TEXT PRIMARY KEY, kind TEXT, goal TEXT, json TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS samples(
	  sample_id TEXT PRIMARY KEY, case_id TEXT, manifest_json TEXT NOT NULL,
	  status TEXT NOT NULL DEFAULT 'LOCAL',
	  origin_seeder TEXT, license TEXT, created_at TEXT,
	  pinned INTEGER NOT NULL DEFAULT 0, hot_score REAL NOT NULL DEFAULT 0, last_used TEXT,
	  has_artifact INTEGER NOT NULL DEFAULT 0)`,
	`CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(
	  doc_id UNINDEXED, kind UNINDEXED, title, body, packages, symbols, error_codes)`,
	`CREATE TABLE IF NOT EXISTS shards(
	  key TEXT PRIMARY KEY,
	  etag TEXT, json TEXT NOT NULL, synced_at TEXT)`,
	`CREATE TABLE IF NOT EXISTS upload_queue(
	  id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL,
	  payload TEXT NOT NULL, created_at TEXT, attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT)`,
	`CREATE TABLE IF NOT EXISTS receipts(receipt_id TEXT PRIMARY KEY, sample_id TEXT, json TEXT, created_at TEXT)`,
	`CREATE TABLE IF NOT EXISTS hits(
	  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT, query TEXT, grade TEXT,
	  sample_id TEXT, adopted INTEGER DEFAULT 0, post_build_pass INTEGER)`,
	// A deliberately narrow, local-only journey record. It contains no
	// query, fingerprint, package, environment, path, user or peer identity.
	// offer_id is an opaque random capability returned only by local search
	// surfaces; hit_id binds the journey to the exact local hit written in
	// the same transaction. NULL in either column marks a pre-upgrade row,
	// which is deliberately ineligible for failure-avoidance credit.
	`CREATE TABLE IF NOT EXISTS interventions(
	  ts TEXT NOT NULL, offer_id TEXT, hit_id INTEGER,
	  sample_id TEXT NOT NULL,
	  exact_failure_matched INTEGER NOT NULL DEFAULT 0,
	  verified_offer INTEGER NOT NULL DEFAULT 0,
	  applied INTEGER, build_pass INTEGER)`,
	`CREATE TABLE IF NOT EXISTS excluded_packages(pattern TEXT PRIMARY KEY)`,
	// Samples an agent prepared but nobody has reviewed yet. Publishing
	// needs the user's explicit approval (goal.md §12.4) — but asking for
	// that approval requires remembering the proposal exists, and until
	// this table the workspace was created, filled in, and then silently
	// forgotten. Every unreviewed proposal is a sample the network lost.
	`CREATE TABLE IF NOT EXISTS proposals(
	  workdir TEXT PRIMARY KEY, goal TEXT NOT NULL, packages TEXT NOT NULL,
	  created_at TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'pending')`,
}

// migrate applies the schema; every statement is IF NOT EXISTS so repeated
// opens are no-ops.
func (d *DB) migrate(ctx context.Context) error {
	conn, err := d.sql.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	// BEGIN IMMEDIATE takes SQLite's write reservation before inspecting the
	// legacy schema. A process-local mutex is insufficient because the daemon
	// and MCP can open the same database from different processes; this lock
	// makes the second migrator wait, then inspect the already-upgraded table.
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	for _, stmt := range ddl {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := migrateInterventionCorrelation(ctx, conn); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO meta(key, value) VALUES('schema_version', ?)
		ON CONFLICT(key) DO NOTHING`, schemaVersion); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

// migrateInterventionCorrelation upgrades databases created by the first
// failure-detour implementation. SQLite's CREATE TABLE IF NOT EXISTS does not
// add later columns, so inspect the table and add only the missing local
// correlation fields. Existing rows intentionally remain NULL: they have no
// offer capability or exact hit identity and must be re-searched before an
// adoption can earn failure-avoidance credit.
func migrateInterventionCorrelation(ctx context.Context, tx migrationExecutor) error {
	columns := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(interventions)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !columns["offer_id"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE interventions ADD COLUMN offer_id TEXT`); err != nil {
			return err
		}
	}
	if !columns["hit_id"] {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE interventions ADD COLUMN hit_id INTEGER`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS interventions_offer_id_unique
		ON interventions(offer_id) WHERE offer_id IS NOT NULL`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		CREATE UNIQUE INDEX IF NOT EXISTS interventions_hit_id_unique
		ON interventions(hit_id) WHERE hit_id IS NOT NULL`)
	return err
}
