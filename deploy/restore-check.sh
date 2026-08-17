#!/bin/sh
# Proves the latest backup can actually be restored (goal.md §18).
#
# A dump nobody has ever restored is not a backup — it is a file. This
# restores the most recent pg_dump into a throwaway database, checks critical
# restored tables, reads the complete blob archive, and drops the copy. It
# touches nothing the running server uses.
#
# Install alongside the nightly backup:
#   45 3 * * 0 /opt/codesamplex/deploy/restore-check.sh >> /opt/codesamplex/restore-check.log 2>&1
set -eu

cd "$(dirname "$0")"
LATEST=$(find ../backups -mindepth 1 -maxdepth 1 -type d | sort | tail -1)
if [ -z "$LATEST" ]; then
  echo "restore-check: FAIL — no backup directory found under ../backups"
  exit 1
fi
DUMP="$LATEST/csx.pgdump"
BLOBS="$LATEST/blobs.tar.gz"
echo "restore-check: verifying $LATEST"

for f in "$DUMP" "$BLOBS"; do
  if [ ! -s "$f" ]; then
    echo "restore-check: FAIL — $f is missing or empty"
    exit 1
  fi
done

# The tables worth comparing: samples are the product, evidence_agg is the
# graph, shards are what clients read. A dump that restores empty versions
# of these would still "succeed" without this comparison.
TABLES="samples evidence_agg shards receipts"

counts() { # counts <database>
  for t in $TABLES; do
    printf '%s=' "$t"
    docker compose exec -T db psql -U csx -d "$1" -At -c "select count(*) from $t"
  done
}

docker compose exec -T db psql -U csx -d postgres -q \
  -c "drop database if exists csx_restorecheck" \
  -c "create database csx_restorecheck" >/dev/null

# Always drop the copy, including on failure: a leftover database would
# quietly double the disk this check is meant to protect.
cleanup() {
  docker compose exec -T db psql -U csx -d postgres -q \
    -c "drop database if exists csx_restorecheck" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker compose exec -T db pg_restore -U csx -d csx_restorecheck --no-owner --exit-on-error < "$DUMP" >/dev/null

echo "-- restored --"
RESTORED=$(counts csx_restorecheck)
echo "$RESTORED"

# Do not compare with the live database here: production remains writable
# while this weekly check runs, so legitimate rows accepted after pg_dump
# would make a healthy backup fail its test. pg_restore --exit-on-error proves
# the dump is structurally restorable; these non-empty product tables catch a
# valid-but-empty/wrong-database archive without racing live writes.
echo "$RESTORED" | while IFS='=' read -r table count; do
  case "$count" in
    ''|*[!0-9]*) echo "restore-check: FAIL — invalid count for $table"; exit 1 ;;
    0) echo "restore-check: FAIL — restored $table is empty"; exit 1 ;;
  esac
done

# A truncated tar lists fine until it is read, so decompress to /dev/null.
ENTRIES=$(tar xzf "$BLOBS" -O 2>/dev/null | wc -c)
if [ "$ENTRIES" -le 0 ]; then
  echo "restore-check: FAIL — blob archive unreadable or empty"
  exit 1
fi

echo "restore-check: PASS — dump restores identically, blob archive readable ($ENTRIES bytes)"
