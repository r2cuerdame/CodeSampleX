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

# Migration 0024 preserves old derived rows instead of deleting them. The
# current builder collapses missing/legacy fingerprints to error_fp='', so
# only that row is live; non-empty legacy rows remain recoverable historical
# material and must not double the current cluster observation invariant.
invariants=$(docker compose exec -T db psql -U csx -d csx -Atqc "
SELECT json_build_object(
  'pass', COALESCE(SUM(observation_count) FILTER (WHERE result='PASS'),0),
  'fail', COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0),
  'publishedSamples', (SELECT count(*) FROM samples WHERE status='PUBLISHED'),
  'failureClusterObservations', (SELECT COALESCE(SUM(observation_count),0) FROM failure_clusters
    WHERE COALESCE(evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete')
       OR COALESCE(error_fp,'') = ''),
  'pgxParseConfigPass', COALESCE(SUM(observation_count) FILTER (
    WHERE purl='pkg:golang/github.com/jackc/pgx/v5@v5.10.0' AND symbol='ParseConfig' AND result='PASS'),0),
  'pgxParseConfigFail', COALESCE(SUM(observation_count) FILTER (
    WHERE purl='pkg:golang/github.com/jackc/pgx/v5@v5.10.0' AND symbol='ParseConfig' AND result='FAIL'),0)
)::text FROM evidence_agg")

modern_failure_clusters=0
failure_evidence_quality='{"available":false}'
if [ "$(docker compose exec -T db psql -U csx -d csx -Atqc \
  "SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='failure_clusters' AND column_name='termination_kind')")" = t ]; then
  modern_failure_clusters=$(docker compose exec -T db psql -U csx -d csx -Atqc "
    SELECT count(*) FROM failure_clusters
    WHERE evidence_quality IN ('complete','partial')
      AND termination_kind <> ''
      AND error_summary <> ''")
  failure_evidence_quality=$(docker compose exec -T db psql -U csx -d csx -Atqc "
    WITH quality AS (
      SELECT
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0) AS fail,
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='complete'),0) AS complete,
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='partial'),0) AS partial,
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='missing'),0) AS missing,
        COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL' AND evidence_quality='legacy-evidence-incomplete'),0) AS legacy
      FROM evidence_agg
    )
    SELECT json_build_object(
      'available', true,
      'fail', fail,
      'complete', complete,
      'partial', partial,
      'missing', missing,
      'legacyEvidenceIncomplete', legacy,
      'balanced', fail = complete + partial + missing + legacy
    )::text FROM quality")
fi

printf 'revision=%s\n' "$revision"
printf 'image_digest=%s\n' "$image_digest"
printf 'image_revision=%s\n' "$image_revision"
printf 'migration_version=%s\n' "$migration_version"
printf 'migration_count=%s\n' "$migration_count"
printf 'health=%s\n' "$health"
printf 'invariants=%s\n' "$invariants"
printf 'modern_failure_clusters=%s\n' "$modern_failure_clusters"
printf 'failure_evidence_quality=%s\n' "$failure_evidence_quality"
