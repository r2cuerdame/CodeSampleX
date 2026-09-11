#!/bin/sh
set -eu

cd /opt/codesamplex/deploy

revision=$(docker inspect codesamplex-server-1 --format '{{range .Config.Env}}{{println .}}{{end}}' |
  sed -n 's/^CSX_VERSION=//p' | head -n 1)
image_digest=$(docker inspect codesamplex-server-1 --format '{{.Image}}')
image_revision=$(docker image inspect "$image_digest" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
migration_version=$(docker compose exec -T db psql -U csx -d csx -Atqc \
  "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
migration_count=$(docker compose exec -T db psql -U csx -d csx -Atqc \
  "SELECT count(*) FROM schema_migrations")
health=$(docker compose exec -T server wget -qO- http://127.0.0.1:8080/healthz)
server_started_at=$(docker inspect codesamplex-server-1 --format '{{.State.StartedAt}}')
builder_generated_at=$(docker compose exec -T db psql -U csx -d csx -Atqc \
  "SELECT COALESCE(stats->>'generatedAt','') FROM stats_daily ORDER BY day DESC LIMIT 1")
server_started_epoch=$(date -u -d "$server_started_at" +%s 2>/dev/null || true)
builder_generated_epoch=$(date -u -d "$builder_generated_at" +%s 2>/dev/null || true)
builder_fresh=false
if [ -n "$server_started_epoch" ] && [ -n "$builder_generated_epoch" ] && \
   [ "$builder_generated_epoch" -ge "$server_started_epoch" ]; then
  builder_fresh=true
fi

# What the process answering requests says it was built from. The container
# environment above records what was configured; only this records what
# started. Tolerant on purpose: this collector also runs against the server
# that is about to be replaced, and a build older than /version must report
# unavailable rather than fail the probe that is measuring it.
served_revision=$(docker compose exec -T server wget -qO- http://127.0.0.1:8080/version 2>/dev/null |
  sed -n 's/.*"revision":"\([0-9a-f]\{40\}\)".*/\1/p' | head -n 1 || true)
if [ -z "$served_revision" ]; then served_revision=unavailable; fi

# Exact detail is optional evidence, not permission for unbounded census work.
# Each prefix includes a sentinel. No aggregate below can present a partial
# prefix as the full corpus, and the caller retains its SQL timeout. Prefixes
# have no ORDER BY, which could cause a full-table sort before LIMIT. Ordering
# does not affect completeness: only an exhaustive set can publish exact data.
bounded_ledger_scope="
WITH source_rows AS MATERIALIZED (
  SELECT result, observation_count, purl, symbol, evidence_quality
  FROM evidence_agg LIMIT 250001
), cluster_rows AS MATERIALIZED (
  SELECT observation_count, evidence_quality, error_fp,
    evidence_breakdown IS NULL OR (pg_column_size(evidence_breakdown) <= 4096
      AND pg_column_compression(evidence_breakdown) IS NULL) AS within_json_budget,
    CASE WHEN pg_column_size(evidence_breakdown) <= 4096
           AND pg_column_compression(evidence_breakdown) IS NULL THEN evidence_breakdown END AS evidence_breakdown
  FROM failure_clusters LIMIT 250001
), sample_rows AS MATERIALIZED (
  SELECT status FROM samples LIMIT 250001
), scope AS MATERIALIZED (
  SELECT (SELECT count(*) FROM source_rows) <= 250000
     AND (SELECT count(*) FROM cluster_rows) <= 250000
     AND (SELECT COALESCE(bool_and(within_json_budget),true) FROM cluster_rows)
     AND (SELECT count(*) FROM sample_rows) <= 250000 AS complete
), bounded_evidence AS MATERIALIZED (
  SELECT * FROM source_rows WHERE (SELECT complete FROM scope)
), current_clusters AS MATERIALIZED (
  SELECT * FROM cluster_rows
  WHERE (SELECT complete FROM scope)
    AND (COALESCE(evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete')
         OR COALESCE(error_fp,'') = '')
)"

# Migration 0024 preserves old derived rows instead of deleting them. The
# current builder collapses missing/legacy fingerprints to error_fp='', so
# only that row is live; non-empty legacy rows remain recoverable historical
# material and must not double the current cluster observation invariant.
invariants=$(docker compose exec -T db psql -U csx -d csx -Atqc "$bounded_ledger_scope
SELECT CASE WHEN (SELECT complete FROM scope) THEN json_build_object(
  'pass', COALESCE(SUM(observation_count) FILTER (WHERE result='PASS'),0),
  'fail', COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0),
  'publishedSamples', (SELECT count(*) FROM sample_rows WHERE status='PUBLISHED'),
  'failureClusterObservations', (SELECT COALESCE(SUM(observation_count),0) FROM current_clusters),
  'unbalancedFailureClusterRows', (SELECT count(*) FROM current_clusters fc
    CROSS JOIN LATERAL (
      SELECT COALESCE(SUM(CASE WHEN jsonb_typeof(item.value) = 'number'
                              THEN (item.value::text)::numeric ELSE 0 END),0) AS total,
        COALESCE(bool_or(item.value IS NOT NULL AND
          CASE WHEN jsonb_typeof(item.value) = 'number'
               THEN (item.value::text)::numeric < 0 ELSE true END),false) AS invalid_value
      FROM (VALUES (fc.evidence_breakdown->'complete'), (fc.evidence_breakdown->'partial'),
                   (fc.evidence_breakdown->'missing'), (fc.evidence_breakdown->'legacy-evidence-incomplete')) item(value)
    ) breakdown
    WHERE fc.observation_count IS NULL OR fc.observation_count <= 0
      OR CASE WHEN jsonb_typeof(fc.evidence_breakdown) IS DISTINCT FROM 'object' THEN true
              ELSE fc.evidence_breakdown - ARRAY['complete','partial','missing','legacy-evidence-incomplete'] <> '{}'::jsonb END
      OR breakdown.invalid_value
      OR fc.observation_count::numeric <> breakdown.total),
  'pgxParseConfigPass', COALESCE(SUM(observation_count) FILTER (
    WHERE purl='pkg:golang/github.com/jackc/pgx/v5@v5.10.0' AND symbol='ParseConfig' AND result='PASS'),0),
  'pgxParseConfigFail', COALESCE(SUM(observation_count) FILTER (
    WHERE purl='pkg:golang/github.com/jackc/pgx/v5@v5.10.0' AND symbol='ParseConfig' AND result='FAIL'),0)
)::text ELSE 'collection-budget-exceeded' END FROM bounded_evidence")
if [ "$invariants" = collection-budget-exceeded ]; then
  printf 'detail_budget_status=collection-budget-exceeded\n'
  exit 3
fi

modern_failure_clusters=0
failure_evidence_quality='{"available":false}'
if [ "$(docker compose exec -T db psql -U csx -d csx -Atqc \
  "SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='failure_clusters' AND column_name='termination_kind')")" = t ]; then
  modern_failure_clusters=$(docker compose exec -T db psql -U csx -d csx -Atqc "
    WITH cluster_rows AS MATERIALIZED (
      SELECT evidence_quality, termination_kind, error_summary
      FROM failure_clusters LIMIT 250001
    )
    SELECT CASE WHEN count(*) <= 250000 THEN
      count(*) FILTER (WHERE evidence_quality IN ('complete','partial')
      AND termination_kind <> ''
      AND error_summary <> '')::text ELSE 'collection-budget-exceeded' END FROM cluster_rows")
  if [ "$modern_failure_clusters" = collection-budget-exceeded ]; then
    printf 'detail_budget_status=collection-budget-exceeded\n'
    exit 3
  fi
  failure_evidence_quality=$(docker compose exec -T db psql -U csx -d csx -Atqc "$bounded_ledger_scope,
    quality AS (
      SELECT
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0) AS fail,
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='complete'),0) AS complete,
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='partial'),0) AS partial,
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='missing'),0) AS missing,
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='legacy-evidence-incomplete'),0) AS legacy
      FROM bounded_evidence
    )
    SELECT CASE WHEN (SELECT complete FROM scope) THEN json_build_object(
      'available', true,
      'fail', fail,
      'complete', complete,
      'partial', partial,
      'missing', missing,
      'legacyEvidenceIncomplete', legacy,
      'balanced', fail = complete + partial + missing + legacy
    )::text ELSE 'collection-budget-exceeded' END FROM quality")
  if [ "$failure_evidence_quality" = collection-budget-exceeded ]; then
    printf 'detail_budget_status=collection-budget-exceeded\n'
    exit 3
  fi
fi

printf 'revision=%s\n' "$revision"
printf 'image_digest=%s\n' "$image_digest"
printf 'image_revision=%s\n' "$image_revision"
printf 'migration_version=%s\n' "$migration_version"
printf 'migration_count=%s\n' "$migration_count"
printf 'health=%s\n' "$health"
printf 'server_started_at=%s\n' "$server_started_at"
printf 'builder_generated_at=%s\n' "$builder_generated_at"
printf 'builder_fresh=%s\n' "$builder_fresh"
printf 'served_revision=%s\n' "$served_revision"
printf 'invariants=%s\n' "$invariants"
printf 'modern_failure_clusters=%s\n' "$modern_failure_clusters"
printf 'failure_evidence_quality=%s\n' "$failure_evidence_quality"
