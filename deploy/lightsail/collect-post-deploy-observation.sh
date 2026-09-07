#!/bin/sh
set -eu

# One bounded, privacy-safe production observation sample. The caller supplies
# the observation start through a strictly validated environment value and may
# opt into the fixed-path latency probes only after builder convergence.
# Nothing here changes container, database, or deployment state.

cd /opt/codesamplex/deploy

container=codesamplex-server-1
observe_since=${CSX_OBSERVE_SINCE:?CSX_OBSERVE_SINCE is required}
include_latency=${CSX_OBSERVE_LATENCY:-0}
include_detail=${CSX_OBSERVE_DETAIL:-0}

revision=$(docker inspect "$container" --format '{{range .Config.Env}}{{println .}}{{end}}' |
  sed -n 's/^CSX_VERSION=//p' | head -n 1)
image_digest=$(docker inspect "$container" --format '{{.Image}}')
image_revision=$(docker image inspect "$image_digest" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
migration_version=$(docker compose exec -T db psql -U csx -d csx -Atqc \
  "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
health=$(docker compose exec -T server wget -qO- http://127.0.0.1:8080/healthz 2>/dev/null || true)
served_revision=$(docker compose exec -T server wget -qO- http://127.0.0.1:8080/version 2>/dev/null |
  sed -n 's/.*"revision":"\([0-9a-f]\{40\}\)".*/\1/p' | head -n 1 || true)

server_started_at=$(docker inspect "$container" --format '{{.State.StartedAt}}')
restart_count=$(docker inspect "$container" --format '{{.RestartCount}}')
oom_killed=$(docker inspect "$container" --format '{{.State.OOMKilled}}')
container_status=$(docker inspect "$container" --format '{{.State.Status}}')

builder_generated_at=$(docker compose exec -T db psql -U csx -d csx -Atqc \
  "SELECT COALESCE(stats->>'generatedAt','') FROM stats_daily ORDER BY day DESC LIMIT 1")
server_started_epoch=$(date -u -d "$server_started_at" +%s 2>/dev/null || true)
builder_generated_epoch=$(date -u -d "$builder_generated_at" +%s 2>/dev/null || true)
builder_fresh=false
if [ -n "$server_started_epoch" ] && [ -n "$builder_generated_epoch" ] && \
   [ "$builder_generated_epoch" -ge "$server_started_epoch" ]; then
  builder_fresh=true
fi

resource_sample=$(docker stats --no-stream --format '{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}' "$container")
cpu_percent=$(printf '%s\n' "$resource_sample" | cut -d '|' -f 1 | tr -d '%')
memory_usage=$(printf '%s\n' "$resource_sample" | cut -d '|' -f 2)
memory_percent=$(printf '%s\n' "$resource_sample" | cut -d '|' -f 3 | tr -d '%')
load_average=$(cut -d ' ' -f 1-3 /proc/loadavg)

detail_collected=false
pressure_lines=0
pool_busy_events=0
query_timeout_events=0
oom_events=0
restart_events=0
die_events=0
settled_fail_observations=0
settled_failure_cluster_observations=0
settled_unbalanced_failure_cluster_rows=0
if [ "$include_detail" = 1 ]; then
  detail_collected=true
  # Only counts leave the host. The existing pressure line contains a fixed
  # path and bounded counters, never a query string; returning the log itself
  # would unnecessarily widen that already privacy-reviewed boundary.
  pressure_lines=$(docker logs --since "$observe_since" "$container" 2>&1 |
    grep -c 'csx-server: db pressure ' || true)
  pool_busy_events=$(docker logs --since "$observe_since" "$container" 2>&1 |
    grep 'csx-server: db pressure ' | grep -Ec 'pool_busy=[1-9][0-9]*' || true)
  query_timeout_events=$(docker logs --since "$observe_since" "$container" 2>&1 |
    grep 'csx-server: db pressure ' | grep -Ec 'query_timeout=[1-9][0-9]*' || true)

  events_until=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  oom_events=$(docker events --since "$observe_since" --until "$events_until" \
    --filter "container=$container" --filter event=oom --format '{{.Action}}' | wc -l | tr -d ' ')
  restart_events=$(docker events --since "$observe_since" --until "$events_until" \
    --filter "container=$container" --filter event=restart --format '{{.Action}}' | wc -l | tr -d ' ')
  die_events=$(docker events --since "$observe_since" --until "$events_until" \
    --filter "container=$container" --filter event=die --format '{{.Action}}' | wc -l | tr -d ' ')

  # This is read only once the observation reaches a terminal state. On a
  # converged pass it proves the settled derived ledger is neither absent nor
  # internally unbalanced; the cheap polling path never scans these tables.
  settled_invariant=$(docker compose exec -T db psql -U csx -d csx -At -F '|' -c "
SELECT
  COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0),
  (SELECT COALESCE(SUM(observation_count),0) FROM failure_clusters
    WHERE COALESCE(evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete')
       OR COALESCE(error_fp,'') = ''),
  (SELECT count(*) FROM failure_clusters fc
    WHERE (COALESCE(fc.evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete')
           OR COALESCE(fc.error_fp,'') = '')
      AND (fc.observation_count <= 0
           OR EXISTS (
             SELECT 1 FROM jsonb_each(fc.evidence_breakdown) AS item(key, value)
             WHERE item.key NOT IN ('complete','partial','missing','legacy-evidence-incomplete')
                OR jsonb_typeof(item.value) <> 'number'
                OR CASE WHEN jsonb_typeof(item.value) = 'number'
                        THEN (item.value::text)::numeric < 0 ELSE false END)
           OR fc.observation_count::numeric <> COALESCE((
             SELECT SUM((item.value::text)::numeric)
             FROM jsonb_each(fc.evidence_breakdown) AS item(key, value)
             WHERE jsonb_typeof(item.value) = 'number'), 0)))
FROM evidence_agg")
  settled_fail_observations=$(printf '%s\n' "$settled_invariant" | cut -d '|' -f 1)
  settled_failure_cluster_observations=$(printf '%s\n' "$settled_invariant" | cut -d '|' -f 2)
  settled_unbalanced_failure_cluster_rows=$(printf '%s\n' "$settled_invariant" | cut -d '|' -f 3)
fi

printf 'observed_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'revision=%s\n' "$revision"
printf 'image_digest=%s\n' "$image_digest"
printf 'image_revision=%s\n' "$image_revision"
printf 'migration_version=%s\n' "$migration_version"
printf 'health=%s\n' "$health"
printf 'served_revision=%s\n' "$served_revision"
printf 'server_started_at=%s\n' "$server_started_at"
printf 'restart_count=%s\n' "$restart_count"
printf 'oom_killed=%s\n' "$oom_killed"
printf 'container_status=%s\n' "$container_status"
printf 'builder_generated_at=%s\n' "$builder_generated_at"
printf 'builder_fresh=%s\n' "$builder_fresh"
printf 'cpu_percent=%s\n' "$cpu_percent"
printf 'memory_usage=%s\n' "$memory_usage"
printf 'memory_percent=%s\n' "$memory_percent"
printf 'load_average=%s\n' "$load_average"
printf 'detail_collected=%s\n' "$detail_collected"
printf 'pressure_lines=%s\n' "$pressure_lines"
printf 'pool_busy_events=%s\n' "$pool_busy_events"
printf 'query_timeout_events=%s\n' "$query_timeout_events"
printf 'oom_events=%s\n' "$oom_events"
printf 'restart_events=%s\n' "$restart_events"
printf 'die_events=%s\n' "$die_events"
printf 'settled_fail_observations=%s\n' "$settled_fail_observations"
printf 'settled_failure_cluster_observations=%s\n' "$settled_failure_cluster_observations"
printf 'settled_unbalanced_failure_cluster_rows=%s\n' "$settled_unbalanced_failure_cluster_rows"

probe_latency() {
  key=$1
  path=$2
  result=000\|unavailable
  if measured=$(curl --noproxy '*' --connect-timeout 5 --max-time 25 \
      --resolve 'codesamplex.dev:443:127.0.0.1' -sS -o /dev/null \
      -w '%{http_code}|%{time_total}' "https://codesamplex.dev$path"); then
    result=$measured
  fi
  status=${result%%|*}
  seconds=${result#*|}
  printf 'latency_%s_status=%s\n' "$key" "$status"
  printf 'latency_%s_seconds=%s\n' "$key" "$seconds"
}

if [ "$include_latency" = 1 ]; then
  # Fixed public paths only: no identifiers, queries, request bodies, or
  # credentials can enter either the request or the evidence.
  probe_latency healthz /healthz
  probe_latency landing /
  probe_latency stats /v1/stats
  probe_latency wanted /v1/wanted
fi
