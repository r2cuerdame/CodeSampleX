# Issue #433 independent cross-validation

Frozen inputs: FABLE commit `fb69368`, OPUS commit `5657019`; both audit the
repository at `8e4f4fc` and production at `8e822f11` / v0.1.189. This review
did not edit either audit, read another auditor worktree, touch production, or
run a new HTTP/DB probe. It compared all 38 ranked findings, recomputed the
rankings from the committed JSON exports, and inspected the current worktree
read-only. The machine-readable result is
[`CROSS_VALIDATION.json`](CROSS_VALIDATION.json).

## Result

| Classification | Findings | Meaning here |
| --- | ---: | --- |
| Confirmed | **28** | The bounded factual claim has direct support plus agreement or source/existing-data corroboration. Listed caveats still apply. |
| False Positive | **2** | The headline/causal conclusion is materially broader than its own evidence. A narrower metric is retained. |
| Needs Evidence | **8** | A material duration, universal quantifier, causal attribution, semantic judgment, or population definition remains open. |

The central operational result is robust: severe host CPU starvation, a
builder that outruns its five-minute interval, a deployed two-connection /
250 ms interactive policy, fail-closed package-page reads, and an unbounded
failure-cluster query combine into an approximately 95% package-page 503 rate.
The central corpus result is also robust but needs precise wording: 5,713 of
6,621 `CROSS_PASS` samples do not have two PASS peer keys in persisted receipts,
217 live samples have a latest non-PASS result, and duplicate, coordinate, and
metadata defects are material. The evidence proves those stored-record gaps;
it does not prove that an author and verifier were actually the same actor.

Two broad conclusions do not survive cross-validation:

1. FABLE P2-5's “demand is already saturated” headline is false outside the
   wanted board. Its narrower counts—1,180/1,192 wanted packages and
   4,578/4,627 wanted versions covered—are valid, but 1,775 observed packages
   have no live sample and OPUS measures 14.5% of project-bucket demand
   uncovered.
2. OPUS P1-9's “memory is the reason nothing stays cached” is false as written.
   OPUS itself reports a 95.85% overall cache-hit ratio and at least 96% outside
   `evidence_agg` and `failure_clusters`. The narrower finding is confirmed:
   those two tables have 64.38% and 76.15% hit ratios and account for 94.6% of
   lifetime block reads.

## Evidence independence

The numerical overlap is not automatically independent corroboration. Both
audits read the same production deployment in overlapping periods on
2026-09-15, use the same `pg_stat_statements` window beginning
2026-09-09T05:49:06Z, and scan the same corpus tables. Near-identical row and
statement totals are therefore **shared evidence with agreement**.

The genuinely separate corroboration is narrower:

- The audits made different package-page samples and found 28/29 and 47/50
  503s; OPUS repeated 19/20 after the container recreation.
- OPUS had no Lightsail API credential but independently observed repeated
  76–80% steal, zero idle, and no cgroup throttle. That corroborates FABLE's
  host-pressure result, while only FABLE directly establishes exhausted burst
  credit.
- Read-only source inspection independently corroborates mechanisms, not live
  counts: pool defaults at `internal/serverstore/pool.go:154`, fail-closed
  required reads at `internal/web/explorer.go:1049`, post-read cluster filtering
  at `cmd/csx-server/webstore.go:2161`, the second `CROSS_PASS` promotion at
  `internal/serverstore/pg.go:1871`, unconditional snapshot upserts at
  `internal/serverstore/pg.go:748`, and safe-log page/duration removal at
  `deploy/caddy/Caddyfile:45` and `:123`.
- Recomputing package rankings from the frozen exports checks arithmetic and
  ordering. It does not make their underlying production extraction a new,
  independent observation.

No new production probe was justified: the host was already measured as
starved, and more traffic or SQL would not close the remaining semantic or
longitudinal gaps.

## Every finding classified

### FABLE

| ID | Class | Comparison and preserved caveat |
| --- | --- | --- |
| P0-1 | **Confirmed** | OPUS P0-3 independently confirms severe CPU steal and excludes cgroup throttling. Burst-credit exhaustion is directly supported only by FABLE's Lightsail metrics. |
| P0-2 | **Confirmed** | OPUS P0-1 reproduces the package-page failure rate; source confirms the 2/250 ms deployed policy and fail-closed required reads. The percentage is a window estimate. |
| P0-3 | **Needs Evidence** | Interval overrun and write amplification are confirmed, but the completed durations differ: FABLE infers about 41 minutes while OPUS measured 8m36s and a later pass exceeding 20 minutes. Do not call 40 minutes typical yet. |
| P1-1 | **Confirmed** | OPUS P1-5 matches about 337k calls, 95.6k seconds total, and 82.5k seconds I/O; source confirms ecosystem filtering and the 500-row cap occur after the unbounded read. |
| P1-2 | **Confirmed** | OPUS P1-8 matches all three query families; current source retains the MATERIALIZED authoring coverage path. Historical calls cannot all be assigned to one route. |
| P1-3 | **Confirmed** | FABLE's bounded EXPLAIN scans all 109,701 dependency edges and source retains the whole-table grouping. No second audit repeated that EXPLAIN. |
| P1-4 | **Needs Evidence** | 502 storms and pre-listen reconciliation are observed, but OPUS excludes the same 502s as deploy artifacts and page routes are not logged. “Every restart” and “everyone” need controlled deploy telemetry. |
| P1-5 | **Confirmed** | OPUS P1-10's safe-log totals agree on shard 429s. Client retry/backoff behavior remains unknown. |
| P1-6 | **Confirmed** | FABLE's exhaustive latest-receipt set is 120 FAIL + 97 SKIPPED; OPUS independently finds 166 live PASS+FAIL samples. Intended environments are missing for 215/217, so do not assign all failures to broken code. |
| P1-7 | **Confirmed** | Both audits agree on 518 identical-contract groups, 677 redundant samples, and 44 shared-case groups. FABLE's “exact purl+symbols” is OPUS's near-coordinate definition, not strict content identity. |
| P1-8 | **Confirmed** | Counts and 130 candidates are in the exhaustive export; source contains compatibility branches for the inconsistent spellings. The raw-`@` candidate list exports 54 rows against the 68-row aggregate and is incomplete. |
| P2-1 | **Confirmed** | 1,679 live samples lack a PASS receipt naming a verifier image. This is missing provenance, not proof that a mutable image was used. |
| P2-2 | **Confirmed** | OPUS P0-4 matches 5,713/6,621 and five PASS-signing keys. This proves a persisted receipt gap, not actual author/verifier identity reuse. |
| P2-3 | **Needs Evidence** | Structural counts agree, but template predicates give 3,506 versus 5,406 and symbol heuristics give a 105 floor, 157 working set, and 798 broader candidates. Preserve each predicate; semantic correctness needs parsing or re-execution. |
| P2-4 | **Needs Evidence** | The 388-major-behind computation is real, but OPUS's recency test finds the corpus young. “Newest observed” is not necessarily current stable or demanded; validate against registries and demand. |
| P2-5 | **False Positive** | Wanted-board saturation is valid; the generalized demand conclusion is contradicted by 1,775 observed packages without samples and 14.5% uncovered project-bucket demand. |
| P2-6 | **Confirmed** | OPUS P1-10 and Caddy source confirm page routes are skipped and duration is deleted. Point-in-time curl/Playwright timing remains possible; retained history does not. |
| P2-7 | **Confirmed** | OPUS P1-7/P1-9/P2-18 match churn, size, memory, and index figures; source confirms unconditional snapshot updates. Lifetime table counters are not recent rates. |
| P2-8 | **Confirmed** | FABLE's authenticated probe plus both log analyses confirm farm timeouts and analytics write loss. Browser rendering cost remains unmeasured. |

### OPUS

| ID | Class | Comparison and preserved caveat |
| --- | --- | --- |
| P0-1 | **Confirmed** | FABLE P0-2's separate probes converge at about 95% package-page 503s. OPUS's confidence interval applies only to its uniform 50-coordinate draw. |
| P0-2 | **Confirmed** | The measured 8m36s pass exceeds the five-minute interval and the next exceeded 20 minutes; FABLE confirms the overrun and churn. Whole-window connection ownership was not traced per connection. |
| P0-3 | **Confirmed** | Repeated host samples, cgroup inspection, and FABLE's separate observations agree. OPUS alone does not prove burst-credit exhaustion. |
| P0-4 | **Needs Evidence** | The 5,713 receipt-rule failures, second promotion path, and L4 badge are confirmed. “Unearned” is stronger: an author LOCAL_PASS may have happened but was not persisted. Confirmed conclusion is that the label is not auditable from receipts. |
| P1-5 | **Confirmed** | FABLE P1-1 and source agree on the dominant unbounded cluster read. |
| P1-6 | **Confirmed** | FABLE reports the same 10.4M calls / 66k seconds; source confirms the multi-CTE function in two expression indexes. Call-site attribution is inferred from source and call pattern, not labeled by PostgreSQL. |
| P1-7 | **Confirmed** | FABLE matches both upsert totals; source confirms the missing snapshot guard and wide cluster comparison guard. |
| P1-8 | **Confirmed** | Server-log cadence, query totals, and FABLE's authenticated request agree. “Every poll” is bounded to the observation window. |
| P1-9 | **False Positive** | The literal “nothing stays cached” cause is contradicted by the audit's own overall hit ratios. Retain only the two-table working-set finding until controlled deltas rank memory against churn and CPU steal. |
| P1-10 | **Confirmed** | Caddy source confirms the omission; both audits find the same API error families. |
| P1-11 | **Needs Evidence** | Weekly output decline and backlog counts are direct, but “untouched” and the Farm-causality loop rely on point polls and an external incident narrative. Lane-level offer/claim/completion time series are needed. |
| P1-12 | **Confirmed** | The project-bucket calculation is complete and FABLE independently finds the same 1,775 observed packages without samples. Project buckets remain a proxy polluted by optional binaries. |
| P2-13 | **Needs Evidence** | Both heuristics find bad symbol declarations but use incompatible predicates. Use 105 as a conservative floor and 157 as OPUS's working set; do not merge with FABLE's 798. |
| P2-14 | **Confirmed** | Both audits confirm templated descriptions and exactly 1,809 missing subjects. Keep the 5,406 broad `^verify` and 3,506 narrow verify-in-pkg populations separate. |
| P2-15 | **Confirmed** | Strict/near definitions are explicit, and the 518/677/44 totals match FABLE. |
| P2-16 | **Confirmed** | Exhaustive exported counts support the jobs/drafts/tables; current-source search also finds no references to the six manual tables. Receipts on quarantined samples are retained evidence, not automatically garbage. |
| P2-17 | **Confirmed** | `docs/schema.md:139` still says the modern count is zero; the export says 229,517 complete clusters, and both `0034_*.sql` files exist. Duplicate numbering is confusing but non-breaking today. |
| P2-18 | **Confirmed** | FABLE independently reports the 26 MB trigram index and one lifetime scan. The counter has no known reset timestamp. |
| P2-19 | **Needs Evidence** | One 48.84s cold request, one 1.23s warm request, and one post-restart cancellation are valid samples, not proof of every-window/every-restart behavior. Repeat after host stabilization with retained duration. |

## Corpus defect counts

These are exhaustive over 7,070 live samples unless stated otherwise. Counts
with different predicates are deliberately not collapsed.

| Defect class | Count | Cross-validation boundary |
| --- | ---: | --- |
| `CROSS_PASS` with fewer than two PASS peer keys | **5,713 / 6,621** | Confirmed stored-evidence gap; 5,117 have exactly one receipt total. Does not prove actual non-independence. |
| Latest verification not PASS | **217** | 120 FAIL + 97 SKIPPED; 77 lack `stageFailures`. |
| No PASS receipt naming verifier image | **1,679** | Missing provenance only. |
| Identical-contract redundancy | **677 samples / 518 groups** | Confirmed; 9.6% of live corpus. |
| Strict exact duplicates | **82 samples / 82 groups** | OPUS: packages + symbols + contract. |
| Near coordinate duplicates | **213 / 204 groups** | OPUS definition; FABLE's close first-purl+symbols definition is 214 / 205. |
| Shared live `case_id` | **44** | Both audits agree. |
| Receipt linkage broken only by purl spelling | **130** | 75 golang + 55 npm. |
| Golang purl missing `v` | **76** | Confirmed aggregate. |
| Raw-`@` npm scoped manifest | **68** | Aggregate confirmed; candidate export lists only 54. |
| No symbols | **1,228** | Both audits agree. |
| Manifest/case symbol mismatch | **49** | Both audits agree; package mismatch count is 1. |
| Unsupported-symbol candidates | **105 floor; 157 working; 798 broad** | Needs one shared parser/re-execution predicate. |
| Broad `^verify` goal | **5,406** | OPUS broad predicate; FABLE's narrower template is 3,506. |
| Missing subject | **1,809** | Both audits agree. |
| Major behind newest observed | **388 coordinates / 1,131 samples** | Candidate signal only, not a confirmed stale defect. |
| Orphan authoring drafts | **73** | Confirmed frozen full-table result. |
| Cross jobs open / unsupported | **6 / 20** | Five open jobs were at least seven days old at audit time. |
| Manual orphan tables | **6** | Names absent from repository source; removal is not authorized here. |

Latest-non-PASS defects are most concentrated in
`pkg:golang/modernc.org/sqlite` (13), `pkg:gem/csv` (13),
`pkg:gem/mustermann` (9), `pkg:gem/rack-test` (9),
`pkg:golang/go.opentelemetry.io/otel` (8), and `pkg:cargo/rustls` (7).
This grouping removes the final version suffix and combines FAIL and SKIPPED;
it does not imply a shared root cause.

Identical-contract redundancy is led by:

| Package | Groups | Redundant samples |
| --- | ---: | ---: |
| `pkg:golang/golang.org/x/net` | 26 | 45 |
| `pkg:golang/google.golang.org/genproto/googleapis/rpc` | 18 | 33 |
| `pkg:golang/golang.org/x/sys` | 18 | 29 |
| `pkg:golang/golang.org/x/crypto` | 10 | 20 |
| `pkg:npm/vitest` | 9 | 19 |
| `pkg:npm/electron` | 11 | 16 |

## Undersampled ranking

The defensible ranking uses OPUS's distinct observed project buckets, requires
zero live samples, and removes platform-specific optional binaries. FABLE
independently supports the existence of the uncovered evidence tail. A bucket
is a demand proxy, not a user or direct API call, so this ranks investigation
and authoring opportunity rather than guaranteed value.

| Rank | Package | Project buckets | Rationale/caveat |
| ---: | --- | ---: | --- |
| 1 | `pkg:npm/recharts` | 494 | Callable library, zero live samples. |
| 2 | `pkg:npm/next` | 457 | Callable library; also one wanted ask. |
| 3 | `pkg:npm/zustand` | 358 | Callable library, zero live samples. |
| 4 | `pkg:npm/jiti` | 333 | Callable library, zero live samples. |
| 5 | `pkg:npm/%40dnd-kit/core` | 320 | Callable library, zero live samples. |
| 6 | `pkg:generic/cli/go` | 274 | Real observed gap, but requires a product-policy decision because generic package pages are unroutable today. |
| 7 | `pkg:generic/cli/npm` | 247 | Same generic-policy caveat. |
| 8 | `pkg:golang/github.com/grpc-ecosystem/grpc-gateway/v2` | 243 | Callable library, zero live samples. |
| 9 | `pkg:npm/webcrypto-core` | 233 | Callable library, zero live samples. |
| 10 | `pkg:npm/%40aws-sdk/client-s3` | 225 | Callable library, zero live samples. |
| 11 | `pkg:npm/%40tanstack/react-query` | 211 | Callable library, zero live samples. |

Wanted-only gaps should be a separate queue because they lack comparable
project-bucket counts: `pkg:pub/path_provider` (22 asks),
`pkg:pub/shared_preferences` (19), `pkg:golang/encoding/json` (4; standard
library policy gap), and `pkg:golang/net/http` (3; standard library policy gap).

Do not feed the raw uncovered ranking directly to authoring. OPUS shows that
25 of the top 40 are optional platform binaries such as `fsevents`,
`@esbuild/*`, and `@rollup/rollup-*-*`; lockfile resolution makes them look
popular even when they have no callable API surface.

## Oversampled ranking

Absolute density is not waste. FABLE's largest packages—`pgx/v5`, `x/net`, and
`x/sys`—also have substantial asks/project usage. The evidence-backed
oversampling signal is samples per project bucket (OPUS requires at least five
samples), supplemented by byte-identical contract redundancy.

| Rank | Package | Samples / versions | Project buckets | Samples per bucket |
| ---: | --- | ---: | ---: | ---: |
| 1 | `pkg:composer/league/csv` | 5 / 1 | 0 | unobserved |
| 2 | `pkg:gem/csv` | 16 / 3 | 9 | 1.778 |
| 3 | `pkg:gem/rack-test` | 21 / 1 | 36 | 0.583 |
| 4 | `pkg:gem/mustermann` | 22 / 2 | 39 | 0.564 |
| 5 | `pkg:gem/set` | 15 / 2 | 27 | 0.556 |
| 6 | `pkg:gem/activesupport` | 5 / 1 | 9 | 0.556 |
| 7 | `pkg:gem/sinatra` | 13 / 1 | 24 | 0.542 |
| 8 | `pkg:gem/rack` | 20 / 2 | 39 | 0.513 |
| 9 | `pkg:gem/rack-protection` | 16 / 1 | 33 | 0.485 |
| 10 | `pkg:gem/json` | 17 / 1 | 36 | 0.472 |

The ranking does not prove an individual sample is redundant. The stricter
deletion/suppression candidates are the identical-contract list above. That
list explains why high-demand `x/net` and `x/sys` can still deserve duplicate
control without being called globally over-sampled.

## Remediation order

No fix was implemented. This order respects dependencies between availability,
measurement, and corpus mutation.

### P0

1. **Stop the starvation loop.** Move production off exhausted burst capacity
   or reduce builder duty cycle below the available baseline; set the interval
   no shorter than the measured completed pass. Query comparisons are distorted
   until CPU scheduling is available.
2. **Restore interactive availability after headroom exists.** Move the deployed
   2-connection/250 ms override toward the shipped 6-connection/3 s policy with
   refusal and latency monitoring. Raising concurrency first could amplify load.
3. **Remove builder write amplification.** Add a no-op snapshot guard, skip
   unchanged clusters before SQL or compare digests, batch target evidence, and
   replace `builder_purl_coord` expression-index work with a stored normalized
   coordinate.
4. **Make trust output auditable.** Gate L4/CROSS_PASS presentation on persisted
   independent evidence, surface the latest result, require failure detail, and
   requeue the 217 latest non-PASS samples with explicit intended environments.

### P1

1. Bound `ListFailureClusters` in SQL by ecosystem and page limit; defer wide
   JSONB and provide an index-only/covering serving path.
2. Materialize builder-fed authoring/farm and dependency summaries instead of
   executing whole-corpus CTEs and whole-edge aggregates on polls/views.
3. Add privacy-safe page route, status, and duration telemetry and preserve
   restart logs. Then re-test the 502, sitemap, memory, and latency hypotheses
   using short counter deltas.
4. Keep the old server serving through reconciliation or listen before
   background reconciliation; validate the universal 502 claim with the new
   telemetry.
5. Normalize purls at ingest and linkage, enforce publish-time duplicate checks,
   and repair the incomplete raw-`@` candidate export.
6. Retarget authoring with filtered project buckets plus wanted asks: exclude
   optional binaries, prioritize the undersampled list, and suppress
   byte-identical contracts across unchanged releases.
7. Validate shard-client 429/502 backoff before tuning the limiter.
8. After availability work, update stale schema text, resolve duplicate
   migration numbering prospectively, triage orphan drafts/tables, and review
   the trigram index with reset-aware counters.

## Evidence still required

- A controlled post-capacity observation with page-route duration/status,
  builder pass IDs, connection occupancy, and short DB/cache counter deltas.
- Session-to-peer provenance that can test actual author/verifier independence,
  rather than inferring it from absent receipts.
- A common symbol parser or artifact re-execution pass for the 105/157/798
  candidate disagreement.
- Registry-current stable versions joined to search/project demand before any
  “stale” remediation.
- Lane-level authoring offer, claim, completion, and rejection time series to
  test the Farm feedback-loop claim.
