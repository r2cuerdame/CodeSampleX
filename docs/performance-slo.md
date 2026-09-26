# Performance SLO (#511)

A slowdown on a public path used to be noticed only when someone looked
(#485, #149). The performance SLO probe measures it and opens an Issue.

## What runs

`.github/workflows/perf-slo.yml` runs `scripts/perf-slo.py`:

- after every successful **Production deploy** (`workflow_run`),
- daily at 03:17 UTC (`schedule`),
- on demand (`workflow_dispatch`),
- on a pull request that changes the probe, in report-only mode (no Issue is
  touched).

Each run sends 20 rounds of `GET` to each path in
`scripts/perf-slo-baseline.json`, round-robin, one request at a time with
0.5 s between requests (100 requests, about two minutes). It records time to
first byte on a fresh connection, the same measurement #485 took with
`curl -w`, and writes `perf-slo-result.json` (median, p95, failures and every
sample per path), kept as the run's `perf-slo-result` artifact for 90 days.

A failed request (non-2xx, timeout, connection error) counts as a sample at
the 10 s timeout. One failure in twenty leaves p95 alone; two fail it, however
fast the failures answered.

## Staying light

The probe never takes a Farm slot and never writes data: read-only GETs, no
authoring or verification claim, no admin credential, no SSH, and no search
(a search records an outcome row). It uses its own concurrency group, so a
queued probe can never displace a pending deploy in `codesamplex-production`.
`scripts/perf_slo_test.go` pins these properties.

## Paths, baseline and targets

Measured on production `v0.1.199` / `ebde5fc4d120c23b122a48ae6ea14bbdfde65ac2`
at 2026-09-26 00:19 UTC from a GitHub `ubuntu-latest` runner, the vantage the
recurring probe uses (run 36204251792, probe commit `8acc4f1`).

| name | path | median s | p95 s = target s |
| --- | --- | ---: | ---: |
| healthz | `/healthz` | 0.550 | 0.683 |
| version | `/version` | 0.525 | 0.675 |
| stats | `/v1/stats` | 0.527 | 0.693 |
| shard-lookup | `/v1/shards/npm/zod/3` (what the CLI syncs) | 0.530 | 0.847 |
| verification-jobs | `/v1/verification/jobs?peerId=…&limit=10` | 0.531 | 1.162 |

Target = baseline p95, unless the path already violated its last known-good
p95; then the known-good p95 is the target. The only known-good number is
#485's verifier queue on the same build: 20 probes, max TTFB 0.816 s, taken
from the operator workstation. A GitHub runner is farther from the server, so
the comparison was made from the workstation too: p95 0.682 s ≤ 0.816 s, so the
path does not violate it and its target is the runner baseline. Both raw
results are in `docs/evidence/issue-511/`.

## Violation and recovery

Per path, each run compares itself with the previous run's artifact:

| this run | previous run | open Issue | action |
| --- | --- | --- | --- |
| over target | over target | none | open one Issue with the measurements |
| over target | anything | yes | comment the measurements |
| within target | over target / unknown | yes | comment; one more passing run closes it |
| within target | within target | yes | close with the measurements |
| anything else | | none | nothing |

The Issue body carries `<!-- perf-slo path=NAME -->`; that marker, not the
title, is how a later run finds it, so there is at most one per path. With no
readable previous result (first run, expired artifact, a new path) a run can
neither open nor close anything. Fixing the slowdown is ordinary Worker work
on that Issue; there is no always-on repair process.

## Re-baselining

When a target no longer describes the service (a deliberate change, a new
path), measure again and rewrite the targets:

```sh
python3 scripts/perf-slo.py measure --vantage "github-actions ubuntu-latest" --out runner.json   # or the run's artifact
python3 scripts/perf-slo.py baseline --result runner.json [--same-vantage-result workstation.json]
```

`baseline` records the date, server version and revision next to the targets.
