package serverstore

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// dependencyAxisOpenSQL selects the coordinates whose dependency axis nothing
// has answered, with the passing sample that could answer them.
//
// The two ways the axis can already be closed are named once, in
// completenessRelationsCTE, and read here: appearing as a PARENT in
// dependency_edge, or carrying a dependency_resolution row that says the
// resolver looked and found nothing. That is the same pair the census and the
// /gaps page classify by, which is the property this whole area exists to
// keep -- the queue and the number printed beside it read one definition.
//
// What it does NOT decide here is applicability. The ecosystem rule lives in
// internal/domain and is applied by dependencyAxisAdmit, in Go, for both
// stores. $1 is the attempt ceiling and $2 the row cap.
const dependencyAxisOpenSQL = `
			WITH ` + completenessRelationsCTE + `, live_or_spent AS MATERIALIZED (
				-- A sample the fleet is already answering, or has answered as
				-- often as this scheduler is allowed to ask. Counting job ROWS
				-- rather than a status is deliberate: a job that came back
				-- without a tree still spent an attempt, and a ceiling that
				-- only counted failures would never stop.
				SELECT sample_id
				  FROM verification_jobs
				 GROUP BY sample_id
				HAVING count(*) FILTER (WHERE status IN ('open','claimed')) > 0
				    OR count(*) FILTER (WHERE reason='cross') >= $1
			), axis_open AS MATERIALIZED (
				-- The open set, before it is ranked. It is small -- one row
				-- per (proven coordinate, sample) with no dependency answer --
				-- which is what makes the per-row demand lookup below cheap.
				SELECT pk.purl, pk.ecosystem, pk.name, pk.version, sp.sample_id
				  FROM sample_packages sp
				  JOIN samples s ON s.sample_id=sp.sample_id AND NOT s.quarantined
				  JOIN packages pk ON pk.purl=sp.purl
				 WHERE ` + completenessSubjectSQL + `
				   AND EXISTS (SELECT 1 FROM receipts r
				                WHERE r.sample_id=s.sample_id AND r.contract_result='PASS')
				   AND NOT EXISTS (SELECT 1 FROM dependency_edge_parents dep WHERE dep.purl=pk.purl)
				   AND NOT EXISTS (SELECT 1 FROM resolved_none rn WHERE rn.purl=pk.purl)
				   AND NOT EXISTS (SELECT 1 FROM live_or_spent l WHERE l.sample_id=sp.sample_id)
			)
			-- The same weighting the candidate query ranks sample work by: a
			-- sighting somebody CHOSE counts a thousand carried ones.
			-- Mirrored by (*Fake).packageDemand.
			--
			-- Read per candidate through evidence_agg_target_idx rather than
			-- as a GROUP BY over the whole table. evidence_agg is the corpus's
			-- largest relation -- reading it whole is the ~700MB scan that
			-- starved the candidate query for a week (#173) -- and this runs
			-- on every builder pass on a two-core host, so an aggregate whose
			-- cost is the corpus rather than the backlog does not belong in
			-- it.
			SELECT a.ecosystem, a.name, a.version, a.sample_id,
			       COALESCE((SELECT SUM(e.observation_count * CASE WHEN e.direct THEN 1000 ELSE 1 END)
			                   FROM evidence_agg e WHERE e.purl=a.purl),0) AS score
			  FROM axis_open a
			 ORDER BY score DESC, a.ecosystem, a.name, a.version, a.sample_id
			 LIMIT $2`

// DependencyAxisOpen lists the coordinates whose dependency axis is open.
//
// Read under the farm-aggregate budget rather than a poll's: it answers the
// builder, not a request, and no worker is waiting on it. A pass that cannot
// finish returns the error and the next pass tries again -- the backlog is a
// stock, so nothing is lost by arriving late to it.
func (p *PG) DependencyAxisOpen(ctx context.Context, maxAttempts, limit int) ([]DependencyAxisWork, error) {
	if limit < 1 {
		return nil, nil
	}
	ctx, cancel := farmAggregateContext(ctx)
	defer cancel()
	var rows []DependencyAxisWork
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		tx, err := beginFarmAggregate(ctx, c, farmAggregateTimeout)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		// Read more rows than the cap so the shared admission rule still has
		// choices after it drops the unscannable ecosystems and folds several
		// coordinates of one sample together. Without the headroom a pass
		// whose first rows were all Maven would open nothing at all while the
		// npm backlog sat behind them.
		q, err := tx.Query(ctx, dependencyAxisOpenSQL, maxAttempts, limit*8)
		if err != nil {
			return err
		}
		defer q.Close()
		for q.Next() {
			var w DependencyAxisWork
			if err := q.Scan(&w.Ecosystem, &w.Name, &w.Version, &w.SampleID, &w.Score); err != nil {
				return err
			}
			rows = append(rows, w)
		}
		return q.Err()
	})
	if err != nil {
		return nil, err
	}
	return dependencyAxisAdmit(rows, limit), nil
}
