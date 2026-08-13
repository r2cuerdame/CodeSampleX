#!/bin/sh
# Generates package-lock.json for every seed sample inside a container so the
# host never gets node_modules. --package-lock-only resolves the tree without
# installing anything.
set -e
for d in axios-post-json zod-parse-validate express-json-route dayjs-utc-format; do
  cd "/w/$d"
  npm install --package-lock-only --ignore-scripts --no-audit --no-fund --loglevel=error
  echo "$d: $(node -e 'const l=require("./package-lock.json");console.log(Object.keys(l.packages||{}).length+" resolved entries")')"
done
