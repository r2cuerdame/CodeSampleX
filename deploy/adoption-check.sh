#!/bin/sh
# Detects the first sign that someone other than the operator is using the
# network, and says so loudly and permanently.
#
# This exists because the decision it feeds is time-sensitive: sample
# publishing and the verification ladder are deliberately left open while
# there are no users, and both should be closed behind `csx login` once
# there are. Nobody watches a dashboard for that, so the network watches
# itself.
#
# Install:  20 4 * * * /opt/codesamplex/deploy/adoption-check.sh >> /opt/codesamplex/adoption.log 2>&1
# Check:    /opt/codesamplex/deploy/adoption-check.sh --status
set -eu

cd "$(dirname "$0")"
STATE=../adoption-state
MARKER="$STATE/EXTERNAL_TRAFFIC_DETECTED"
mkdir -p "$STATE"

q() { docker compose exec -T db psql -U csx -d csx -At -c "$1" 2>/dev/null | tr -d '\r'; }

# Signals, chosen so the operator's own activity cannot trigger them:
#
# - peers_today counts distinct anonymous buckets that filed evidence today.
#   Those rotate daily, so one machine is exactly one bucket: two or more
#   means a second machine.
# - receipt_keys counts distinct verifier keys ever seen. The operator's
#   seeding identity and daemon account for the baseline; anything above it
#   is a peer that verified something.
# - foreign_samples counts published samples whose seeder is not the
#   operator's, i.e. somebody else contributed content.
PEERS_TODAY=$(q "select count(distinct bucket) from evidence_dedup where bucket_kind='peer' and epoch = to_char(now() at time zone 'utc','YYYY-MM-DD')")
RECEIPT_KEYS=$(q "select count(distinct peer_id) from receipts")
FOREIGN_SAMPLES=$(q "select count(*) from samples where coalesce(origin_seeder,'anonymous') not in ('anonymous','r2cuerdame')")
NOW=$(date -u +%Y-%m-%dT%H:%M:%SZ)

# The baseline is written once, from the state at install time, so growth is
# measured against reality rather than against zero.
if [ ! -f "$STATE/baseline" ]; then
  printf 'recorded=%s\npeers_today=%s\nreceipt_keys=%s\nforeign_samples=%s\n' \
    "$NOW" "$PEERS_TODAY" "$RECEIPT_KEYS" "$FOREIGN_SAMPLES" > "$STATE/baseline"
  echo "$NOW baseline recorded: peers_today=$PEERS_TODAY receipt_keys=$RECEIPT_KEYS foreign_samples=$FOREIGN_SAMPLES"
fi
# shellcheck disable=SC1091
. "$STATE/baseline"
BASE_KEYS=$receipt_keys
BASE_FOREIGN=$foreign_samples

if [ "${1:-}" = "--status" ]; then
  if [ -f "$MARKER" ]; then
    echo "EXTERNAL TRAFFIC DETECTED"
    cat "$MARKER"
    echo
    echo "Next step: turn on GitHub sign-in for publishing and for the trust ladder."
    echo "  1. Create a GitHub OAuth app (device flow enabled)."
    echo "  2. Add CSX_GITHUB_CLIENT_ID / CSX_GITHUB_CLIENT_SECRET to deploy/.env"
    echo "  3. Redeploy. csx login and the device flow are already implemented."
    exit 0
  fi
  echo "no external traffic yet (baseline $recorded)"
  echo "  peers today   : $PEERS_TODAY (baseline $peers_today)"
  echo "  verifier keys : $RECEIPT_KEYS (baseline $BASE_KEYS)"
  echo "  outside samples: $FOREIGN_SAMPLES (baseline $BASE_FOREIGN)"
  exit 0
fi

TRIGGER=""
[ "$PEERS_TODAY" -ge 2 ] && TRIGGER="$TRIGGER peers_today=$PEERS_TODAY(>=2)"
[ "$RECEIPT_KEYS" -gt "$BASE_KEYS" ] && TRIGGER="$TRIGGER verifier_keys=$RECEIPT_KEYS(was $BASE_KEYS)"
[ "$FOREIGN_SAMPLES" -gt "$BASE_FOREIGN" ] && TRIGGER="$TRIGGER outside_samples=$FOREIGN_SAMPLES(was $BASE_FOREIGN)"

if [ -n "$TRIGGER" ] && [ ! -f "$MARKER" ]; then
  # Written once and kept: the point is that it is still here whenever
  # somebody next looks, not that it scrolled past in a log.
  printf 'first detected: %s\nsignals:%s\n' "$NOW" "$TRIGGER" > "$MARKER"
  echo "$NOW *** EXTERNAL TRAFFIC DETECTED ***$TRIGGER"
  echo "$NOW ACTION: turn on GitHub sign-in (see --status for the steps)"
  exit 0
fi

echo "$NOW quiet: peers_today=$PEERS_TODAY receipt_keys=$RECEIPT_KEYS outside_samples=$FOREIGN_SAMPLES"
