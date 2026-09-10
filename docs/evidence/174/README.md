# Issue 174 DevHotel acceptance harness

Run only inside an owned DevHotel managed web room with a managed PostgreSQL 17 service. The room's normal app port may proxy to the candidate's internal port. Do not use this fixture against production.

The frozen source archive, SHA-256, Git commit, Go version, binary SHA-256, room ID, managed command run IDs, test JSON, screenshots, and final sleeping state belong in the accompanying job/PR evidence. A source archive copied while the working tree is dirty is development evidence, not an exact-commit release build.

Install Go 1.26.5 and these browser dependencies in the room:

```sh
export PLAYWRIGHT_BROWSERS_PATH=/workspace/acceptance/browsers
mkdir -p /workspace/acceptance
cd /workspace/acceptance
npm init -y
npm install --save-exact @playwright/test@1.58.2 pg@8.16.3
npx playwright install --with-deps chromium
```

Extract the reviewed source archive to a fresh directory. Set `CSX_DSN` to a disposable database schema that exists and belongs only to this acceptance run. For DevHotel's disposable PostgreSQL service, the default connection is `postgres://devhotel:devhotel@127.0.0.1:5432/devhotel?sslmode=disable`; append `&search_path=<new_schema>` after creating that schema. The PostgreSQL integration suites create and drop their own schemas.

```sh
export CSX_TEST_DSN="$CSX_DSN" CSX_REQUIRE_TEST_DSN=1
# Capture JSON and inspect it even when go test exits nonzero.
go test -json ./internal/serverstore ./internal/compatibility ./internal/deploygate -count=1 > pg-tests.json
go test -json ./cmd/csx-server -run '^TestIntegration' -count=1 > pressure-tests.json
```

Require nonempty integration results and zero failed/skipped tests. This harness complements PostgreSQL plan/row/byte/checkout assertions; browser timing does not establish database complexity or production recovery.

From the extracted repository root, seed the real sample blob, receipts, package/symbol evidence, and initial builder output:

```sh
export CSX_BLOB_DIR=/workspace/blobs CSX_ACCEPTANCE_ROOT=/workspace/acceptance
mkdir -p .devhotel-seed
cp docs/evidence/174/seed.go.txt .devhotel-seed/main.go
go run ./.devhotel-seed
# Avoid stamping an ancestor checkout's unrelated VCS metadata into an archive build.
go build -trimpath -buildvcs=false -o /workspace/candidate-server ./cmd/csx-server
sha256sum /workspace/candidate-server
```

Record `/workspace/acceptance/build.json` with `commit`, `sourceArchiveSha256`, `binarySha256`, `goVersion`, and `builtAt`. Start the candidate through DevHotel managed execution with `CSX_VERSION=<exact_commit>`, `CSX_BUILD_VERSION=devhotel-174-<short_commit>`, `CSX_BUILT_AT=<actual_build_time>`, `CSX_LISTEN=:3100`, and `CSX_SNAPSHOT_INTERVAL=1s`. Preserve its stdout/stderr. Leave the pool/query guards at their shipped defaults. Point the room app/proxy at port 3100, then run:

```sh
export PLAYWRIGHT_BROWSERS_PATH=/workspace/acceptance/browsers
export CSX_ACCEPTANCE_ROOM_ID=<owned_room_id>
export CSX_ACCEPTANCE_BASE_URL=http://127.0.0.1:3000
cp docs/evidence/174/acceptance.mjs /workspace/acceptance/acceptance.mjs
node /workspace/acceptance/acceptance.mjs
```

The script waits for the candidate's real `/version` identity, checks home, sample collection, package, exact symbol, and sample artifact code at 1440×1000 and 390×844, exercises sample search, rejects horizontal overflow and browser/network/HTTP errors, records ten screenshots, and performs five rounds of seven HTTP route checks. It writes `result.json` on completed browser runs. Also inspect candidate builder logs for completed incremental passes and failure/pressure records within that exact acceptance window. The accelerated interval exercises the runtime; it does not claim production load equivalence.

Export the result, screenshots, build identity, and builder logs through DevHotel. Sleep the room after the final check and record the resulting sleeping/stopped state. A failed or unavailable required acceptance blocks release/deployment.
