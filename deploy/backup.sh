#!/bin/sh
# Nightly backup for the CodeSampleX host (goal.md §14.2, §18 backup/restore).
# Dumps PostgreSQL (custom format) and archives the blob store into
# ./backups/<UTC date>/, pruning archives older than 14 days.
# Install: crontab -e →  15 3 * * * /opt/codesamplex/deploy/backup.sh
set -eu

cd "$(dirname "$0")"
STAMP=$(date -u +%Y-%m-%d)
DEST="../backups/$STAMP"
mkdir -p "$DEST"

docker compose exec -T db pg_dump -U csx -d csx -Fc > "$DEST/csx.pgdump"

# Blob store lives in the named volume; archive via a throwaway container.
docker run --rm -v codesamplex_blobs:/blobs:ro -v "$(cd "$DEST" && pwd)":/backup \
  alpine:3.22 tar czf /backup/blobs.tar.gz -C /blobs .

find ../backups -mindepth 1 -maxdepth 1 -type d -mtime +14 -exec rm -rf {} +
echo "backup complete: $DEST"
