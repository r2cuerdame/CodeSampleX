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
0.5 s between requests (100 requests, about two minutes), and writes
`perf-slo-result.json` (median, p95, failures and every sample per path),
kept as the run's `perf-slo-result` artifact for 90 days.

Each request opens a fresh connection and is split into TCP connect, TLS
handshake and request-to-first-byte. The raw time to first byte (what #485
measured with `curl -w`) is recorded, but the SLO metric is **server time**:
request-to-first-byte minus one round trip, the round trip being the TCP
connect. The first version of the probe used raw TTFB: a second runner run
ten minutes after the first moved `/healthz` p95 from 0.68 s to 0.93 s while
the server was unchanged, because the runner's distance to Seoul changes from
run to run. Server time does not move with the vantage: from the operator
workstation (round trip 0.11 s) and from a runner (0.20 s), `/healthz` measured
0.196 s and 0.201 s median, 0.288 s and 0.292 s p95.

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
at 2026-09-26 00:38 UTC from a GitHub `ubuntu-latest` runner, the vantage the
recurring probe uses (run 36205251181, attempt 2, whose artifact is the
stored baseline). Server time in seconds:

| name | path | median | p95 = target | raw TTFB p95 |
| --- | --- | ---: | ---: | ---: |
| healthz | `/healthz` | 0.225 | 0.373 | 1.015 |
| version | `/version` | 0.233 | 0.331 | 0.922 |
| stats | `/v1/stats` | 0.233 | 0.343 | 0.926 |
| shard-lookup | `/v1/shards/npm/zod/3` (the shard the CLI syncs) | 0.334 | 0.416 | 1.005 |
| verification-jobs | `/v1/verification/jobs?peerId=…&limit=10` | 0.302 | 0.577 | 1.021 |

Target = baseline p95, unless the path already violated its last known-good
p95; then the known-good p95 is the target. The only known-good number is
#485's verifier queue on this same build: 20 probes, max TTFB 0.816 s (an upper
bound on their p95), taken from the operator workstation with `curl -w`. That
is raw TTFB from one vantage, so it is compared with raw TTFB from the same
vantage: the workstation measured TTFB p95 0.762 s at 00:34 UTC, within 0.816 s,
so the path does not violate it and its target is the runner baseline. Both
raw results are in `docs/evidence/issue-511/`.

**How noisy one run is.** The same probe run's first attempt, minutes earlier,
measured p95 0.292 / 0.325 / 0.273 / 0.375 / 0.280 s on the same five paths:
the verifier queue's p95 halved between two runs with nothing deployed. One
run's p95 on a busy 2-vCPU host is a wide draw, and a target equal to one
draw will be exceeded by roughly every other run. The two-consecutive-runs rule
absorbs part of that; how much headroom a target gets on top of the baseline
is a decision #511 did not take, and the probe does not add any.

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
