#!/bin/sh
# Generates package-lock.json for every seed sample inside a container so
# the host never gets node_modules. --package-lock-only resolves the tree
# without installing anything. Seeds are discovered, never listed.
set -e
cd /w
for csx in */csx.json; do
  d=$(dirname "$csx")
  # Only npm seeds have a package.json. Running npm here against a python,
  # go or cargo seed used to leave a stray package-lock.json behind, which
  # then travelled inside the published artifact.
  grep -o '"ecosystem": *"[^"]*"' "$csx" | head -1 | grep -q '"npm"' || continue
  cd "/w/$d"
  npm install --package-lock-only --ignore-scripts --no-audit --no-fund --loglevel=error
  echo "$d: lockfile written"
  cd /w
done
