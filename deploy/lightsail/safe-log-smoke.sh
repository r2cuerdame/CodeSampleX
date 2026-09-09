set -eu
cd /opt/codesamplex/deploy
docker compose exec -T caddy sh -s <<'CSX_SAFE_LOG_EPOCH'
  umask 022
  marker=/var/log/caddy-safe/access-safe.log.since
  if [ ! -f "$marker" ]; then
    tmp=$(mktemp /var/log/caddy-safe/.access-safe.since.XXXXXX)
    date -u +%Y-%m-%dT%H:%M:%SZ > "$tmp"
    chmod 0644 "$tmp"
    mv "$tmp" "$marker"
  fi
CSX_SAFE_LOG_EPOCH

# These three are the FIRST requests this deploy makes through the proxy, and
# they carry a ten-second ceiling. When one of them timed out, the only thing
# the transcript said was `curl: (28)` -- identifying which of the three had
# stalled took the edge access log of the box afterwards. A fail-closed
# production rollout has to name the request it failed on.
log_probe() {
    probe_path="$1"
    probe_code=0
    curl --noproxy '*' --connect-timeout 5 --max-time 10 --resolve '__CSX_DOMAIN__:443:127.0.0.1' -sS -o /dev/null "https://__CSX_DOMAIN__$probe_path" || probe_code=$?
    if [ "$probe_code" -ne 0 ]; then
        echo "FAIL privacy-safe log probe $probe_path: curl exit $probe_code" >&2
        exit 1
    fi
}
log_probe '/v1/stats?csx_safe_log_smoke=discard-this-query'
log_probe '/v1/samples%2Fencoded-marker-must-not-log/path'
log_probe '/v1/secret-marker-must-not-log/path'
i=0
while [ "$i" -lt 10 ]; do
  if docker compose exec -T caddy sh -c "grep -q '\"csx_route\":\"stats\"' /var/log/caddy-safe/access-safe.log 2>/dev/null"; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
docker compose exec -T caddy sh -s <<'CSX_SAFE_LOG_VERIFY'
  test -f /var/log/caddy-safe/access-safe.log
  test "$(stat -c %a /var/log/caddy-safe/access-safe.log)" = 644
  test "$(stat -c %a /var/log/caddy-safe/access-safe.log.since)" = 644
  ! grep -q "discard-this-query" /var/log/caddy-safe/access-safe.log
  ! grep -q "encoded-marker-must-not-log" /var/log/caddy-safe/access-safe.log
  ! grep -q "secret-marker-must-not-log" /var/log/caddy-safe/access-safe.log
  ! grep -q '?' /var/log/caddy-safe/access-safe.log
  grep -q '"csx_method":"get_head"' /var/log/caddy-safe/access-safe.log
  ! grep -Eq 'remote_ip|client_ip|headers|user_id|"request"' /var/log/caddy-safe/access-safe.log
CSX_SAFE_LOG_VERIFY
docker compose exec -T server sh -s <<'CSX_SAFE_LOG_SERVER_VERIFY'
  test -r /var/log/caddy-safe/access-safe.log
  test -r /var/log/caddy-safe/access-safe.log.since
  test ! -e /var/log/caddy/access.log
CSX_SAFE_LOG_SERVER_VERIFY
