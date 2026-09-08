# CodeSampleX #174 — v0.1.150 startup / migration 0036 controlled reproduction

Date: 2026-09-09 KST

Target: `28e0397fb68e34e0cd1433f51d3027040dfc34a6` (`v0.1.150`)

Baseline: `3b6bb9292488d6e9fc2b62ff0db1d13e177ebc61` (`v0.1.149`)

Branch: `csx-startup-repro-174`

## Result

The production activation failure was **not reproduced as a startup-health timeout** on the published-scale synthetic fixture. The target served both `/version` and `/healthz` after 36.714 seconds, within both the deploy script's 120-second explicit poll window and the compose healthcheck's approximately 135-second nominal window.

Migration/index locking was also not reproduced: migration 0036 became visible after 27.077 seconds, both projection backfills were observed complete by 37.073 seconds, and all 227 database samples reported zero ungranted locks and zero blocking edges. Deadlocks remained zero.

The full background builder did reproduce sustained work. It had not completed at the 240-second observation cutoff and was canceled in `snapshot_retire`; this occurred after the server was already healthy. This proves that builder completion is not a prerequisite for readiness in this fixture, but it does not prove why the production candidate became unhealthy.

The production failure therefore remains **unclassified**. An isolated pre-applied/offline-migration control was required before proposing a deploy change, but DevHotel could not start the second managed room because its Docker address pools were exhausted. No health timeout was extended, no deploy fix was guessed, and production was not accessed or changed.

## Environment and identity

- DevHotel combined room: `jdjm0usn` (`CodeSampleX / csx-startup-repro-174`), managed web/Linux room, final state sleeping.
- Combined run: `4a830ac6-59e7-4a72-a918-1cdb270f1a6c`, exit code 0.
- Room limit: 2-vCPU quota (`cpu.max=200000 100000`) and 768 MiB (`memory.max=805306368`).
- PostgreSQL: 17.11, UTF-8, `shared_buffers=256MB`, `max_connections=40`, `effective_cache_size=768MB`, `maintenance_work_mem=64MB`, `work_mem=16MB`, `wal_compression=pglz`.
- PostgreSQL is a separate DevHotel managed service. Its production-like 640 MiB cgroup limit is not exposed or configurable by the available DevHotel API; DB-memory-pressure equivalence is therefore not established.
- v0.1.149 binary: clean VCS revision, SHA-256 `8c00caba65ed323d7470b013466ffc421418b46c51b3e92e5dbd7da4a05f8edc`.
- Target binary: clean VCS revision, SHA-256 `f8155f1627b3141162cac5a21e6d8a8c1324a59c7d8bb9d6d231979d5bfb2380`.
- Executed harness SHA-256: `0e021bd3db3ae4cc59878de10ab909902d2d70ff742bff634336dbc3627a8f12`.
- Corrected committed harness SHA-256: `d12385d9f637122e8a38d2779752d9297adcd0ee35a0b4f13781069269f27ce1`.
- Seed SHA-256: `76a784a0146e1d89533856fbf01c89ce345b98b10918b88502a36b96eaafc34d`.

## Synthetic fixture

Only generated data was used. The fixture contains 7,800 samples, 7,800 receipts, 166,333 evidence rows, 19,102 compatibility snapshots, and 166,333 failure clusters. Before the target start the schema occupied 565,264,384 bytes, including 229,949,440 bytes for `evidence_agg` and 301,342,720 bytes for `failure_clusters`.

These counts and relation sizes follow public issue evidence; they are not copied production rows. Exact current production row counts, index sizes, storage latency, and cache state were unavailable and were not inferred.

## Timings and overlap

| Event | From target start | Evidence |
|---|---:|---|
| Migration/index transaction first visible | 27.077 s | first observer sample with migration 0036 and projection columns visible |
| Sample backfill observed at zero stale rows | 32.065 s | observer stale count |
| Receipt backfill observed at zero stale rows | 37.073 s | observer stale count |
| `/version` and `/healthz` first HTTP 200 | 36.714 s | process/health sampler |
| Builder cutoff | 240.207 s observation span | final log outcome `canceled` in `snapshot_retire` |

Index progress was visible for the large relations: `evidence_agg` was sampled from about 1.042 to 23.047 seconds and `compatibility_snapshots` from about 24.047 to 26.047 seconds. The sample/receipt and other small indexes completed below the one-second observer resolution.

The first builder pass began at approximately the same second as listener readiness. Its measured long phases included `target_evidence` 58.868 s, `snapshot_calculate` 40.005 s, and `snapshot_write` 79.912 s. The pass was still active at the harness cutoff, so its final duration is a lower bound, not 240 seconds.

## Database and resource observations

- 227 valid DB observations across 240.207 s; zero ungranted locks and blocking edges throughout.
- Observed waits: `VacuumDelay` in 16 samples, `WalSync` in 5, `DataFileRead` in 1, and `DataFileExtend` in 2. Other observed target sessions were predominantly idle `ClientRead`.
- DB deltas from fixture-ready to post-target: 109,392 blocks read, 4,118,047 blocks hit, 26,959,872 temporary bytes in 6 files, 389,433,886 WAL bytes, 1,173,569 WAL records, 8,206 full-page images, 2 rollbacks, and 0 deadlocks.
- 221 process samples: maximum RSS 597,444 KiB, maximum HWM 597,604 KiB, maximum observed cgroup usage 650,424,320 bytes of 805,306,368, average CPU 30.1%, p95 39.8%, maximum 100%.
- The process stayed alive and shut down cleanly on the harness interrupt. There was no observed startup OOM or cgroup-limit hit.

The executed harness terminated its monitor with status 143 before the monitor's after-loop cgroup snapshots were written. Consequently an exact `memory.events`/`cpu.stat` before-after delta and final PSI snapshot are unavailable. The committed harness takes those snapshots from the parent before terminating the monitor, but that correction has not been re-run because DevHotel could not allocate another room network.

## Blocked control and classification limits

The isolated offline room `4ypmsbj1` never became awake. DevHotel failed three independent wake operations with `all predefined address pools have been fully subnetted`; the final operation was `9fcd35be-1618-48ad-9188-f3c510556801` (the earlier operation IDs begin `b9c0a42e` and `37f4c75e`). No local Docker/browser, Orca, or production fallback was used.

Because the isolated control and PostgreSQL 640 MiB limit are unavailable, the evidence supports only these bounded conclusions:

1. The published-scale synthetic 0035→0036 startup does not deterministically exceed either deployment health window.
2. Migration/index locking and app cgroup exhaustion were not observed in this run.
3. The first builder pass is long and overlaps the healthy serving period, but this run does not show it preventing health readiness.
4. Production-only scale, storage/cache behavior, PostgreSQL cgroup pressure, or another deployment-specific condition remain possible and unproven.

No code or deploy-script fix is justified by this evidence alone. The existing #174 implementation PR #254 is already merged, and no open #174-linked PR exists; no new PR was opened.

## Raw evidence

The checked-in raw bundle is under [`runs/combined`](runs/combined). `manifest.sha256` authenticates the exported files. The primary records are `result.json`, `pre-target.json`, `post-target.json`, `process-health.tsv`, `db-observations.validated.jsonl`, `server.stderr.log`, `timeline.tsv`, build info, and the PostgreSQL/resource preflight files.
