# Issue #445 Result: Reliability regression: transient DB/query timeouts must never render or cache as 404

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/445
- Branch: `issue/445-reliability-regression-transient-db-query-timeouts`
- Milestone: v0.1.194
- Prior deliveries on this issue: PR #448 (strict 404 / bounded retry /
  cache headers / error UI, v0.1.195), PR #449 (retry storm, builder yield,
  v0.1.196). This branch is the coverage audit the issue asked for on top
  of them, plus the surface the production acceptance gate was missing.

## What the audit found

Every public detail route was read for the pattern the issue names --
`(nil, err)`, timeout, cancelled context, pool busy, empty fallback, or a
failed secondary lookup becoming "not found". The handlers (`internal/web`)
and the API (`internal/httpapi`) were clean after #448/#449. The remaining
holes were one layer down and one layer out:

1. **The adapter lied about an unreadable index.** Since #396,
   `webStore.PackageSymbols` and `SymbolPackageSpread` answered a failed
   read of the corpus-wide target index with `(nil, nil)` so a cold index
   would not 503 every package page. An empty list is an absence claim.
   The version page's own absence rule -- no symbols, no matrix, no
   samples, no failures -> 404 -- could therefore fire on an unreadable
   index as if it were proven absence, and the page had no way to know.
2. **`POST /v1/adoptions`** answered a store timeout on its sample lookup
   with a bare 500 (`writeErr`, not `writeStoreErr`): not a 404, but not
   the retryable status a machine client is promised either.
3. **The route ledger was invisible.** The counters #448 added
   (`proven_not_found`, `db_query_timeout`, `pool_busy`,
   `retry_attempted/suppressed/exhausted`, `final_503/504`) were read by
   nothing outside the test suite. The acceptance gate -- a production
   route panel showing `timeout -> 404 = 0` -- had no surface to read.

## What this branch delivers

1. **An unreadable symbol index is unknown, never absent** (`fccb406`).
   The adapter propagates the error. `internal/web/symbolsOrUnknown` keeps
   the #396 behaviour -- a release or package with other evidence still
   renders without its symbol list -- but the version page answers 503 +
   `Retry-After` instead of 404 when the list is unknown and nothing else
   was found. The #396 test that pinned the swallow is rewritten to pin
   the new contract.
2. **The ledger on production** (`3e2d3e6`). `GET /v1/ops/pool-metrics`
   gains a `routes` object (`measured: false` when unwired, following the
   `host.error` rule). Every transient final response writes one
   `web: transient final ... proven_not_found_total=N ... final_503_total=M`
   log line, throttled to one per second with exact totals (counted before
   the throttle), so `docker compose logs server | grep 'transient final'`
   is the route panel. `POST /v1/adoptions` joins the 503/504 +
   `Retry-After` contract. `docs/operations.md` records all three.

## Regression tests (the issue's seven, mapped)

| # | Requirement | Test |
| --- | --- | --- |
| 1 | proven absent -> 404 | `TestProvenAbsentEntityReturns404` (#448); `TestVersionRouteWithUnknownSymbolsNever404s` step 1 |
| 2 | first timeout, retry succeeds -> 200 | `TestTransientStoreTimeoutRetriesAndSucceeds`, `TestTransportFaultRetriesOnceAndSucceeds` (#448/#449) |
| 3 | repeated timeout -> 503/504 + retry metadata, never 404 | `TestRepeatedStoreTimeoutReturns503Never404`, `TestContextDeadlineExceededReturns504` (#448); `TestVersionRouteWithUnknownSymbolsNever404s` step 2 |
| 4 | pool busy -> retryable, never 404 | `TestStorePoolBusyReturns503Never404` (#448); `TestPackageSymbolsPropagatesIndexFailureInsteadOfEmpty`, `TestPackageSymbolsPropagatesErrorWhenSnapshotKeysFails` (`cmd/csx-server`) |
| 5 | healthy store unchanged | `TestHealthyStoreReturns200OK` (#448); recovery steps of every new test |
| 6 | transient failure not cached as negative 404 | `TestSampleMetaFailureIsNotCachedAsAbsence`, `TestSnapshotLoadFailureAnswersErrorNotAbsenceWhileDeferring`, `TestPackageVersionsFailureIsNotCachedAsAbsence`, `TestPackageSamplesFailureIsNotCachedAsEmpty` (`cmd/csx-server`) |
| 7 | localized/detail variants obey the contract | `TestLocalizedDetailRoutesUnderTransientFailureNever404` (ko/ja x version/symbol/sample/package) |
| obs | log/metric classification | `TestTransientFinalLogsClassifiedTotals`, `TestOpsMetricsHandlerReportsRouteOutcomes`, `TestBuildMuxOpsMetricsRouteRequiresAdminAuth` (wired ledger, proven 404 counted as such) |
| api | adoption lookup retryable | `TestAdoption_StoreTimeoutIsRetryableNever404` |

## Verification (this workstation, Windows 11, 2026-09-17)

| Check | Result |
| --- | --- |
| `go build ./...` via `run_observed_command` | PASS |
| `go test ./cmd/csx-server/... ./internal/web/... ./internal/httpapi/...` with `CSX_TEST_DSN` on a local `postgres:17-alpine` via `run_observed_command` | PASS (20.3 s / 53.5 s / 2.0 s; the PG integration suites `TestIntegrationOneStuckPageDoesNotTakeTheSiteDown`, `TestIntegrationBlockedAPIReadIsRetryableNotABug` ran against PostgreSQL) |
| `go test ./...` via `run_observed_command` | every touched package PASS; `deploy/lightsail`, `internal/daemon`, `internal/serverstore` (lease fencing) failed in the full parallel run and PASS re-run alone -- the known Windows parallel-run flake, packages untouched by this branch |

### Live route panel under induced pressure

Real `csx-server` binary from this branch against a throwaway
`postgres:17-alpine` (`live445`), governor off, one release seeded with a
package-level and a symbol snapshot, then an open transaction holding
`ACCESS EXCLUSIVE` on `compatibility_snapshots, packages, samples,
sample_packages, failure_clusters, wanted`.

Healthy baseline: `/npm/left-pad/1.3.0` 200, `/npm/left-pad/9.9.9` 404,
`/npm/left-pad/1.3.0/leftPad` 200, `/npm/left-pad/1.3.0/nope` 404,
`/samples/sha256:000...` 404 -> `routes.provenNotFound = 3`.

Under the lock (cold routes, statement ceiling ~8 s each):

| Route | HTTP | Retry-After | Cache-Control |
| --- | ---: | --- | --- |
| `/npm/right-pad/2.0.0` (exists) | 503 | 2 | no-cache, no-store, must-revalidate |
| `/npm/right-pad/9.9.9` (absent when healthy) | 503 | 2 | same |
| `/npm/right-pad` | 503 | 2 | same |
| `/npm/right-pad/2.0.0/nope` (absent when healthy) | 503 | 2 | same |
| `/samples/sha256:111...` (absent when healthy) | 503 | 2 | same |
| `/npm/right-pad/2.0.0?lang=ko` | 503 | 2 | same |
| `/v1/samples/sha256:111...` | 503 | 2 | same |
| `/v1/registry/symbols/npm/right-pad/leftPad` | 503 | 2 | same |

`routes` during the window: `provenNotFound 3 -> 3`, `dbQueryTimeout 0 -> 7`,
`retrySuppressed 0 -> 7`, `retryAttempted 0`, `final503 0 -> 7`,
`final504 0`. Log: 7 `web: transient final` lines, last one
`proven_not_found_total=3 db_query_timeout_total=7 ... final_503_total=7`.
**timeout -> 404 = 0.**

Localized 503 bodies (`?lang=ko`, `?lang=ja`): `<html lang>` correct,
`error.unavailable` text present, `error.not_found` text absent,
`#error-retry-btn` present, `maxAutoRetries = 1`, `noindex`, canonical link
preserved.

After the lock released: every route above returned to its healthy status
(200 / 404 exactly as in the baseline) on the first request -- no negative
entry survived the outage; `provenNotFound` then moved 3 -> 8 for the five
proven 404s.

One observation outside this issue's scope: a package seeded AFTER server
start answered 404 on its package page until restart, because the
prewarmed corpus-wide target index (`recordSnapshotCacheTTL`) was stale.
That is a successful-but-stale read, not a transient failure; in production
the builder pass is what refreshes it. Noted for a follow-up if newly built
packages are observed to 404 between passes.

## Deployment impact

- No migration, no new environment knob.
- `GET /v1/ops/pool-metrics` is additive: a new `routes` object; no field
  renamed. The observation scripts read by field name and are unaffected.
- One new throttled log line (`web: transient final`, <= 1/s), only while
  transient finals are being served.
- Behaviour change: a version page whose symbol index is unreadable and
  that has no other evidence now answers 503 instead of 404; `POST
  /v1/adoptions` answers 503/504 instead of 500 for a store timeout on its
  lookup. Everything else the visitor sees is unchanged.
- Ships with the next Production deploy (deploys carry all of `main`).
  Post-deploy route panel: `docker compose logs server | grep 'transient
  final'` across the builder pass -- `proven_not_found_total` must not move
  while `final_503_total` does.
