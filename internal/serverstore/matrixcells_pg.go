package serverstore

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// matrixCellsQuery counts the compatibility grid straight out of
// compatibility_snapshots -- the same documents the package pages render.
//
// Shape notes, because two of them are load-bearing:
//
//   - Observations sum every key of observationClassCounts; verifications
//     read SAMPLE_VERIFICATION by name. "distinctVerifyingPeers" shares that
//     map and counts peers, so summing the map would let one sample verified
//     by three machines read as three runs. (*Fake).matrixCells reads the two
//     the same way, through snapshotCellEvidence.
//   - The grid is releases × symbols per package NAME, not the stored row
//     count. The page draws every symbol the network knows for a package
//     against every release of it, so the cells with no stored row -- the
//     plain dashes -- exist only as that difference.
//   - The RELEASE axis counts every release with any snapshot at all,
//     including one that has only the package-level row. That is what the
//     page draws: a version measured at package grain and never at symbol
//     grain still gets its column, and every symbol row shows a plain dash in
//     it. Restricting the axis to releases with symbol rows would drop
//     exactly the columns that are entirely dashes -- the ones this census
//     exists to count. The SYMBOL axis is the opposite: only real symbols,
//     because the package-level row is a total over them rather than one of
//     them.
//
// It is one statement returning one row. Written as a UNION of scalar
// aggregates it planned to 83 JIT-compiled functions and 1.37 s against
// production; folded into one row it is 37 functions and 930 ms, of which the
// work is 162 ms -- `SET jit=off` on the same 9,455 documents -- and the rest
// is PostgreSQL compiling a plan for a query that runs once. It is on an
// admin endpoint nobody polls, so the cost is recorded rather than tuned; if
// this ever moves somewhere hot it belongs in the snapshot pipeline that
// already materialises on CSX_SNAPSHOT_INTERVAL, not in a request.
const matrixCellsQuery = `
	WITH cell AS (
		SELECT p.ecosystem, p.name, cs.purl, cs.symbol,
		  COALESCE((
		    SELECT SUM((observed.value)::bigint)
		      FROM jsonb_array_elements(cs.snapshot->'rows') AS bucket(row)
		      CROSS JOIN LATERAL jsonb_each_text(
		        COALESCE(bucket.row->'observationClassCounts','{}'::jsonb)) AS observed(key,value)
		  ),0) AS observations,
		  COALESCE((
		    SELECT SUM(COALESCE((bucket.row->'verificationCounts'->>'SAMPLE_VERIFICATION')::bigint,0))
		      FROM jsonb_array_elements(cs.snapshot->'rows') AS bucket(row)
		  ),0) AS verifications
		FROM compatibility_snapshots cs
		JOIN packages p ON p.purl=cs.purl
		WHERE p.version<>'' AND p.publicness='PUBLIC'
	), grid AS (
		SELECT count(DISTINCT purl)*count(DISTINCT symbol) FILTER (WHERE symbol<>'') AS cells,
		       count(*) FILTER (WHERE symbol<>'') AS measured,
		       count(*) FILTER (WHERE symbol<>'' AND observations>0) AS observed,
		       count(*) FILTER (WHERE symbol<>'' AND observations=0 AND verifications>0) AS verified_only
		FROM cell GROUP BY ecosystem,name
	)
	SELECT COALESCE(SUM(cells),0), COALESCE(SUM(observed),0),
	       COALESCE(SUM(verified_only),0), COALESCE(SUM(cells-measured),0),
	       count(*) FILTER (WHERE verified_only>0 AND cells>measured)
	FROM grid`

// matrixCells counts the compatibility grid. The connection is the caller's
// so the farm panel reads every one of its numbers on one connection.
func matrixCells(ctx context.Context, c *pgx.Conn) (MatrixCells, error) {
	var census MatrixCells
	err := c.QueryRow(ctx, matrixCellsQuery).Scan(&census.Cells, &census.Observed,
		&census.VerifiedNoObservation, &census.Unmeasured, &census.PackagesShowingBothDashes)
	return census, err
}
