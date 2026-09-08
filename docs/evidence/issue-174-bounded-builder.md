# #174 implementation evidence

Canonical incident: [#174](https://github.com/r2cuerdame/CodeSampleX/issues/174).
Canonical implementation and exact-head verification record:
[PR #254](https://github.com/r2cuerdame/CodeSampleX/pull/254).

The current implementation uses source-bound, Go-validated projections with
atomic writer maintenance and bounded incremental reads. See the
[design, failure contract and reversal procedure](../issue-174-incremental-builder.md)
and [DevHotel acceptance harness](174/README.md).

The expression-index candidate's measurements and release notes are preserved
in the [historical evidence at 50184e59](https://github.com/r2cuerdame/CodeSampleX/blob/50184e59716f1c607ffdac52dcacf3d9ff43c707/docs/evidence/issue-174-bounded-builder.md).
Those measurements describe that earlier implementation. Its helper functions,
migration digest, phase units and acceptance identity are superseded; use the
final PR head's CI and DevHotel evidence for merge/release decisions.

Neither implementation's synthetic fixture establishes sustained production
recovery. The incident remains open pending the production acceptance described
in the current design document.
