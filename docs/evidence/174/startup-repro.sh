#!/usr/bin/env bash
set -euo pipefail

# Controlled startup reproduction for issue #174. It accepts only a loopback
# PostgreSQL DSN, owns two fixed schemas, and never reads production data.

readonly TARGET_SHA=28e0397fb68e34e0cd1433f51d3027040dfc34a6
readonly BASE_SHA=3b6bb9292488d6e9fc2b62ff0db1d13e177ebc61
readonly EVIDENCE_BASE=/workspace/csx-startup-repro-174
readonly COMBINED_SCHEMA=csx_startup_repro_174_combined
readonly OFFLINE_SCHEMA=csx_startup_repro_174_offline
readonly EXPLICIT_HEALTH_POLL_WINDOW_MS=120000
readonly COMPOSE_HEALTH_NOMINAL_WINDOW_MS=135000

die() { printf 'startup-repro: %s\n' "$*" >&2; exit 64; }

# Ignore ambient credentials: this harness is pinned to the owned loopback service.
unset CSX_REPRO_DSN
: "${CSX_REPRO_DSN:=postgres://devhotel:devhotel@127.0.0.1:5432/devhotel?sslmode=disable}"
: "${CSX_OLD_BINARY:=/workspace/csx-startup-repro-174/csx-server-v149}"
: "${CSX_TARGET_BINARY:=/workspace/csx-startup-repro-174/csx-server-target}"
: "${CSX_REPRO_LISTEN:=127.0.0.1:3100}"
: "${CSX_REPRO_TIMEOUT_SECONDS:=240}"
: "${CSX_REPRO_SCENARIO:=combined}"
: "${CSX_DEVHOTEL_ROOM_ID:?CSX_DEVHOTEL_ROOM_ID is required}"
readonly CSX_EXPECTED_MEMORY_MAX_BYTES=805306368

case "$CSX_REPRO_SCENARIO" in
  combined|offline) ;;
  *) die "scenario must be combined or offline" ;;
esac
readonly EVIDENCE_ROOT="$EVIDENCE_BASE/evidence-$CSX_REPRO_SCENARIO"

[[ $CSX_REPRO_DSN =~ ^postgres(ql)?://[^/]+@(127\.0\.0\.1|localhost)(:[0-9]+)?/ ]] ||
  die "refusing non-loopback PostgreSQL DSN"
shopt -s nocasematch
[[ $CSX_REPRO_DSN =~ (^|[?\&])(options|search_path)= ]] &&
  die "DSN must not override PostgreSQL startup options or search_path"
shopt -u nocasematch
[[ $CSX_REPRO_TIMEOUT_SECONDS =~ ^[0-9]+$ ]] || die "timeout must be an integer"
(( CSX_REPRO_TIMEOUT_SECONDS >= 135 && CSX_REPRO_TIMEOUT_SECONDS <= 600 )) ||
  die "timeout must be between 135 and 600 seconds"
[[ $CSX_REPRO_LISTEN =~ ^(127\.0\.0\.1|localhost):[0-9]+$ ]] ||
  die "listen address must be loopback"

for command_name in curl git go jq nproc psql realpath sha256sum; do
  command -v "$command_name" >/dev/null || die "missing command: $command_name"
done
for binary in "$CSX_OLD_BINARY" "$CSX_TARGET_BINARY"; do
  [[ -x $binary ]] || die "missing executable: $binary"
done

repo_root=$(realpath "$(dirname "$0")/../../..")
seed_sql="$repo_root/docs/evidence/174/startup-repro-seed.sql"
[[ -f $seed_sql ]] || die "missing seed SQL"
[[ $(git -C "$repo_root" rev-parse HEAD) == "$TARGET_SHA" ]] || die "HEAD is not target SHA"
[[ $(git -C "$repo_root" rev-parse origin/main) == "$TARGET_SHA" ]] || die "origin/main is not target SHA"
git -C "$repo_root" diff --quiet HEAD -- || die "tracked worktree differs from target SHA"
git -C "$repo_root" diff --cached --quiet HEAD -- || die "index differs from target SHA"

assert_binary_revision() {
  local binary=$1 expected=$2 build_info
  build_info=$(go version -m "$binary")
  grep -Fq $'path\tgithub.com/r2cuerdame/codesamplex/cmd/csx-server' <<<"$build_info" ||
    die "$binary has an unexpected Go command path"
  grep -Eq "vcs\.revision=$expected([[:space:]]|$)" <<<"$build_info" ||
    die "$binary does not embed expected vcs.revision $expected"
  grep -Eq 'vcs\.modified=false([[:space:]]|$)' <<<"$build_info" ||
    die "$binary was built from a modified worktree"
}
assert_binary_revision "$CSX_OLD_BINARY" "$BASE_SHA"
assert_binary_revision "$CSX_TARGET_BINARY" "$TARGET_SHA"

# Deletion is intentionally limited to one canonical, non-symlink path.
[[ $(realpath -m "$EVIDENCE_ROOT") == "$EVIDENCE_ROOT" ]] || die "unexpected evidence path"
[[ $EVIDENCE_ROOT == "$EVIDENCE_BASE/evidence-combined" || $EVIDENCE_ROOT == "$EVIDENCE_BASE/evidence-offline" ]] ||
  die "unexpected evidence root"
[[ ! -L /workspace && ! -L "$EVIDENCE_BASE" && ! -L "$EVIDENCE_ROOT" ]] ||
  die "refusing symlinked evidence path"
if [[ -e $EVIDENCE_ROOT ]]; then
  rm -rf -- "$EVIDENCE_ROOT"
fi
mkdir -p "$EVIDENCE_ROOT"

active_pid=
observer_pid=
monitor_pid=
last_reap_status=0
bounded_reap() {
  local pid=${1:-} first_signal=${2:-TERM} wait_seconds=${3:-10} i
  [[ -n $pid ]] || return 0
  if ! kill -0 "$pid" 2>/dev/null; then
    set +e; wait "$pid" 2>/dev/null; last_reap_status=$?; set -e
    return 0
  fi
  kill -"$first_signal" "$pid" 2>/dev/null || true
  for ((i=0; i<wait_seconds*10; i++)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      set +e; wait "$pid" 2>/dev/null; last_reap_status=$?; set -e
      return 0
    fi
    sleep 0.1
  done
  kill -TERM "$pid" 2>/dev/null || true
  for ((i=0; i<50; i++)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      set +e; wait "$pid" 2>/dev/null; last_reap_status=$?; set -e
      return 0
    fi
    sleep 0.1
  done
  kill -KILL "$pid" 2>/dev/null || true
  set +e; wait "$pid" 2>/dev/null; last_reap_status=$?; set -e
}
cleanup_processes() {
  bounded_reap "${active_pid:-}" TERM 2
  bounded_reap "${observer_pid:-}" TERM 2
  bounded_reap "${monitor_pid:-}" TERM 2
}
trap cleanup_processes EXIT
trap 'exit 130' INT TERM

exec 3>"$EVIDENCE_ROOT/timeline.tsv"
printf 'timestamp_ns\tevent\tdetail\n' >&3
record() { printf '%s\t%s\t%s\n' "$(date +%s%N)" "$1" "$2" >&3; }

schema_options() {
  local schema=$1
  printf '%s' "-c search_path=$schema -c work_mem=16MB -c maintenance_work_mem=64MB -c statement_timeout=0"
}

assert_schema_connection() {
  local schema=$1 actual
  actual=$(PGCONNECT_TIMEOUT=3 PGOPTIONS="$(schema_options "$schema")" \
    psql -X "$CSX_REPRO_DSN" -At -v ON_ERROR_STOP=1 -c 'SELECT current_schema()')
  [[ $actual == "$schema" ]] || die "connection resolved schema $actual instead of $schema"
}

psql_schema() {
  local schema=$1
  shift
  PGCONNECT_TIMEOUT=3 PGOPTIONS="-c search_path=$schema -c statement_timeout=5s" \
    psql -X "$CSX_REPRO_DSN" -v ON_ERROR_STOP=1 "$@"
}

preflight=$(PGCONNECT_TIMEOUT=3 psql -X "$CSX_REPRO_DSN" -At -v ON_ERROR_STOP=1 -c "
SELECT jsonb_build_object(
  'version', current_setting('server_version'),
  'major', current_setting('server_version_num')::integer/10000,
  'encoding', pg_encoding_to_char(encoding),
  'collation', datcollate,
  'sharedBuffers', current_setting('shared_buffers'),
  'effectiveCacheSize', current_setting('effective_cache_size'),
  'workMem', current_setting('work_mem'),
  'maintenanceWorkMem', current_setting('maintenance_work_mem'),
  'walCompression', current_setting('wal_compression'),
  'maxConnections', current_setting('max_connections'),
  'collationAvailable', EXISTS(SELECT 1 FROM pg_collation WHERE collname='pg_c_utf8')
) FROM pg_database WHERE datname=current_database();")
printf '%s\n' "$preflight" >"$EVIDENCE_ROOT/postgres-preflight.json"
grep -q '"major": 17' <<<"$preflight" || die "PostgreSQL 17 is required"
grep -q '"encoding": "UTF8"' <<<"$preflight" || die "UTF8 database is required"
grep -q '"collationAvailable": true' <<<"$preflight" || die "pg_c_utf8 collation is required"
grep -q '"sharedBuffers": "256MB"' <<<"$preflight" || die "shared_buffers must be 256MB"
grep -q '"effectiveCacheSize": "768MB"' <<<"$preflight" || die "effective_cache_size must be 768MB"
grep -q '"maxConnections": "40"' <<<"$preflight" || die "max_connections must be 40"
grep -q '"maintenanceWorkMem": "64MB"' <<<"$preflight" || die "maintenance_work_mem must be 64MB"
grep -q '"workMem": "16MB"' <<<"$preflight" || die "work_mem must be 16MB"
grep -q '"walCompression": "pglz"' <<<"$preflight" || die "wal_compression must be on/pglz"
record preflight "$preflight"

self_cgroup_rel=$(awk -F: '$1=="0" {print $3}' /proc/self/cgroup)
self_cgroup_root="/sys/fs/cgroup${self_cgroup_rel:-/}"
actual_memory_max=$(cat "$self_cgroup_root/memory.max")
[[ $actual_memory_max == "$CSX_EXPECTED_MEMORY_MAX_BYTES" ]] ||
  die "room memory.max=$actual_memory_max, expected $CSX_EXPECTED_MEMORY_MAX_BYTES"
read -r cpu_quota cpu_period <"$self_cgroup_root/cpu.max"
[[ $cpu_quota =~ ^[0-9]+$ && $cpu_period =~ ^[1-9][0-9]*$ ]] || die "room has no finite CPU quota"
(( cpu_quota == 2 * cpu_period )) || die "room CPU quota is not exactly 2 vCPU"
cpu_set=$(cat "$self_cgroup_root/cpuset.cpus.effective")
nproc_count=$(nproc)
(( nproc_count >= 2 )) || die "nproc=$nproc_count, expected at least 2 for a 2-vCPU quota"
cpu_set_count=$(awk -F, '{
  n=0
  for (i=1; i<=NF; i++) {
    split($i, bounds, "-")
    n += (bounds[2] == "" ? 1 : bounds[2]-bounds[1]+1)
  }
  print n
}' <<<"$cpu_set")
(( cpu_set_count >= 2 )) || die "effective cpuset has only $cpu_set_count runnable CPU"
printf 'cgroup=%s\nmemory.max=%s\ncpu.max=%s %s\ncpuset.cpus.effective=%s\nnproc=%s\n' \
  "$self_cgroup_rel" "$actual_memory_max" "$cpu_quota" "$cpu_period" "$cpu_set" "$nproc_count" \
  >"$EVIDENCE_ROOT/room-resource-limits.txt"
printf 'room.id=%s\nscenario=%s\n' "$CSX_DEVHOTEL_ROOM_ID" "$CSX_REPRO_SCENARIO" \
  >"$EVIDENCE_ROOT/devhotel-run.txt"
record room_limits "cgroup=$self_cgroup_rel memory.max=$actual_memory_max cpu.max=$cpu_quota/$cpu_period cpuset=$cpu_set"

schema_state() {
  local schema=$1 output=$2
  psql_schema "$schema" -At -c "
SELECT jsonb_build_object(
  'schema', current_schema(),
  'migrationCount', (SELECT count(*) FROM schema_migrations),
  'migrationVersion', (SELECT max(version) FROM schema_migrations),
  'migration0036AppliedAt', (SELECT applied_at FROM schema_migrations WHERE version='0036_builder_projections.sql'),
  'schemaBytes', (SELECT sum(pg_total_relation_size(format('%I.%I', schemaname, tablename)::regclass)) FROM pg_tables WHERE schemaname=current_schema()),
  'evidenceAggBytes', pg_total_relation_size('evidence_agg'),
  'failureClustersBytes', pg_total_relation_size('failure_clusters'),
  'observation', csx_startup_repro_observation()
);" >"$output"
}

fresh_fixture() {
  local schema=$1 fixture_root="$EVIDENCE_ROOT/fixtures/$1"
  [[ $schema == "$COMBINED_SCHEMA" || $schema == "$OFFLINE_SCHEMA" ]] || die "refusing schema $schema"
  mkdir -p "$fixture_root"
  PGCONNECT_TIMEOUT=3 psql -X "$CSX_REPRO_DSN" -v ON_ERROR_STOP=1 \
    -c "DROP SCHEMA IF EXISTS $schema CASCADE" \
    -c "CREATE SCHEMA $schema"
  assert_schema_connection "$schema"
  record fixture_schema_created "$schema"
  PGOPTIONS="$(schema_options "$schema")" CSX_DSN="$CSX_REPRO_DSN" \
    "$CSX_OLD_BINARY" migrate >"$fixture_root/v149-migrate.stdout.log" 2>"$fixture_root/v149-migrate.stderr.log"
  [[ $(psql_schema "$schema" -At -c "SELECT count(*) || '|' || max(version) FROM schema_migrations") == \
      '36|0035_recent_wanted_demand.sql' ]] || die "unexpected v0.1.149 migration ledger"
  PGCONNECT_TIMEOUT=3 PGOPTIONS="-c search_path=$schema -c statement_timeout=0" \
    psql -X "$CSX_REPRO_DSN" -v ON_ERROR_STOP=1 -f "$seed_sql" \
      >"$fixture_root/seed.log" 2>&1
  PGCONNECT_TIMEOUT=3 psql -X "$CSX_REPRO_DSN" -v ON_ERROR_STOP=1 -c CHECKPOINT \
    >"$fixture_root/checkpoint.log" 2>&1
  schema_state "$schema" "$fixture_root/pre-target.json"
  printf '%s\n' 'seed + ANALYZE + CHECKPOINT; no operating-system cache drop; fixtures are independently recreated' \
    >"$fixture_root/cache-protocol.txt"
  record fixture_ready "$schema"
}

start_observer() {
  local schema=$1 run_root=$2
  cat >"$run_root/observer.sql" <<'SQL'
SELECT csx_startup_repro_observation();
\watch 1
SQL
  PGAPPNAME=csx_startup_repro_observer PGCONNECT_TIMEOUT=3 \
    PGOPTIONS="-c search_path=$schema -c statement_timeout=2s" \
    psql -X -q -t -A "$CSX_REPRO_DSN" -f "$run_root/observer.sql" \
      >"$run_root/db-observations.jsonl" 2>"$run_root/db-observer.stderr.log" &
  observer_pid=$!
}

assert_observation_processes_alive() {
  if kill -0 "$active_pid" 2>/dev/null; then
    kill -0 "$observer_pid" 2>/dev/null || die "database observer exited before target completion"
    kill -0 "$monitor_pid" 2>/dev/null || die "process monitor exited before target completion"
  fi
}

snapshot_room_cgroup_after() {
  local run_root=$1 pressure
  cp "$self_cgroup_root/memory.events" "$run_root/cgroup-memory.events-after.txt"
  cat "$self_cgroup_root/memory.current" >"$run_root/cgroup-memory.current-after.txt"
  cat "$self_cgroup_root/memory.peak" >"$run_root/cgroup-memory.peak-after.txt"
  cp "$self_cgroup_root/cpu.stat" "$run_root/cgroup-cpu.stat-after.txt"
  cp /proc/loadavg "$run_root/loadavg-final.txt"
  for pressure in cpu memory io; do
    cp "/proc/pressure/$pressure" "$run_root/pressure-$pressure-final.txt"
  done
}

verify_observation_coverage() {
  local run_root=$1 started_ns=$2 observation_end_ns=$3 observer_status=$4 monitor_status=$5
  local duration_ms minimum_db_samples minimum_process_samples db_samples process_samples first_db last_db first_process last_process
  duration_ms=$(( (observation_end_ns-started_ns)/1000000 ))
  minimum_db_samples=$(( duration_ms/3000 ))
  minimum_process_samples=$(( duration_ms/5000 ))
  (( minimum_db_samples >= 1 )) || minimum_db_samples=1
  (( minimum_process_samples >= 1 )) || minimum_process_samples=1
  grep '^{' "$run_root/db-observations.jsonl" >"$run_root/db-observations.validated.jsonl" || true
  jq -s -e 'length > 0 and all(.[];
    type=="object" and (.observedAtNs|type=="number") and
    (.migration0036Visible|type=="boolean") and (.activity|type=="array") and
    (.indexProgress|type=="array") and (.ungrantedLocks|type=="number") and
    (.blockingEdges|type=="number"))' "$run_root/db-observations.validated.jsonl" >/dev/null ||
    die "database observer emitted invalid or incomplete JSON"
  db_samples=$(jq -s 'length' "$run_root/db-observations.validated.jsonl")
  first_db=$(jq -s '.[0].observedAtNs' "$run_root/db-observations.validated.jsonl")
  last_db=$(jq -s '.[-1].observedAtNs' "$run_root/db-observations.validated.jsonl")
  process_samples=$(awk 'NR>1 {
    if (NF != 10 || $1 !~ /^[0-9]+$/ || $3 !~ /^[0-9]+$/ || $4 !~ /^[0-9]+$/ ||
        $7 !~ /^[0-9]+$/ || $8 !~ /^[0-9]+$/) bad++
    n++
  } END {if (bad) exit 2; print n+0}' "$run_root/process-health.tsv") ||
    die "process monitor emitted an incomplete sample"
  first_process=$(awk 'NR==2 {print $1}' "$run_root/process-health.tsv")
  last_process=$(awk 'END {print $1}' "$run_root/process-health.tsv")
  (( db_samples >= minimum_db_samples )) || die "database observer cadence is too sparse"
  (( process_samples >= minimum_process_samples )) || die "process monitor cadence is too sparse"
  (( first_db-started_ns <= 3000000000 && first_process-started_ns <= 3000000000 )) ||
    die "observation did not start within three seconds"
  (( observation_end_ns-last_db <= 5000000000 && observation_end_ns-last_process <= 5000000000 )) ||
    die "observation stopped more than five seconds before target observation ended"
  if grep -Eq '(ERROR:|FATAL:|statement timeout|connection .*failed)' "$run_root/db-observer.stderr.log"; then
    die "database observer reported an error"
  fi
  [[ ! -s $run_root/process-monitor.stderr.log ]] || die "process monitor wrote to stderr"
  [[ $observer_status == 0 || $observer_status == 143 ]] || die "unexpected observer exit status $observer_status"
  [[ $monitor_status == 0 || $monitor_status == 143 ]] || die "unexpected monitor exit status $monitor_status"
  printf 'duration_ms=%s\nminimum_db_samples=%s\nminimum_process_samples=%s\ndb_json_samples=%s\nprocess_samples=%s\nobserver_exit=%s\nmonitor_exit=%s\n' \
    "$duration_ms" "$minimum_db_samples" "$minimum_process_samples" "$db_samples" "$process_samples" "$observer_status" "$monitor_status" \
    >"$run_root/observation-coverage.txt"
}

monitor_process() {
  local pid=$1 run_root=$2 started_ns=$3 cgroup_rel cgroup_root
  cgroup_rel=$(awk -F: '$1=="0" {print $3}' "/proc/$pid/cgroup" 2>/dev/null || true)
  cgroup_root="/sys/fs/cgroup${cgroup_rel:-/}"
  printf '%s\n' "$cgroup_rel" >"$run_root/cgroup-path.txt"
  cat "$cgroup_root/memory.max" >"$run_root/cgroup-memory.max.txt"
  cat "$cgroup_root/memory.current" >"$run_root/cgroup-memory.current-before.txt"
  cat "$cgroup_root/memory.peak" >"$run_root/cgroup-memory.peak-before.txt"
  cp "$cgroup_root/memory.events" "$run_root/cgroup-memory.events-before.txt"
  cp "$cgroup_root/cpu.stat" "$run_root/cgroup-cpu.stat-before.txt"
  printf 'timestamp_ns\telapsed_ms\trss_kib\thwm_kib\tthreads\tcpu_percent\tcgroup_current\tcgroup_peak\tversion_http\thealth_http\n' \
    >"$run_root/process-health.tsv"
  while kill -0 "$pid" 2>/dev/null; do
    local now rss hwm threads cpu current peak version_code health_code
    now=$(date +%s%N)
    rss=$(awk '/^VmRSS:/ {print $2}' "/proc/$pid/status" 2>/dev/null || true)
    hwm=$(awk '/^VmHWM:/ {print $2}' "/proc/$pid/status" 2>/dev/null || true)
    threads=$(awk '/^Threads:/ {print $2}' "/proc/$pid/status" 2>/dev/null || true)
    cpu=$(ps -p "$pid" -o %cpu= 2>/dev/null | tr -d ' ' || true)
    current=$(cat "$cgroup_root/memory.current" 2>/dev/null || true)
    peak=$(cat "$cgroup_root/memory.peak" 2>/dev/null || true)
    version_code=$(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 1 --max-time 2 "http://$CSX_REPRO_LISTEN/version" 2>/dev/null || true)
    health_code=$(curl -sS -o /dev/null -w '%{http_code}' --connect-timeout 1 --max-time 2 "http://$CSX_REPRO_LISTEN/healthz" 2>/dev/null || true)
    printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
      "$now" "$(( (now-started_ns)/1000000 ))" "$rss" "$hwm" "$threads" "$cpu" "$current" "$peak" "$version_code" "$health_code" \
      >>"$run_root/process-health.tsv"
    if [[ $version_code == 200 && ! -e $run_root/version-ready.ns ]]; then
      printf '%s\n' "$now" >"$run_root/version-ready.ns"
    fi
    if [[ $health_code == 200 && ! -e $run_root/health-ready.ns ]]; then
      printf '%s\n' "$now" >"$run_root/health-ready.ns"
    fi
    sleep 1
  done
  cp "$cgroup_root/memory.events" "$run_root/cgroup-memory.events-after.txt" 2>/dev/null || true
  cat "$cgroup_root/memory.current" >"$run_root/cgroup-memory.current-after.txt" 2>/dev/null || true
  cat "$cgroup_root/memory.peak" >"$run_root/cgroup-memory.peak-after.txt" 2>/dev/null || true
  cp "$cgroup_root/cpu.stat" "$run_root/cgroup-cpu.stat-after.txt" 2>/dev/null || true
  cp /proc/loadavg "$run_root/loadavg-final.txt" 2>/dev/null || true
  for pressure in cpu memory io; do
    cp "/proc/pressure/$pressure" "$run_root/pressure-$pressure-final.txt" 2>/dev/null || true
  done
}

run_target() {
  local label=$1 schema=$2 mode=$3
  local run_root="$EVIDENCE_ROOT/runs/$label" started_ns observation_end_ns deadline_s timed_out=0 exit_status=0 observer_status=0 monitor_status=0 target_cgroup_rel
  local ready_ns= builder_success=false builder_retry_exhausted=false builder_error_finals=0 harness_terminated=false
  mkdir -p "$run_root"
  started_ns=$(date +%s%N)
  deadline_s=$(( $(date +%s) + CSX_REPRO_TIMEOUT_SECONDS ))
  record "${label}_start" "schema=$schema mode=$mode"
  assert_schema_connection "$schema"
  start_observer "$schema" "$run_root"
  PGOPTIONS="$(schema_options "$schema")" \
    CSX_DSN="$CSX_REPRO_DSN" CSX_LISTEN="$CSX_REPRO_LISTEN" \
    CSX_PUBLIC_CHECK=trust CSX_SNAPSHOT_INTERVAL=1h \
    CSX_VERSION="$TARGET_SHA" CSX_BUILD_VERSION=devhotel-csx-startup-repro-174 \
    GOMEMLIMIT=600MiB \
    "$CSX_TARGET_BINARY" "$mode" >"$run_root/server.stdout.log" 2>"$run_root/server.stderr.log" &
  active_pid=$!
  target_cgroup_rel=$(awk -F: '$1=="0" {print $3}' "/proc/$active_pid/cgroup")
  [[ $target_cgroup_rel == "$self_cgroup_rel" ]] || die "target escaped expected room cgroup"
  monitor_process "$active_pid" "$run_root" "$started_ns" \
    >"$run_root/process-monitor.stdout.log" 2>"$run_root/process-monitor.stderr.log" &
  monitor_pid=$!
  sleep 0.2
  assert_observation_processes_alive

  if [[ $mode == serve ]]; then
    while kill -0 "$active_pid" 2>/dev/null; do
      assert_observation_processes_alive
      [[ -e $run_root/health-ready.ns && -e $run_root/version-ready.ns ]] && ready_ns=$(cat "$run_root/health-ready.ns")
      if grep -q 'event=final outcome=success' "$run_root/server.stderr.log" 2>/dev/null; then
        builder_success=true
      fi
      builder_error_finals=$(grep -c 'event=final outcome=error' "$run_root/server.stderr.log" 2>/dev/null || true)
      if (( builder_error_finals >= 6 )); then
        builder_retry_exhausted=true
      fi
      if [[ -n $ready_ns && ( $builder_success == true || $builder_retry_exhausted == true ) ]]; then
        break
      fi
      if (( $(date +%s) >= deadline_s )); then
        timed_out=1
        break
      fi
      sleep 1
    done
    curl -sS --connect-timeout 1 --max-time 3 "http://$CSX_REPRO_LISTEN/version" >"$run_root/version.json" 2>"$run_root/version.error" || true
    curl -sS --connect-timeout 1 --max-time 3 "http://$CSX_REPRO_LISTEN/healthz" >"$run_root/healthz.json" 2>"$run_root/healthz.error" || true
    observation_end_ns=$(date +%s%N)
    if kill -0 "$active_pid" 2>/dev/null; then assert_observation_processes_alive; fi
    if kill -0 "$active_pid" 2>/dev/null; then
      harness_terminated=true
      bounded_reap "$active_pid" INT 10
      exit_status=$last_reap_status
    fi
  else
    while kill -0 "$active_pid" 2>/dev/null; do
      assert_observation_processes_alive
      if (( $(date +%s) >= deadline_s )); then
        timed_out=1
        harness_terminated=true
        observation_end_ns=$(date +%s%N)
        bounded_reap "$active_pid" TERM 5
        exit_status=$last_reap_status
        break
      fi
      sleep 1
    done
  fi

  if [[ -z ${observation_end_ns:-} ]]; then observation_end_ns=$(date +%s%N); fi
  if kill -0 "$active_pid" 2>/dev/null; then
    assert_observation_processes_alive
  else
    kill -0 "$observer_pid" 2>/dev/null || die "database observer exited before target completion"
  fi

  if kill -0 "$active_pid" 2>/dev/null; then
    bounded_reap "$active_pid" TERM 5
    exit_status=$last_reap_status
  elif [[ $harness_terminated == false ]]; then
    set +e
    wait "$active_pid"
    exit_status=$?
    set -e
  fi
  active_pid=
  snapshot_room_cgroup_after "$run_root"
  bounded_reap "$observer_pid" TERM 3
  observer_status=$last_reap_status
  bounded_reap "$monitor_pid" TERM 3
  monitor_status=$last_reap_status
  observer_pid=
  monitor_pid=

  local finished_ns elapsed_ms readiness_json within_explicit_window within_compose_window
  finished_ns=$(date +%s%N)
  elapsed_ms=$(( (finished_ns-started_ns)/1000000 ))
  verify_observation_coverage "$run_root" "$started_ns" "$observation_end_ns" "$observer_status" "$monitor_status"
  readiness_json=null
  within_explicit_window=false
  within_compose_window=false
  if [[ -n $ready_ns ]]; then
    readiness_json=$(( (ready_ns-started_ns)/1000000 ))
    if (( readiness_json <= EXPLICIT_HEALTH_POLL_WINDOW_MS )); then within_explicit_window=true; fi
    if (( readiness_json <= COMPOSE_HEALTH_NOMINAL_WINDOW_MS )); then within_compose_window=true; fi
  fi
  printf '{"label":"%s","schema":"%s","mode":"%s","startedNs":%s,"elapsedMs":%s,"readinessMs":%s,"explicitHealthPollWindowMs":%s,"composeHealthNominalWindowMs":%s,"withinExplicitHealthPollWindow":%s,"withinComposeHealthNominalWindow":%s,"builderSuccess":%s,"builderErrorFinals":%s,"builderRetryExhausted":%s,"timedOut":%s,"harnessTerminated":%s,"exitStatus":%s,"observerExitStatus":%s,"monitorExitStatus":%s}\n' \
    "$label" "$schema" "$mode" "$started_ns" "$elapsed_ms" "$readiness_json" "$EXPLICIT_HEALTH_POLL_WINDOW_MS" "$COMPOSE_HEALTH_NOMINAL_WINDOW_MS" \
    "$within_explicit_window" "$within_compose_window" "$builder_success" "$builder_error_finals" "$builder_retry_exhausted" \
    "$([[ $timed_out == 1 ]] && echo true || echo false)" "$harness_terminated" "$exit_status" "$observer_status" "$monitor_status" >"$run_root/result.json"
  record "${label}_finish" "elapsed_ms=$elapsed_ms ready_ms=$readiness_json exit=$exit_status timeout=$timed_out"

  if [[ $mode == migrate && ( $timed_out == 1 || $exit_status != 0 ) ]]; then
    die "offline migration failed or timed out"
  fi
}

go version -m "$CSX_OLD_BINARY" >"$EVIDENCE_ROOT/v149-buildinfo.txt"
go version -m "$CSX_TARGET_BINARY" >"$EVIDENCE_ROOT/target-buildinfo.txt"
sha256sum "$CSX_OLD_BINARY" "$CSX_TARGET_BINARY" "$seed_sql" "$0" >"$EVIDENCE_ROOT/sha256sums.txt"
git -C "$repo_root" status --short >"$EVIDENCE_ROOT/git-status-before.txt"

if [[ $CSX_REPRO_SCENARIO == combined ]]; then
  # A: exact production order. Serve starts from a fresh 0035 fixture;
  # migration, indexes, and backfill must finish before the listener exists.
  fresh_fixture "$COMBINED_SCHEMA"
  run_target combined_0035_to_0036 "$COMBINED_SCHEMA" serve
  schema_state "$COMBINED_SCHEMA" "$EVIDENCE_ROOT/runs/combined_0035_to_0036/post-target.json"
else
  # B uses a separate DevHotel room/database. Apply 0036 offline, then measure
  # serve without schema work so startup is not conflated with migration.
  fresh_fixture "$OFFLINE_SCHEMA"
  run_target offline_migration_0035_to_0036 "$OFFLINE_SCHEMA" migrate
  schema_state "$OFFLINE_SCHEMA" "$EVIDENCE_ROOT/runs/offline_migration_0035_to_0036/post-target.json"
  run_target preapplied_0036_serve "$OFFLINE_SCHEMA" serve
  schema_state "$OFFLINE_SCHEMA" "$EVIDENCE_ROOT/runs/preapplied_0036_serve/post-target.json"
fi

record complete "$EVIDENCE_ROOT"
exec 3>&-
trap - EXIT INT TERM
