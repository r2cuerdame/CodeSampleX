#!/bin/sh
set -eu

# Collect top PostgreSQL slow queries from pg_stat_statements with execution
# statistics, I/O time, buffer cache hit ratios, and query normalization.
#
# Query texts in pg_stat_statements are already parameterized ($1, $2) by
# PostgreSQL, so literal values and potential secrets are not stored or emitted.
# Parameter values are also stripped by log_parameter_max_length=0 in logs.

LIMIT=${1:-15}
SORT_COL=${2:-total_exec_time}

cd /opt/codesamplex/deploy 2>/dev/null || true

docker_bin=$(command -v docker || echo "docker")

"$docker_bin" compose exec -T db psql -U csx -d csx -c "
SELECT
  queryid,
  calls,
  round(total_exec_time::numeric, 2) AS total_ms,
  round(mean_exec_time::numeric, 2) AS mean_ms,
  round(max_exec_time::numeric, 2) AS max_ms,
  round((shared_blk_read_time + shared_blk_write_time)::numeric, 2) AS io_ms,
  shared_blks_hit,
  shared_blks_read,
  round(shared_blks_hit::numeric / nullif(shared_blks_hit + shared_blks_read, 0) * 100, 1) AS hit_pct,
  temp_blks_read + temp_blks_written AS temp_blks,
  LEFT(regexp_replace(query, '\s+', ' ', 'g'), 120) AS query_preview
FROM pg_stat_statements
ORDER BY ${SORT_COL} DESC
LIMIT ${LIMIT};
"
