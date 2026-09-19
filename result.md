# Issue #444 Result: upstream bug-fix claims, verified by execution across versions

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/444
- Branch: `issue/444-farm-verify-upstream-bugfix-claims-across`
- Semantics, pipeline, budget and the measured Phase 0: `docs/fix-verification.md`
- Executed evidence: `docs/evidence/issue-444-phase0-runs.json`

## Verdict

The pipeline exists end to end and has run end to end. Twenty-one real
release-note claims from seven packages (axios, express, pydantic, requests,
tokio, undici, zod) went from the collector's candidate document through the
deterministic validator, the leased queue, a reproducer executed in the
Docker sandbox at the claimed-bad and claimed-fixed releases, a signed
receipt per probe, receipt-checked run admission, the evaluator and the
public read. Result: 15 `VERIFIED_FIX`, 4 `REGRESSED`, 2 `REPRODUCED_BUG`
(`AMBIGUOUS`), 0 `CLAIMED_FIX` left, 69 runs, 566 Farm seconds, 10 min of
wall clock on one worker. No claim and no model statement reaches a verified
state without a FAIL receipt on the bad release and a PASS receipt on the
fixed one, and the test that ran it asserts exactly that.

## What this delivery adds on top of the earlier checkpoints

1. `POST /v1/fix-claims/{id}/samples` (`internal/httpapi/fixclaims.go`): the
   fix worker's own sample upload -- lease-gated, bound to the candidate's
   package, stored as a quarantined `DRAFT` for FAIL and PASS alike, never
   queued for cross-verification, never in the authoring inbox. The worker
   passes the lease id through; `csx fix-claims probe` without `--id` is a
   dry run that files nothing. Two consecutive `INFRASTRUCTURE` outcomes stop
   the loop. The receipt's `sha256:` fingerprint prefix is trimmed on both
   sides.
2. `TestPhase0ReproducersRunEndToEnd` (`internal/cli/fix_claims_phase0_test.go`,
   `CSX_TEST_DOCKER=1`): every checked-in reproducer against the real server
   mux and the real sandbox; writes the evidence file when
   `CSX_FIX_PHASE0_EVIDENCE` names a path.
3. A sanitizer defect the run exposed (`internal/sanitizer`): the sandbox's
   per-workspace container name (`csx-<16 hex>`) sat in every contract
   failure summary below the token floor, so every FAIL fingerprint the
   verifier ever produced was unique per run. `REPRODUCED_NOT_FIXED`,
   `REGRESSED` and the fingerprint-hint priority were unreachable. It is
   now `csx-<token>`; nothing that clustered before splits, because nothing
   did.
4. Docs: worker commands, the samples endpoint, the measured Phase 0
   numbers and findings (`docs/fix-verification.md`, `docs/rest.md`).

## Findings the release notes do not carry

- pydantic 2.13.3's `from_attributes` fix reproduces and is fixed on Python
  3.12 and does not reproduce on 3.14 (`ENVIRONMENT_DEPENDENT`).
- axios 1.18.1's `AxiosError#cause` note: 1.18.1 fails differently under the
  same contract (`AMBIGUOUS`).
- undici 6.28.1's WebSocket patch neither throws nor closes with 1002 within
  the contract's timeout (`AMBIGUOUS`); the 7.x and 8.x patches pass.
- undici 7.29.1's four patches read as `REGRESSED` at 8.10.1 because the
  planner's regression-watch probe found the same fingerprint one release
  line up; the 8.10.2 records beside them are `VERIFIED_FIX`.

## Source decisions this run surfaces (not made here)

- Whether a maintenance-line fix should be evaluated within its own release
  line, so that 8.10.1 does not turn the 7.29.1 record `REGRESSED`. The
  evaluator does what `docs/fix-verification.md` says; the data shows what
  that means for parallel lines.
- The sanitizer rule changes the fingerprint of every future contract
  failure whose summary starts with the docker command line. Those were
  singletons before, so no cluster splits, but it is a product-wide
  normalization change and is called out as such.

## Tests

2026-09-19, this workstation (Windows 11, Go from `go.mod`, Docker Desktop,
`CSX_TEST_DSN` on 127.0.0.1:5433 with `CSX_REQUIRE_TEST_DSN=1`):

```text
go build ./...                                                                    ok
go vet ./internal/cli/ ./internal/sanitizer/ ./internal/httpapi/                  ok
go test -count=1 ./internal/sanitizer/ ./internal/fixclaims/ ./internal/httpapi/
  ./internal/cli/ ./internal/serverstore/ ./internal/verifier/ ./internal/domain/
  ./internal/sandbox/                                                             ok (serverstore 500 s on PostgreSQL)
go test -count=1 ./internal/evidence/ ./internal/mcp/                             ok
CSX_TEST_DOCKER=1 go test ./internal/cli/ -run TestPhase0ReproducersRunEndToEnd   PASS (605 s)
```

## Not done here, and why

- **Farm deployment of the lane.** The Farm side (CodeSampleX-Farm) has to
  run `csx fix-claims work` with a writer session against production; that
  is a separate repository and a deploy, both outside this issue's checkout.
- **Automated reproducer generation.** The 16 reproducers were written by
  hand from the upstream issues and PRs (`source: UPSTREAM_REPRO` /
  `GENERATED`); `fixclaims.Resolve` prefers an existing sample and records
  the source, but no model writes reproducers in this milestone, by the
  issue's own non-goal.
- **Migration 0049 in production** ships with the next deploy through the
  offline-migration gate, as recorded in commit c50b63a.
