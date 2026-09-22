package localdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
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
	  termination_kind TEXT NOT NULL DEFAULT '', exit_code INTEGER,
	  signal TEXT NOT NULL DEFAULT '', timeout_millis INTEGER NOT NULL DEFAULT 0,
	  error_summary TEXT NOT NULL DEFAULT '', evidence_quality TEXT NOT NULL DEFAULT '',
	  outer_command TEXT NOT NULL DEFAULT '', outer_stage TEXT NOT NULL DEFAULT '',
	  actual_toolchain TEXT NOT NULL DEFAULT '', stage_evidence TEXT NOT NULL DEFAULT '',
	  failure_evidence_gap TEXT NOT NULL DEFAULT '',
	  direct INTEGER NOT NULL DEFAULT 0,
	  coresident TEXT NOT NULL DEFAULT '',
	  depends_on TEXT NOT NULL DEFAULT '',
	  depends_on_none INTEGER NOT NULL DEFAULT 0,
	  uploaded INTEGER NOT NULL DEFAULT 0,
	  legacy_reconciled_count INTEGER NOT NULL DEFAULT 0,
	  PRIMARY KEY(epoch,purl,symbol,env_hash,stage,result,error_fp))`,
	`CREATE TABLE IF NOT EXISTS environments(hash TEXT PRIMARY KEY, json TEXT NOT NULL)`,
	// Structured CLI executions retain the exact, secret-safe evidence shape
	// that the daily observations aggregate cannot represent. Identical
	// coordinate/outcome/stream signatures are compressed by evidence_id while
	// first/last timestamps preserve the measured window. Raw stdout/stderr,
	// paths, project names, and arbitrary environment variables never enter.
	`CREATE TABLE IF NOT EXISTS cli_execution_evidence(
	  evidence_id TEXT PRIMARY KEY, coordinate_id TEXT NOT NULL,
	  tool TEXT NOT NULL, tool_version TEXT NOT NULL DEFAULT '',
	  subcommand TEXT NOT NULL DEFAULT '', args_pattern TEXT NOT NULL DEFAULT '',
	  shell TEXT NOT NULL DEFAULT '', env_hash TEXT NOT NULL,
	  provenance TEXT NOT NULL, result TEXT NOT NULL,
	  termination_kind TEXT NOT NULL DEFAULT '', exit_code INTEGER,
	  signal TEXT NOT NULL DEFAULT '', timeout_millis INTEGER NOT NULL DEFAULT 0,
	  error_fp TEXT NOT NULL DEFAULT '', error_code TEXT NOT NULL DEFAULT '',
	  error_summary TEXT NOT NULL DEFAULT '', evidence_quality TEXT NOT NULL,
	  stdout_fp TEXT NOT NULL DEFAULT '', stdout_excerpt TEXT NOT NULL DEFAULT '',
	  stdout_truncated INTEGER NOT NULL DEFAULT 0,
	  stderr_fp TEXT NOT NULL DEFAULT '', stderr_excerpt TEXT NOT NULL DEFAULT '',
	  stderr_truncated INTEGER NOT NULL DEFAULT 0,
	  started_at TEXT NOT NULL DEFAULT '', finished_at TEXT NOT NULL DEFAULT '',
	  count INTEGER NOT NULL DEFAULT 1,
	  subject_id TEXT NOT NULL DEFAULT '')`,
	`CREATE INDEX IF NOT EXISTS cli_execution_evidence_coordinate
	  ON cli_execution_evidence(coordinate_id, finished_at DESC)`,
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
	// Status is sampled frequently by the Farm. Its old queue-depth query
	// materialized up to 1,000 complete observation rows (including long
	// diagnostic strings) and up to 1,000 queued payloads every time. These
	// narrow indexes let the diagnostic count keys instead of revisiting the
	// payload-bearing tables (CodeSampleX-Farm#18).
	`CREATE INDEX IF NOT EXISTS observations_pending
	  ON observations(uploaded) WHERE uploaded = 0`,
	`CREATE INDEX IF NOT EXISTS observations_legacy_windows
	  ON observations(exit_code)
	  WHERE exit_code > 2147483647 AND exit_code <= 4294967295`,
	`CREATE INDEX IF NOT EXISTS upload_queue_pending
	  ON upload_queue(attempts, kind, id)`,
	`CREATE TABLE IF NOT EXISTS receipts(receipt_id TEXT PRIMARY KEY, sample_id TEXT, json TEXT, created_at TEXT)`,
	// Every candidate a search scores has its receipts read, and there were
	// 2,788 candidates on the machine where this was measured. Without this
	// index each of those reads scanned the whole receipts table: about 8.5
	// million row visits for ONE search, 10.4 of its 11.3 seconds, and 89.6%
	// of Engine.Search in a CPU profile.
	//
	// The ORDER BY is part of it. Covering created_at and receipt_id lets
	// SQLite walk the index in order instead of building a temp b-tree per
	// candidate, and putting json last means the row itself is never touched.
	`CREATE INDEX IF NOT EXISTS receipts_by_sample
	  ON receipts(sample_id, created_at, receipt_id, json)`,
	`CREATE TABLE IF NOT EXISTS hits(
	  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT, query TEXT, grade TEXT,
	  sample_id TEXT, adopted INTEGER DEFAULT 0, post_build_pass INTEGER)`,
	// A deliberately narrow, local-only journey record. It contains no
	// query, fingerprint, package, environment, path, user or peer identity.
	// offer_id is an opaque random capability returned only by local search
	// surfaces; hit_id binds the journey to the exact local hit written in
	// the same transaction. NULL in either column marks a pre-upgrade row,
	// which is deliberately ineligible for failure-avoidance credit. A
	// search that listed several candidates writes one row per candidate
	// under the same offer_id and hit_id (#344): the offer names the search,
	// the sample names which of its answers the report is about.
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
	// Evidence the server has decided it will never accept.
	//
	// A refusal used to be restored to pending and sent again on the next
	// sync, forever: production measured 7,432 batches sent, 852 refused, and
	// the pending queue pinned at its 1,000 cap by the same refusals coming
	// back. Dropping them silently would have been worse — that is evidence
	// disappearing with nothing to say it ever existed — so a terminal
	// refusal stops being retried and becomes a row here instead.
	//
	// It records what the batch was ABOUT, not the batch: purl, symbol,
	// environment hash, stage and result are the coordinate, and they are the
	// same fields the observation itself carries. No path, no source, no
	// local identity.
	`CREATE TABLE IF NOT EXISTS refused_evidence(
	  epoch TEXT NOT NULL, purl TEXT NOT NULL, symbol TEXT NOT NULL DEFAULT '',
	  env_hash TEXT NOT NULL DEFAULT '', stage TEXT NOT NULL DEFAULT '',
	  result TEXT NOT NULL DEFAULT '', code TEXT NOT NULL DEFAULT '',
	  reason TEXT NOT NULL DEFAULT '', refused_at TEXT NOT NULL,
	  PRIMARY KEY(epoch, purl, symbol, env_hash, stage, result))`,
}

// additiveColumn is one column a later release added to an existing table.
// SQLite's CREATE TABLE IF NOT EXISTS does not add columns, so each is
// inspected and added only when missing. The list is shared with
// schemaCurrent: a column migrate would add is a column an open must look
// for, or a store could be called current while still lacking it.
type additiveColumn struct{ table, name, ddl string }

var additiveColumns = []additiveColumn{
	// Local databases created before the flag existed. Every adapter already
	// worked out direct-versus-transitive from the lockfile and threw it away
	// at the wire, so old rows default to transitive.
	//
	// depends_on: who this package pulled in the same resolution. Coresident
	// says two versions were installed together; this says who wanted each.
	{"observations", "depends_on", `ALTER TABLE observations ADD COLUMN depends_on TEXT NOT NULL DEFAULT ''`},
	// coresident: the other versions of this library present in the same
	// resolution, comma separated. One library at two versions is the
	// commonest reason a build does not work and the server cannot see it: a
	// batch carries one package, so a lockfile arrives already shredded.
	{"observations", "coresident", `ALTER TABLE observations ADD COLUMN coresident TEXT NOT NULL DEFAULT ''`},
	{"observations", "direct", `ALTER TABLE observations ADD COLUMN direct INTEGER NOT NULL DEFAULT 0`},
	{"observations", "depends_on_none", `ALTER TABLE observations ADD COLUMN depends_on_none INTEGER NOT NULL DEFAULT 0`},
	{"observations", "termination_kind", `ALTER TABLE observations ADD COLUMN termination_kind TEXT NOT NULL DEFAULT ''`},
	{"observations", "exit_code", `ALTER TABLE observations ADD COLUMN exit_code INTEGER`},
	{"observations", "signal", `ALTER TABLE observations ADD COLUMN signal TEXT NOT NULL DEFAULT ''`},
	{"observations", "timeout_millis", `ALTER TABLE observations ADD COLUMN timeout_millis INTEGER NOT NULL DEFAULT 0`},
	{"observations", "error_summary", `ALTER TABLE observations ADD COLUMN error_summary TEXT NOT NULL DEFAULT ''`},
	{"observations", "evidence_quality", `ALTER TABLE observations ADD COLUMN evidence_quality TEXT NOT NULL DEFAULT ''`},
	{"observations", "outer_command", `ALTER TABLE observations ADD COLUMN outer_command TEXT NOT NULL DEFAULT ''`},
	{"observations", "outer_stage", `ALTER TABLE observations ADD COLUMN outer_stage TEXT NOT NULL DEFAULT ''`},
	{"observations", "actual_toolchain", `ALTER TABLE observations ADD COLUMN actual_toolchain TEXT NOT NULL DEFAULT ''`},
	{"observations", "stage_evidence", `ALTER TABLE observations ADD COLUMN stage_evidence TEXT NOT NULL DEFAULT ''`},
	{"observations", "failure_evidence_gap", `ALTER TABLE observations ADD COLUMN failure_evidence_gap TEXT NOT NULL DEFAULT ''`},
	{"observations", "legacy_reconciled_count", `ALTER TABLE observations ADD COLUMN legacy_reconciled_count INTEGER NOT NULL DEFAULT 0`},
	// Databases created by the first failure-detour implementation. Existing
	// rows intentionally remain NULL: they have no offer capability or exact
	// hit identity and must be re-searched before an adoption can earn
	// failure-avoidance credit.
	{"interventions", "offer_id", `ALTER TABLE interventions ADD COLUMN offer_id TEXT`},
	{"interventions", "hit_id", `ALTER TABLE interventions ADD COLUMN hit_id INTEGER`},
	// Structured CLI evidence's first-class subject address (#79). Rows
	// recorded before the column existed are backfilled from their own
	// columns by migrateCLISubjectID.
	{"cli_execution_evidence", "subject_id", `ALTER TABLE cli_execution_evidence ADD COLUMN subject_id TEXT NOT NULL DEFAULT ''`},
}

// droppedIndexes are indexes an earlier release created and this one must
// not have. One search offers a ranked list, and every candidate on it is
// recorded under the same offer_id and hit_id so an adoption of the second
// or third result correlates as well as the first (#344). The first build
// keyed both indexes on the offer alone, which refused the second candidate;
// they are dropped, not merely joined by the composite ones, because a
// surviving single-column UNIQUE would still reject the insert.
var droppedIndexes = []string{
	"interventions_offer_id_unique",
	"interventions_hit_id_unique",
}

// postColumnDDL is created after the additive columns exist: on an old
// database each of these indexes must follow the ALTER that adds its column.
var postColumnDDL = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS interventions_offer_sample_unique
		ON interventions(offer_id, sample_id) WHERE offer_id IS NOT NULL`,
	`CREATE UNIQUE INDEX IF NOT EXISTS interventions_hit_sample_unique
		ON interventions(hit_id, sample_id) WHERE hit_id IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS cli_execution_evidence_subject
	  ON cli_execution_evidence(subject_id, finished_at DESC)`,
}

// schemaObject is one table, index or trigger migrate guarantees exists.
type schemaObject struct{ kind, name string }

// createStatement recognises every shape of CREATE this package writes. It
// is deliberately narrow: a statement it cannot read is a statement
// schemaCurrent cannot look for, and expectedSchemaObjects refuses to build
// rather than let such an object go silently unapplied on current stores.
var createStatement = regexp.MustCompile(
	`(?is)^\s*CREATE\s+(?:UNIQUE\s+)?(TABLE|INDEX|TRIGGER|VIRTUAL\s+TABLE)\s+IF\s+NOT\s+EXISTS\s+(\w+)`)

// expectedSchemaObjects is everything migrate creates, derived from the same
// statements it executes so the two cannot drift apart.
var expectedSchemaObjects = func() []schemaObject {
	var out []schemaObject
	for _, group := range [][]string{ddl, corpusGenerationDDL, postColumnDDL} {
		for _, stmt := range group {
			m := createStatement.FindStringSubmatch(stmt)
			if m == nil {
				panic("localdb: migration statement schemaCurrent cannot read: " + stmt)
			}
			kind := strings.ToLower(m[1])
			if strings.HasPrefix(kind, "virtual") {
				kind = "table"
			}
			out = append(out, schemaObject{kind, m[2]})
		}
	}
	return out
}()

// schemaCurrent reports, from reads alone, whether migrate would change
// anything. It is what lets Open skip the write reservation on a store that
// is already up to date (#377): BEGIN IMMEDIATE on every open made each CLI
// start wait behind whatever evidence writer held the lock, for up to the
// 30-second busy timeout, on a store that needed nothing.
//
// "Current" is checked against what migrate does, not against the
// schema_version marker: that marker does not move for additive migrations,
// so it can say "1" of a store that still lacks a column. Everything here
// is conservative — any doubt, any read error, and the caller runs the full
// locked migration exactly as before. That keeps the concurrency story
// unchanged: two processes that both find the store current both skip; a
// process that finds it stale takes the lock and re-inspects under it.
func (d *DB) schemaCurrent(ctx context.Context) (bool, error) {
	rows, err := d.sql.QueryContext(ctx,
		`SELECT type, name FROM sqlite_master WHERE type IN ('table','index','trigger')`)
	if err != nil {
		return false, err
	}
	have := map[schemaObject]bool{}
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.kind, &o.name); err != nil {
			rows.Close()
			return false, err
		}
		have[o] = true
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, o := range expectedSchemaObjects {
		if !have[o] {
			return false, nil
		}
	}
	for _, name := range droppedIndexes {
		if have[schemaObject{"index", name}] {
			return false, nil
		}
	}
	columns := map[string]map[string]bool{}
	for _, c := range additiveColumns {
		cols, ok := columns[c.table]
		if !ok {
			cols, err = tableColumns(ctx, d.sql, c.table)
			if err != nil {
				return false, err
			}
			columns[c.table] = cols
		}
		if !cols[c.name] {
			return false, nil
		}
	}
	var n int
	if err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM meta WHERE key = 'schema_version'`).Scan(&n); err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	if err := d.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM (SELECT 1 FROM cli_execution_evidence WHERE subject_id = '' LIMIT 1)`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
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
	// After the tables exist: these are triggers on them.
	for _, stmt := range corpusGenerationDDL {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := migrateAdditiveColumns(ctx, conn); err != nil {
		return err
	}
	for _, name := range droppedIndexes {
		if _, err := conn.ExecContext(ctx, `DROP INDEX IF EXISTS `+name); err != nil {
			return err
		}
	}
	for _, stmt := range postColumnDDL {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := migrateCLISubjectID(ctx, conn); err != nil {
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

// migrateAdditiveColumns inspects each table and adds only the columns it
// lacks, so a fresh database (already carrying them) and an old one end up
// the same.
func migrateAdditiveColumns(ctx context.Context, tx migrationExecutor) error {
	columns := map[string]map[string]bool{}
	for _, c := range additiveColumns {
		cols, ok := columns[c.table]
		if !ok {
			var err error
			cols, err = tableColumns(ctx, tx, c.table)
			if err != nil {
				return err
			}
			columns[c.table] = cols
		}
		if cols[c.name] {
			continue
		}
		if _, err := tx.ExecContext(ctx, c.ddl); err != nil {
			return err
		}
		cols[c.name] = true
	}
	return nil
}

// migrateCLISubjectID backfills structured CLI evidence's subject address
// (#79) on rows recorded before the column existed. They carry every fact
// the subject is derived from, so they become addressable without being
// re-recorded. The column and its index are additive and created above.
func migrateCLISubjectID(ctx context.Context, tx migrationExecutor) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.evidence_id, e.tool, e.tool_version, e.subcommand, e.args_pattern, e.shell,
		       COALESCE(env.json, '')
		FROM cli_execution_evidence e LEFT JOIN environments env ON env.hash = e.env_hash
		WHERE e.subject_id = ''`)
	if err != nil {
		return err
	}
	type pending struct{ evidenceID, subjectID string }
	var backfill []pending
	for rows.Next() {
		var evidenceID, tool, version, subcommand, argsPattern, shell, envJSON string
		if err := rows.Scan(&evidenceID, &tool, &version, &subcommand, &argsPattern, &shell, &envJSON); err != nil {
			rows.Close()
			return err
		}
		var env domain.EnvironmentFingerprint
		if envJSON != "" {
			_ = json.Unmarshal([]byte(envJSON), &env)
		}
		coord := domain.CLIExperienceCoordinate{
			Tool: tool, ToolVersion: version, Subcommand: subcommand,
			ArgsPattern: argsPattern, Shell: shell, Environment: env,
		}
		backfill = append(backfill, pending{evidenceID, coord.Subject().SubjectID()})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, p := range backfill {
		if _, err := tx.ExecContext(ctx,
			`UPDATE cli_execution_evidence SET subject_id = ? WHERE evidence_id = ?`, p.subjectID, p.evidenceID); err != nil {
			return err
		}
	}
	return nil
}

// tableColumns reads a table's column names so an additive migration can
// tell a fresh database (already carrying the column) from an old one.
func tableColumns(ctx context.Context, tx migrationExecutor, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}
