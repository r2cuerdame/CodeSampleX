#!/bin/sh
# Materializes node_modules for each seed project from its lockfile, inside
# a container. The seed dirs are mounted, so `csx run` on the host can then
# observe a real build/test of real public packages without the host ever
# running npm itself. Seeds are discovered, never listed.
set -e
cd /w
for csx in */csx.json; do
  d=$(dirname "$csx")
  cd "/w/$d"
  npm ci --ignore-scripts --no-audit --no-fund --loglevel=error >/dev/null
  echo "$d: node_modules ready"
  cd /w
done
