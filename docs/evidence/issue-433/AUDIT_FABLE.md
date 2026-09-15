# Issue #433 audit (FABLE): corpus trust and site/admin performance

Frozen 2026-09-15T15:40Z. Read-only. Production was not modified; every
number below came from a `SELECT`, a public HTTP request, an SSH read of
counters and logs, or the Lightsail metrics API. Machine-readable candidates
are in `AUDIT_FABLE_DATA.json`; every command is in `AUDIT_FABLE_COMMANDS.md`;
the SQL is under `sql/`.

Code audited: `origin/main` at `8e4f4fc`. Production ran `8e822f11`
(v0.1.189) throughout, which is two package-page fixes behind main (#431,
#432). The Farm repository was read and not changed.

## Populations

| population | count | scope |
|---|---|---|
| samples rows | 8,346 | exhaustive |
| live samples (`NOT quarantined`) | 7,070 (6,621 CROSS_PASS, 355 STABLE, 94 PUBLISHED) | exhaustive |
| quarantined | 1,276 (983 dedup 2026-08-19, 244 template-goal dedup, 45 drafts, 4 superseded) | exhaustive |
| receipts | 10,966 from 6 peer keys | exhaustive |
| wanted tuples / asks | 4,681 / 5,637 over 1,192 packages | exhaustive |
| search hits / misses (30 d) | 6,864 / 4,151 from 100 / 88 anonymous ids | exhaustive |
| `pg_stat_statements` window | since 2026-09-09T05:49Z (6.4 days) | exhaustive |
| HTTP probes | 51 requests over 17 endpoints in 3 rounds, plus 20 spaced package pages | sampled |

## Verdict in one paragraph

The site is slow and mostly refuses package pages because the production
host has had no CPU burst credit since 2026-09-02 and is pinned at the
Lightsail small_3_0 baseline of 20% CPU, while a 5-minute aggregation
builder whose passes take about 40 minutes keeps the database busy with
20 million mostly no-op upserts. On top of that starvation the host carries
an operator override of two interactive connections and a 250 ms wait, so
28 of 29 package-page requests answered 503. The corpus itself is
structurally sound (every live sample has a PASS receipt, no dangling
case/receipt links) but 217 live samples failed their latest re-verification
and are still badged verified, duplicates re-accumulated after the August
dedup (214 exact, 677 identical-contract), purl spelling is inconsistent
(130 samples cannot be matched to their own receipt), and demand is already
saturated: 1,180 of 1,192 wanted packages have a sample while 76% of live
samples have never been shown in any search hit.

## Ranked findings

### P0-1  Host CPU: burst credits exhausted since 2026-09-02, pinned at 20%

Evidence:

- Lightsail `BurstCapacityPercentage` for `csx-prod-1` (bundle
  `small_3_0`, 2 vCPU / 2 GB): 0.00–0.05% in every 6-hour bucket from
  2026-09-02T11:37 KST to now, except a recovery window 2026-09-12T17:37 to
  2026-09-13T11:37 KST when average CPU dropped to 5% and credit climbed
  to 53%, then was consumed in six hours.
- `CPUUtilization` average is 20.00% in 44 of the last 56 buckets: the
  baseline ceiling, not the demand.
- On the host at 14:33Z: `top` 63.4% steal, 0.0% idle, 19.5% iowait, load
  7.7 on 2 vCPU; `/proc/stat` steal = 26.4M jiffies against 19.2M idle and
  9.4M user over 3 d 8 h uptime (45% of all time stolen); PSI cpu `some
  avg300=83.4`.
- Consequence measured on the DB: an index scan that reads 15 pages
  (`compatibility_snapshots` by purl) took 790 ms, 651 ms of it I/O wait
  at 43 ms per page, on a disk whose own `r_await` is 4.4 ms. CPU
  starvation is what turns every page read into tens of milliseconds.

Affected surface: every request, the builder, the farm's authoring and
verification polls, deploy smoke checks.

Expected fix: move the host off the burstable baseline (larger bundle or a
non-burstable class), or cut the builder load below the baseline first
(P0-3) so the credit balance can recover. Nothing in the query/index family
can be evaluated honestly until the CPU floor is gone.

### P0-2  Package pages answer 503: READ_CONNS=2 / READ_WAIT=250ms override plus per-page fan-out

Evidence:

- `/opt/codesamplex/deploy/.env` on the host sets `CSX_DB_READ_CONNS=2` and
  `CSX_DB_READ_WAIT=250ms`. The shipped policy (docs/operations.md) is 6
  and 3 s. The server container confirms both values in its environment.
- Probe: 9 of 9 package-page requests in three rounds returned 503
  (`/npm/lru-cache`, `/pypi/sqlalchemy`, `/golang/…pgx/v5`); a second probe
  of 20 distinct package pages spaced 2 s apart returned 503 for 19
  (`/golang/…testify` answered 200 in 1.2 s). `/samples` 503 on 2 of 3,
  `/v1/registry/packages/…` 503 on 3 of 3, `/v1/stats` 503 on 1 of 3, a
  sample page 503 on 1 of 3.
- Server log, 40 minutes after the 14:32Z restart: 46 `cause=pool_busy`
  lines (`waited=250–549ms`), 23 on package pages, 15 on sample pages, 14
  on `/dependencies`; 6 interactive `query_timeout`; 2 `admission_refused`.
- Package page render issues up to 20 distinct store reads per request
  (`cmd/csx-server/webstore.go`: SnapshotKeys, ListSnapshots,
  ListFailureClusters, GetSample, ReceiptsForSample, DependencyParents,
  …), each needing one of the two connections; two concurrent visitors
  exhaust the class and the third waits 250 ms and is refused.

Affected surface: all package, sample and listing pages; registry API.

Expected fix: the override is a knob, not code. Raising it back toward the
shipped 6 / 3 s restores availability at the cost of latency, which is the
documented trade. The durable fix is fewer reads per page (P1-1, P1-3) and
the CPU floor (P0-1).

### P0-3  Builder: 5-minute passes that take 40 minutes and rewrite everything they touch

Evidence (pg_stat_statements, 6.4 days):

| statement | calls | rows | total s | mean |
|---|---|---|---|---|
| `INSERT INTO failure_clusters … ON CONFLICT DO UPDATE … WHERE (row changed)` | 20,035,511 | 84,580 | 41,479 | 2.1 ms |
| `builder_purl_coord(raw)` SQL function (expression-index maintenance) | 10,408,036 | — | 66,094 | 6.4 ms |
| `INSERT INTO compatibility_snapshots … DO UPDATE` | 2,333,516 | 2,333,516 | 14,865 | 6.4 ms |
| `SELECT … FROM evidence_agg WHERE purl=$1 AND symbol = ANY($2)` | 2,517,012 | 33.6M | 14,072 | 5.6 ms |
| `SELECT purl, symbol FROM compatibility_snapshots ORDER BY purl, symbol` | 5,000 | 103.8M | 7,346 | 1.47 s |

- The cluster upsert is guarded by a `WHERE` that only writes changed rows:
  20.0M statements produced 84,580 row writes (0.4%). The work is the
  statement itself: `pg_stat_user_tables` shows 15.2M updates on 235,988
  rows lifetime and the heap is 314 MB at 1,397 bytes per row.
- The snapshot upsert has no such guard (`generated_at = now()` always
  changes), so 21,692 rows were rewritten 107 times each in 6 days
  (11.1M updates lifetime, 5,440 dead tuples at probe).
- `internal/compatibility/builder.go` rebuilds every cluster of every
  touched package on every incremental pass (correct by design, see the
  comment at the cluster loop), with `fullPassEvery = 12` and
  `CSX_SNAPSHOT_INTERVAL=5m`. The pass that started 14:32Z reached
  `cluster_read` at 14:38Z and the next pass started 15:18:51Z: about 41
  minutes per incremental pass followed by a 5-minute gap. Phase timings
  from that pass: `target_evidence` 66.9 s over 2,436 targets,
  `snapshot_calculate` 25.9 s, `snapshot_write` 87.2 s for 2,436 rows,
  `snapshot_retire` 5.8 s.
- The builder shares the box and the 8-connection pool with the site. Six
  `postgres` backends were the top CPU consumers at probe time.

Affected surface: every interactive read (contention), disk cache (the
rewrites evict the pages the pages need), autovacuum.

Expected fix: write only what changed (a content hash on the snapshot, and
skip the cluster upsert when the computed row equals the stored one before
sending it), lengthen the interval to the pass duration, and stop the
expression index from re-evaluating a regex/decode function per row.

### P1-1  `ListFailureClusters` per package: the largest statement by total time

- `SELECT id, COALESCE(ecosystem,…) … FROM failure_clusters WHERE
  package_name=$1 AND (quality filter) ORDER BY observation_count DESC, id`:
  337,224 calls, 283 ms mean, 8.0 s max, 95,569 s total, 30.0M shared
  blocks read, 82,486 s of read I/O. It is called by the package page
  (`webstore.FailureClusters`, 30-minute cache) and by the issue route
  (`FailureIssueClusters`, uncached).
- `EXPLAIN (ANALYZE, BUFFERS)` for `lru-cache`: bitmap index scan, 355
  rows, 227 heap pages read from disk, 3,049 ms I/O, 3,123 ms total. Rows
  per package: p50 27, p90 282, p99 468, max 9,352 (`golang.org/x/sys`).
- 337k calls in 6.4 days is 37 per minute against a 30-minute cache: the
  cache is keyed by ecosystem|name and expires under crawler traffic, and
  every miss reads the full package ledger to keep at most 500 rows.

Expected fix: index-only or covering read of the columns the page uses;
cap the read at the page bound in SQL; a longer TTL or builder-fed cache.

### P1-2  Authoring candidate scan: whole-corpus CTE run 3,375 times

- Three variants of `WITH verified_samples AS MATERIALIZED (…)` in
  `internal/serverstore/dependencyclosure_pg.go`: 192 calls at 183 s mean
  (433 s max, 5.3M temp blocks written), 2,124 calls at 10.4 s, 1,059
  calls at 10.1 s: 68,148 s total.
- `internal/httpapi/authoring_work.go` caches the snapshot for 30 minutes
  and retries a failed scan up to 5 times with backoff; the server log
  shows `authoring candidate scan failed (statement timeout)` and `retry
  1/5` at 14:36Z and again at 15:19Z. With an 8–10 s ceiling and a 10–183 s
  scan, the retry series runs the scan to its timeout repeatedly; the pass
  that finally completes is the one that wrote 5.3M temp blocks.

Expected fix: materialize the candidate set in the builder (which already
holds every verified sample in memory) instead of re-deriving it from
`samples × receipts × sample_packages × dependency_edge` per poll.

### P1-3  `/dependencies` aggregates the whole edge table per view

- `SELECT ecosystem, child_name, child_version, count(DISTINCT parent…),
  count(*), count(*) OVER () FROM dependency_edge WHERE (…) GROUP BY 1,2,3
  ORDER BY … LIMIT $2 OFFSET $3` (`internal/serverstore/dependencyatlas.go`):
  904 calls, 4.1 s mean, 8.0 s max.
- `EXPLAIN ANALYZE`: seq scan of 109,701 rows, sort 14 MB in memory,
  GroupAggregate to 4,264 groups, then top-50: 10,163 ms with every buffer
  a cache hit and zero disk reads. This is pure CPU at the baseline floor,
  and it crosses the 8 s ceiling: `/dependencies class=interactive
  cause=query_timeout` appears in the log.

Expected fix: a materialized child-summary table maintained by the
builder (it already reconciles the atlas at boot), or at least an index on
`(ecosystem, child_name, child_version)` plus a pre-aggregated count.

### P1-4  Deploys and restarts serve 502 to everyone for minutes

- Caddy error log, last 90 minutes: 1,923 `dial tcp 172.18.0.2:8080:
  connect: connection refused`, 390 `lookup server … server misbehaving`,
  8 `i/o timeout`. The server container was recreated twice during the
  audit (14:31:59Z and 15:18:41Z, same v0.1.189) by an operator; the
  server reconciles stranded drafts, cross-job lanes, publicness and the
  dependency atlas before it listens (`cmd/csx-server/main.go:178-219`).
- Safe access log (API routes only): 502 = 1,255 today (277 in the 14h
  bucket, 978 in the 15h bucket), 431 on 2026-09-14, 2,529 on 2026-09-13.
  Page views are not logged (`log_skip`), so the visitor-facing 502 count
  is higher than these API-only figures.

Expected fix: listen first, reconcile in the background (the reconcile
loops are bounded and idempotent), or keep the old container serving until
the new one is healthy.

### P1-5  Shard API 429s

- `csx_route=shards`: 6,390 × 200, 2,518 × 304, 1,255 × 502, 749 × 429
  today; 1,354 × 429 on 2026-09-14. The rate limit and the restart 502s hit
  the same route the `csx sync` warm path depends on. Client retry
  behaviour on 429/502 was not found in `internal/` by a grep for
  `Retry-After`/`429`; whether clients back off is unverified.

### P1-6  217 live samples failed their latest verification and stay badged verified

- Latest receipt per live sample: PASS 6,853, FAIL 120, SKIPPED (resolve
  failed) 97. All 217 are `status IN (CROSS_PASS, PUBLISHED)` and every
  one has an earlier PASS receipt, so `levelBadge` (any PASS) renders them
  L4/L3 and the sitemap advertises them.
- FAIL set: 109 CROSS_PASS re-run by the farm verify key on 2026-09-07/08
  (84 golang, 29 cargo), average 17.8 days after their last PASS. For 53
  golang and 26 cargo samples the FAIL image is `golang:1.26-alpine` /
  `rust:1-alpine` while the PASS receipt named no image at all (older
  schema); for 21 golang samples the PASS was on alpine and the FAIL on
  debian. The receipts do not say which environment was intended:
  `verification_jobs.want_env` is empty for 215 of 217. 77 of the 217
  latest non-PASS receipts carry no `stageFailures` at all (9 FAIL, 68
  SKIPPED), so the reason is unrecoverable from the record.
- SKIPPED set: 68 PUBLISHED gem samples (rack-test, mustermann, csv,
  sinatra, faraday, json, set, ostruct) whose 2026-08-18 re-run failed at
  resolve under the retired mill key and were never re-queued.
- Receipt sequences over live samples: `PASS` 5,131, `PASS>PASS` 1,269,
  `PASS>PASS>PASS` 346, `PASS>SKIPPED` 86, `PASS>PASS>FAIL` 71, `PASS>FAIL`
  47.

Expected fix: surface the latest result on the sample page and in search
grading (a sample whose newest pinned re-run failed is a finding, not a
verified answer), require `stageFailures` on every non-PASS receipt, and
re-queue the 97 resolve failures. Candidate list: `candidates.latestNonPass`.

### P1-7  Duplicates re-accumulated after the 2026-08-19 dedup

- Exact `first package + sorted symbols` duplicates among live samples:
  205 groups, 214 redundant samples (the admin panel query
  `WITH pub AS …` counts the same thing at 1.7 s per call, 7,690 calls).
- Identical contract arrays: 518 groups, 677 redundant samples (largest
  groups of 6: estraverse KEYS, update-browserslist-db, bcrypt, electron-
  to-chromium; these are the same contract published once per version).
- Same `case_id` under two live samples: 44 groups. Same package name and
  symbols across versions: 795 groups, 1,212 redundant.
- Search hits confirm the cost: 1,721 of 7,070 live samples have ever
  been shown; duplicates split the small demand further.

Expected fix: the dedup that ran on 2026-08-19 needs to run at publish
time (the work queue re-issued answered coordinates: 244 were quarantined
for exactly that on 2026-08-20). Candidates: `dupPurlSymbolGroups`,
`dupContractGroups`, `dupCaseIdGroups`.

### P1-8  Coordinate spelling is inconsistent, and it breaks receipt linkage

- Live manifests: 68 use `pkg:npm/@scope/name`, 1,617 use
  `pkg:npm/%40scope/name`; 76 golang purls omit the `v` prefix, 2,062
  carry it. `sample_packages` has 65 raw-`@` rows and 24 packages present
  under both spellings; `builder_purls` does not cover `sample_packages`
  for 76 samples.
- 130 live samples (75 golang, 55 npm) have a PASS receipt whose
  `resolvedPackages` does not contain the declared package only because of
  spelling (`@babel/core@7.29.6` vs `%40babel/core@7.29.6`;
  `go-yaml@1.19.2` vs `go-yaml@v1.19.2`). Any reader that files receipts
  under the resolved version, as README promises, misses these.
- The wanted board uses raw `@`; a naive join of wanted to samples reports
  `@noble/hashes` and `@babel/core` (37 and 36 asks) as uncovered while
  they hold 39 and 73 samples. The server's own `wanted_key` CTE handles
  both spellings, at the cost of every such query.

Expected fix: one canonical purl spelling enforced at ingest (`csx sample
create`, receipt upload, wanted upsert) and a one-time normalization.
Candidates: `declaredNotResolved`, `rawAtScopedPurls`.

### P2-1  Image-digest pinning is missing for 24% of the corpus

1,679 live samples have no PASS receipt naming `verifierImage`; 657
receipts are schema v1 and 4,058 v2 receipts carry `resolvedPackages` but
no image. README's "pinned by image digest, not by tag" holds for 5,921
receipts (54%).

### P2-2  Peer diversity is nominal

CROSS_PASS on one PASS peer key: 5,713 of 6,621; two keys: 907; STABLE
(three keys): 355. Six keys ever signed a receipt; the three that matter
are the farm verify node (6,726 receipts), the retired local mill (2,641)
and a third operator key (1,400). Runtime diversity in PASS receipts: node
22 only, go 1.26 only, python 3.12 (747) plus 3.14 (2), rust 1, ruby 3,
php 8. `MATRIX_PASS` claims of "≥2 OS/runtime-major boundaries" cannot be
met by this fleet for node, go or python.

### P2-3  Descriptions and symbol declarations

3,506 live samples (50%) carry the template goal `verify … in pkg:…`;
1,809 (26%) have no `subject`; 1,228 (17%) declare no symbols; 49 disagree
between `manifest.symbols` and `case.symbols` (candidate list
`symbolsVsCase`); 798 samples name a symbol that never appears in any
contract line (2,016 of 11,385 symbol rows). Structural checks only found
no contract line duplicated within a sample and no existence-only
assertions by regex; contract accuracy beyond structure needs
re-execution and was not attempted on the starved host.

### P2-4  Stale versions

Of 2,145 sampled package versions, 2,118 have an observed version history
in `evidence_agg`; 1,014 are not the newest observed version and 388 are a
whole major behind (candidate list `staleMajor`, e.g. `@babel/types@7.29.8`
with 23 samples while 8.0.4 is observed, `type-fest@0.13.1` with 22 while
5.9.0 is, `react@18.3.1` with 17 while 19.3.0 is). Evidence does not decay,
but a new visitor asking about the current major gets nothing.

### P2-5  Coverage, value and demand

- Demand is saturated where it is measured: 1,180 of 1,192 wanted packages
  have a sample (the 12 missing are `pub/path_provider`,
  `pub/shared_preferences` and ten one-ask platform binaries); 4,578 of
  4,627 wanted versions are covered exactly; 57 of 5,637 asks are
  uncovered. Symbol-level coverage could not be computed (query exceeded
  100 s).
- Usage says the corpus is under-consumed: 6,864 search hits from 100
  anonymous ids in 30 days, 1,721 live samples ever shown, 5,349 never;
  195 adoption reports.
- Density: 1,334 packages, median 2 samples, 510 packages with one sample,
  61 packages with ≥20 samples holding 2,441 (35%). Top: pgx/v5 131,
  x/net 129, x/sys 119, semver 104, genproto/googleapis/rpc 97, uuid 87.
  `densityTop` carries asks and search hits per package so density can be
  read against demand.
- Evidence without samples: 1,775 packages carrying 223,617 of 1,992,417
  observations, dominated by platform binaries (`@esbuild/*`, `@rollup/*`,
  `fsevents`) that are not sample-worthy; the sample-worthy tail includes
  `jiti`, `zustand`, `recharts`, `next`, `@dnd-kit/core`,
  `@tanstack/react-query`, `playwright-core`, `grpc-gateway/v2`.
- Only 5 packages (19 samples) have zero evidence, zero asks and zero hits.

### P2-6  Nothing records latency

The Caddy safe log keeps status, method and route and deletes duration;
page views are `log_skip`ed; `docker logs` are lost on recreate (the
14:32Z container's logs were gone by 15:20Z). The pressure lines are the
only server-side timing and they fire only on refusal. Server render vs
DB split per request is therefore unmeasurable today.

### P2-7  Table churn and memory

`failure_clusters` 314 MB heap for 236k rows (1,397 B/row, 15.2M updates
lifetime), `compatibility_snapshots` 37 MB heap plus 77 MB index/toast for
21.7k rows (11.1M updates), `evidence_agg` 462 MB total. PostgreSQL:
`shared_buffers` 256 MB, `work_mem` 16 MB, `effective_cache_size` 768 MB
on a 1.9 GB host with 120 MB swapped; lifetime temp files 1,049,651
totalling 4.6 TB. The manifest trigram index (26 MB) has been used once.

### P2-8  Admin and analytics under load

`/admin/api/farm` returned 503 (background-class query timeout, 2.2 s
wait); 5 background `query_timeout` lines in 40 minutes including
`/v1/authoring/work/next` and `preload wanted snapshot`; `anonymous
analytics write unavailable (activity undercounted)` 40 times per hour.

## Not defects, recorded so nobody re-derives them

- Every live sample has ≥1 PASS receipt; no receipt disagrees with its
  `contract_result` column, `sampleId`, `caseId` or `environmentHash`;
  no sample lacks its `cases` row or its `sample_packages` rows.
- `failure_clusters` current-vs-preserved split is 234,857 / 236,167:
  the legacy preservation described in docs/schema.md is small now.
- golang symbols use the bare package alias (`cases.Upper`) while the
  purl carries the module path; docs/schema.md documents both spellings.
- Ad-hoc audit statements (`WITH audit_candidates …`, `COPY (…)`) are
  visible in `pg_stat_statements` and were excluded from every total above.

## Limits

Exhaustive: samples, receipts, sample_packages, wanted, search_hits,
adoptions, pg_stat_statements since 2026-09-09, Lightsail metrics 14 days,
safe access logs 2026-09-13..15. Sampled: HTTP probes (51 + 20 requests),
EXPLAIN on one package per query family, one 5-second host window plus
cumulative counters. Not measured: symbol-level wanted coverage (timed out
at 100 s), FAIL-row quality split on `evidence_agg` (timed out at 240 s),
per-request render/DB split (no data exists), semantic contract accuracy.
