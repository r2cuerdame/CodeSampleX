# Issue #426 Result: Web: eliminate intermittent slow/503 package detail navigation

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/426
- Branch: `issue/426-web-eliminate-intermittent-slow-503-package`
- Milestone: v0.1.194
- Related: #174 (serial admission accumulation), #429/#431 (package gate
  reservation, cold fan-out bound), #433 (saturated-gate shed contract),
  #445 (retry storm)

## What was wrong

The 2026-09-15 internal-link crawl navigated never-visited package pages
while the hourly builder pass held the two-core host. Two independent
mechanisms produced the numbers in the issue:

1. **A cold page made its gated reads in sequence.** The cube read its
   release window one release at a time -- up to six admission-gated
   `GetSnapshotsForPURL` round trips, each waiting for one of the four
   package cache-miss slots -- and the dependency table then read every
   child release the same way, three abreast, up to forty of them. Each
   read is sub-millisecond idle (measured on production 2026-09-17:
   `GetSnapshotsForPURL` 0.4 ms) and hundreds of milliseconds during a
   builder pass; the sequence is where /npm/fs-extra 2.65 s, /npm/tmp
   2.09 s, /npm/got 1.77 s, /npm/globals 1.69 s and /npm/jsonfile 1.44 s
   went, and it is what kept the four slots full for the next visitor.
2. **The gate refused with the pool idle.** Every gated read waited at most
   250 ms for a slot and then was refused with `ErrPoolBusy` -- a 503 --
   and the gate had no memory across a request's reads. Two overlapping
   cold pages, each making seven gated reads, were enough for one page's
   read to be refused while no connection was busy. Production's pressure
   line recorded exactly that shape a minute after the 2026-09-17 restart:
   `admission_refused=1 pool_busy=0` on two package pages. That is the
   /npm/strip-ansi, external-editor, flora-colossus and galactus 503s.

## What this branch delivers

1. **One page, one gated release read** (`86525f6`).
   `Store.GetSnapshotsForPURLs(ctx, purls)` (`purl = ANY($1)`, primary-key
   probes, one checkout) on both the PG store and the Fake;
   `webStore.PrefetchSnapshots` loads every not-yet-cached release of a
   page in one gated read with the per-release loader's semantics (no rows
   = authoritatively absent, a deferring lane is left to its deferral, a
   real failure arms the same 15 s deferral, an admission refusal or a
   departed caller arms nothing). `loadCubeFacts` prefetches the window
   before reading it; `packageDeps` prefetches every child before the
   three workers start, and renders every child as unknown when the
   prefetch is refused instead of re-asking forty times. `retryStore`
   forwards the prefetch so the wrapper does not hide it.
2. **One admission allowance per request** (`e685417`). An interactive
   request stands at the cache-miss gate for at most
   `packageLoadAdmissionBudget` = 3 s in total across every cold read it
   makes -- the pool's own `ReadWait`, so the gate never refuses sooner
   than the pool would have. The time is measured as the union of the
   request's waits (`admissionClock` in `dbclass.go`): four reads waiting
   side by side for half a second cost the visitor half a second, not two.
   Once the allowance is spent further reads are refused at once, which is
   what keeps serial cold reads from accumulating (#174). A background
   stale-cache refresh has nobody waiting on it and keeps the 250 ms
   patience. The #433 shed contract is unchanged -- one wait, then a 503,
   never a retry -- with the allowance as the wait, and the test now also
   asserts the shed does not come before it.
3. **PG parity for the new read** (`f9a2f9e`). The bulk read is played
   against the per-release reads on both stores: every symbol of every
   requested release, (purl, symbol) order, blanks and duplicates in the
   request tolerated, absent release and empty request answered with
   nothing, a wildcard-shaped name (`snap_x` vs `snapXx`) unable to widen
   the match, and six releases = one checkout on PG. Mutation check:
   `purl LIKE ANY($1)` fails the PG test.
4. **Operator record** in `docs/operations.md` ("Cold package navigation
   (#426)") naming the two mechanisms, the two constants and the
   post-deploy number to watch.

## Regression tests

| Test | Pins |
| --- | --- |
| `TestConcurrentColdPackageNavigationNever503sWhenPoolIsIdle` (`cmd/csx-server`) | six cold package pages navigated at once with 350 ms reads and an idle pool all answer 200 -- fails before `e685417` with a 503 (`admission_refused=1 pool_busy=0`) |
| `TestColdPackagePageReadsAllReleaseSnapshotsInOneRoundTrip` | a cold page issues exactly one bulk snapshot read and zero per-release reads; the warm visit issues no store read |
| `TestAdmissionAllowanceIsSpentOncePerRequest` | after one allowance the request's next cold read is refused at once |
| `TestAdmissionAllowanceChargesParallelWaitsOnce` | parallel waits are charged as one; half an allowance spent in parallel leaves half for the read that decides the page |
| `TestBackgroundRefreshKeepsShortAdmissionPatience`, `TestInteractiveReadWithoutRequestKeepsShortAdmissionPatience` | the 250 ms patience is unchanged where nobody is waiting |
| `TestSaturatedAdmissionGateShedsInsteadOfRetrying` (#433) | a saturated gate sheds after one allowance -- not before it, not three times it |
| `TestFakeGetSnapshotsForPURLsMatchesPerReleaseReads`, `TestIntegrationGetSnapshotsForPURLsMatchesPerReleaseReadsInOneCheckout` | bulk read == per-release reads on both stores; one checkout on PG |

## Verification (this workstation, Windows 11, 2026-09-17)

| Check | Result |
| --- | --- |
| `go build ./...` via `run_observed_command` | PASS |
| `go vet ./cmd/csx-server/ ./internal/web/ ./internal/serverstore/` | PASS |
| `go test -count=1 ./cmd/csx-server/ ./internal/web/ ./internal/serverstore/` with `CSX_TEST_DSN` on a local `postgres:17-alpine` via `run_observed_command` | PASS (21.1 s / 41.7 s / 248.6 s; every `TestIntegration*` in serverstore ran against PostgreSQL, none skipped) |
| Mutation: `= ANY` -> `LIKE ANY` in `PG.GetSnapshotsForPURLs` | `TestIntegrationGetSnapshotsForPURLsMatchesPerReleaseReadsInOneCheckout` FAILS; restored |

## Production measurements

- **Before (2026-09-15 crawl, from the issue):** /npm/fs-extra 2.65 s,
  /npm/tmp 2.09 s, /npm/got 1.77 s, /npm/globals 1.69 s, /npm/jsonfile
  1.44 s; /npm/strip-ansi, external-editor, flora-colossus, galactus 503.
- **Before, idle (2026-09-17 ~13:00Z, this workstation, read-only curl,
  builder pass complete at `generatedAt` 12:34:46Z):** `/version` (no store
  call) TTFB 0.46 s with TLS complete at 0.26 s, so ~0.2 s of every number
  below is the network floor from here. The nine pages above: all 200,
  TTFB 0.35-0.57 s. The issue's numbers do not reproduce on a warm, idle
  host; they need a cold page during a builder pass, which is what the two
  regression tests model with 350 ms reads.
- **After deploy, the number to watch:** the post-deploy observation
  artifact's `admission_refused_event_total` across a builder pass. With
  `pool_busy_total=0` it must stay at zero, and package pages must not 503.
  Cold-page TTFB during a builder pass should fall from N x (per-read wait)
  to roughly one wait plus the bulk read.

## Deployment impact

- No migration. `GetSnapshotsForPURLs` reads `compatibility_snapshots` by
  its primary key.
- No new environment knob. `packageLoadAdmissionBudget` is a constant (3 s
  = `DefaultPoolPolicy.ReadWait`); `packageLoadAdmissionWait` (250 ms)
  remains for background refreshes.
- Behaviour change under saturation: an interactive cold page that cannot
  get a slot now waits up to 3 s before its 503 instead of 250 ms. The
  pool's own `ReadWait` is already 3 s, so the page's worst case is
  unchanged; what changes is that an idle pool no longer produces 503s.
- Ships with the next Production deploy (deploys carry all of `main`).
