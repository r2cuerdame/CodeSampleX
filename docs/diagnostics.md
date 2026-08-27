# Decision diagnostics

CodeSampleX normal output remains the concise fact layer. Diagnostic mode adds
an explanation of how that same answer was reached; it never changes ranking,
grading, command exit codes, or evidence recording.

## Enable it

The following entry points use the same `csx.debug.v1` object:

```text
csx --debug search "axios multipart upload"
CSX_DEBUG=1 csx search "axios multipart upload"
search_known_solution({"query":"axios multipart upload","debug":true})
run_observed_command({"command":["npm","test"],"debug":true})
```

`csx --debug search ... --json` returns the object under `diagnostic`. MCP
returns it in `structuredContent` and renders the same object after a `DEBUG
DIAGNOSTIC (csx.debug.v1)` human-readable section. `CSX_DEBUG=1` is intended
for a development or dogfooding session so an agent does not have to remember
the option on every call. The default is off; `CSX_DEBUG=0` is off.

A debug CLI search reads the normal local cache through the current CLI
binary's embedded engine instead of delegating the decision to an
already-running daemon. This makes the recorded client/server/protocol
versions truthful and prevents an older daemon that does not know `debug`
from silently returning an answer without a trace. Non-debug searches retain
the daemon fast path.

## Trace contract

Every trace has one request ID and records the client/server/protocol/payload
versions that are actually known. An unavailable version is omitted in JSON
and rendered as `unknown`; it is never guessed. The normalized coordinate
contains parseable public purls, bounded symbol coordinates, and the coarse
environment fingerprint. Query prose, prompt context, source, local paths,
usernames, and raw diagnostics are absent.

Search diagnostics expose:

- actual corpus/FTS candidate count;
- version, environment, symbol/API, and evidence-sufficiency check counts;
- score/rank and coordinate-dedupe counts;
- the dependency lookup state (`unknown`, `resolved`, or
  `proven-no-dependencies`) and the immutable-evidence freshness policy;
- at most 50 ranked candidate decisions, with stable rejection reason codes,
  selected-candidate relevance signals, and numeric feature contributions but
  no goal, contract, source, or evidence body; a larger set carries
  `TRACE/CANDIDATE_LIST_TRUNCATED` rather than silently appearing complete;
- canonical S/E/D/DIAG/ENV/BOUNDARY gaps and the unchanged final decision;
- measured elapsed microseconds for the stages that perform work.

The check counts are observations about the candidate set, not invented
filter stages. For example, an adaptation candidate may fail the
`environment-compatibility-check` count and still be returned as
`ADAPTATION_REQUIRED`. `filter-rank` and `output-relevance` identify actual
rejections.

`USAGE_OBSERVATION` is always described as project-level and the structured
field `usageObservationIsSymbolProof` is always false. A query that asks for a
symbol but has no contract proof carries `E/SYMBOL_EXECUTION_EVIDENCE`.
Dependency silence is `D/DEPENDENCY_GRAPH`; it is never rendered as
`proven-no-dependencies`.

Failure diagnostics reuse the actual-stage analyzer documented in
[architecture.md](architecture.md). They keep outer command/intent separate
from actual toolchain/stage, structured termination, sanitized diagnostic code
and fingerprint, evidence quality, and DIAG gap. Thus `go test` containing a
nested `tsc` `TS2352` failure is outer `PROJECT_TEST` but actual
`PROJECT_COMPILE` / `typescript/tsc`; a Go assertion remains
`PROJECT_TEST` / `go/test`. Missing diagnostics stay a DIAG gap instead of
becoming a guessed cause.

`potentialAnomaly` defaults to false. A trace is diagnostic evidence, not a
bug verdict. Submit `report_anomaly` only when a local measured result
concretely contradicts the CSX answer. A lone `NO_SAFE_MATCH`, a weak
candidate, or a trace gap is not an anomaly.

## Privacy and bounds

Diagnostic output may contain only public coordinates, stable IDs, sanitized
failure identifiers, aggregate counts, reason codes, and timings. It must not
contain secrets, tokens, credentials, prompt/agent context, customer data,
private source, local usernames, or absolute paths. Candidate diagnostics are
bounded to 50 entries and deliberately exclude contract and goal text; putting
the suppressed answer body in a neighbouring debug field would undo normal
output suppression.

## Dogfooding

During CodeSampleX and ProjectOps investigation sessions, prefer session-level
`CSX_DEBUG=1`. Record a concrete trace ID and reason code when it reveals an
over-broad candidate, an evidence-scope promotion, an environment
normalization defect, or incomplete failure lineage. Keep external/default
sessions off so normal users continue to receive the concise fact layer.
