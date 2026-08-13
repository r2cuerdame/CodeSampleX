#!/bin/sh
# Runs every seed contract in a clean container: npm ci from the lockfile,
# then the contract command. Any failure fails the whole run.
#
# Seeds are discovered, not listed: a hard-coded list silently skipped
# every sample added after it was written.
set -e
cd /w
for csx in */csx.json; do
  d=$(dirname "$csx")
  cd "/w/$d"
  npm ci --ignore-scripts --no-audit --no-fund --loglevel=error >/dev/null
  printf '%-26s ' "$d"
  node test/contract.mjs
  rm -rf node_modules
  cd /w
done
echo "ALL SEED CONTRACTS PASS"
