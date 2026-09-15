# AUDIT_FABLE — CodeSampleX corpus and performance audit

Auditor: Claude Fable 5.1, independent run. Date: 2026-09-15 (09:40Z–14:40Z window).
Scope: production `codesamplex.dev` (server `v0.1.189`, commit `8e822f1`), the production PostgreSQL
database read through a `READ ONLY` transaction, the privacy-safe Caddy log, the server's own
pressure/builder log, and the source at `origin/main` (`7c6033c`). Nothing in production was written.
Machine-readable candidates: `AUDIT_FABLE_DATA.json`. Exact commands: `AUDIT_FABLE_COMMANDS.md`.
Helpers: `scripts/audit-fable/`.

## 0. Population and method

| Population | Count | How |
|---|---|---|
| samples rows | 8,345 | `SELECT count(*) FROM samples` |
| live (not quarantined) samples | 7,069 | 6,620 CROSS_PASS + 355 STABLE + 94 PUBLISHED |
| quarantined | 1,276 | 983 dedup 2026-08-19, 244 template-goal reissue, 45 drafts, 4 corrections |
| receipts | 10,965 | 10,466 PASS, 272 FAIL, 227 SKIPPED |
| distinct live purls / package names | 2,278 / 1,375 | manifest.packages |
| contract lines (live) | 39,580 (33,953 distinct) | manifest.case.contract |
| compatibility_snapshots / evidence_agg / failure_clusters | 21,686 / 318,688 / 236,167 | pg_stat_user_tables |

Every corpus scan below is **exhaustive over the live corpus** (7,069 samples / 10,965 receipts) unless the
row says "heuristic". The one external check (registry version currency) covered all 2,278 live purls
against the public registries; 6 Go std-lib coordinates could not be resolved. The Farm node itself was not
reachable from this machine (SSH allowlist), so farm-side state is taken from the server's tables only.

The "~10k corpus" in the brief is the samples-plus-receipts view; the trustworthy unit is the **7,069 live
samples**, and 1,276 (15.3%) of everything ever published is already quarantined.

## 1. Ranked findings

Severity: **P0** = the public claim is wrong or the public surface is down; **P1** = trust or availability
materially degraded, fix in the next cycle; **P2** = quality/efficiency debt.

### P0

**P0-1 · Package pages return 503 for the whole duration of a builder pass; the builder runs ~78% of the time.**
Surface: every `/{eco}/{name}` and `/{eco}/{name}/{version}` page (the product's core view) and `/dependencies`.
Evidence: 12 package-page probes 14:00–14:27Z → 11 × `503 Retry-After: 2`
(`AUDIT_FABLE_DATA.json → performance.publicPageProbes`); server log 09:39–14:00Z: 1,044 `db pressure` lines,
878 of them `interactive pool_busy` on `/npm/*`, `/golang/*`, `/v1/shards/*`, `/dependencies`; `/healthz`
itself waited up to 1.8 s (probe class). Root causes, all measured:
1. Production runs `CSX_DB_READ_CONNS=2` and `CSX_DB_READ_WAIT=250ms` (container env), not the documented 6 / 3 s.
   Two interactive connections shared by every visitor while the builder (background class, no ceiling) holds
   its own — one cluster read of 32 s (see P0-3) is enough to refuse every other page for 250 ms windows.
2. The package cache-miss gate (`cmd/csx-server/webstore.go:381` `packageLoadSlotCount = 4`,
   `packageLoadAdmissionWait = 250ms`) refuses above the pool: with loads taking seconds, four in-flight misses
   make every further miss a 503 after 250 ms. The log shows `admission_refused` and `deferred_refused` for
   `/npm/*`, `/golang/*`, `/pypi/*`, `/cargo/*`, `/composer/*`.
3. Builder duty cycle: 12 passes started between 09:39 and 13:56Z (`CSX_SNAPSHOT_INTERVAL=5m` gap between
   passes); durations 54 s … 31 m for incremental passes and **2 h 12 m** for the 10:44 full pass; a second full
   pass started 13:56 (`fullPassEvery = 12`, `internal/compatibility/builder.go:56`). ≈78% of wall time is
   inside a pass, on a 2-vCPU host whose load average was 6.5–8.9 and whose server container sat at
   694/768 MiB.
Reproduction: `scripts/audit-fable/probe-public-pages.sh` while `docker logs codesamplex-server-1 | grep "builder pass start"` shows an open pass.
Expected fix: (a) restore interactive share to ≥4 conns and a ≥1 s wait, or move the builder to a separate
pool/connection budget so a background pass cannot starve pages; (b) make the miss gate degrade to *stale*
rather than 503 (the caches already keep stale entries for `packageDetailCacheTTL = 30m`, but a cold key has
nothing to serve); (c) stop the pass from being CPU/IO-bound on the items in P0-3/P0-4; (d) raise
`fullPassEvery` or schedule full passes off-peak.

**P0-2 · 5,712 of 6,620 live CROSS_PASS samples (86%) have exactly one signing peer.**
Surface: README "CROSS_PASS = a peer key other than the one that published it re-ran it and it passed again";
every sample page badge, `/v1/search` grade, `/stats` rollups.
Evidence: exhaustive join of `samples` × `receipts` grouped by distinct `peer_id` with `contract_result='PASS'`:
CROSS_PASS with 1 peer = 5,712, 2 peers = 907, 3 = 1; all 5,712 have an `authoring_drafts` row with
`local_status = LOCAL_PASS`. Code: `internal/serverstore/pg.go:1871-1877` promotes `DRAFT → CROSS_PASS` on the
*first* farm PASS because "a designated sample author already proved LOCAL_PASS before upload" — but that local
pass is unsigned, unstored and produced by the same farm operator; `internal/httpapi/verifications.go:596-651`
(`sampleStatusFromReceipts`) would not grant CROSS_PASS on these rows. 6,372 of 10,466 PASS receipts come
from one peer key (`ed25519:c197…`); five keys sign everything.
Expected fix: either record the author's local run as a signed receipt (so the page shows two parties) or grade
these as `PUBLISHED`/`SAMPLE_VERIFICATION` until a second verifier signs; the badge copy and the README must say
what the data shows. Population is 5,712 rows; the fix is a status recomputation, not a re-run.

**P0-3 · One query family costs 26.4 h of DB time per 6.3 days: `listFailureClusters` per package.**
Surface: package/version/leaf pages (cache-miss), failure-issue pages (uncached, `webstore.go:2165`),
`POST /v1/search` per survivor, and the builder's `cluster_read` phase.
Evidence: `pg_stat_statements` queryid `-8919612395518868614`: 336,679 calls, 95,112 s total, mean 283 ms,
**max 8,000 ms = the interactive statement ceiling**, 55.4 M rows returned, 82,083 s of shared-block read time.
EXPLAIN ANALYZE for `package_name='golang.org/x/sys'`: 9,225 rows, 6,541 heap blocks read from disk, **32.2 s**.
Why: `failure_clusters` is 390 MB for 236,167 rows (≈1.3 KB/row of jsonb: `env_summary`, `hypotheses`,
`versions`, `env_variants`, `evidence_breakdown`) while the network holds only 16,755 FAIL observations; the
row count is a per-(symbol, stage, error_fp) explosion (golang.org/x/sys alone has 9,352 clusters, electron
4,978). The read pulls every column for every cluster of a package and filters in Go
(`cmd/csx-server/webstore.go:2110-2140` keeps at most `maxClustersToPage`), and it does so on a host with
76 MB free RAM, so the 314 MB heap is never cache-resident.
Expected fix: page the read (`LIMIT` by `observation_count DESC` with the ecosystem filter in SQL), select only
the columns the page renders, and collapse clusters at write time (one row per symbol×stage×error_fp is the
current identity; the page needs per-package top-N). The `failure_clusters_pkey` index has **zero** scans and
`failure_clusters_pkg_count_idx` exists but the current predicate defeats it.

**P0-4 · `builder_purl_coord()` is a SQL-language function costing 17 ms per row; it was executed 10.36 M times (18.2 h) in 6.3 days.**
Surface: every evidence ingest (`INSERT/UPDATE evidence_agg`, 401 k each), every snapshot upsert
(2.32 M `INSERT INTO compatibility_snapshots … ON CONFLICT`), and every builder scoped read
(`WHERE builder_purl_coord(purl)=ANY($1)`), because migration 0036 put the function into two expression
indexes (`evidence_agg_builder_coord_idx`, `snapshots_builder_coord_idx`).
Evidence: queryid `5126368844612351286` (the function body `WITH parsed AS (SELECT regexp_match(raw,…`)
10,358,032 calls, 65,367 s. EXPLAIN ANALYZE: 1,000 rows → **17,440 ms** with the function vs 1.2 ms with a
plain `lower(split_part(purl,'@',1))||'@'` expression (14,000×). The function is `LANGUAGE sql IMMUTABLE`
but contains a CTE with `regexp_matches … WITH ORDINALITY` + `string_agg`, so PostgreSQL cannot inline it
and plans/executes it per call (`internal/serverstore/pg_builder_prestage.go:13-33`).
Expected fix: replace the body with a non-set-returning expression (or a C/PLpgSQL-free `lower()` +
`replace()` chain with an explicit percent-decode only for the `%40` case that actually occurs), or store a
`coord` column populated by the application (as `sample_packages.coord` already is) and index that.

### P1

**P1-1 · The farm/admin coverage CTE runs for 3 minutes, spills 41 GB of temp, and is polled.**
`authoringCoverageCTE` (`internal/serverstore/dependencyclosure_pg.go:16`) is shared by
`ListAuthoringExpansionCandidates` (queryid `-1838375435808251149`: 192 calls, **mean 183 s, max 433 s**,
5.34 M temp blocks) and `FarmBacklogNow` (queryid `5754581964168977175`: 2,107 calls, mean 10.4 s, and
`-8060285611411695903`: 1,059 calls, mean 10.1 s). `/admin/api/farm` runs the backlog on every load and
`admin.js:569` polls it every **60 s**; the server logged 76 `background query_timeout` on `/admin/api/farm`
and 5 "farm coverage calculation failed (statement timeout)" in 4.5 h. `pg_stat_database` shows
1,049,628 temp files / 4,615 GB written since stats reset (work_mem 16 MB).
Expected fix: materialize `verified_packages` / `dependency_open` once per builder pass into a small table
(they only change when a sample is published or an edge arrives); serve `/admin/api/farm` from that
materialization; poll at 5 min, not 60 s; never run the 8-minute "unhurried" variant while pages are being served.

**P1-2 · Full-corpus reads on hot paths.** (per-route map in `AUDIT_FABLE_COMMANDS.md §C`)
- `HotPackages` refresh (`webstore.go:2041`, TTL **60 s**) → `SnapshotKeys` = every row of
  `compatibility_snapshots` (queryid `-4544060618235083690`: 4,979 calls, mean 1.47 s, max 32 s,
  103 M rows returned). Landing page needs 6 hot packages; it reads 21,686 keys a minute.
- `SnapshotUpdatedAt` (`pg.go:838`) `jsonb_array_elements` over every snapshot: 517 calls, **mean 11.7 s,
  max 38 s**; used by `/compatibility` and the sitemap.
- `/sitemap.xml` rebuild is synchronous inside the crawler's request under a mutex every 15 min: measured
  **44.8 s TTFB** on the cold probe (`internal/web/sitemap.go:146-210`, `ListSamples(50_000)` +
  `RecordPackages`).
- `GET /v1/registry/packages/{purl}` calls `ListSnapshotTargets` (`pg.go:876-949`) on every request: a
  `DISTINCT` over all of `evidence_agg` plus a join of every live sample with every v2 receipt decoded in Go
  (queryid `-181493637755466483`: mean 18.5 s, **max 477 s**); 34 of 138 requests in 27 days were 503.
- `HotShardKeys` for `/v1/stats` reads `evidence_agg GROUP BY purl` (mean 7.6 s) **plus all 7,069 manifests
  plus all receipts** (`pg.go:2705-2790`) — 561 full manifest reads at 3.7 s mean.
- `stats refresh` in each builder pass: `COUNT(DISTINCT bucket) FROM evidence_dedup` (870 k rows) — mean
  21 s, max 102 s; `refresh_stats` phase took 103.7 s of a 5-minute incremental pass.
- `/samples` counts the whole table with `count(*) OVER()` per request (cheap today at 9.5 ms, but unbounded).
`pg_stat_user_tables` confirms the shape: `samples` 2.58 M sequential scans / 7.55 G tuples, `sample_packages`
3.78 M / 25.1 G, `receipts` 1.35 M / 6.95 G, `evidence_agg` 255 k / 9.4 G.

**P1-3 · The builder rewrites derived tables it did not change.** 20,025,622 `INSERT … ON CONFLICT` on
`failure_clusters` (41,416 s) and 2,322,683 on `compatibility_snapshots` (14,692 s) in 6.3 days for tables of
236 k and 21.7 k rows — every pass re-upserts everything it read, and the `WHERE … IS DISTINCT FROM` guard
only saves the heap write, not the index maintenance (which is where P0-4 is paid). `cluster_write` alone was
82 s for one package in an incremental pass. A change-detection hash per row (as `builder_source_hash` already
does for samples/receipts) would skip the 99% no-op upserts.

**P1-4 · 1,679 live samples are verified only by receipts with no `verifierImage` — 100% of gem, composer,
hex, pub and maven, and 791 npm / 353 golang / 110 pypi.** The README promises "pinned by image digest … so
anyone can re-run the same bytes"; these cannot be re-run by digest and the receipt audit
(`internal/verifier/receiptaudit.go`) treats them as NOT ESTABLISHED. 657 receipts are still schema v1.
Also 1,004 PASS receipts (75 golang + 55 npm samples) carry `resolvedPackages` that do not contain the manifest
purl, because of the spelling defects in P1-6.
Expected fix: schedule re-verification (cross jobs) for these 1,679 with a digest-pinned lane; there are
no pinned images for gem/composer/hex/pub/maven at all in the verifier registry today (`docs/adapters.md`).

**P1-5 · 120 live samples' newest receipt is FAIL, and status never downgrades.** 78 golang CROSS_PASS,
26 cargo CROSS_PASS, 6 golang PUBLISHED, others; 166 live samples carry at least one FAIL. `statusRank`
only upgrades (`verifications.go:630-651`); a sample that now fails still renders "Verified sample".
Expected fix: a "last verification FAILED at <date>" state on the page and in grading; quarantine after N
consecutive FAILs from distinct peers. IDs in `AUDIT_FABLE_DATA.json → defects.newest_receipt_fail`.

**P1-6 · Coordinate spelling is inconsistent inside the corpus, so joins silently miss.**
- 55 live samples (65 purls) declare `pkg:npm/@scope/…` while receipts, `sample_packages.coord` derivation
  and the wanted matcher use `%40scope`; `sample_packages` holds 81 such rows.
- 86 live golang samples declare versions without the `v` prefix (`decimal@1.4.0`) while receipts resolve
  `v1.4.0`; migration 0030 canonicalised `packages` but not manifests.
- 21 samples record Go standard-library paths as packages, with two version spellings (`1.26.5` /
  `go1.26.5`); no registry can answer for them and the version-currency check returned unknown.
- 1,379 golang symbols are spelled `alias.Name` and 1,788 `import/path.Name`; `symbolSpellings` papers over
  it for snapshots but `wanted.symbol ?` matching and `farmBacklogStocksSQL` compare exact strings.
Expected fix: canonicalise at ingest (`internal/samples` manifest normaliser) and one-time rewrite of
`manifest.packages` on the 141 affected rows (content addresses do not change: the artifact is the identity,
the manifest columns are projections).

**P1-7 · Duplicates survive the 2026-08-19 dedup.** 204 groups / 417 live samples share identical
`manifest.packages` + `manifest.symbols`; 135 groups / 274 live samples share a byte-identical contract for
the same subject (517 groups / 1,193 samples if the subject is ignored); 44 case_ids are shared by 2 samples
each. The dedup rule keys on `purl+symbols`, and 1,228 live samples have **no symbols at all**, so every
symbol-less sample of a purl is a distinct "coordinate" to that rule.
Expected fix: extend the dedup key to `(packages, symbols-or-contract-hash)`; quarantine the newer member; add
the check to `POST /v1/samples` / draft submission so the queue stops re-issuing answered coordinates
(244 samples were already quarantined for exactly that).

**P1-8 · 49.6% of live samples (3,505) carry the unedited authoring template as their goal**, and 56 samples'
contracts assert only package.json / type-declaration / binary-header facts (`undici-types package.json
declares name … and version 8.3.0`, `@types/*`, prebuilt `.node` ELF headers). Architecture.md already
documents the goal problem for SEO copy; the MCP `search_known_solution` text still surfaces the goal.
Expected fix: reject template goals at `csx sample create`/submit (the string is known); classify
metadata-only contracts as `REFERENCE_ONLY` weight in search grading.

### P2

- **P2-1 · Weak-value saturation.** 30 package names carry ≥27 live samples each (pgx/v5 131, x/net 129,
  x/sys 118, semver 104, genproto/rpc 97, uuid 87, x/crypto 78, @babel/core 73, sqlalchemy 63…). Usage
  telemetry (`search_hits`, 6,859 offers to 100 reporters over 25 days) touched 1,989 distinct samples;
  **5,349 of 7,069 live samples (76%) have never been offered**, and 79 samples take >10 offers each. Density
  and demand are uncorrelated: `google/uuid` has 87 samples and 1 hit; `vitest` has 49 samples and 196 hits.
  Table in `AUDIT_FABLE_DATA.json → coverageValue.saturation`.
- **P2-2 · Demand with sparse coverage.** Observed-but-unsampled packages by evidence volume (excluding
  platform-binary optional deps like `fsevents`, `@esbuild/*`, `@rollup/*` which are correctly withheld):
  `zustand` (2,259 obs), `jiti` (2,405), `webcrypto-core` (1,982), `recharts` (1,320), `next` (1,191),
  `@dnd-kit/core` (1,154), `grpc-gateway/v2` (758), `playwright-core` (664), `@tanstack/react-query` (577),
  `@aws-sdk/credential-provider-*` (566 each). `search_misses` recorded 4,150 questions from 88 reporters
  with no answer. The `wanted` table is 99.5% "answered" at name level because it is fed by the farm's own
  expansion loop, so it is no longer a demand signal. List: `coverageValue.demandGapsObservedNoSample`.
- **P2-3 · Version currency.** 587 live purls (1,597 sample-purl rows, 22.6%) are an older *major* than the
  registry latest (react 18 vs 19, @babel/core 7 vs 8, semver 5/6 vs 7, electron 33/38 vs 44, vitest 2/3 vs
  5). Evidence does not decay, but the "latest" cells for these packages are empty while the old ones are
  saturated (`coverageValue.olderMajorTop`).
- **P2-4 · 947 `search_hits` rows reference sample_ids that no longer exist** (hard-deleted, not in the undo
  tables), and 33 reference quarantined samples; adoption/hit telemetry for those offers is unattributable.
- **P2-5 · Unused / oversized indexes and tables.** `samples_manifest_lower_trgm_idx` 26 MB with 1 scan;
  `failure_clusters_pkey` 0 scans; four `*_before` / `*_undo_*` snapshot tables from August migrations
  (`failure_clusters_before` 5 MB, `evidence_agg_before`, `target_*`, `dedup_quarantine_undo_20260819`,
  `stub_dedup_undo_20260820`) still live in the serving database.
- **P2-6 · Farm polling load.** `verification_jobs` claim polling is 630 k requests / 27 days (one every
  3.7 s) against a Background-class DB read; `/v1/shards/*` is 1.23 M requests with 21,405 × 502 and
  9,057 × 503 (2.5%) — the 502s are the 60 s `WriteTimeout` being hit behind the pool, i.e. the R2C-55 shape
  is back for shards during passes.
- **P2-7 · 2 h 12 m full pass cost breakdown** (from the phase log of the 10:44Z pass and the 13:46 incremental
  pass): `target_evidence` 8 min, `snapshot_write` >20 min, `cluster_write` 82 s per large package,
  `refresh_stats` 104 s, `dependency_axis` 19.5 s, `load_samples` decodes 22.6 MB of JSON per pass. The
  builder reads every sample and receipt on every pass regardless of `full`.
- **P2-8 · Memory headroom.** Server container at 694 of 768 MiB with `GOMEMLIMIT=600MiB` (GC will run
  continuously above 600 MiB); PostgreSQL at 418/640 MiB with `shared_buffers=256MB`; host free memory 76 MB.
  Any growth in the in-process caches (30-minute package caches, 5-minute whole-snapshot cache of 72 MB of
  jsonb text) lands on swap.
- **P2-9 · Compose vs runtime drift.** `log_min_duration_statement` is `2s` in the running PostgreSQL but
  `-1` in `deploy/docker-compose.yml`; `CSX_DB_READ_CONNS/READ_WAIT` are set to non-default values by `.env`
  with no runbook entry describing why.

## 2. Where the time goes (performance summary)

| Layer | Evidence | Verdict |
|---|---|---|
| Network / cold start | `/healthz` 0.42–0.78 s, `/features` (static) 0.73 s from the probe client | RTT-bound; no cold-start component observed (container up 4 h) |
| DB | 119.3 h of statement time in 152 h of wall clock (78% of one core); 1 M temp files / 4.6 TB spilled; 97.5% buffer hit but 82 M ms of read wait on one query | **The bottleneck.** Builder + admin aggregates + cluster reads |
| Server render / API | `/compatibility` 3.6–7.1 s, `/samples` 2.7–4.0 s, sitemap 44.8 s cold, package pages 503 | Render is cheap; the request time is DB wait + refused admissions |
| Browser / admin JS | `site.css` 104 KB (zstd, 300 s cache); `admin.js` 33 KB; pages 30–68 KB HTML; admin polls `/admin/api/farm` and `/admin/api/withheld-work` every 60 s | Client cost is small; the 60 s poll is a server problem |
| Lock / contention | 3 deadlocks total; no long lock waits in `pg_stat_activity` | Not a lock problem; it is CPU + IO + pool share |
| Retry / backoff | 503+`Retry-After: 2` on package pages; shards return 502 after the 60 s write timeout; admin coverage backs off 15 min after failure | Package `Retry-After: 2` invites retry storms during a 2-hour pass |

Top bottlenecks, in order of DB time: (1) `listFailureClusters` 26.4 h; (2) `builder_purl_coord()` 18.2 h;
(3) `failure_clusters` upserts 11.5 h; (4) `authoringCoverageCTE` family 18.9 h across three callers;
(5) `compatibility_snapshots` upserts 4.1 h; (6) `EvidenceForTarget` 3.8 h; (7) `SnapshotKeys` full scans 2.0 h.

## 3. Defect census (live corpus, exhaustive)

| Class | Samples | Severity | Category |
|---|---:|---|---|
| CROSS_PASS with a single signing peer | 5,712 | P0 | invalid verification linkage |
| template goal `verify X in pkg:…` | 3,505 | P1 | inaccurate description |
| PASS receipts only without verifier image | 1,679 | P1 | invalid verification linkage |
| older-major version than registry latest | 1,597 rows / 587 purls | P2 | stale version |
| no symbols declared | 1,228 | P2 | inaccurate description |
| golang bare symbol spelling | 603 | P2 | package/symbol mismatch |
| duplicate packages+symbols | 417 (204 groups) | P1 | duplicate |
| contract mentions no declared symbol (heuristic) | 316 | P2 | inaccurate description |
| duplicate contract, same subject | 274 (135 groups) | P1 | duplicate |
| newest receipt FAIL | 120 | P1 | non-runnable / regressed |
| shared case_id | 88 (44 pairs) | P2 | duplicate |
| golang purl without `v` | 86 | P1 | package/symbol mismatch |
| metadata-only contract | 56 | P1 | weak value |
| raw `@scope` npm purl | 55 | P1 | package/symbol mismatch |
| Go std-lib as package | 21 | P2 | package/symbol mismatch |
| orphan `search_hits` sample ids | 947 rows | P2 | invalid evidence linkage |

Classes overlap (a template-goal sample is often also single-peer). Every sample-level class carries its
content addresses in `AUDIT_FABLE_DATA.json → defects`; the two large classes (single-peer, no-image) are
listed by ecosystem with full id arrays for the no-image class and counts for the single-peer class (the
query in `AUDIT_FABLE_COMMANDS.md §B2` reproduces the ids in seconds).

Checks that came back clean (worth stating): receipt `sampleId`/`caseId`/`ecosystem` all agree with their
sample (0 mismatches of 10,965); no receipt predates its sample; every live sample has ≥1 PASS receipt; no
live sample has only FAIL receipts; every PASS receipt reports `CONTAINER_RUN` on linux; `subject` is always
inside `packages`.

## 4. What to do first

1. Restore the interactive pool share (or give the builder its own budget) and change the package miss gate
   to serve stale — this alone stops the 503s. Half a day.
2. Replace `builder_purl_coord` with an inlinable expression or a stored `coord` column — removes 18 h of DB
   time per week and most of the index-maintenance cost of P1-3. One migration.
3. Page and column-trim `listFailureClusters`; add a top-N per package projection. Removes 26 h/week.
4. Materialise the coverage CTE per pass and poll the admin panel at 5 min.
5. Recompute status for the 5,712 single-peer CROSS_PASS rows and fix the badge copy; queue digest-pinned
   re-verification for the 1,679 image-less samples; quarantine the 120 newest-FAIL samples pending re-run.
6. Canonicalise the 141 mis-spelled manifests and extend the dedup key; reject template goals at submit.
