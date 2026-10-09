#!/bin/sh
set -eu

# The observer embeds this exact dependency before the shell collector.
if ! command -v csx_collect_failure_ledger >/dev/null 2>&1; then
  csx_ledger_program="$(CDPATH= cd -- "$(dirname "$0")" && pwd)/collect-failure-ledger.py"
  csx_collect_failure_ledger() {
    timeout --kill-after=5s 30s python3 "$csx_ledger_program" "$1"
  }
fi

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

# Exact failure-cluster pages share a read-only snapshot with the existing
# capped source/sample censuses; all work shares the existing SQL budget.
if ledger_detail=$(csx_collect_failure_ledger extended 2>/dev/null); then
  invariants=$(printf '%s\n' "$ledger_detail" | sed -n 's/^invariants=//p')
  modern_failure_clusters=$(printf '%s\n' "$ledger_detail" | sed -n 's/^modern_failure_clusters=//p')
  failure_evidence_quality=$(printf '%s\n' "$ledger_detail" | sed -n 's/^failure_evidence_quality=//p')
else
  printf 'detail_budget_status=collection-budget-exceeded\n'
  exit 3
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
