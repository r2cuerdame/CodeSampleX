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

# The same digest-pinned references the verifier runs, copied from the one
# registry in internal/sandbox/images.go. Checking a seed against the floating
# tag would check it against different bytes than the network will, which is
# the whole failure this script is meant to catch early.
image_for() {
  case "$1" in
    bun) echo "oven/bun:1-alpine@sha256:07235578f79ef8c6f97d94aee7938e76f5cdba5f21ae5dbfdd3d3d38058437eb" ;;
    deno) echo "denoland/deno:alpine@sha256:b49ac52f05c3d8d0da890b6628168e9bfb5721f7bccc00305bb3ad29ed0e40af" ;;
    pypi) echo "python:3.12-alpine@sha256:d09d15e60962ca365d1cd544a48773bac9d33f2fb1b00f2aa0deec78ade7dc31" ;;
    golang) echo "golang:1.26-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468" ;;
    cargo) echo "rust:1-alpine@sha256:a10e64dd139b7387337c7fbe8aca31b959b57b2fd4c8ae20a02cf1d6ea424dce" ;;
    composer) echo "composer:2@sha256:4d71c3c2109c61d5415544264b59ad4087e4c5b7244481723664138fd36d5040" ;;
    gem) echo "ruby:3@sha256:364bd08657bc1106373e8c2fc1b39b68f384f339decc5867374caf6e2e112927" ;;
    pub) echo "dart:3.13.0@sha256:8b6175f6c6b89aaf31ffdace4a22d17715c07f1cf3a772dadb10c658f779e23d" ;;
    hex) echo "elixir:1.20.1-alpine@sha256:f50894ff69b0d07b310fe9c97b48b3475568ecccb7f0ccd7c350a789feb395a3" ;;
    *) echo "" ;;
  esac
}

# Same env the sandbox exports, kept in one place per ecosystem.
env_for() {
  case "$1" in
    deno) echo "--env DENO_DIR=/work/.csx-vendor/deno" ;;
    bun) echo "" ;;
    pypi) echo "--env PYTHONPATH=/work/.csx-vendor/py --env PYTHONDONTWRITEBYTECODE=1" ;;
    golang) echo "--env GOMODCACHE=/work/.csx-vendor/gomod --env GOCACHE=/work/.csx-vendor/gobuild --env GOFLAGS=-mod=readonly" ;;
    cargo) echo "--env CARGO_HOME=/work/.csx-vendor/cargo --env CARGO_TARGET_DIR=/work/.csx-vendor/target" ;;
    composer) echo "--env COMPOSER_HOME=/work/.csx-vendor/composer --env COMPOSER_CACHE_DIR=/work/.csx-vendor/composer/cache" ;;
    gem) echo "--env GEM_HOME=/work/.csx-vendor/gems --env GEM_PATH=/work/.csx-vendor/gems --env BUNDLE_PATH__SYSTEM=true --env BUNDLE_FROZEN=true --env BUNDLE_APP_CONFIG=/work/.csx-vendor/bundle" ;;
    pub) echo "--env PUB_CACHE=/work/.csx-vendor/pub" ;;
    hex) echo "--env MIX_HOME=/work/.csx-vendor/mix --env HEX_HOME=/work/.csx-vendor/hex --env MIX_ENV=test" ;;
  esac
}

# Mirrors resolveCommand() in internal/sandbox/runner.go. A seed that passes
# here but fails there means these two have drifted — runner.go is the one
# that decides whether the network accepts a receipt.
resolve_for() {
  case "$1" in
    bun) echo "rm -rf /work/node_modules; bun install --frozen-lockfile --ignore-scripts" ;;
    deno) echo "rm -rf /work/node_modules /work/.csx-vendor/deno; deno install --frozen" ;;
    pypi) echo "set -e; rm -rf /work/.csx-vendor/py /work/.csx-vendor/pip-report.json; mkdir -p /work/.csx-vendor; pip install --no-deps --no-compile --report /work/.csx-vendor/pip-report.json --target /work/.csx-vendor/py -r requirements.txt" ;;
    golang) echo "set -e; rm -rf /work/.csx-vendor/gomod /work/.csx-vendor/gobuild /work/.csx-vendor/go-modules.json; mkdir -p /work/.csx-vendor; go mod download; go list -m -json all > /work/.csx-vendor/go-modules.json" ;;
    cargo) echo "rm -rf /work/.csx-vendor/cargo /work/.csx-vendor/target; cargo fetch --locked" ;;
    composer) echo "rm -rf /work/vendor /work/.csx-vendor/composer; composer install --no-scripts --no-plugins --no-interaction --no-progress --prefer-dist" ;;
    gem) cat <<'GEM_RESOLVE'
set -e
if [ ! -f Gemfile ]; then
  echo "csx: no Gemfile. A gem sample must pin its dependency in a Gemfile" >&2
  echo "csx: (with the exact version) so the resolve can be reproduced." >&2
  exit 1
fi
if grep -qE "require ['\"](minitest|rspec|test-unit)" test/*.rb 2>/dev/null; then
  if ! grep -qE "(minitest|rspec|test-unit)" Gemfile; then
    echo "csx: the contract requires a test framework that is not in the Gemfile." >&2
    echo "csx: GEM_PATH is the vendor directory only, so nothing the base image" >&2
    echo "csx: ships is visible. Assert with plain Ruby -- raise unless ... -- or" >&2
    echo "csx: declare and pin the framework as a dependency." >&2
    exit 1
  fi
fi
rm -rf /work/.csx-vendor/gems /work/.csx-vendor/bundle
gem install bundler --no-document -q
bundle install --quiet
GEM_RESOLVE
    ;;
    pub) echo "rm -rf /work/.dart_tool /work/.csx-vendor/pub; dart pub get --enforce-lockfile" ;;
    # Single-quoted so the outer shell leaves the parameter expansions alone;
    # mix never sees the sample's mix.exs, it only reads mix.lock as text.
    hex) echo 'set -e; rm -rf /work/.csx-vendor/mix /work/.csx-vendor/hex /work/.csx-vendor/nomix /work/deps /work/_build; mkdir -p /work/.csx-vendor/nomix /work/deps; cd /work/.csx-vendor/nomix; mix local.hex --force >/dev/null; mix local.rebar --force >/dev/null; for s in $(grep -oE ":hex, :[A-Za-z0-9_]+, \"[^\"]+\"" /work/mix.lock | tr -d " \""); do n=${s#:hex,:}; n=${n%%,*}; v=${s##*,}; mix hex.package fetch "$n" "$v" --unpack --output "/work/deps/$n"; done' ;;
    # No silent success: an ecosystem with an image but no resolve step would
    # otherwise run its contract against an empty workspace.
    *) echo "echo 'checkall-multi: no resolve command for $1' >&2; exit 1" ;;
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

  # Every run starts from the immutable seed, never output from a previous
  # resolver. The target is validated against the absolute seeds directory
  # before the recursive delete is allowed.
  vendor_dir="$dir/.csx-vendor"
  case "$vendor_dir" in
    "$SEEDS"/*/.csx-vendor) rm -rf -- "$vendor_dir" ;;
    *) echo "$name: unsafe generated-directory path" >&2; exit 1 ;;
  esac

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
