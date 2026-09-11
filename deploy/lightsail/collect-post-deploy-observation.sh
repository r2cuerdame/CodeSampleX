#!/bin/sh
set -eu

# One bounded, privacy-safe production observation sample. The caller supplies
# the immutable server start and a separate observation boundary, and may
# opt into fixed public-route TTFB probes while builder work is active or after
# convergence.
# Nothing here changes container, database, or deployment state.

cd /opt/codesamplex/deploy

docker_bin=$(command -v docker)
docker() {
  if [ "$1" = compose ] && [ "${2:-}" = exec ] && [ "${3:-}" = -T ] && [ "${4:-}" = db ]; then
    shift 4
    timeout --kill-after=5s 30s "$docker_bin" compose exec -T -e PGOPTIONS='-c statement_timeout=20000' db "$@"
  else
    timeout --kill-after=5s 30s "$docker_bin" "$@"
  fi
}

container=codesamplex-server-1
observe_since=${CSX_OBSERVE_SINCE:?CSX_OBSERVE_SINCE is required}
observation_started_at=${CSX_OBSERVATION_STARTED_AT:-}
include_latency=${CSX_OBSERVE_LATENCY:-0}
include_detail=${CSX_OBSERVE_DETAIL:-0}
builder_lifecycle_pattern='compatibility: builder (pass start|pass complete|run failed(:| after [0-9]+ retries:)|run:)'
builder_error_pattern='compatibility: builder (run failed(:| after [0-9]+ retries:)|run:)'

# Keep the server-lifetime error and pressure totals intact. Only timestamped
# events from a successfully read log can be assigned to the declared observation
# window. Normalize UTC fractions to nanoseconds before comparing: an error at
# the boundary belongs to the observation, including a fractional boundary.
summarize_builder_error_window() {
  awk -v window_start="$1" -v lifetime_start="$2" -v window_end="$3" \
      -v log_status="$4" -v error_pattern="$builder_error_pattern" '
    function normalize(value, base, fraction, year, month, day, days) {
      if (length(value) < 20 || substr(value, length(value), 1) != "Z") return ""
      base = substr(value, 1, 19)
      if (substr(base, 5, 1) != "-" || substr(base, 8, 1) != "-" ||
          substr(base, 11, 1) != "T" || substr(base, 14, 1) != ":" ||
          substr(base, 17, 1) != ":") return ""
      if (substr(base, 1, 4) !~ /^[0-9][0-9][0-9][0-9]$/ ||
          substr(base, 6, 2) !~ /^[0-9][0-9]$/ ||
          substr(base, 9, 2) !~ /^[0-9][0-9]$/ ||
          substr(base, 12, 2) !~ /^[0-9][0-9]$/ ||
          substr(base, 15, 2) !~ /^[0-9][0-9]$/ ||
          substr(base, 18, 2) !~ /^[0-9][0-9]$/) return ""
      year = substr(base, 1, 4) + 0
      month = substr(base, 6, 2) + 0
      day = substr(base, 9, 2) + 0
      if (year < 1 || month < 1 || month > 12 || day < 1 ||
          substr(base, 12, 2) + 0 > 23 || substr(base, 15, 2) + 0 > 59 ||
          substr(base, 18, 2) + 0 > 59) return ""
      days = 31
      if (month == 4 || month == 6 || month == 9 || month == 11) days = 30
      if (month == 2) days = 28 + (year % 4 == 0 && (year % 100 != 0 || year % 400 == 0))
      if (day > days) return ""
      fraction = ""
      if (length(value) != 20) {
        if (substr(value, 20, 1) != ".") return ""
        fraction = substr(value, 21, length(value) - 21)
        if (length(fraction) < 1 || length(fraction) > 9 || fraction ~ /[^0-9]/) return ""
      }
      return base "." substr(fraction "000000000", 1, 9) "Z"
    }
    function duration_seconds(duration, token, unit, number, total) {
      if (duration == "") return -1
      total = 0
      while (match(duration, /^[0-9]+([.][0-9]+)?(ms|us|µs|ns|h|m|s)/)) {
        token = substr(duration, 1, RLENGTH)
        unit = token
        sub(/^[0-9]+([.][0-9]+)?/, "", unit)
        number = token
        sub(/(ms|us|µs|ns|h|m|s)$/, "", number)
        if (unit == "h") total += number * 3600
        else if (unit == "m") total += number * 60
        else if (unit == "s") total += number
        else if (unit == "ms") total += number / 1000
        else if (unit == "us" || unit == "µs") total += number / 1000000
        else total += number / 1000000000
        duration = substr(duration, RLENGTH + 1)
      }
      if (duration != "") return -1
      return total
    }
    BEGIN {
      start = normalize(window_start)
      lifetime = normalize(lifetime_start)
      end = normalize(window_end)
      available = log_status == "complete" && start != "" && lifetime != "" &&
                  end != "" && start >= lifetime && start <= end
      pressure_available = available
    }
    $0 ~ error_pattern {
      total++
      timestamp = normalize($1)
      if (timestamp == "" || timestamp < lifetime || timestamp > end) available = 0
      else if (timestamp < start) before++
      else during++
    }
    /csx-server: db pressure / {
      timestamp = normalize($1)
      if (timestamp == "" || timestamp < lifetime || timestamp > end) pressure_available = 0
      else if (timestamp >= start) {
        pressure_lines++
        pool_fields = 0
        timeout_fields = 0
        wait_fields = 0
        for (i = 1; i <= NF; i++) {
          if ($i ~ /^pool_busy=/) {
            pool_fields++
            if ($i !~ /^pool_busy=[0-9]+$/) pressure_available = 0
            else if (substr($i, 11) + 0 > 0) pool_busy_lines++
          }
          if ($i ~ /^query_timeout=/) {
            timeout_fields++
            if ($i !~ /^query_timeout=[0-9]+$/) pressure_available = 0
            else if (substr($i, 15) + 0 > 0) query_timeout_lines++
          }
          if ($i ~ /^waited=/) {
            wait_fields++
            wait = duration_seconds(substr($i, 8))
            if (wait < 0) pressure_available = 0
            else if (wait > max_wait) max_wait = wait
          }
        }
        if (pool_fields != 1 || timeout_fields != 1 || wait_fields != 1) pressure_available = 0
      }
    }
    END {
      # Unavailable attribution must never turn a known error into history.
      printf "builder_error_events_before_observation=%d\n", available ? before + 0 : 0
      printf "builder_error_events_during_observation=%d\n", available ? during + 0 : total + 0
      printf "builder_error_window_status=%s\n", available ? "complete" : "unavailable"
      printf "window_pressure_lines=%d\n", pressure_lines + 0
      printf "window_pool_busy_events=%d\n", pool_busy_lines + 0
      printf "window_query_timeout_events=%d\n", query_timeout_lines + 0
      printf "window_max_pressure_wait_seconds=%.9f\n", max_wait + 0
      printf "pressure_window_status=%s\n", pressure_available ? "complete" : "unavailable"
    }
  '
}

revision=$(docker inspect "$container" --format '{{range .Config.Env}}{{println .}}{{end}}' |
  sed -n 's/^CSX_VERSION=//p' | head -n 1)
image_digest=$(docker inspect "$container" --format '{{.Image}}')
image_revision=$(docker image inspect "$image_digest" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
migration_version=$(docker compose exec -T db psql -U csx -d csx -Atqc \
  "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")
health=$(docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/healthz 2>/dev/null || true)
served_revision=$(docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/version 2>/dev/null |
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
  grep -E "$builder_lifecycle_pattern" | tail -n 1 || true)

resource_sample=$(docker stats --no-stream --format '{{.CPUPerc}}|{{.MemUsage}}|{{.MemPerc}}' "$container")
cpu_percent=$(printf '%s\n' "$resource_sample" | cut -d '|' -f 1 | tr -d '%')
memory_usage=$(printf '%s\n' "$resource_sample" | cut -d '|' -f 2)
memory_percent=$(printf '%s\n' "$resource_sample" | cut -d '|' -f 3 | tr -d '%')
load_average=$(cut -d ' ' -f 1-3 /proc/loadavg)

# The server caps the pressure line at one per second per class, so counting
# lines measures how long an incident lasted, not how much traffic it refused:
# the canonical v0.1.153 observation reported pool-busy 0 and query-timeout 0
# while the site was serving 503s. Every line now carries the server's
# process-lifetime totals, so the largest total seen in the window is the event
# count.
#
# max, not last-minus-first: within one process the totals only ever grow, and
# the first line in the window may already be nonzero. A server that restarted
# mid-window resets them, which undercounts rather than invents -- and that
# restart is reported separately as restart_events and die_events.
summarize_pressure() {
  awk '
    BEGIN {
      wanted["pool_busy_total"] = 1
      wanted["query_timeout_total"] = 1
      wanted["admission_refused_total"] = 1
      wanted["deferred_refused_total"] = 1
    }
    {
      for (i = 1; i <= NF; i++) {
        if (split($i, kv, "=") == 2 && (kv[1] in wanted) && kv[2] + 0 > seen[kv[1]]) {
          seen[kv[1]] = kv[2] + 0
        }
      }
    }
    END {
      printf "pool_busy_event_total=%d\n", seen["pool_busy_total"] + 0
      printf "query_timeout_event_total=%d\n", seen["query_timeout_total"] + 0
      printf "admission_refused_event_total=%d\n", seen["admission_refused_total"] + 0
      printf "deferred_refused_event_total=%d\n", seen["deferred_refused_total"] + 0
    }
  '
}

detail_collected=false
pressure_lines=0
pool_busy_events=0
query_timeout_events=0
pool_busy_event_total=0
query_timeout_event_total=0
admission_refused_event_total=0
deferred_refused_event_total=0
max_pressure_wait_seconds=0.000000
oom_events=0
restart_events=0
die_events=0
die_event_first_epoch=0
die_event_last_epoch=0
settled_invariant_status=not-collected
settled_invariant_exit_code=
settled_invariant_seconds=
settled_invariant_row_limit=250000
settled_invariant_json_byte_limit=4096
settled_source_row_limit=10000
settled_failure_cluster_rows_examined=
settled_source_rows_examined=
settled_fail_observations=
settled_failure_cluster_observations=
settled_unbalanced_failure_cluster_rows=
if [ "$include_detail" = 1 ]; then
  detail_collected=true
  # Only counts leave the host. The existing pressure line contains a fixed
  # path and bounded counters, never a query string; returning the log itself
  # would unnecessarily widen that already privacy-reviewed boundary.
  # Docker forwards container stderr here, including the server pressure logger.
  if ! pressure_log=$(docker logs --since "$observe_since" "$container" 2>&1); then
    detail_collected=false
    pressure_log=
  fi
  pressure_log=$(printf '%s\n' "$pressure_log" | grep 'csx-server: db pressure ' || true)
  # Kept exactly as they were, so every observation already on the tracking
  # issue stays comparable with the ones written from here on. The `_total`
  # suffix below is deliberate: a `total_pool_busy=` prefix would contain the
  # token these two grep for and would silently inflate them.
  pressure_lines=$(printf '%s\n' "$pressure_log" | grep -c . || true)
  pool_busy_events=$(printf '%s\n' "$pressure_log" | grep -Ec 'pool_busy=[1-9][0-9]*' || true)
  query_timeout_events=$(printf '%s\n' "$pressure_log" | grep -Ec 'query_timeout=[1-9][0-9]*' || true)
  pressure_event_totals=$(printf '%s\n' "$pressure_log" | summarize_pressure)
  pool_busy_event_total=$(printf '%s\n' "$pressure_event_totals" | sed -n 's/^pool_busy_event_total=//p')
  query_timeout_event_total=$(printf '%s\n' "$pressure_event_totals" | sed -n 's/^query_timeout_event_total=//p')
  admission_refused_event_total=$(printf '%s\n' "$pressure_event_totals" | sed -n 's/^admission_refused_event_total=//p')
  deferred_refused_event_total=$(printf '%s\n' "$pressure_event_totals" | sed -n 's/^deferred_refused_event_total=//p')
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
  if oom_log=$(docker events --since "$observe_since" --until "$events_until" \
      --filter "container=$container" --filter event=oom --format '{{.Action}}'); then
    oom_events=$(printf '%s\n' "$oom_log" | grep -c . || true)
  else detail_collected=false; fi
  if restart_log=$(docker events --since "$observe_since" --until "$events_until" \
      --filter "container=$container" --filter event=restart --format '{{.Action}}'); then
    restart_events=$(printf '%s\n' "$restart_log" | grep -c . || true)
  else detail_collected=false; fi
  if ! die_event_epochs=$(docker events --since "$observe_since" --until "$events_until" \
      --filter "container=$container" --filter event=die --format '{{.Time}}'); then
    detail_collected=false
    die_event_epochs=
  fi
  die_event_epochs=$(printf '%s\n' "$die_event_epochs" | grep -E '^[0-9]+$' | sort -n || true)
  die_events=$(printf '%s\n' "$die_event_epochs" | grep -c '^[0-9][0-9]*$' || true)
  if [ "$die_events" -gt 0 ]; then
    die_event_first_epoch=$(printf '%s\n' "$die_event_epochs" | head -n 1)
    die_event_last_epoch=$(printf '%s\n' "$die_event_epochs" | tail -n 1)
  fi

  # Acceptance needs a nonempty, internally balanced current ledger when FAIL
  # evidence exists. A positive balanced ledger proves that without summing
  # the entire source corpus. Only an empty current ledger needs source proof.
  # The sentinel bounds work before filtering/aggregation and prevents a
  # partial prefix from being represented as a complete ledger. Missing totals
  # mean unmeasured, never zero. JSON validation examines four fixed keys, not
  # an unbounded jsonb_each expansion. The existing SQL timeout remains intact.
  # No ORDER BY: a planner-selected sort could read the whole table before
  # LIMIT. Only exhaustive sets can pass, so prefix ordering cannot affect a
  # successful invariant decision.
  settled_invariant_status=unavailable
  settled_invariant_started=$(date +%s)
  if settled_invariant=$(docker compose exec -T db psql -U csx -d csx -At -F '|' -c "
WITH cluster_rows AS MATERIALIZED (
  SELECT id, observation_count, evidence_quality, error_fp,
    evidence_breakdown IS NULL OR (pg_column_size(evidence_breakdown) <= 4096
      AND pg_column_compression(evidence_breakdown) IS NULL) AS within_json_budget,
    CASE WHEN pg_column_size(evidence_breakdown) <= 4096
           AND pg_column_compression(evidence_breakdown) IS NULL THEN evidence_breakdown END AS evidence_breakdown
  FROM failure_clusters LIMIT 250001
), cluster_scope AS MATERIALIZED (
  SELECT count(*) AS examined,
    count(*) <= 250000 AND COALESCE(bool_and(within_json_budget),true) AS complete FROM cluster_rows
), current_clusters AS MATERIALIZED (
  SELECT fc.* FROM cluster_rows fc
  WHERE (SELECT complete FROM cluster_scope)
    AND (COALESCE(fc.evidence_quality,'legacy-evidence-incomplete') NOT IN ('missing','legacy-evidence-incomplete')
         OR COALESCE(fc.error_fp,'') = '')
), cluster_totals AS MATERIALIZED (
  SELECT count(*) AS current_rows,
    COALESCE(SUM(fc.observation_count),0) AS observations,
    count(*) FILTER (WHERE fc.observation_count IS NULL OR fc.observation_count <= 0
      OR CASE WHEN jsonb_typeof(fc.evidence_breakdown) IS DISTINCT FROM 'object' THEN true
              ELSE fc.evidence_breakdown - ARRAY['complete','partial','missing','legacy-evidence-incomplete'] <> '{}'::jsonb END
      OR breakdown.invalid_value
      OR fc.observation_count::numeric <> breakdown.total) AS unbalanced
  FROM current_clusters fc
  CROSS JOIN LATERAL (
    SELECT COALESCE(SUM(CASE WHEN jsonb_typeof(item.value) = 'number'
                            THEN (item.value::text)::numeric ELSE 0 END),0) AS total,
      COALESCE(bool_or(item.value IS NOT NULL AND
        CASE WHEN jsonb_typeof(item.value) = 'number'
             THEN (item.value::text)::numeric < 0 ELSE true END),false) AS invalid_value
    FROM (VALUES (fc.evidence_breakdown->'complete'), (fc.evidence_breakdown->'partial'),
                 (fc.evidence_breakdown->'missing'), (fc.evidence_breakdown->'legacy-evidence-incomplete')) item(value)
  ) breakdown
), source_rows AS MATERIALIZED (
  SELECT result, observation_count FROM evidence_agg
  WHERE (SELECT complete FROM cluster_scope)
    AND (SELECT current_rows = 0 FROM cluster_totals)
  LIMIT 10001
), source_totals AS MATERIALIZED (
  SELECT count(*) AS examined,
    COALESCE(SUM(observation_count) FILTER (WHERE result='FAIL'),0) AS fail
  FROM source_rows
)
SELECT CASE WHEN NOT cs.complete OR (ct.current_rows = 0 AND st.examined > 10000)
            THEN 'budget-exceeded' ELSE 'complete' END,
  cs.examined, st.examined,
  CASE WHEN cs.complete AND ct.current_rows = 0 AND st.examined <= 10000 THEN st.fail END,
  CASE WHEN cs.complete THEN ct.observations END,
  CASE WHEN cs.complete THEN ct.unbalanced END
FROM cluster_scope cs CROSS JOIN cluster_totals ct CROSS JOIN source_totals st" 2>/dev/null); then
    settled_invariant_exit_code=0
    settled_invariant_status=$(printf '%s\n' "$settled_invariant" | cut -d '|' -f 1)
    settled_failure_cluster_rows_examined=$(printf '%s\n' "$settled_invariant" | cut -d '|' -f 2)
    settled_source_rows_examined=$(printf '%s\n' "$settled_invariant" | cut -d '|' -f 3)
    settled_fail_observations=$(printf '%s\n' "$settled_invariant" | cut -d '|' -f 4)
    settled_failure_cluster_observations=$(printf '%s\n' "$settled_invariant" | cut -d '|' -f 5)
    settled_unbalanced_failure_cluster_rows=$(printf '%s\n' "$settled_invariant" | cut -d '|' -f 6)
  else
    settled_invariant_exit_code=$?
  fi
  settled_invariant_seconds=$(( $(date +%s) - settled_invariant_started ))
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
printf 'pool_busy_event_total=%s\n' "$pool_busy_event_total"
printf 'query_timeout_event_total=%s\n' "$query_timeout_event_total"
printf 'admission_refused_event_total=%s\n' "$admission_refused_event_total"
printf 'deferred_refused_event_total=%s\n' "$deferred_refused_event_total"
printf 'max_pressure_wait_seconds=%s\n' "$max_pressure_wait_seconds"
printf 'oom_events=%s\n' "$oom_events"
printf 'restart_events=%s\n' "$restart_events"
printf 'die_events=%s\n' "$die_events"
printf 'die_event_first_epoch=%s\n' "$die_event_first_epoch"
printf 'die_event_last_epoch=%s\n' "$die_event_last_epoch"
printf 'settled_invariant_status=%s\n' "$settled_invariant_status"
printf 'settled_invariant_exit_code=%s\n' "$settled_invariant_exit_code"
printf 'settled_invariant_seconds=%s\n' "$settled_invariant_seconds"
printf 'settled_invariant_row_limit=%s\n' "$settled_invariant_row_limit"
printf 'settled_invariant_json_byte_limit=%s\n' "$settled_invariant_json_byte_limit"
printf 'settled_source_row_limit=%s\n' "$settled_source_row_limit"
printf 'settled_failure_cluster_rows_examined=%s\n' "$settled_failure_cluster_rows_examined"
printf 'settled_source_rows_examined=%s\n' "$settled_source_rows_examined"
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
builder_error_window_until=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
builder_log_after_status=unavailable
if builder_log_after=$(docker logs --since "$observe_since" --until "$builder_error_window_until" --timestamps "$container" 2>&1); then
  builder_log_after_status=complete
fi
builder_lifecycle_after=$(printf '%s\n' "$builder_log_after" |
  grep -E "$builder_lifecycle_pattern" | tail -n 1 || true)
builder_error_events=$(printf '%s\n' "$builder_log_after" |
  grep -Ec "$builder_error_pattern" || true)
builder_active=false
builder_lifecycle_state=race
if [ -n "$builder_lifecycle_before" ] && [ "$builder_lifecycle_before" = "$builder_lifecycle_after" ]; then
  case "$builder_lifecycle_after" in
    *'compatibility: builder pass start '*) builder_active=true; builder_lifecycle_state=start ;;
    *'compatibility: builder pass complete '*) builder_lifecycle_state=complete ;;
    *'compatibility: builder run failed:'*|*'compatibility: builder run failed after '*|*'compatibility: builder run:'*) builder_lifecycle_state=error ;;
  esac
elif [ -z "$builder_lifecycle_before" ] && [ -z "$builder_lifecycle_after" ]; then
  builder_lifecycle_state=none
fi
printf 'observed_at=%s\n' "${builder_error_window_until%%.*}Z"
printf 'builder_active=%s\n' "$builder_active"
printf 'builder_lifecycle_state=%s\n' "$builder_lifecycle_state"
printf 'builder_error_events=%s\n' "$builder_error_events"
printf 'builder_error_window_ended_at=%s\n' "$builder_error_window_until"
printf '%s\n' "$builder_log_after" | summarize_builder_error_window \
  "$observation_started_at" "$observe_since" "$builder_error_window_until" "$builder_log_after_status"
