#!/bin/sh
set -eu

# Bind the error window to the same host clock as Docker log timestamps,
# before any observation probe. Controller/host clock skew must not hide errors.
printf 'observation_started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"

# Observation only: initialize missing safe-log epoch metadata, but never
# reload, activate, use credentials, write to the DB, or purge logs.
# Each subprocess and the parent SSH invocation have independent deadlines.
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
DOMAIN=codesamplex.dev
tmp=$(mktemp -d)
name=csx-caddy-observe-$(basename "$tmp" | tr -cd 'a-zA-Z0-9')
cleanup() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  rm -rf "$tmp"
}
trap cleanup EXIT HUP INT TERM
identity() {
  docker inspect "$container" --format '{{range .Config.Env}}{{println .}}{{end}}' |
    sed -n 's/^CSX_VERSION=//p' | head -n 1
  docker inspect "$container" --format '{{.Image}}|{{.State.StartedAt}}'
  docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/version |
    sed -n 's/.*"revision":"\([0-9a-f]\{40\}\)".*/\1/p'
}
before=$(identity)
printf 'identity_before=%s\n' "$(printf '%s\n' "$before" | tr '\n' '|')"

run_check() {
  key=$1; shift
  start=$(date +%s)
  result=pass
  "$@" >"$tmp/check-output" 2>/dev/null || result=unavailable
  # Fixed probe identifiers and numeric diagnostics only; no arbitrary
  # curl/docker stderr, headers, bodies, or log lines leave the host.
  grep -E '^probe_[a-z0-9_]+_(status|curl_exit|content_valid)=[0-9]+$' "$tmp/check-output" || true
  if [ "$result" != unavailable ]; then
    result=$(tail -n 1 "$tmp/check-output")
    case "$result" in pass|fail|violation|unavailable) ;; *) result=unavailable ;; esac
  fi
  printf '%s=%s\n' "$key" "$result"
  printf '%s_seconds=%s\n' "$key" "$(( $(date +%s) - start ))"
}

privacy_preflight() {
  caddy_image=$(docker inspect codesamplex-caddy-1 --format '{{.Image}}') || return 1
  docker run -d --pull=never --name "$name" --tmpfs /var/log/caddy-safe:rw,mode=755 \
    -e CADDY_SITE=:18080 -v /opt/codesamplex/deploy/caddy/Caddyfile:/etc/caddy/Caddyfile:ro \
    "$caddy_image" >/dev/null || return 1
  for attempt in 1 2 3; do
    docker exec "$name" wget -q -T 3 -t 1 -O /dev/null \
      'http://127.0.0.1:18080/v1/samples/known-id-must-not-log?query-must-not-log=1' >/dev/null 2>&1 || true
    if docker exec "$name" grep -q '"csx_route":"samples"' /var/log/caddy-safe/access-safe.log; then break; fi
    sleep 1
  done
  docker exec "$name" wget -q -T 3 -t 1 -O /dev/null \
    'http://127.0.0.1:18080/v1/samples%2Fencoded-id-must-not-log/path' >/dev/null 2>&1 || true
  docker exec "$name" wget -q -T 3 -t 1 -O /dev/null \
    'http://127.0.0.1:18080/v1/unknown-secret-must-not-log/path' >/dev/null 2>&1 || true
  docker exec "$name" sh -c '
    log=/var/log/caddy-safe/access-safe.log
    test -r "$log" || exit 1
    if grep -Eq "known-id-must-not-log|query-must-not-log|encoded-id-must-not-log|unknown-secret-must-not-log|remote_ip|client_ip|headers|user_id|\"request\"|\?" "$log"; then
      echo fail
    elif test "$(stat -c %a "$log")" = 644 &&
         grep -q "\"csx_route\":\"samples\"" "$log" &&
         grep -q "\"csx_method\":\"get_head\"" "$log"; then echo pass
    else echo unavailable; fi
  '
}

privacy_live() {
  # The collection epoch belongs to observation metadata, outside the deploy
  # transaction. A permission/disk failure is an observation incident only.
  docker compose exec -T caddy sh -c '
    set -eu
    umask 022
    marker=/var/log/caddy-safe/access-safe.log.since
    if [ ! -f "$marker" ]; then
      temporary=$(mktemp /var/log/caddy-safe/.access-safe.since.XXXXXX)
      trap '\''rm -f "$temporary"'\'' EXIT HUP INT TERM
      date -u +%Y-%m-%dT%H:%M:%SZ > "$temporary"
      chmod 0644 "$temporary"
      mv "$temporary" "$marker"
    fi
  ' || return 1
  # Unique synthetic markers prove a leak in THIS observation. A missing log,
  # failed grep/read, transport error or old unsafe fields never prove a leak.
  nonce=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n') || return 1
  case "$nonce" in *[!0-9a-f]*|'') return 1 ;; esac
  marker=csx-observe-secret-$nonce
  request_failed=0
  probe_index=0
  for path in "/v1/stats?csx_safe_log_smoke=$marker" \
    "/v1/samples%2F$marker/path" "/v1/$marker/path"; do
    probe_index=$((probe_index + 1))
    probe_code=0
    curl --noproxy '*' --connect-timeout 5 --max-time 10 \
      --resolve "$DOMAIN:443:127.0.0.1" -sS -o /dev/null "https://$DOMAIN$path" || probe_code=$?
    if [ "$probe_code" -ne 0 ]; then request_failed=1; fi
    printf 'probe_privacy_live_%s_curl_exit=%s\n' "$probe_index" "$probe_code"
  done
  sleep 2
  docker compose exec -T caddy sh -c '
    log=/var/log/caddy-safe/access-safe.log
    test -r "$log" || exit 1
    grep -F "$1" "$log" >/dev/null
    result=$?
    if [ "$result" = 0 ]; then echo violation; exit 0; fi
    [ "$result" = 1 ] || exit 1
    [ "$2" = 0 ] || { echo unavailable; exit 0; }
    test "$(stat -c %a "$log")" = 644 &&
      test "$(stat -c %a "$log.since")" = 644 &&
      grep -q "\"csx_route\":\"stats\"" "$log" &&
      grep -q "\"csx_method\":\"get_head\"" "$log" || { echo unavailable; exit 0; }
    if grep -Eq "remote_ip|client_ip|headers|user_id|\"request\"|\?" "$log"; then echo fail
    else echo pass; fi
  ' sh "$marker" "$request_failed" >"$tmp/privacy-live" || return 1
  result=$(cat "$tmp/privacy-live")
  if [ "$result" = violation ]; then printf '%s\n' "$nonce" >"$tmp/privacy-proof"; fi
  if [ "$result" = pass ]; then
    docker compose exec -T server sh -c '
      test -r /var/log/caddy-safe/access-safe.log &&
      test -r /var/log/caddy-safe/access-safe.log.since &&
      test ! -e /var/log/caddy/access.log' || result=fail
  fi
  printf '%s\n' "$result"
}

public_surface() {
  failure=0
  probe_index=0
  check() {
    path=$1; want_type=$2; marker=$3
    probe_index=$((probe_index + 1))
    probe_code=0
    code=$(curl --noproxy '*' --connect-timeout 5 --max-time 10 \
      --resolve "$DOMAIN:443:127.0.0.1" -sS -D "$tmp/header" -o "$tmp/body" \
      -w '%{http_code}' "https://$DOMAIN$path") || { probe_code=$?; code=000; }
    content_valid=1
    if [ "$code" != 200 ] ||
      ! grep -qi "^content-type: *$want_type" "$tmp/header" ||
      ! grep -qF "$marker" "$tmp/body"; then failure=1; content_valid=0; fi
    printf 'probe_public_surface_%s_status=%s\n' "$probe_index" "$code"
    printf 'probe_public_surface_%s_curl_exit=%s\n' "$probe_index" "$probe_code"
    printf 'probe_public_surface_%s_content_valid=%s\n' "$probe_index" "$content_valid"
  }
  canonical() { printf '<link rel="canonical" href="https://%s%s">' "$DOMAIN" "$1"; }
  check / 'text/html' "$(canonical /)"
  check /gaps 'text/html' "$(canonical /gaps)"
  check /compatibility 'text/html' "$(canonical /compatibility)"
  check /findings 'text/html' "$(canonical /findings)"
  check /features 'text/html' "$(canonical /features)"
  check /v1/stats 'application/json' '"packages":'
  check /v1/wanted 'application/json' '"schemaVersion"'
  check /install.sh 'text/plain' 'SHA256SUMS.txt'
  check /install.ps1 'text/plain' 'Get-FileHash'
  if [ "$failure" = 0 ]; then echo pass; else echo fail; fi
}

admin_state() {
  response=$(docker compose exec -T server wget -S -T 5 -t 1 -O /dev/null http://127.0.0.1:8080/admin 2>&1 || true)
  expected=404
  if grep -Eq '^CSX_ADMIN_TOKEN_SHA256=[0-9a-f]{64}$' .env; then expected=401; fi
  # Status mismatch is diagnostic only: an HTTP 200 alone is not proof of
  # private data exposure, and this observer never sends admin credentials.
  if printf '%s\n' "$response" | grep -Eq "HTTP/[0-9.]+ $expected "; then echo pass; else echo fail; fi
}

activity_state() {
  docker compose exec -T server sh -c 'printf "%s\n" "$CSX_ACTIVITY_HASH_KEY" | grep -Eq "^[0-9a-f]{64}$"' || return 1
  result=$(docker compose exec -T db psql -U csx -d csx -Atqc "
    SELECT to_regclass('public.activity_buckets') IS NOT NULL
      AND to_regclass('public.activity_health') IS NOT NULL
      AND (SELECT string_agg(column_name, ',' ORDER BY ordinal_position)
        FROM information_schema.columns WHERE table_schema='public' AND table_name='activity_buckets')
        = 'kind,epoch,bucket,owner,first_seen,last_seen'") || return 1
  if [ "$result" = t ]; then echo pass; else echo fail; fi
}

run_check privacy_preflight privacy_preflight
run_check privacy_live privacy_live
if [ -f "$tmp/privacy-proof" ]; then printf 'privacy_live_probe_id=%s\n' "$(cat "$tmp/privacy-proof")"; fi
run_check public_surface public_surface
run_check admin_state admin_state
run_check activity_state activity_state
# Embed the existing detailed collector from the exact checked-out revision.
# A staged shell preserves errexit and emits only counts / sanitized JSON.
detail_started=$(date +%s)
cat >"$tmp/detail.sh" <<'CSX_DETAIL_COLLECTOR'
# The staged process needs the same bounded Docker and SQL policy.
docker_bin=$(command -v docker)
docker() {
  if [ "$1" = compose ] && [ "${2:-}" = exec ] && [ "${3:-}" = -T ] && [ "${4:-}" = db ]; then
    shift 4
    timeout --kill-after=5s 30s "$docker_bin" compose exec -T -e PGOPTIONS='-c statement_timeout=20000' db "$@"
  else
    timeout --kill-after=5s 30s "$docker_bin" "$@"
  fi
}
__CSX_DETAILED_COLLECTOR__
CSX_DETAIL_COLLECTOR
if timeout --kill-after=5s 180s sh "$tmp/detail.sh" >"$tmp/detail" 2>/dev/null; then
  sed -n '/^invariants=/s/^/detail_/p; /^failure_evidence_quality=/s/^/detail_/p; /^modern_failure_clusters=/s/^/detail_/p' "$tmp/detail"
  printf 'detail_status=pass\n'
else
  # Fixed diagnostic only; never expose SQL stderr or arbitrary staged output.
  if grep -qx 'detail_budget_status=collection-budget-exceeded' "$tmp/detail"; then
    printf 'detail_budget_status=collection-budget-exceeded\n'
  fi
  printf 'detail_status=unavailable\n'
fi
printf 'detail_seconds=%s\n' "$(( $(date +%s) - detail_started ))"
after=$(identity)
printf 'identity_after=%s\n' "$(printf '%s\n' "$after" | tr '\n' '|')"
printf 'observed_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
