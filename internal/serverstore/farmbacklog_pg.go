package serverstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// FarmBacklogNow reads the two stocks and the two flows the coverage
// scheduler is judged by.
//
// The dependency count comes from the same dependency_open CTE the scheduler
// hands work out of (authoringCoverageCTE), not from a second definition
// written to look like it. That is the whole point of the shared constant: a
// backlog counted from a different predicate than the queue would report a
// figure that never moves however hard the fleet runs.
func (p *PG) FarmBacklogNow(ctx context.Context, since, now time.Time) (FarmBacklog, error) {
	backlog := FarmBacklog{ClaimedByKind: map[string]int{}}
	err := p.withConn(ctx, func(c *pgx.Conn) error {
		if err := c.QueryRow(ctx, `
			WITH `+authoringCoverageCTE+`
			SELECT
			  -- The `+"`-`"+` cells: a PUBLIC release the network watches people
			  -- use and has never proven.
			  (SELECT count(*) FROM (
			     SELECT DISTINCT pk.purl
			     FROM packages pk
			     JOIN evidence_agg e ON e.purl=pk.purl
			     WHERE pk.version<>'' AND pk.publicness='PUBLIC'
			       AND NOT EXISTS (SELECT 1 FROM verified_packages v WHERE v.purl=pk.purl)
			   ) h),
			  -- The dependency backlog, whole rather than capped.
			  (SELECT count(*) FROM (
			     SELECT DISTINCT ecosystem,child_name,child_version FROM dependency_open
			   ) d)`).Scan(&backlog.CoverageHoles, &backlog.Dependencies); err != nil {
			return err
		}
		// The same absence at the grain a reader sees it: symbol × version
		// cells rather than releases. Counted from the stored snapshots the
		// package pages render, so the panel and the page cannot disagree
		// about how many dashes there are.
		census, err := matrixCells(ctx, c)
		if err != nil {
			return err
		}
		backlog.Matrix = census
		// Generation: what the scheduler actually handed out in the window,
		// by queue source. Read from claimed_at rather than from a counter,
		// so a restart does not reset it.
		claimed, err := c.Query(ctx, `
			SELECT kind,count(*) FROM authoring_assignments
			 WHERE claimed_at >= $1 AND claimed_at <= $2
			 GROUP BY 1`, since, now)
		if err != nil {
			return err
		}
		for claimed.Next() {
			var kind string
			var n int
			if err := claimed.Scan(&kind, &n); err != nil {
				claimed.Close()
				return err
			}
			backlog.ClaimedByKind[kind] = n
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
		return c.QueryRow(ctx, `
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
