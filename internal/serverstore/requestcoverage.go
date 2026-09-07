package serverstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	AdminCoverageDays                  = 7
	adminCoveragePackageLimit          = 10
	adminCoverageCoordinatesPerPackage = 8
)

// AdminCoverageState is the current evidence state of a coordinate that was
// reported as a miss during the recent demand window. It is not the historical
// outcome of that request; hit telemetry intentionally carries no coordinate.
type AdminCoverageState string

const (
	AdminCoverageHit     AdminCoverageState = "hit"
	AdminCoveragePartial AdminCoverageState = "partial"
	AdminCoverageMiss    AdminCoverageState = "miss"
)

// AdminCoverageNode is one privacy-bounded requested coordinate. Impact is the
// number of retained reporter/day coordinate rows, not people, raw searches or
// cumulative samples. No anon_id leaves the aggregate query.
type AdminCoverageNode struct {
	Ecosystem          string
	Name               string
	Version            string
	Symbol             string
	TargetOS           string
	Impact             int64
	PackageImpact      int64
	PackageCoordinates int64
	LastDay            string
	State              AdminCoverageState
	Boundary           string
	NearestEnvironment string
	FailureStage       string
	FailureFingerprint string
	InMap              bool
	InTopMissing       bool
}

// AdminRequestCoverage is the bounded read model for the admin coverage map.
// PackageImpact includes every recent coordinate for a package. Nodes is the
// deduplicated union of map children and globally ranked missing coordinates;
// InMap and InTopMissing identify the two bounded projections.
type AdminRequestCoverage struct {
	WindowStart  string
	WindowEnd    string
	Nodes        []AdminCoverageNode
	TotalImpact  int64
	ShownImpact  int64
	PackageCount int64
}

// adminRequestCoverage reads a seven-UTC-calendar-day demand window. It first
// selects ten packages and eight coordinate children per package, plus the
// twenty highest-impact coordinates without an exact current pass, then joins
// only that bounded union to the existing compatibility snapshot aggregate.
// It never reparses samples or receipts and does not duplicate Wanted/Farm
// answer-resolution logic.
func adminRequestCoverage(ctx context.Context, conn *pgx.Conn, now time.Time) (AdminRequestCoverage, error) {
	now = now.UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -(AdminCoverageDays - 1))
	out := AdminRequestCoverage{
		WindowStart: start.Format("2006-01-02"),
		WindowEnd:   today.Format("2006-01-02"),
	}

	rows, err := conn.Query(ctx, `
		WITH recent_coordinates AS MATERIALIZED (
			SELECT ecosystem, name, version, symbol, target_os,
			       COUNT(*)::bigint AS impact, MAX(epoch) AS last_day
			  FROM wanted_dedup
			 WHERE epoch >= $1 AND epoch <= $2
			 GROUP BY ecosystem, name, version, symbol, target_os
		), ranked_packages AS MATERIALIZED (
			SELECT ecosystem, name, SUM(impact)::bigint AS package_impact,
			       COUNT(*)::bigint AS package_coordinates,
			       ROW_NUMBER() OVER (ORDER BY SUM(impact) DESC, ecosystem, name) AS package_rank
			  FROM recent_coordinates
			 GROUP BY ecosystem, name
		), recent_status AS MATERIALIZED (
			SELECT r.*, p.package_impact, p.package_coordinates, p.package_rank,
			       EXISTS (
			         SELECT 1
			           FROM packages pkg
			           JOIN compatibility_snapshots cs ON cs.purl=pkg.purl
			                AND cs.symbol=r.symbol
			           JOIN LATERAL jsonb_array_elements(
			                CASE WHEN jsonb_typeof(cs.snapshot->'rows')='array'
			                     THEN cs.snapshot->'rows' ELSE '[]'::jsonb END
			           ) AS exact_bucket(row) ON TRUE
			          WHERE pkg.ecosystem=r.ecosystem AND pkg.name=r.name
			            AND pkg.publicness='PUBLIC'
			            AND (r.version='' OR pkg.version=r.version)
			            AND (r.target_os='' OR
			                 CASE LOWER(COALESCE(exact_bucket.row #>> '{envBucket,os}', ''))
			                   WHEN 'linux' THEN 'linux'
			                   WHEN 'windows' THEN 'windows'
			                   WHEN 'darwin' THEN 'darwin'
			                   WHEN 'macos' THEN 'darwin'
			                   WHEN 'android' THEN 'android'
			                   WHEN 'ios' THEN 'ios'
			                   ELSE ''
			                 END = CASE LOWER(r.target_os)
			                         WHEN 'macos' THEN 'darwin'
			                         WHEN 'darwin' THEN 'darwin'
			                         ELSE LOWER(r.target_os)
			                       END)
			            AND COALESCE((exact_bucket.row #>> '{byStage,CONTRACT,pass}')::bigint,0) > 0
			       ) AS has_exact_pass
			  FROM recent_coordinates r
			  JOIN ranked_packages p USING(ecosystem,name)
		), top_packages AS MATERIALIZED (
			SELECT * FROM ranked_packages WHERE package_rank <= $3
		), ranked_coordinates AS MATERIALIZED (
			SELECT r.*,
			       ROW_NUMBER() OVER (
			         PARTITION BY r.ecosystem, r.name
			         ORDER BY r.impact DESC, r.last_day DESC, r.version, r.symbol, r.target_os
			       ) AS coordinate_rank
			  FROM recent_status r
			  JOIN top_packages p USING(ecosystem, name)
		), map_candidates AS MATERIALIZED (
			SELECT ecosystem,name,version,symbol,target_os
			  FROM ranked_coordinates WHERE coordinate_rank <= $4
		), missing_candidates AS MATERIALIZED (
			SELECT ecosystem,name,version,symbol,target_os
			  FROM recent_status
			 WHERE NOT has_exact_pass
			 ORDER BY impact DESC,last_day DESC,ecosystem,name,version,symbol,target_os
			 LIMIT 20
		), candidate_keys AS MATERIALIZED (
			SELECT ecosystem,name,version,symbol,target_os,
			       BOOL_OR(source='map') AS in_map,
			       BOOL_OR(source='missing') AS in_top_missing
			  FROM (
			    SELECT m.*,'map'::text AS source FROM map_candidates m
			    UNION ALL
			    SELECT m.*,'missing'::text AS source FROM missing_candidates m
			  ) candidates
			 GROUP BY ecosystem,name,version,symbol,target_os
		), demand AS MATERIALIZED (
			SELECT r.*, k.in_map, k.in_top_missing
			  FROM candidate_keys k
			  JOIN recent_status r USING(ecosystem,name,version,symbol,target_os)
		), package_facts AS MATERIALIZED (
			SELECT d.ecosystem,d.name,d.version,d.symbol,d.target_os,
			       EXISTS (
			         SELECT 1 FROM packages p
			         JOIN compatibility_snapshots cs ON cs.purl=p.purl
			          WHERE p.ecosystem=d.ecosystem AND p.name=d.name
			            AND p.publicness='PUBLIC'
			       ) AS has_package_snapshot,
			       EXISTS (
			         SELECT 1 FROM packages p
			         JOIN compatibility_snapshots cs ON cs.purl=p.purl
			          WHERE p.ecosystem=d.ecosystem AND p.name=d.name
			            AND p.publicness='PUBLIC'
			            AND (d.version='' OR p.version=d.version)
			       ) AS has_version_snapshot,
			       EXISTS (
			         SELECT 1 FROM packages p
			         JOIN compatibility_snapshots cs ON cs.purl=p.purl AND cs.symbol=d.symbol
			          WHERE p.ecosystem=d.ecosystem AND p.name=d.name
			            AND p.publicness='PUBLIC'
			            AND (d.version='' OR p.version=d.version)
			       ) AS has_exact_snapshot
			  FROM demand d
		), exact_snapshot_rows AS MATERIALIZED (
			SELECT d.ecosystem, d.name, d.version, d.symbol, d.target_os,
			       p.version AS evidence_version, cs.symbol AS evidence_symbol,
			       bucket.row,
			       CASE LOWER(COALESCE(bucket.row #>> '{envBucket,os}', ''))
			         WHEN 'linux' THEN 'linux'
			         WHEN 'windows' THEN 'windows'
			         WHEN 'darwin' THEN 'darwin'
			         WHEN 'macos' THEN 'darwin'
			         WHEN 'android' THEN 'android'
			         WHEN 'ios' THEN 'ios'
			         ELSE ''
			       END AS evidence_os
			  FROM demand d
			  JOIN packages p ON p.ecosystem=d.ecosystem AND p.name=d.name
			                 AND p.publicness='PUBLIC'
			                 AND (d.version='' OR p.version=d.version)
			  JOIN compatibility_snapshots cs ON cs.purl=p.purl AND cs.symbol=d.symbol
			  LEFT JOIN LATERAL jsonb_array_elements(
			       CASE WHEN jsonb_typeof(cs.snapshot->'rows')='array'
			            THEN cs.snapshot->'rows' ELSE '[]'::jsonb END
			  ) AS bucket(row) ON TRUE
		), snapshot_facts AS MATERIALIZED (
			SELECT d.ecosystem, d.name, d.version, d.symbol, d.target_os,
			       p.has_package_snapshot,p.has_version_snapshot,p.has_exact_snapshot,
			       COALESCE(BOOL_OR(
			          COALESCE((s.row #>> '{byStage,CONTRACT,pass}')::bigint,0) > 0),FALSE) AS has_pass_any_env,
			       COALESCE(BOOL_OR(
			          (d.target_os='' OR s.evidence_os=CASE LOWER(d.target_os)
			               WHEN 'macos' THEN 'darwin' WHEN 'darwin' THEN 'darwin'
			               ELSE LOWER(d.target_os) END)
			          AND COALESCE((s.row #>> '{byStage,CONTRACT,fail}')::bigint,0) > 0),FALSE) AS has_exact_fail,
			       MIN(NULLIF(s.evidence_os,'')) FILTER (
			          WHERE COALESCE((s.row #>> '{byStage,CONTRACT,pass}')::bigint,0) > 0) AS nearest_environment
			  FROM demand d
			  JOIN package_facts p USING(ecosystem,name,version,symbol,target_os)
			  LEFT JOIN exact_snapshot_rows s USING(ecosystem,name,version,symbol,target_os)
			 GROUP BY d.ecosystem,d.name,d.version,d.symbol,d.target_os,
			          p.has_package_snapshot,p.has_version_snapshot,p.has_exact_snapshot
		), failure_ranked AS MATERIALIZED (
			SELECT d.ecosystem,d.name,d.version,d.symbol,d.target_os,
			       COALESCE(failure->>'stage','') AS stage,
			       COALESCE(failure->>'fingerprint','') AS error_fp,
			       ROW_NUMBER() OVER (
			         PARTITION BY d.ecosystem,d.name,d.version,d.symbol,d.target_os
			         ORDER BY COALESCE(NULLIF(failure->>'reporters','')::bigint,0) DESC,
			                  COALESCE(NULLIF(failure->>'count','')::bigint,0) DESC,
			                  COALESCE(failure->>'stage',''),COALESCE(failure->>'fingerprint','')
			       ) AS failure_rank
			  FROM demand d
			  JOIN packages p ON p.ecosystem=d.ecosystem AND p.name=d.name
			       AND p.publicness='PUBLIC' AND (d.version='' OR p.version=d.version)
			  JOIN compatibility_snapshots cs ON cs.purl=p.purl AND cs.symbol=d.symbol
			  JOIN LATERAL jsonb_array_elements(
			       CASE WHEN jsonb_typeof(cs.snapshot->'failures')='array'
			            THEN cs.snapshot->'failures' ELSE '[]'::jsonb END
			  ) AS failures(failure) ON TRUE
			 WHERE d.target_os='' OR
			       CASE LOWER(COALESCE(failure #>> '{envSummary,os}', ''))
			         WHEN 'macos' THEN 'darwin' WHEN 'darwin' THEN 'darwin'
			         ELSE LOWER(COALESCE(failure #>> '{envSummary,os}', ''))
			       END = CASE LOWER(d.target_os)
			               WHEN 'macos' THEN 'darwin' WHEN 'darwin' THEN 'darwin'
			               ELSE LOWER(d.target_os) END
		), coverage AS (
			SELECT d.*, f.has_package_snapshot, f.has_version_snapshot,
			       f.has_exact_snapshot, f.has_pass_any_env,
			       f.has_exact_fail, COALESCE(f.nearest_environment,'') AS nearest_environment,
			       COALESCE(fr.stage,'') AS failure_stage,
			       COALESCE(fr.error_fp,'') AS failure_fingerprint
			  FROM demand d
			  JOIN snapshot_facts f USING(ecosystem,name,version,symbol,target_os)
			  LEFT JOIN (SELECT * FROM failure_ranked WHERE failure_rank=1) fr
			       USING(ecosystem,name,version,symbol,target_os)
		)
		SELECT ecosystem,name,version,symbol,target_os,impact,package_impact,
		       package_coordinates,last_day,
		       CASE WHEN has_exact_pass THEN 'hit'
		            WHEN has_package_snapshot THEN 'partial'
		            ELSE 'miss' END AS state,
		       CASE WHEN has_exact_pass THEN 'exact_pass'
		            WHEN has_pass_any_env AND target_os<>'' THEN 'environment_gap'
		            WHEN has_exact_fail THEN 'fail_only'
		            WHEN symbol<>'' AND has_version_snapshot AND NOT has_exact_snapshot THEN 'symbol_gap'
		            WHEN version<>'' AND NOT has_version_snapshot THEN 'version_gap'
		            WHEN has_exact_snapshot THEN 'evidence_only'
		            WHEN has_package_snapshot THEN 'nearby_coverage'
		            ELSE 'no_evidence' END AS boundary,
		       nearest_environment,failure_stage,failure_fingerprint,in_map,in_top_missing,
		       (SELECT COALESCE(SUM(impact),0) FROM recent_coordinates) AS total_impact,
		       (SELECT COUNT(*) FROM top_packages) AS package_count
		  FROM coverage
		 ORDER BY in_map DESC,package_rank,impact DESC,last_day DESC,ecosystem,name,version,symbol,target_os`,
		out.WindowStart, out.WindowEnd, adminCoveragePackageLimit, adminCoverageCoordinatesPerPackage)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var node AdminCoverageNode
		if err := rows.Scan(&node.Ecosystem, &node.Name, &node.Version, &node.Symbol,
			&node.TargetOS, &node.Impact, &node.PackageImpact, &node.PackageCoordinates,
			&node.LastDay, &node.State, &node.Boundary, &node.NearestEnvironment,
			&node.FailureStage, &node.FailureFingerprint, &node.InMap,
			&node.InTopMissing, &out.TotalImpact,
			&out.PackageCount); err != nil {
			return out, err
		}
		if node.InMap {
			out.ShownImpact += node.Impact
		}
		out.Nodes = append(out.Nodes, node)
	}
	return out, rows.Err()
}
