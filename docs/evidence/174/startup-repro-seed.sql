\set ON_ERROR_STOP on
SET TIME ZONE 'UTC';

-- Synthetic, production-shaped startup fixture for issue #174.
-- Run only in a disposable schema that has been migrated by v0.1.149
-- (0035 is the latest recorded migration). No production data is used.
--
-- Published lower bounds used for the shape:
--   7,800 samples and receipts; 19,102 snapshot targets; 1,800 package
--   identities (inside the published 178-2,617 range). The 166,333 evidence
--   and cluster rows are a declared synthetic choice used to approach the
--   published 220 MiB evidence_agg and 271 MiB failure_clusters footprints.

BEGIN;

INSERT INTO samples(sample_id, manifest, status, license, size_bytes, hot_score, created_at, updated_at)
SELECT
  'repro-sample-' || lpad(i::text, 8, '0'),
  jsonb_build_object(
    'schemaVersion', 1,
    'packages', jsonb_build_array('pkg:npm/repro-pkg-' || (i % 1800) || '@1.0.' || i),
    'symbols', jsonb_build_array('repro.call.' || (i % 32)),
    'subject', 'pkg:npm/repro-pkg-' || (i % 1800) || '@1.0.' || i,
    'license', 'MIT-0'
  ),
  'CROSS_PASS', 'MIT-0', 1024, 0,
  now() - interval '2 days', now() - interval '2 days'
FROM generate_series(1, 7800) AS g(i);

INSERT INTO sample_packages(sample_id, purl, coord)
SELECT
  'repro-sample-' || lpad(i::text, 8, '0'),
  'pkg:npm/repro-pkg-' || (i % 1800) || '@1.0.' || i,
  'pkg:npm/repro-pkg-' || (i % 1800) || '@'
FROM generate_series(1, 7800) AS g(i);

INSERT INTO receipts(receipt_id, sample_id, peer_id, env_hash, receipt, contract_result, created_at)
SELECT
  'repro-receipt-' || lpad(i::text, 8, '0'),
  'repro-sample-' || lpad(i::text, 8, '0'),
  'ed25519:repro-' || (i % 64),
  md5('env-' || (i % 16)),
  jsonb_build_object(
    'schemaVersion', 2,
    'sampleId', 'repro-sample-' || lpad(i::text, 8, '0'),
    'stages', jsonb_build_object('resolve', 'PASS', 'compile', 'PASS', 'contract', 'PASS'),
    'resolvedPackages', jsonb_build_array('pkg:npm/repro-pkg-' || (i % 1800) || '@1.0.' || i),
    'environment', jsonb_build_object('ecosystem', 'npm', 'os', 'linux', 'arch', 'amd64'),
    'verifierAdapter', 'node-typescript@1'
  ),
  'PASS', now() - interval '1 day'
FROM generate_series(1, 7800) AS g(i);

WITH source AS (
  SELECT i,
         i % 1800 AS package_no,
         array_to_string(ARRAY(
           SELECT md5(i::text || '-' || j::text)
           FROM generate_series(1, 16) AS h(j)
         ), '') AS pad
  FROM generate_series(1, 166333) AS g(i)
)
INSERT INTO evidence_agg(
  purl, symbol, symbol_confidence, env_hash, env_json, stage, result,
  error_fp, error_code, observation_count, unique_peer_buckets,
  unique_project_buckets, first_seen, last_seen)
SELECT
  'pkg:npm/repro-pkg-' || package_no || '@1.0.' || i,
  'repro.call.' || (i % 32), 'PROBABLE', md5('env-' || (i % 16)),
  jsonb_build_object(
    'schemaVersion', 1, 'ecosystem', 'npm', 'os', 'linux',
    'arch', 'amd64', 'runtime', 'node', 'runtimeVersion', '22.18.1',
    'syntheticPadding', pad
  ),
  'PROJECT_COMPILE', 'FAIL', md5('failure-' || i), 'EREPRO',
  2 + (i % 9), 1, 1, now() - interval '2 days', now() - interval '1 day'
FROM source;

INSERT INTO compatibility_snapshots(purl, symbol, snapshot, generated_at)
SELECT
  'pkg:npm/repro-pkg-' || (i % 1800) || '@1.0.' || i,
  'repro.call.' || (i % 32),
  jsonb_build_object(
    'schemaVersion', 1,
    'package', 'pkg:npm/repro-pkg-' || (i % 1800) || '@1.0.' || i,
    'symbol', 'repro.call.' || (i % 32),
    'result', 'FAIL',
    'syntheticPadding', array_to_string(ARRAY(
      SELECT md5(i::text || '-snapshot-' || j::text)
      FROM generate_series(1, 16) AS h(j)
    ), '')
  ),
  now() - interval '1 day'
FROM generate_series(1, 19102) AS g(i);

WITH source AS (
  SELECT i,
         i % 1800 AS package_no,
         array_to_string(ARRAY(
           SELECT md5(i::text || '-cluster-' || j::text)
           FROM generate_series(1, 32) AS h(j)
         ), '') AS pad
  FROM generate_series(1, 166333) AS g(i)
)
INSERT INTO failure_clusters(
  ecosystem, package_name, symbol, stage, error_fp, error_code,
  observation_count, env_summary, hypotheses, regression_candidate,
  versions, first_seen, last_seen)
SELECT
  'npm', 'repro-pkg-' || package_no, 'repro.call.' || (i % 32),
  'PROJECT_COMPILE', md5('cluster-' || i), 'EREPRO', 2 + (i % 9),
  jsonb_build_object('linux/amd64/node-22', 1, 'syntheticPadding', pad),
  jsonb_build_array('synthetic startup reproduction only'), false,
  jsonb_build_object('1', jsonb_build_object('fail', 1)),
  now() - interval '2 days', now() - interval '1 day'
FROM source;

INSERT INTO stats_daily(day, stats)
VALUES (current_date, jsonb_build_object(
  'pass', 1315607,
  'fail', 311549,
  'publishedSamples', 7800,
  'failureClusterObservations', 311447,
  'generatedAt', to_char(now() - interval '1 day', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
))
ON CONFLICT (day) DO UPDATE SET stats = EXCLUDED.stats;

COMMIT;

ANALYZE samples;
ANALYZE receipts;
ANALYZE evidence_agg;
ANALYZE compatibility_snapshots;
ANALYZE failure_clusters;

CREATE OR REPLACE FUNCTION csx_startup_repro_observation()
RETURNS jsonb
LANGUAGE plpgsql
AS $$
DECLARE
  migration_visible boolean;
  columns_visible boolean;
  stale_samples bigint;
  stale_receipts bigint;
BEGIN
  SELECT EXISTS(
    SELECT 1 FROM schema_migrations
    WHERE version='0036_builder_projections.sql'
  ) INTO migration_visible;
  SELECT EXISTS(
    SELECT 1 FROM information_schema.columns
    WHERE table_schema=current_schema()
      AND column_name='builder_source_hash'
      AND table_name IN ('samples', 'receipts')
    GROUP BY table_schema
    HAVING count(*)=2
  ) INTO columns_visible;
  IF columns_visible THEN
    EXECUTE 'SELECT count(*) FROM samples WHERE builder_source_hash IS DISTINCT FROM md5(manifest::text)'
      INTO stale_samples;
    EXECUTE 'SELECT count(*) FROM receipts WHERE builder_source_hash IS DISTINCT FROM md5(receipt::text)'
      INTO stale_receipts;
  END IF;
  RETURN jsonb_build_object(
    'observedAt', clock_timestamp(),
    'observedAtNs', floor(extract(epoch FROM clock_timestamp()) * 1000000000)::bigint,
    'migration0036Visible', migration_visible,
    'builderColumnsVisible', columns_visible,
    'staleSamples', stale_samples,
    'staleReceipts', stale_receipts,
    'activity', coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'pid', pid,
        'state', state,
        'waitEventType', wait_event_type,
        'waitEvent', wait_event,
        'queryAgeMs', round(extract(epoch FROM (clock_timestamp()-query_start))*1000),
        'query', replace(left(query, 240), E'\n', ' ')
      ) ORDER BY pid)
      FROM pg_stat_activity
      WHERE datname=current_database()
        AND application_name<>'csx_startup_repro_observer'
        AND pid<>pg_backend_pid()
    ), '[]'::jsonb),
    'indexProgress', coalesce((
      SELECT jsonb_agg(jsonb_build_object(
        'pid', p.pid,
        'phase', p.phase,
        'blocksTotal', p.blocks_total,
        'blocksDone', p.blocks_done,
        'relation', c.relname,
        'index', i.relname
      ) ORDER BY p.pid)
      FROM pg_stat_progress_create_index p
      JOIN pg_class c ON c.oid=p.relid
      LEFT JOIN pg_class i ON i.oid=p.index_relid
    ), '[]'::jsonb),
    'ungrantedLocks', (
      SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a USING (pid)
      WHERE a.datname=current_database() AND NOT l.granted
    ),
    'blockingEdges', (
      SELECT count(*) FROM pg_stat_activity a
      CROSS JOIN LATERAL unnest(pg_blocking_pids(a.pid)) blocker
      WHERE a.datname=current_database()
    ),
    'database', (
      SELECT jsonb_build_object(
        'sessions', numbackends,
        'xactCommit', xact_commit,
        'xactRollback', xact_rollback,
        'blocksRead', blks_read,
        'blocksHit', blks_hit,
        'tempFiles', temp_files,
        'tempBytes', temp_bytes,
        'deadlocks', deadlocks,
        'checksumFailures', checksum_failures
      ) FROM pg_stat_database WHERE datname=current_database()
    ),
    'wal', (
      SELECT jsonb_build_object(
        'records', wal_records,
        'fpi', wal_fpi,
        'bytes', wal_bytes,
        'buffersFull', wal_buffers_full
      ) FROM pg_stat_wal
    )
  );
END
$$;

SELECT jsonb_build_object(
  'schema', current_schema(),
  'migrationCount', (SELECT count(*) FROM schema_migrations),
  'migrationVersion', (SELECT max(version) FROM schema_migrations),
  'samples', (SELECT count(*) FROM samples),
  'receipts', (SELECT count(*) FROM receipts),
  'evidenceAgg', (SELECT count(*) FROM evidence_agg),
  'snapshots', (SELECT count(*) FROM compatibility_snapshots),
  'failureClusters', (SELECT count(*) FROM failure_clusters),
  'samplesBytes', pg_total_relation_size('samples'),
  'receiptsBytes', pg_total_relation_size('receipts'),
  'evidenceAggBytes', pg_total_relation_size('evidence_agg'),
  'snapshotsBytes', pg_total_relation_size('compatibility_snapshots'),
  'failureClustersBytes', pg_total_relation_size('failure_clusters'),
  'schemaBytes', (SELECT sum(pg_total_relation_size(
    format('%I.%I', schemaname, tablename)::regclass))
    FROM pg_tables WHERE schemaname=current_schema())
) AS synthetic_fixture;
