# Production uploader liveness fix — 2026-08-18

## Outcome

The community daemon now attempts its first evidence upload after a bounded five-second delay and then resumes the existing 15-minute cadence. A partially accepted evidence response records only the accepted count in `lastUpload` / `evidenceBatchesSent`, restores each refused aggregate to pending, reports the refusal, and defers retry to a later pass instead of immediately resending the same refused payload.

The MCP command still performs no synchronous upload. It starts version-aware daemon ensure asynchronously, serves the MCP transport immediately, and starts no daemon at all in local-only or uninitialized mode.

## Files owned by this fix

- `internal/daemon/daemon.go`
- `internal/daemon/uploader_liveness_test.go`
- `internal/evidence/batch.go`
- `internal/evidence/batch_partial_test.go`
- `internal/cli/mcp.go`
- `internal/cli/mcp_liveness_test.go`
- `docs/production-uploader-liveness-fix-2026-08-18.md`

All preexisting activity, admin, deploy, landing, i18n, search, schema, engine, CSX-Burn, and other dirty work was left untouched. No deployment, production process access, production queue access, job claim, requeue, deletion, or fabricated peer occurred.

## Regression evidence

- `TestDaemonLivingLessThanUploadEveryStillFlushesEvidence` runs a daemon with a one-hour recurring cadence and a short first delay, observes the evidence request, and shuts the daemon down well before the hour.
- `TestPartialEvidenceUploadStampsOnlyAcceptedBatches` returns a mixed 202 response, asserts `lastUpload` is present, asserts `evidenceBatchesSent == 1`, and asserts only the refused aggregate remains pending.
- `TestUploadPartialAcceptanceKeepsOnlyRefusedRowsPending` proves the refused aggregate survives the first call and lands on the next call without resending the already accepted aggregate.
- `TestMCPStartupReturnsToolResponseThenDrainsPreexistingOutboxes` blocks daemon ensure, receives a real `get_local_stats` tool response within one second, releases startup, observes the preexisting Wanted and adoption rows drain on the bounded first queue pass, observes evidence drain on its bounded first upload, and performs bounded daemon and MCP shutdown.
- `TestMCPAutostartIsCommunityOnly` proves local-only and uninitialized MCP sessions serve a tool response without calling daemon ensure.
- Existing `TestEnsureRunningStopsAStaleVersionBeforeSpawning` remains green, proving a stale-version daemon is gracefully stopped before replacement is spawned.
- Existing `TestLocalOnlyAndUninitializedSyncNeverContactNetwork` and `TestRunReloadsAConcurrentCommunityRevocation` remain green, covering automatic/synchronous non-community network gates and a consent change racing daemon startup.

## Cross-verification queue audit

No cross-queue code was changed. Open cross jobs remain durable demand inventory: one outstanding request for an independent second peer, not leaked work to delete or mark complete. The repository has no bounded admin/store label for this population outside activity-owned admin work, so no label or admin file was changed.

Focused existing regressions passed:

- `TestAJobHeldByADeadPeerReturnsToTheQueue`: a live claim is hidden, an expired `JobLease` claim is listed again, and another peer can reclaim it.
- `TestAPeerCannotCrossVerifyItsOwnSample`: the origin receipt does not complete the cross job, the origin is not offered its own job, and a second peer's receipt completes it.

These tests preserve the independent-peer guards and establish that `status='claimed'` is not equivalent to unavailable when the lease is expired.

## Observed validation

Every build/test command below ran through CodeSampleX `run_observed_command`.

- Focused uploader/MCP/version tests: pass.
- Affected packages (`internal/evidence`, `internal/daemon`, `internal/cli`, `internal/httpapi`) individually: pass.
- Focused liveness tests repeated five times: pass.
- Focused MCP startup tests repeated three times after tightening the queue deadline: pass.
- Focused cross-queue lease and independent-peer tests: pass.
- Native `go test ./... -count=1`: pass.
- Native `go vet ./...`: pass.
- Linux container `golang:1.26`, `go test -race ./internal/evidence ./internal/daemon ./internal/cli -count=1`: pass.
- Linux container `golang:1.26`, `go test -race ./... -count=1`: pass.

Native Windows race setup was also checked: the host has no CGO C compiler, so the repository's Go 1.26 Linux container path was used. One preliminary combined affected-package run returned a sanitized failure fingerprint; every package passed immediately in isolation, and the subsequent repeated focused, full native, and full race runs all passed.

## Frozen hashes

Base Git HEAD before this fix: `024462a72dd3a8594f841fd8dcb6d572648fd3b1`.

```text
591a5fdb5362c5d6f5bc52721204f270a9d7a567ee52a133de48d3202c1254d1  internal/cli/mcp.go
3e5bcd9f6c9bea3d2d89437a525356b0e53cb874653eaaff3be220a36bbb2842  internal/cli/mcp_liveness_test.go
fb5320c164ad03c5f2d0298db0f69fe11baaf15e4e62e78b95b7e8db95e1c41b  internal/daemon/daemon.go
64b8ca6ad89db8b66832ce37e5a5159ae7658f0c5cac2725580c4f8de25ffdb3  internal/daemon/uploader_liveness_test.go
a71d16f2aa7e64a861dec3051aa1356a581210b48822317147d867c1f7104027  internal/evidence/batch.go
44f7b97a94cacbc819781f16bcad656b40981e7d39832807f1cbb095c529b5cb  internal/evidence/batch_partial_test.go
```

`git diff --check` passed at freeze time.
