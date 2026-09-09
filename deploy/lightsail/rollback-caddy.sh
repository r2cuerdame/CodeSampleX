set -eu
cd /opt/codesamplex/deploy
live=/opt/codesamplex/deploy/caddy/Caddyfile
rollback=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-predeploy
absent=/opt/codesamplex/deploy/caddy/Caddyfile.rollback-absent
candidate=/opt/codesamplex/deploy/caddy/Caddyfile.candidate
container_present=/opt/codesamplex/deploy/caddy/container.rollback-present
container_absent=/opt/codesamplex/deploy/caddy/container.rollback-absent
container_running=/opt/codesamplex/deploy/caddy/container.rollback-running
container_stopped=/opt/codesamplex/deploy/caddy/container.rollback-stopped
image_id=/opt/codesamplex/deploy/caddy/container.rollback-image-id
one_of() {
  count=0
  for marker in "$@"; do if [ -e "$marker" ]; then count=$((count + 1)); fi; done
  test "$count" -eq 1
}
one_of "$rollback" "$absent"
one_of "$container_present" "$container_absent"
if [ -f "$container_present" ]; then one_of "$container_running" "$container_stopped"; test -f "$image_id"; fi
if docker container inspect codesamplex-caddy-1 >/dev/null 2>&1; then docker rm -f codesamplex-caddy-1 >/dev/null; fi
if [ -f "$rollback" ]; then
  chmod 0644 "$rollback"
  cp -p "$rollback" "$live"
else
  rm -f "$live" "$candidate"
fi
rm -f "$candidate"
if [ -f "$container_present" ]; then
  test -f "$rollback"
  old=$(cat "$image_id")
  printf '%s\n' "$old" | grep -Eq '^sha256:[0-9a-f]{64}$'
  if [ -f "$container_running" ]; then
    docker compose up -d --no-build --no-deps --force-recreate caddy
    test "$(docker inspect codesamplex-caddy-1 --format '{{.Image}}')" = "$old"
    docker compose exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile
    test "$(docker inspect codesamplex-caddy-1 --format '{{.State.Running}}')" = true
  else
    docker compose up --no-start --no-build --no-deps --force-recreate caddy >/dev/null
    test "$(docker inspect codesamplex-caddy-1 --format '{{.Image}}')" = "$old"
    test "$(docker inspect codesamplex-caddy-1 --format '{{.State.Running}}')" = false
  fi
else
  ! docker container inspect codesamplex-caddy-1 >/dev/null 2>&1
fi
if [ -f "$rollback" ]; then cmp -s "$rollback" "$live"; else test ! -e "$live"; fi
test ! -e "$candidate"
