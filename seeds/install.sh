#!/bin/sh
# Materializes node_modules for each seed project from its lockfile, inside a
# container. The seed dirs are mounted, so `csx run` on the host can then
# observe a real build/test of real public packages without the host ever
# running npm itself.
set -e
for d in axios-post-json zod-parse-validate express-json-route dayjs-utc-format; do
  cd "/w/$d"
  npm ci --ignore-scripts --no-audit --no-fund --loglevel=error >/dev/null
  echo "$d: node_modules ready"
done
