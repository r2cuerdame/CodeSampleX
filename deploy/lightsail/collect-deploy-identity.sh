#!/bin/sh
# Cheap identity only. Detailed SQL/log/quality scans belong to observation.
set -eu
cd /opt/codesamplex/deploy
revision=$(docker inspect codesamplex-server-1 --format '{{range .Config.Env}}{{println .}}{{end}}' | sed -n 's/^CSX_VERSION=//p' | head -n 1)
image_digest=$(docker inspect codesamplex-server-1 --format '{{.Image}}')
image_revision=$(docker image inspect "$image_digest" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
health=$(docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/healthz)
served_revision=$(docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/version | sed -n 's/.*"revision":"\([0-9a-f]\{40\}\)".*/\1/p' | head -n 1)
printf 'revision=%s\nimage_digest=%s\nimage_revision=%s\nhealth=%s\nserved_revision=%s\n' "$revision" "$image_digest" "$image_revision" "$health" "$served_revision"
