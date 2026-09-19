# Issue #485 Result: verifier queue recovery accepted in production

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/485
- Branch: `issue/485-p0-astra-deep-performance-analysis-and`
- Implementation PR: #486
- Production release: `v0.1.199`
- Production deploy: run `35429687395`
- Full analysis: `docs/issue-485-verifier-queue-root-cause.md`

## Result

The verifier queue is recovered in production. The root cause was not the
`OpenJobsPage` query or PostgreSQL capacity: the resource governor set the
Farm admission ceiling to zero whenever host CPU steal was high, producing
`503 {"error":"database busy"}` while the pool was idle. PR #486 changed
`host-cpu-steal` to pause only the Builder; true interactive pool pressure
still sheds Farm and preserves bounded backpressure.

Release `v0.1.199` deployed exact revision
`ebde5fc4d120c23b122a48ae6ea14bbdfde65ac2`. A first observation failed in
the restart/convergence window, so the issue was correctly reopened. Fresh
production evidence on 2026-09-19 11:06–11:11 UTC proves the steady state:

- `/version` stayed on `v0.1.199` / `ebde5fc4`; `/healthz` returned 200.
- 20/20 verifier-queue requests returned 200 under active Farm load; maximum
  TTFB was 0.816 seconds.
- Farm evidence run `35439176130`: at 11:00 UTC,
  `queue_unavailable=0`, `receipt_completed=28`, `verdict_fail=0`.
- Farm health run `35439337257` passed: receipt age 0 seconds, 133 completed
  verifier jobs in 24 hours, and the server-error alert cleared.
- Authenticated pool reads 11 seconds apart: `farm_ingest.limit=2`,
  `busy=557 → 557`, `timeouts=0`; pool `inUse=1 → 0`, `idle=8 → 9`.

The measured before/after is therefore:

| Reading | Before deploy | Accepted production |
| --- | ---: | ---: |
| `farm_ingest.limit` | 0 | 2 |
| `farm_ingest.busy` | 168327 and growing | 557 → 557 |
| `farm_ingest.timeouts` | 0 | 0 |
| queue HTTP | 503 | 20/20 HTTP 200 |
| Farm verifier | no verdict progress | 28 receipts in the current hour |

The after-state runtime baseline is `memoryTotalBytes=770884024`,
`memoryLimitBytes=629145600`, `gcCycles=1575`,
`gcLimiterLastEnabledCycle=0`, and `gcCPUFraction=0.0487`. The retained-cache
memory reduction remains the separately scoped follow-up described in the
analysis; this issue does not expand into that implementation.

## Verification

The branch preserves the implementation tests introduced by #486 and the
latest main boot/runtime metrics contract. The final local verification run
and CI result are recorded in the pull request.

No paid infrastructure, data mutation, manual receipt creation, rollback, or
security-boundary change was used.
