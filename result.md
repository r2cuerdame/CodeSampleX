# Issue #485 Result: the verifier queue's 503 was the governor, not the database

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/485
- Branch: `issue/485-p0-astra-deep-performance-analysis-and`
- Full analysis: `docs/issue-485-verifier-queue-root-cause.md`
- Related: #454 (the governor), #453 (ClassFarmIngest), CodeSampleX-Farm
  #163 / PR #186 (the on-node probe that proved the failure server-side)

## Verdict

`GET /v1/verification/jobs` answered `503 {"error":"database busy"}` for 33+
hours while the database was idle. Measured live on production 2026-09-19
02:28–02:45 UTC: pool `inUse: 0` with 10 idle connections, 12 idle
PostgreSQL backends, and the queue query (`OpenJobsPage`) at **75 ms /
833 buffer hits / 0 reads** under `EXPLAIN (ANALYZE, BUFFERS)`. The 503 was
the pool's admission gate: `pool.classes[farm_ingest].limit: 0`, `busy:
159104`, set to zero by the resource governor's `host-cpu-steal` branch
within 16 s of the process starting on 2026-09-17 11:51 UTC and re-asserted
on every 5 s tick since.

The steal (54% averaged over 6.8 days of host uptime, 77% in a 30 s window)
is self-inflicted: `csx-server` accounts 1.5 cores of task time continuously
with the Builder paused and ~2 req/s arriving, because its Go-owned memory
(617 MiB resident + 199 MiB swapped) is past its 600 MiB `GOMEMLIMIT` and the
collector runs at its 50 % CPU cap on every cycle. That drains the burstable
instance's credit balance; the hypervisor reports the rest as steal; the
governor reads steal as "shed Farm". What holds the memory is the whole
`compatibility_snapshots` corpus — 22,547 rows, 234 MB of JSON, up from
149 MB on 2026-09-02 — retained as Go strings by `cmd/csx-server/webstore.go`
in caches that are refreshed but never evicted.

## Changed

1. `cmd/csx-server/governor.go`: `host-cpu-steal` pauses the Builder only.
   Farm ingest keeps its configured ceiling; it is still shed on
   `interactive-pool-pressure`, which takes precedence. The pause line
   reports the live ceiling (`farm_ingest=2`) rather than `paused` when Farm
   was not shed.
2. `internal/httpapi/opsruntime.go`: `GET /v1/ops/pool-metrics` gains an
   additive `runtime` section (`memoryLimitBytes`, `memoryTotalBytes`,
   `heapLiveBytes`, `heapGoalBytes`, `gcCycles`, `gcLimiterLastEnabledCycle`,
   `gcCPUSeconds`, `totalCPUSeconds`, `gcCPUFraction`, `goMaxProcs`,
   `goroutines`) — the reading that separates self-inflicted steal from a
   noisy neighbour, and the before/after instrument for the memory lane.
3. `docs/operations.md`: governor table, the steal runbook (read `runtime`
   before "resize"), the `runtime` field reference.
4. `docs/issue-485-verifier-queue-root-cause.md`: the evidence, what was
   ruled out, and the memory plan for Chief to split (compact record-filter
   index instead of the retained corpus; evict expired cache entries;
   Builder memory; instance size last).

## Tests

- 2026-09-19, this workstation (Windows 11, Go toolchain from `go.mod`):
  ```text
  go build ./...                                                     ok
  go vet ./cmd/csx-server/ ./internal/httpapi/                       ok
  go test ./cmd/csx-server/ ./internal/httpapi/ ./deploy/lightsail/ -count=1
  ok  github.com/r2cuerdame/codesamplex/cmd/csx-server     24.583s
  ok  github.com/r2cuerdame/codesamplex/internal/httpapi    1.831s
  ok  github.com/r2cuerdame/codesamplex/deploy/lightsail   60.048s
  go test ./internal/hostpressure/ -count=1                          ok 0.175s
  ```
  `deploy/lightsail` is in the run because it guards the Builder/governor
  source shape by regex; it passed unchanged.
- New/updated: `TestGovernorDecidePausesOnlyTheBuilderOnSustainedHostSteal`,
  `TestGovernorDecideStillShedsFarmOnPoolPressureUnderSteal`,
  `TestGovernorShedsOnHostStealWithAHealthyPool`,
  `TestOpsMetricsHandlerReportsTheRuntimeSection`,
  `TestRuntimeFromValuesMapsEachMetric`. The existing
  `TestIntegrationGovernorPausesBuilderAndFarmIngestUnderPressure` (true
  saturation still sheds Farm) is unchanged.

## Not done here, and why

- **Deploy and live proof.** A Worker cannot merge, tag or deploy
  (release-tag push is a human gate). The exact post-deploy checks are in
  the analysis doc under "Acceptance". Expected after deploy: `200` on the
  queue while `host.stealPercent` is still ≥ 20 and
  `pool.classes[farm_ingest].limit` = 2; `runtime.gcLimiterLastEnabledCycle`
  within a few cycles of `runtime.gcCycles` as the "before" figure.
- **The memory root cause.** A refactor of the whole-corpus caches in
  `webstore.go` (~3,000 lines, dozens of tests) is beyond this issue's
  "analysis and root-cause plan" scope; the plan is written with expected
  savings per step. The standalone Builder's OOM kills at its 256 MiB limit
  (three on 2026-09-18) belong to the same lane.
- **No paid change.** Nothing here touches instance size; the analysis shows
  the demand is the process's own and the plan exhausts code fixes first.
