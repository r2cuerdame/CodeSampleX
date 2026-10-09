# Farm202 snapshot cache retention repair

The v0.2.7 production activation at 2026-10-09 03:29 UTC restored Farm assignment,
draft creation, persisted artifact storage and cross verification. Builder
materialization remained paused; the public statistics timestamp was still
2026-09-17, so full Farm202 recovery was not established.

At 04:08 UTC the existing two-vCPU production host measured 79.19% CPU steal.
Existing runtime logs showed heap allocation around 599–648 MB near the
600 MiB Go memory limit, with roughly 16–20 collections per 30 seconds.
Package page traffic accumulated per-release snapshot JSON in a sync.Map:
expiration changed read eligibility but never removed retained values.

## Regression and change

On the previous production commit 06c8912a8a6e9680297fed45dc54f5f4ce04fa97,
TestFarm202SnapshotCrawlReleasesOldPayloads failed after a 40-release crawl
retained 83,887,550 serialized JSON bytes. The fixture requires at most 64 MiB
and requires an evicted positive release to be read again, rather than reported
absent.

The repair limits retained per-release payloads to an accounted 64 MiB and
1,024 release groups. Eviction removes both payloads and their complete-load
authority stamps. Negative results share a bounded release stamp rather than
creating an entry for every absent symbol. Oversized releases remain complete
uncached responses, including for concurrent shared-load waiters.

A complete corpus read uses an index into its immutable row slice and clears
the separate point cache. A newer complete corpus remains authoritative for
retired coordinates. The complete corpus itself retains its existing full-read
and stale-response contract; it is not truncated to the point-cache budget.

The repair changes no governor thresholds, pool budgets, infrastructure,
credentials, schemas or Farm capacity.

## Validation boundaries

Local snapshot/prefetch regression tests and the race detector passed.
Local server unit tests passed with TestIntegration explicitly excluded.
The first local all-server run correctly failed because its inherited test DSN
pointed to an unavailable local PostgreSQL at port 5433. This is not recorded
as a successful integration run; the required GitHub Test check executes against
the existing workflow's disposable PostgreSQL 17 instance.

Production effectiveness remains to be measured after deployment. Farm202
requires rolling six-hour generation at least 2.333 GEN/h, a fresh successful
Builder/statistics materialization and the new sample in a service shard.
Passing the cache regression alone does not satisfy those criteria.
