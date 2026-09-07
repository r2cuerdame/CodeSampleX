#!/bin/sh
set -eu

# One bounded, privacy-safe production observation sample. The caller supplies
# the observation start through a strictly validated environment value and may
# opt into fixed public-route TTFB probes while builder work is active or after
# convergence.
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
builder_log_before=$(docker logs --since "$observe_since" --timestamps "$container" 2>&1 || true)
builder_lifecycle_before=$(printf '%s\n' "$builder_log_before" |
  grep -E 'compatibility: builder (pass start|pass complete|run:)' | tail -n 1 || true)

resource_sample=$(docker stats --no-stream --format '{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}' "$container")
cpu_percent=$(printf '%s\n' "$resource_sample" | cut -d '|' -f 1 | tr -d '%')
memory_usage=$(printf '%s\n' "$resource_sample" | cut -d '|' -f 2)
memory_percent=$(printf '%s\n' "$resource_sample" | cut -d '|' -f 3 | tr -d '%')
load_average=$(cut -d ' ' -f 1-3 /proc/loadavg)

detail_collected=false
pressure_lines=0
pool_busy_events=0
query_timeout_events=0
max_pressure_wait_seconds=0.000000
oom_events=0
restart_events=0
die_events=0
die_event_first_epoch=0
die_event_last_epoch=0
settled_fail_observations=0
settled_failure_cluster_observations=0
settled_unbalanced_failure_cluster_rows=0
if [ "$include_detail" = 1 ]; then
  detail_collected=true
  # Only counts leave the host. The existing pressure line contains a fixed
  # path and bounded counters, never a query string; returning the log itself
  # would unnecessarily widen that already privacy-reviewed boundary.
  pressure_log=$(docker logs --since "$observe_since" "$container" 2>&1 |
    grep 'csx-server: db pressure ' || true)
  pressure_lines=$(printf '%s\n' "$pressure_log" | grep -c . || true)
  pool_busy_events=$(printf '%s\n' "$pressure_log" | grep -Ec 'pool_busy=[1-9][0-9]*' || true)
  query_timeout_events=$(printf '%s\n' "$pressure_log" | grep -Ec 'query_timeout=[1-9][0-9]*' || true)
  # Go durations may contain more than one unit (for example 1m2.5s). Convert
  # every fixed-format `waited=` value to seconds without returning log text.
  max_pressure_wait_seconds=$(printf '%s\n' "$pressure_log" |
    sed -n 's/.* waited=\([^ ]*\).*/\1/p' |
    awk '
      function seconds(duration, token, unit, number, total) {
        total = 0
        while (match(duration, /^[0-9]+([.][0-9]+)?(ms|us|ns|h|m|s)/)) {
          token = substr(duration, 1, RLENGTH)
          unit = token
          sub(/^[0-9]+([.][0-9]+)?/, "", unit)
          number = token
          sub(/(ms|us|ns|h|m|s)$/, "", number)
          if (unit == "h") total += number * 3600
          else if (unit == "m") total += number * 60
          else if (unit == "s") total += number
          else if (unit == "ms") total += number / 1000
          else if (unit == "us") total += number / 1000000
          else total += number / 1000000000
          duration = substr(duration, RLENGTH + 1)
        }
        return total
      }
      { value = seconds($0); if (value > maximum) maximum = value }
      END { printf "%.6f\n", maximum + 0 }
    ')

  events_until=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  oom_events=$(docker events --since "$observe_since" --until "$events_until" \
    --filter "container=$container" --filter event=oom --format '{{.Action}}' | wc -l | tr -d ' ')
  restart_events=$(docker events --since "$observe_since" --until "$events_until" \
    --filter "container=$container" --filter event=restart --format '{{.Action}}' | wc -l | tr -d ' ')
  die_event_epochs=$(docker events --since "$observe_since" --until "$events_until" \
    --filter "container=$container" --filter event=die --format '{{.Time}}' |
    grep -E '^[0-9]+$' | sort -n || true)
  die_events=$(printf '%s\n' "$die_event_epochs" | grep -c '^[0-9][0-9]*$' || true)
  if [ "$die_events" -gt 0 ]; then
    die_event_first_epoch=$(printf '%s\n' "$die_event_epochs" | head -n 1)
    die_event_last_epoch=$(printf '%s\n' "$die_event_epochs" | tail -n 1)
  fi

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
printf 'max_pressure_wait_seconds=%s\n' "$max_pressure_wait_seconds"
printf 'oom_events=%s\n' "$oom_events"
printf 'restart_events=%s\n' "$restart_events"
printf 'die_events=%s\n' "$die_events"
printf 'die_event_first_epoch=%s\n' "$die_event_first_epoch"
printf 'die_event_last_epoch=%s\n' "$die_event_last_epoch"
printf 'settled_fail_observations=%s\n' "$settled_fail_observations"
printf 'settled_failure_cluster_observations=%s\n' "$settled_failure_cluster_observations"
printf 'settled_unbalanced_failure_cluster_rows=%s\n' "$settled_unbalanced_failure_cluster_rows"

probe_latency() {
  key=$1
  path=$2
  marker=$3
  body=$(mktemp)
  result=000\|unavailable
  content_valid=false
  if measured=$(curl --noproxy '*' --connect-timeout 5 --max-time 10 \
      --resolve 'codesamplex.dev:443:127.0.0.1' -sS -o "$body" \
      -w '%{http_code}|%{time_starttransfer}' "https://codesamplex.dev$path"); then
    result=$measured
  fi
  status=${result%%|*}
  seconds=${result#*|}
  if [ "$status" = 200 ] && grep -qF "$marker" "$body"; then
    content_valid=true
  fi
  rm -f "$body"
  printf 'latency_%s_status=%s\n' "$key" "$status"
  printf 'latency_%s_ttfb_seconds=%s\n' "$key" "$seconds"
  printf 'latency_%s_content_valid=%s\n' "$key" "$content_valid"
}

if [ "$include_latency" = 1 ]; then
  # Fixed, already-public corpus paths only: no user identifiers, queries,
  # request bodies, or credentials can enter either request or evidence.
  probe_latency healthz /healthz 'ok'
  probe_latency landing / '<link rel="canonical" href="https://codesamplex.dev/">'
  probe_latency wanted /v1/wanted '"schemaVersion":1'
  probe_latency otel /golang/go.opentelemetry.io/otel/v1.45.0 \
    '<link rel="canonical" href="https://codesamplex.dev/golang/go.opentelemetry.io/otel/v1.45.0">'
  probe_latency package /golang/github.com/jackc/pgx/v5/v5.10.0 \
    '<link rel="canonical" href="https://codesamplex.dev/golang/github.com/jackc/pgx/v5/v5.10.0">'
  probe_latency sample /samples/sha256:13f4bcf31db6296c4d9325831f69e508e320520ab70dd6b2d237a11557c9fe9a \
    '<p class="dim mono small sample-id">sha256:13f4bcf31db6296c4d9325831f69e508e320520ab70dd6b2d237a11557c9fe9a</p>'
fi

# A latency round counts as active-builder evidence only when the same start
# marker is still the latest lifecycle marker after every request. This avoids
# labeling requests that raced with pass completion as active work.
builder_log_after=$(docker logs --since "$observe_since" --timestamps "$container" 2>&1 || true)
builder_lifecycle_after=$(printf '%s\n' "$builder_log_after" |
  grep -E 'compatibility: builder (pass start|pass complete|run:)' | tail -n 1 || true)
builder_error_events=$(printf '%s\n' "$builder_log_after" |
  grep -c 'compatibility: builder run:' || true)
builder_active=false
builder_lifecycle_state=race
if [ -n "$builder_lifecycle_before" ] && [ "$builder_lifecycle_before" = "$builder_lifecycle_after" ]; then
  case "$builder_lifecycle_after" in
    *'compatibility: builder pass start '*) builder_active=true; builder_lifecycle_state=start ;;
    *'compatibility: builder pass complete '*) builder_lifecycle_state=complete ;;
    *'compatibility: builder run:'*) builder_lifecycle_state=error ;;
  esac
elif [ -z "$builder_lifecycle_before" ] && [ -z "$builder_lifecycle_after" ]; then
  builder_lifecycle_state=none
fi
printf 'observed_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'builder_active=%s\n' "$builder_active"
printf 'builder_lifecycle_state=%s\n' "$builder_lifecycle_state"
printf 'builder_error_events=%s\n' "$builder_error_events"
