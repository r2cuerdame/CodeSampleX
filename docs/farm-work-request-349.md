# Farm work-request receipt diagnostics (LoopOffice #349)

Tracking: https://github.com/r2cuerdame/LoopOffice/issues/349
Evidence: https://github.com/r2cuerdame/CodeSampleX-Farm/actions/runs/43181610484
Farm alert/collection PR: https://github.com/r2cuerdame/CodeSampleX-Farm/pull/267

The evidence run reports 98 work refusals/hour while author-1 holds a lease for
1h52m. This is not enough to identify the failing server stage. The CLI previously
discarded the response diagnostic headers, including on 503.

`sampleWorkerRequestWork` now retains HTTP status, a validated 32-character lowercase
hex `X-CSX-Request-ID`, and a closed-set `X-CSX-503-Reason` in its existing rejection
line. Missing or invalid headers become `unknown`. The line includes RFC3339 KST
time and distinguishes HTTP rejection, body read failure and oversized body.
It never includes response bodies, raw errors, request envelopes, URLs or tokens.
Farm #267 already accepts the `request_id=` / `reason=` format. It collects server
SHA separately from the public `/version` endpoint; that SHA describes collection
time, not necessarily the failed request. No extra request or lease change is added.

The reason allowlist mirrors `writeAuthoringWorkBusy`/`writeAuthoringWork503`:
`session_refresh`, `candidate_scan`, `held_work`, `completeness`, `cli_completeness`,
`claim` crossed with `deadline`, `pool_busy`, `query_timeout`, `canceled`, plus
`storage_unavailable`. New reasons require updating the allowlist.

## What #594 does and does not prove

Merge 0aa8ee4e changes authoring admission and DB class defaults, with three-slot
regression coverage: two stalled author claims must leave the third able to claim,
and verification remains available. It is a plausible fix for admission starvation.
It does not change `sampleWorkerRequestWork`, and does not prove which stage caused
this specific long-held lease. The 18:30 KST public revision b33f00ea from the earlier
Farm investigation predates #594. No deployment, operational restart, ledger or lock
editing is part of this change. Actual recovery needs correlated server receipts
and observation after a separately authorized rollout.

## Verification

Go 1.26.5 / Windows: focused rejection and success tests pass; `go build ./...` passes.
The tests exercise missing headers, hostile headers, secret response bodies,
HTTP 200 read/size failures, KST time, and a silent successful poll. Full-suite and
PR CI results are recorded in the PR and the operator report.
