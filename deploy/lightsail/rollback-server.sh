set -eu
cd /opt/codesamplex/deploy
restore_dist=__CSX_RESTORE_DIST__
one_of() {
  count=0
  for marker in "$@"; do if [ -e "$marker" ]; then count=$((count + 1)); fi; done
  test "$count" -eq 1
}
one_of docker-compose.yml.rollback-predeploy docker-compose.yml.rollback-absent
one_of .env.rollback-predeploy .env.rollback-absent
one_of server-container.rollback-present server-container.rollback-absent
one_of server-latest.rollback-id server-latest.rollback-absent
if [ -f server-container.rollback-present ]; then
  one_of server-container.rollback-running server-container.rollback-stopped
  test -f server-image.rollback-id
  old=$(cat server-image.rollback-id)
  printf '%s\n' "$old" | grep -Eq '^sha256:[0-9a-f]{64}$'
  test "$(docker image inspect codesamplex/csx-server:rollback-predeploy --format '{{.Id}}')" = "$old"
else
  test ! -e server-image.rollback-id
  test ! -e server-container.rollback-running
  test ! -e server-container.rollback-stopped
fi
if [ -f .env.rollback-predeploy ]; then
  test ! -e .env.rollback-absent
fi
if [ "$restore_dist" -eq 1 ] && { [ ! -f dist.rollback-promoted ] || [ ! -d /opt/codesamplex/dist.previous ]; }; then restore_dist=0; fi
if [ "$restore_dist" -eq 1 ]; then test -d /opt/codesamplex/dist.previous; fi
if docker container inspect codesamplex-server-1 >/dev/null 2>&1; then docker rm -f codesamplex-server-1 >/dev/null; fi
if [ -f docker-compose.yml.rollback-predeploy ]; then
  cp -p docker-compose.yml.rollback-predeploy docker-compose.yml
else
  rm -f docker-compose.yml
fi
if [ -f .env.rollback-predeploy ]; then
  cp -p .env.rollback-predeploy .env
  chmod 0600 .env
else
  rm -f .env
fi
rm -f docker-compose.yml.candidate .env.new .env.activity.* .env.admin.* caddy/Caddyfile.candidate
if [ "$restore_dist" -eq 1 ]; then
  rm -rf /opt/codesamplex/dist.rollback-stage /opt/codesamplex/dist.failed-rollback
  cp -a /opt/codesamplex/dist.previous /opt/codesamplex/dist.rollback-stage
  if [ -d /opt/codesamplex/dist ]; then mv /opt/codesamplex/dist /opt/codesamplex/dist.failed-rollback; fi
  if mv /opt/codesamplex/dist.rollback-stage /opt/codesamplex/dist; then
    rm -rf /opt/codesamplex/dist.failed-rollback
  else
    mv /opt/codesamplex/dist.failed-rollback /opt/codesamplex/dist
    exit 68
  fi
fi
if [ -f server-container.rollback-present ]; then
  docker tag codesamplex/csx-server:rollback-predeploy codesamplex/csx-server:latest
  docker compose up -d --no-build --no-deps --force-recreate server
  test "$(docker inspect codesamplex-server-1 --format '{{.Image}}')" = "$old"
  if [ -f server-container.rollback-running ]; then
    i=0
    while [ "$i" -lt 24 ]; do
      if docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/healthz 2>/dev/null | grep -q '^ok'; then break; fi
      i=$((i + 1))
      sleep 5
    done
    test "$i" -lt 24
    test "$(docker inspect codesamplex-server-1 --format '{{.State.Running}}')" = true
    expected=$(docker inspect codesamplex-server-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^CSX_VERSION=//p' | head -n 1)
    served=$(docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/version | sed -n 's/.*"revision":"\([0-9a-f]\{40\}\)".*/\1/p' | head -n 1)
    test "$served" = "$expected"
  else
    docker compose stop server >/dev/null
    test "$(docker inspect codesamplex-server-1 --format '{{.State.Running}}')" = false
  fi
else
  ! docker container inspect codesamplex-server-1 >/dev/null 2>&1
fi
if [ -f server-latest.rollback-id ]; then
  latest=$(cat server-latest.rollback-id)
  printf '%s\n' "$latest" | grep -Eq '^sha256:[0-9a-f]{64}$'
  test "$(docker image inspect codesamplex/csx-server:rollback-latest-predeploy --format '{{.Id}}')" = "$latest"
  docker tag codesamplex/csx-server:rollback-latest-predeploy codesamplex/csx-server:latest
  test "$(docker image inspect codesamplex/csx-server:latest --format '{{.Id}}')" = "$latest"
else
  if docker image inspect codesamplex/csx-server:latest >/dev/null 2>&1; then docker image rm codesamplex/csx-server:latest >/dev/null; fi
  ! docker image inspect codesamplex/csx-server:latest >/dev/null 2>&1
fi
if [ -f docker-compose.yml.rollback-predeploy ]; then cmp -s docker-compose.yml.rollback-predeploy docker-compose.yml; else test ! -e docker-compose.yml; fi
if [ -f .env.rollback-predeploy ]; then cmp -s .env.rollback-predeploy .env; else test ! -e .env; fi
test ! -e docker-compose.yml.candidate
test ! -e .env.new
