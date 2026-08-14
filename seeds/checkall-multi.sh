#!/bin/sh
# Runs every NON-npm seed contract exactly the way the sandbox does:
# resolve with the network on, contract with the network off, in the same
# pinned image, with the toolchain caches pointed inside the workspace.
#
# The npm seeds have checkall.sh; this covers pypi, golang and cargo, whose
# resolve output only survives the stage boundary because it lands in
# /work/.csx-vendor (see internal/sandbox/runner.go).
#
# Usage: sh seeds/checkall-multi.sh   (run from the repo root, needs docker)
set -eu

SEEDS=$(cd "$(dirname "$0")" && pwd)
FAILED=""

image_for() {
  case "$1" in
    bun) echo "oven/bun:1-alpine" ;;
    deno) echo "denoland/deno:alpine" ;;
    pypi) echo "python:3.12-alpine" ;;
    golang) echo "golang:1.26-alpine" ;;
    cargo) echo "rust:1-alpine" ;;
    *) echo "" ;;
  esac
}

# Same env the sandbox exports, kept in one place per ecosystem.
env_for() {
  case "$1" in
    deno) echo "--env DENO_DIR=/work/.csx-vendor/deno" ;;
    bun) echo "" ;;
    pypi) echo "--env PYTHONPATH=/work/.csx-vendor/py --env PYTHONDONTWRITEBYTECODE=1" ;;
    golang) echo "--env GOMODCACHE=/work/.csx-vendor/gomod --env GOCACHE=/work/.csx-vendor/gobuild --env GOFLAGS=-mod=mod" ;;
    cargo) echo "--env CARGO_HOME=/work/.csx-vendor/cargo --env CARGO_TARGET_DIR=/work/.csx-vendor/target" ;;
  esac
}

resolve_for() {
  case "$1" in
    bun) echo "bun install --ignore-scripts" ;;
    deno) echo "deno install" ;;
    pypi) echo "pip install --no-deps --no-compile --target /work/.csx-vendor/py -r requirements.txt" ;;
    golang) echo "go mod download" ;;
    cargo) echo "cargo fetch" ;;
  esac
}

for csx in "$SEEDS"/*/csx.json; do
  dir=$(dirname "$csx")
  name=$(basename "$dir")
  eco=$(grep -o '"ecosystem": *"[^"]*"' "$csx" | head -1 | cut -d'"' -f4)
  rt=$(grep -o '"runtime": *"[^"]*"' "$csx" | head -1 | cut -d'"' -f4)
  # npm-on-node has its own driver (checkall.sh). npm on another runtime
  # belongs here, because the RUNTIME is what selects the image.
  case "$eco/$rt" in
    npm/node|npm/) continue ;;
    npm/*) eco="$rt" ;;
  esac
  image=$(image_for "$eco")
  if [ -z "$image" ]; then
    echo "$name: unknown ecosystem $eco" >&2
    FAILED="$FAILED $name"
    continue
  fi

  # contractCommand from the manifest, so the check cannot drift from what
  # the verifier will actually run.
  cmd=$(sed -n 's/.*"contractCommand": *\[\([^]]*\)\].*/\1/p' "$csx" | tr -d '"' | tr ',' ' ')

  printf '%-28s %-7s ' "$name" "$eco"
  # shellcheck disable=SC2086
  if ! docker run --rm --memory=512m $(env_for "$eco") -v "$dir:/work" -w /work \
      "$image" sh -c "$(resolve_for "$eco")" >/tmp/csx-resolve.log 2>&1; then
    echo "RESOLVE FAILED"; tail -5 /tmp/csx-resolve.log; FAILED="$FAILED $name"; continue
  fi
  # The status must come from docker, not from tail: piping into tail makes
  # the pipeline exit 0 whatever the contract did, which is how the first
  # version of this script reported ALL PASS while a seed was failing.
  # shellcheck disable=SC2086
  if docker run --rm --network=none --memory=512m $(env_for "$eco") -v "$dir:/work" -w /work \
      "$image" $cmd >/tmp/csx-contract.log 2>&1; then
    tail -1 /tmp/csx-contract.log
  else
    echo "CONTRACT FAILED"
    tail -5 /tmp/csx-contract.log
    FAILED="$FAILED $name"
  fi
done

if [ -n "$FAILED" ]; then
  echo "FAILED:$FAILED"
  exit 1
fi
echo "ALL NON-NPM SEED CONTRACTS PASS"
