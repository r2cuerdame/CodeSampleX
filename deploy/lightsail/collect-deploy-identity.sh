#!/bin/sh
# Cheap identity only. Detailed SQL/log/quality scans belong to observation.
set -eu
# Only fixed stage names cross stderr. Docker/Compose/wget errors can contain
# environment values; keep them private and preserve the command's exit code.
run_probe() {
    stage=$1
    shift
    printf 'CSX-IDENTITY-STAGE-V1 %s\n' "$stage" >&2
    "$@" </dev/null 2>/dev/null
}
run_probe deploy-directory cd /opt/codesamplex/deploy
# Fetch before parsing: POSIX pipelines otherwise hide Docker/wget failures
# behind a successful sed/head, including a failed fetch with partial output.
container_env=$(run_probe container-revision docker inspect codesamplex-server-1 --format '{{range .Config.Env}}{{println .}}{{end}}')
revision=$(printf '%s\n' "$container_env" | sed -n 's/^CSX_VERSION=//p' | head -n 1)
image_digest=$(run_probe container-image docker inspect codesamplex-server-1 --format '{{.Image}}')
image_revision=$(run_probe image-revision docker image inspect "$image_digest" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}')
probe_health() {
    attempt=1
    while [ "$attempt" -le 3 ]; do
        status=0
        body=$(docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/healthz 2>/dev/null) || status=$?
        if [ "$status" -eq 0 ] && [ "$body" = "ok" ]; then
            printf '%s\n' "$body"
            return 0
        fi
        if [ "$status" -eq 0 ]; then
            status=1
        fi
        if [ "$attempt" -eq 3 ]; then
            return "$status"
        fi
        sleep 1
        attempt=$((attempt + 1))
    done
    return "${status:-1}"
}
health=$(run_probe health probe_health)
served_body=$(run_probe served-revision docker compose exec -T server wget -q -T 5 -t 1 -O- http://127.0.0.1:8080/version)
served_revision=$(printf '%s\n' "$served_body" | sed -n 's/.*"revision":"\([0-9a-f]\{40\}\)".*/\1/p' | head -n 1)
printf 'revision=%s\nimage_digest=%s\nimage_revision=%s\nhealth=%s\nserved_revision=%s\n' "$revision" "$image_digest" "$image_revision" "$health" "$served_revision"
