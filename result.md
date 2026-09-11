# PM_AUTO_REFILL_V1 result

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/306
- Branch: `fix/306-record-command-output-failures`
- Implementation commit: `cb93c6a`
- Draft PR: https://github.com/r2cuerdame/CodeSampleX/pull/354
- Deployment/merge: not performed

## Canonical-source audit

Issue #306 was open, unassigned, with no comments, no linked or cross-referenced
PR, and no blocking open dependencies. Related PRs #208 and #215 established
the baseline CLI experience recording and structured CLI execution evidence layers.
Fresh `origin/main` at `75db6ba` still retained the restrictive gate
`else if !profile.Known || profile.Stage == domain.StageProjectProcess` in
`internal/evidence/recorder.go`, confirming the bug was present and unaddressed.

## Changes

- In `internal/evidence/recorder.go:RecordCommandOutput`:
  - Removed the restrictive `!profile.Known || profile.Stage == domain.StageProjectProcess`
    gate, ensuring failure outcomes (`exitCode != 0` or non-empty termination)
    for all recognized CLI tools (`domain.IsRecognizedCLITool(tool)`) are recorded into
    the CLI experience ledger via `RecordCLIExperienceObservation`.
  - Retained failure diagnostic analysis (`AnalyzeFailure`) and sanitized classification
    (`SanitizeClassifiedFailure` / `SanitizeFailure`) which extracts error codes, fingerprints,
    and summaries.
- In `internal/evidence/recorder_test.go`:
  - Expanded `TestRecordCommandOutputWiresCLIPassAndFailExperience` to record a failing
    known build profile (`go test` with `StageProjectTest` and exit code 1) and assert
    that `QueryCLIExperience` reports `FieldFailCount == 1`, `Status == "OBSERVED_FAIL"`,
    and populates `RecentFailures`.
  - Added assertion for subsequent pass on the same coordinate to verify `Status == "COEXISTING_BOUNDARY"`
    without survivorship bias.
  - Added `TestRecordCommandOutputRecordsKnownBuildTestCompileFailures` covering known
    compile, test, and typecheck profiles (`npm run build`, `cargo test`, `tsc`, `pytest`),
    verifying both observation summary recall (`FieldFailCount == 1`, `Status == "OBSERVED_FAIL"`)
    and structured execution evidence rows via `ListCLIExecutionEvidence`.

## Verification

- `go test ./internal/evidence -run "TestRecordCommandOutput" -v` — PASS
- `go test ./internal/domain ./internal/sanitizer ./internal/evidence ./internal/storage/localdb -count=1` — PASS
- `go vet ./...` — PASS
- `go build ./...` — PASS
- `git diff --check` — PASS

## DevHotel, tooling, and blockers

DevHotel was not applicable because this change affects Go backend CLI experience
evidence persistence and does not alter deployable web, Android, or desktop UI behavior.
RDC and CSX MCP were not available in this job environment, so execution used the
assigned worktree's Git and Go toolchain. No deployment or merge was attempted.
No blockers remain for PR #354.
