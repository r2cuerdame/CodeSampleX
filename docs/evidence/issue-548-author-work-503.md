# Issue #548: author work 503 diagnosis checkpoint (2026-10-08 UTC)

The direct cause is **unresolved**. This is a read-only evidence checkpoint, not a
server-defect, overload, or Farm-format verdict.

## Observed request window

The [Farm #202 checkpoint](https://github.com/r2cuerdame/CodeSampleX-Farm/blob/ea5cb4c12f8497017cdbc5c298a6239c5342bd72/docs/evidence/issue-202-20261008.md)
ties the active author slot to `POST /v1/authoring/work/next`. The 07:05–07:06Z
[farm-evidence run](https://github.com/r2cuerdame/CodeSampleX-Farm/actions/runs/37741350122)
reduced the slot's journal to hourly counts:

| UTC hour, Oct 8 | HTTP 503 | Assignments | Drafts | Session refreshes |
| --- | ---: | ---: | ---: | ---: |
| 02 | 48 | 0 | 0 | 44–49 per hour across this window |
| 03 | 43 | 0 | 0 | 44–49 per hour across this window |
| 04 | 49 | 0 | 0 | 44–49 per hour across this window |
| 05 | 48 | 0 | 0 | 44–49 per hour across this window |
| 06 | 45 | 0 | 0 | 44–49 per hour across this window |

The reducer provides no individual rejection timestamps or response headers and
bodies. The CodeSampleX CLI prints only `server rejected work request (HTTP
503)` for non-200 work responses (`internal/cli/sample_worker.go`,
`sampleWorkerRequestWork`). The request format therefore cannot be diagnosed
from this log. A public `/v1/wanted` response with 200 rows demonstrates demand,
not that any row passed this author's eligibility or live claim checks.

## Server decision paths

At the deployed server revision
`63f5dbe16cb464e4a4b1b1fdbbe1ecbc563739b7`, the work handler in
`internal/httpapi/authoring_work.go` can answer 503 when the authoring store is
unavailable, or when a deadline, cancellation, query timeout, or pool refusal
occurs during session refresh, candidate loading, held-work lookup,
completeness checks, or claim. The busy branch sends `Retry-After: 5` and
`authoring work is busy; retry shortly`; these are **code paths**, not observed
headers or bodies of Farm's requests. Invalid/unsupported request fields in
`readAuthoringWorkRequest` answer 400, but this does not establish what the
Farm client actually sent or whether an upstream component answered the 503.
An empty eligible candidate list reaches `ClaimAuthoringWork` and can return
`NO_WORK` with HTTP 200; it is not, by itself, a 503 explanation.

The deployed Caddy configuration labels this route `authoring_work` and retains
timestamp, fixed route, method bucket and status, but deletes the request,
response headers and duration. A Caddy 503 record could place the failure in
time, but cannot identify which handler branch produced it. The existing
[CodeSampleX #545 post-deploy observation](https://github.com/r2cuerdame/CodeSampleX/issues/545)
starts at 01:30:15Z and reports zero active builder rounds, 14 pool-busy and
two query-timeout log lines in its own observation window and 3.837 seconds
maximum DB-pressure wait. It does not provide a 02–06Z work-route request
record or a per-request join to Farm's 503s. The concurrent
[#527](https://github.com/r2cuerdame/CodeSampleX/issues/527) and
[#528](https://github.com/r2cuerdame/CodeSampleX/issues/528) p95 violations
support a pressure hypothesis but do not prove it caused this route's 503s.
Builder inactivity is separately observed, not a demonstrated gate on the
author work request.

## Disposition and missing discriminator

No server defect, overload cause, or Farm request-format cause is established.
No code change or RED→GREEN regression can honestly target a specific fault.
The missing evidence is a privacy-safe same-window join of an individual Farm
work rejection timestamp/status with the server's `authoring_work` status and
`db pressure path=/v1/authoring/work/next` or candidate-scan logs, plus a
response class/body or other record that distinguishes handler branches. A
bounded, read-only retrieval of retained logs through a known authenticated
path, if available, would discriminate the hypotheses. No production host
write, restart, SSH attempt, deploy, or paid change was made for this checkpoint.

Until the direct cause is established, Issue #548's cause verdict and, if it
is a server defect, its baseline-RED/head-GREEN regression, independent QA,
merge, deploy, and post-deploy Farm response measurement remain unmet.
