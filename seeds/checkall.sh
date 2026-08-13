#!/bin/sh
# Runs every seed contract in a clean container: npm ci from the lockfile,
# then the contract command. Any failure fails the whole run.
set -e
for d in axios-post-json zod-parse-validate express-json-route dayjs-utc-format; do
  cd "/w/$d"
  npm ci --ignore-scripts --no-audit --no-fund --loglevel=error >/dev/null
  printf '%-22s ' "$d"
  node test/contract.mjs
  rm -rf node_modules
done
echo "ALL SEED CONTRACTS PASS"
