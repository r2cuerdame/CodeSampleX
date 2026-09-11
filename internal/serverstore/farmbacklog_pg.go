package serverstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// Farm aggregates are operator snapshots, not unbounded builder work. The
// admin page polls them every minute, so one query that outlives a poll would
// otherwise let later polls accumulate identical xid-less SELECTs. Cap every
// store call independently as a final defense even when it is invoked outside
// the HTTP handler. An earlier caller deadline still wins.
const farmAggregateTimeout = 25 * time.Second

func farmAggregateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, farmAggregateTimeout)
}

func beginFarmAggregate(ctx context.Context, c *pgx.Conn, ceiling time.Duration) (pgx.Tx, error) {
	tx, err := c.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	// PostgreSQL gets the first say, just before the Go deadline. That
	// cancels the statement without sacrificing this scarce pooled
	// connection; the explicit rollback below clears the aborted transaction.
	// The matrix query also measured 162ms of execution behind roughly 770ms
	// of JIT compilation, so this request-scoped transaction turns JIT off.
	statementTimeout := authoringStatementTimeout(ctx, ceiling)
	if _, err := tx.Exec(ctx, `SELECT set_config('statement_timeout',$1,true), set_config('jit','off',true)`, pgStatementTimeout(statementTimeout)); err != nil {
		_ = tx.Rollback(context.Background())
		return nil, err
	}
	return tx, nil
}

// farmBacklogStocksSQL is also exercised directly by the result-parity and
// query-plan regressions; they must measure the statement production runs.
const farmBacklogStocksSQL = `
			WITH ` + authoringCoverageCTE + `,
			wanted_key AS MATERIALIZED (
				SELECT w.ecosystem, w.name, w.version, w.symbol, w.target_os, w.asks, k.coord
				  FROM wanted w
				  CROSS JOIN LATERAL (VALUES
				      ('pkg:' || w.ecosystem || '/' || w.name || '@'),
				      ('pkg:' || w.ecosystem || '/' ||
				          CASE WHEN left(w.name, 1) = '@'
				               THEN '%40' || substring(w.name from 2)
				               ELSE w.name END || '@')) AS k(coord)
			), wanted_coord AS MATERIALIZED (
				-- Repeated symbols, versions and platforms ask about the same
				-- package coordinate. Join its samples once, before DISTINCT,
				-- rather than multiplying them by every Wanted row (#364).
				SELECT DISTINCT coord FROM wanted_key
			), candidate_samples AS MATERIALIZED (
				SELECT DISTINCT sp.sample_id, sp.coord
				  FROM wanted_coord wk
				  JOIN sample_packages sp ON sp.coord = wk.coord
				  JOIN samples s ON s.sample_id = sp.sample_id AND NOT s.quarantined
			), candidate_receipts AS MATERIALIZED (
				SELECT cs.sample_id,
				       LOWER(COALESCE(r.receipt->'environment'->>'os','')) AS os,
				       r.receipt->>'schemaVersion' AS schema_version,
				       r.receipt->'stages'->>'resolve' AS resolve_stage,
				       COALESCE(r.receipt->'resolvedPackages', '[]'::jsonb) AS resolved_packages
				  FROM (SELECT DISTINCT sample_id FROM candidate_samples) cs
				  CROSS JOIN LATERAL (
				      SELECT r.receipt
				        FROM receipts r
				       WHERE r.sample_id = cs.sample_id
				         AND r.contract_result = 'PASS'
				       ORDER BY r.created_at DESC
				       LIMIT 10
				  ) r
			), answered_wanted AS MATERIALIZED (
				SELECT DISTINCT wk.ecosystem, wk.name, wk.version, wk.symbol, wk.target_os
				  FROM wanted_key wk
				  JOIN candidate_samples cs ON cs.coord = wk.coord
				  JOIN samples answer_sample ON answer_sample.sample_id = cs.sample_id
				  JOIN candidate_receipts cr ON cr.sample_id = cs.sample_id
				 WHERE (wk.symbol = '' OR COALESCE(answer_sample.manifest->'symbols', '[]'::jsonb) ? wk.symbol)
				   AND (wk.target_os = '' OR cr.os = wk.target_os)
				   AND (
				       wk.version = ''
				       OR (
				           cr.schema_version = '2'
				           AND cr.resolve_stage = 'PASS'
				           AND cr.resolved_packages ?
				               ('pkg:' || wk.ecosystem || '/' ||
				                CASE WHEN left(wk.name, 1) = '@'
				                     THEN '%40' || substring(wk.name from 2)
				                     ELSE wk.name END || '@' || wk.version)
				       )
				       OR (
				           cr.schema_version <> '2'
				           AND EXISTS (
				               SELECT 1
				                 FROM sample_packages sp
				                WHERE sp.sample_id = cs.sample_id
				                  AND sp.purl = ('pkg:' || wk.ecosystem || '/' ||
				                                 CASE WHEN left(wk.name, 1) = '@'
				                                      THEN '%40' || substring(wk.name from 2)
				                                      ELSE wk.name END || '@' || wk.version)
				           )
				       ))
			), unanswered_wanted AS (
				SELECT w.ecosystem, w.name, w.version, w.symbol, w.target_os, w.asks
				  FROM wanted w
				  LEFT JOIN answered_wanted an
				    ON an.ecosystem = w.ecosystem AND an.name = w.name
				   AND an.version = w.version AND an.symbol = w.symbol
				   AND an.target_os = w.target_os
				 WHERE an.ecosystem IS NULL
			)
			SELECT
			  -- The ` + "`-`" + ` cells: a PUBLIC release the network watches people
			  -- use and has never proven.
			  (SELECT count(*) FROM (
			     SELECT pk.purl
			     FROM packages pk
			     WHERE pk.version<>'' AND pk.publicness='PUBLIC'
			       AND EXISTS (SELECT 1 FROM evidence_agg e WHERE e.purl=pk.purl)
			       AND NOT EXISTS (SELECT 1 FROM verified_packages v WHERE v.purl=pk.purl)
			   ) h),
			  -- The dependency backlog, whole rather than capped.
			  (SELECT count(*) FROM (
			     SELECT DISTINCT ecosystem,child_name,child_version FROM dependency_open
			   ) d),
			  -- RequestBacklog: unresolved wanted requests
			  (SELECT count(*) FROM unanswered_wanted),
			  -- RepeatedMisses: unresolved wanted requests with asks > 1
			  (SELECT count(*) FROM unanswered_wanted WHERE asks > 1),
			  -- ResolvedRequests: answered wanted requests
			  (SELECT count(*) FROM answered_wanted),
			  -- TotalRequests: total distinct wanted requests
			  (SELECT count(*) FROM wanted)`

// FarmBacklogNow reads the two stocks and the two flows the coverage
// scheduler is judged by.
//
// The dependency count comes from the same dependency_open CTE the scheduler
// hands work out of (authoringCoverageCTE), not from a second definition
// written to look like it. That is the whole point of the shared constant: a
// backlog counted from a different predicate than the queue would report a
// figure that never moves however hard the fleet runs.
func (p *PG) FarmBacklogNow(ctx context.Context, since, now time.Time) (FarmBacklog, error) {
	ctx, cancel := farmAggregateContext(ctx)
	defer cancel()
	return p.farmBacklogNow(ctx, since, now, farmAggregateTimeout)
}

func (p *PG) farmBacklogNow(ctx context.Context, since, now time.Time, statementTimeout time.Duration) (FarmBacklog, error) {
	backlog := FarmBacklog{ClaimedByKind: map[string]int{}, ClaimedByAxis: map[string]int{}}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := beginFarmAggregate(ctx, c, statementTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if err := tx.QueryRow(ctx, farmBacklogStocksSQL).Scan(
			&backlog.CoverageHoles,
			&backlog.Dependencies,
			&backlog.RequestBacklog,
			&backlog.RepeatedMisses,
			&backlog.ResolvedRequests,
			&backlog.TotalRequests,
		); err != nil {
			return err
		}
		// The same absence at the grain a reader sees it: symbol × version
		// cells rather than releases. Counted from the stored snapshots the
		// package pages render, so the panel and the page cannot disagree
		// about how many dashes there are.
		census, err := matrixCells(ctx, tx)
		if err != nil {
			return err
		}
		backlog.Matrix = census
		// Generation: what the scheduler actually handed out in the window,
		// by queue source. Read from claimed_at rather than from a counter,
		// so a restart does not reset it.
		claimed, err := tx.Query(ctx, `
			SELECT kind,axis,count(*) FROM authoring_assignments
			 WHERE claimed_at >= $1 AND claimed_at <= $2
			 GROUP BY 1,2`, since, now)
		if err != nil {
			return err
		}
		for claimed.Next() {
			var kind, axis string
			var n int
			if err := claimed.Scan(&kind, &axis, &n); err != nil {
				claimed.Close()
				return err
			}
			backlog.ClaimedByKind[kind] = n
			backlog.ClaimedByAxis[normalizeAuthoringAxis(axis)] += n
		}
		if err := claimed.Err(); err != nil {
			claimed.Close()
			return err
		}
		claimed.Close()

		// Resolution: coordinates that earned their FIRST passing receipt in
		// the window. Any-pass would count a coordinate re-proven on another
		// platform, which is real work that takes nothing off the stock above
		// -- and a flow that does not drain the stock printed beside it is a
		// number an operator cannot act on.
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM (
			  SELECT package.value AS purl, MIN(r.created_at) AS first_pass
			    FROM samples s
			    JOIN receipts r ON r.sample_id=s.sample_id AND r.contract_result='PASS'
			    CROSS JOIN LATERAL jsonb_array_elements_text(
			      CASE WHEN jsonb_typeof(s.manifest->'packages')='array'
			           THEN s.manifest->'packages' ELSE '[]'::jsonb END) AS package(value)
			   WHERE NOT s.quarantined
			   GROUP BY 1
			) f
			WHERE f.first_pass >= $1 AND f.first_pass <= $2`, since, now).Scan(&backlog.FirstProven)
	})
	return backlog, err
}

// FarmCompletenessNow counts every PUBLIC release by which of Sample,
// Evidence and Dependency it holds.
//
// "Proven" is read through authoringCoverageCTE rather than through a second
// definition written to look like it, for the reason that constant exists: a
// panel counting a different set from the queue it describes reports a figure
// that never moves however hard the fleet runs.
func (p *PG) FarmCompletenessNow(ctx context.Context) (FarmCompleteness, error) {
	ctx, cancel := farmAggregateContext(ctx)
	defer cancel()
	return p.farmCompletenessNow(ctx, farmAggregateTimeout)
}

func (p *PG) farmCompletenessNow(ctx context.Context, statementTimeout time.Duration) (FarmCompleteness, error) {
	out := newFarmCompleteness()
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := beginFarmAggregate(ctx, c, statementTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		rows, err := tx.Query(ctx, completenessClassifiedSQL+`
			SELECT state, proven_none, ecosystem, name,
			       count(*)
			FROM classified
			GROUP BY 1,2,3,4`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var state, ecosystem, name string
			var provenNone bool
			var n int
			if err := rows.Scan(&state, &provenNone, &ecosystem, &name, &n); err != nil {
				return err
			}
			dep := dependencyUnknown
			switch {
			case provenNone:
				dep = dependencyProvenNone
			case state[2] == 'D':
				dep = dependencyGraph
			}
			out.addResolved(state[0] == 'S', state[1] == 'E', dep, ecosystem, name, n)
		}
		return rows.Err()
	})
	return out, err
}
