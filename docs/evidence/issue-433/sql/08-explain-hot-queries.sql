SET statement_timeout = '90s';
\echo ===EXPLAIN_SNAPSHOTS_BY_PURL
EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, SUMMARY ON) SELECT purl, symbol, snapshot::text FROM compatibility_snapshots WHERE purl = 'pkg:npm/lru-cache@11.5.2' ORDER BY symbol;
\echo ===EXPLAIN_FC_BY_PACKAGE
EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, SUMMARY ON) SELECT id FROM failure_clusters WHERE package_name='lru-cache' AND (COALESCE(evidence_quality,'') NOT IN ('missing','legacy-evidence-incomplete') OR COALESCE(error_fp,'') = '') ORDER BY observation_count DESC, id;
\echo ===FC_PKG_ROWCOUNT_DIST
select percentile_cont(0.5) within group (order by n) p50, percentile_cont(0.9) within group (order by n) p90, percentile_cont(0.99) within group (order by n) p99, max(n) from (select package_name, count(*) n from failure_clusters group by 1) d;
\echo ===FC_TOP_PACKAGES
select package_name, count(*) from failure_clusters group by 1 order by 2 desc limit 8;
\echo ===EXPLAIN_DEPENDENCIES_PAGE
EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, SUMMARY ON) SELECT ecosystem, child_name, child_version, count(DISTINCT parent_name || '@' || parent_version) AS parents, count(*) AS projects, count(*) OVER () AS full_count FROM dependency_edge WHERE ('' = '' OR position(lower('') in lower(child_name)) > 0) GROUP BY 1, 2, 3 ORDER BY projects DESC, parents DESC, child_name, child_version, ecosystem LIMIT 50 OFFSET 0;
