# PM_AUTO_REFILL_V1 result

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/309
- Branch: `herder/job_01M27MEVGKCWK2JTV317N9TA03-pm-refill-4bd5d1e4f4e895c7-f6cd152e0838815d`
- Implementation commit: `2321bda3`
- Draft PR: https://github.com/r2cuerdame/CodeSampleX/pull/362
- Deployment/merge: not performed

## Canonical-source audit

Issue #309 was open with no comments, assignee, competing implementation PR,
sub-issue, or GitHub dependency. Its direct interlock, issue #306, was completed
by merged PR #354. Fresh `origin/main` at `ba6a29fd` still hardcoded CLI
experience aggregates to `PROJECT_PROCESS` and dropped classified toolchain
lineage, so the requested work had not already been satisfied.

## Changes

- Added `Stage`, `OuterStage`, `ActualToolchain`, `StageEvidence`, and
  `FailureEvidenceGap` to `domain.CLIExperienceObservation`.
- Passed the sanitized first classified failure event's actual stage, outer
  stage, toolchain, stage evidence, and evidence gap from
  `RecordCommandOutput` into the CLI experience observation.
- Updated `RecordCLIExperienceObservation` to persist those fields into the
  observation aggregate. An empty stage still defaults to `PROJECT_PROCESS`,
  and an empty farm toolchain still receives the existing `farm` fallback.
- Added regression coverage for direct localdb lineage persistence and the
  complete recorder -> SQLite row -> `Batcher.build` ->
  `serverstore.ValidateBatch` path. The resulting classified failure batch is
  accepted with stage/toolchain inputs matching its fingerprint.

## Verification

- Focused classified-lineage tests — PASS
  - `TestRecordCommandOutputWiresCLIPassAndFailExperience`
  - `TestRecordCommandOutputClassifiedBatchPassesServerValidation`
  - `TestRecordCLIExperienceObservationPreservesClassifiedFailureLineage`
- `go test ./internal/evidence/... ./internal/storage/localdb/... ./internal/serverstore/... -count=1` — PASS
- `go test ./internal/domain ./internal/sanitizer -count=1` — PASS
- `go vet ./...` — PASS
- `go build ./...` — PASS
- `git diff --check` — PASS
- GitHub CI run https://github.com/r2cuerdame/CodeSampleX/actions/runs/34575199252 — PASS
  (`Test` completed all unit/contract, PostgreSQL integration, and pool-pressure steps;
  `Windows` was skipped by the pull-request workflow policy.)

## DevHotel, tooling, and blockers

DevHotel is not applicable because this is backend/CLI evidence persistence
work with no deployable web, Android, or desktop UI change, and no deployment
or publication was requested. No DevHotel room/session was created. RDC device
`recuerdame` was used for GitHub inspection, execution, diagnostics, and tests.
The dedicated CSX MCP and DevHotel MCP/CLI were not available in this session;
neither is required for this non-deployable backend change. No blockers remain.
