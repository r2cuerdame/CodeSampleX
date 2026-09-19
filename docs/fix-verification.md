# Verifying upstream bug-fix claims

*#444. The pipeline that turns "fixed in 2.4.1" from a release note into a
FAIL receipt on 2.4.0 and a PASS receipt on 2.4.1 under the same reproducer
-- or into an explicit statement that it could not.*

## What is and is not evidence

| Thing | What it is | Where it lives |
| --- | --- | --- |
| A release-note line, a closed issue, a merged PR | **provenance** | `Candidate.sourceUrl`, `references`, `claim` |
| An AGY answer | a **hypothesis** | a `Candidate`, exactly like a collector's |
| A passing compile | nothing about the bug | refused: a run needs a contract verdict |
| A receipt whose contract FAILED on the bad release and one whose contract PASSED on the claimed-fixed release, same sample code, same environment | **evidence** | `fix_runs`, cited by receipt id |

Everything in this document follows from the last row. A candidate has no
status field (`schemas/v1/fix-candidate.json`, `additionalProperties:
false`); the ingest refuses a document that carries one. The only write that
moves a record past `CLAIMED_FIX` is `POST /v1/fix-claims/{id}/runs`, and
it admits a PASS or FAIL only with a receipt this server already holds for
the sample named, that reached the same verdict, on a sample whose manifest
pins the release the run is filed under (`internal/httpapi/fixclaims.go`,
`admitFixRun`). `UNRUNNABLE` needs no receipt and moves nothing.

## States

| Status | Meaning | `verified` |
| --- | --- | --- |
| `CLAIMED_FIX` | upstream says it is fixed; CSX has executed nothing | false |
| `REPRODUCED_BUG` | the reproducer FAILED on the bad release; the fixed release is untested, unrunnable, failed the same way (`pairOutcome: REPRODUCED_NOT_FIXED`) or failed another way (`AMBIGUOUS`) | false |
| `VERIFIED_FIX` | same reproducer, same environment: FAIL on the bad release, PASS on the claimed-fixed release, both under receipts | **true** |
| `PARTIAL_FIX` | reproduced in more than one environment and fixed only in some | **true** |
| `CLAIM_NOT_REPRODUCED` | the bounded test could not make the bad release fail. Explicit; never a PASS for the fix | false |
| `REGRESSED` | the bug's fingerprint appeared again on a release above a verified-good boundary | false |

`pairOutcome` is kept beside the status because two facts hide behind
`REPRODUCED_BUG`. Per environment it is one of `REPRODUCED_AND_FIXED`,
`REPRODUCED_NOT_FIXED`, `NOT_REPRODUCED`, `FIXED_VERSION_UNRUNNABLE`,
`BAD_VERSION_UNRUNNABLE`, `AMBIGUOUS`; across environments any two different
decided outcomes fold to `ENVIRONMENT_DEPENDENT`. Every list response carries
the table above under `semantics`, so a reader never has to guess.

The evaluator (`internal/fixclaims/evaluate.go`) is a pure function of the
candidate's two claimed versions and the runs recorded. Order of arrival
does not matter; a re-run at the same version supersedes the earlier one in
the reading, but the earlier row stays in `fix_runs`. Its contract is the
fixture set in `internal/fixclaims/testdata/evaluate.json`: confirmed fix,
false claim, partial OS fix, not reproduced, later regression, and the
states in between.

"Same bug" is the normalized CSX failure fingerprint. The fixed side failing
with a *different* fingerprint is `AMBIGUOUS`, not the bug surviving.

## Pipeline

```text
GitHub releases ──csx fix-claims collect──▶ Candidate document
                                                │
                     (optional) AGY lane: csx fix-claims prompt ▶ model ▶ ParseAGYOutput
                                                │
POST /v1/fix-claims/candidates ──── fixclaims.Validate (deterministic) ──▶ fix_candidates (CLAIMED_FIX)
                                                │
POST /v1/fix-claims/work/next ──── lease + fixclaims.Resolve + fixclaims.Plan ──▶ worker
                                                │
        worker runs the reproducer at each probe, files receipts (POST /v1/verifications)
                                                │
POST /v1/fix-claims/{id}/runs ──── admitFixRun (receipt check) ──▶ fixclaims.Evaluate ──▶ status
                                                │
GET /v1/fix-claims?purl=pkg:npm/foo@2.4.1 ──▶ "was it actually fixed in 2.4.1?"
```

### Collector

`csx fix-claims collect --seeds seeds/fix-claims/phase0.json --releases 5
--limit 50` reads each source's newest stable GitHub releases and turns
lines that read as a bug fix into candidates: the release is the claimed
fixed version, the highest lower release in the same major line among those
read is the claimed bad version (by version, not by date -- maintenance
lines interleave on the calendar), linked issues and PRs become
`references`, code spans become `symbols`, OS and runtime words become
`environmentHints`. Confidence is high with both a symbol and a reference,
medium with one, low with neither. Every line read and not kept is listed
under `collections[].skipped` with its reason, so precision can be measured
from what was left out as well as what was kept. `--limit` takes packages
in turn, highest confidence first, so one chatty package cannot crowd the
others out.

### AGY lane

`csx fix-claims prompt input.json` renders the fixed instruction
(`fixclaims.AGYPrompt`); `fixclaims.ParseAGYOutput` reads the answer. The
answer is either a decline or exactly one candidate document with no
unknown fields, which then goes through the same `Validate`. There is no
field a model could set to move a claim anywhere; the lane produces
`FIX_CANDIDATE` work and nothing else.

### Validator

`fixclaims.Validate` consults nothing outside the document and returns
every rejection at once. Beyond the shape rules (ecosystem, versions,
URLs, tokens), two rules decide executability: a line carrying a
documentation/tooling/process mark is refused whatever else it says
(`non-executable-claim`), and a line naming neither a symbol nor a failure
word is refused (`no-executable-target`). Both regexes were tuned on the
Phase 0 corpus and both are deliberately conservative: the cost of a false
accept is Farm minutes, the cost of a false reject is a claim the AGY lane
can rescue.

### Reproducer

`fixclaims.Resolve` prefers, in order: a published sample that already
names the package and one of the claim's symbols (`EXISTING_SAMPLE`; the
worker re-pins its manifest per probe and changes nothing else), a
reproducer carried by the upstream issue or PR (`UPSTREAM_REPRO`), a
generated minimal case of kind `FIX` (`GENERATED`), which must build and
run before it is stored. The same code and the same contract are used on
every side of the boundary.

### Planner

`fixclaims.Plan` is adaptive, never Cartesian. Until the pair has run in the
first environment it asks for the pair and nothing else; an OS named in the
hints adds the pair in that OS. Only where an environment shows
`REPRODUCED_AND_FIXED` or `REPRODUCED_NOT_FIXED` does it walk the boundary:
one known release below the lowest FAIL (`find-first-bad`), one above the
highest PASS (`regression-watch`), one above the claimed fix when the fix was
not where it said (`later-fix`). `NOT_REPRODUCED`, `UNRUNNABLE` and
`AMBIGUOUS` ask for nothing more. Known releases come from the `packages`
table; nothing consults a registry. Probes for an OS the polling worker
cannot run are not handed to it.

## Budget

| Knob | Default | What it bounds |
| --- | --- | --- |
| `CSX_FIX_WORK_MAX_LEASES` | 2 | candidates leased at once across the whole fleet. `0` disables handouts: the no-build rollback |
| `CSX_FIX_MAX_ATTEMPTS` | 3 | handouts for a candidate that has produced no signal, after which it is closed (`attempt-cap`) |
| `CSX_FIX_MAX_RUNS` | 12 | runs per candidate across every version and environment, after which it is closed (`run-cap`) |

The queue is its own table (`fix_candidates`, migration 0049) with its own
lease count, so it cannot take a WANTED, EXPANSION or DEPENDENCY handout
from the authoring lane. Claims are breadth first -- fewest attempts, then
score, then age -- so every candidate gets its pair before any gets a third
look. `INFRASTRUCTURE` and `TRANSIENT` outcomes refund the attempt; a
candidate with a signal is never closed by the attempt cap. Closing takes a
row off the board and deletes nothing; its record and evidence stay
readable.

The score (`fixclaims.Score`) weighs a failure fingerprint the network has
already observed above everything, then a published sample that already
exercises the symbol, then severity words, confidence, asks and freshness.

## Worker protocol

A fix worker is a sample worker with a different queue: the same session
token (`CSX_SESSION_TOKEN`), the same lease-and-outcome shape.

```bash
csx fix-claims next --server URL                     # ASSIGNED | NO_WORK | BUDGET_EXHAUSTED
# work.reproducer.source tells you where to start; work.probes lists version+environment to run
# run the sample at each probe and file its receipt through the verifier as usual
csx fix-claims runs --id 17 runs.json --server URL   # {"runs":[{version, environment, verdict, failureFingerprint, receiptId, sampleId, farmSeconds}]}
csx fix-claims report --id 17 --outcome no-reproducer --server URL   # or infrastructure | transient | no-output
```

`POST /v1/fix-claims/{id}/reproducer` records what was resolved. `runs`
answers with the re-evaluated record and `nextProbes`; the lease is
released with the runs, so a worker takes the next probes on its next poll.
A poll that finds the planner has nothing more to ask of a record closes it
(`complete`) and looks again.

## Reading it

```bash
curl 'https://codesamplex.dev/v1/fix-claims?purl=pkg:npm/foo@2.4.1'
curl 'https://codesamplex.dev/v1/fix-claims?ecosystem=pypi&name=requests&status=VERIFIED_FIX'
curl 'https://codesamplex.dev/v1/fix-claims/17'
```

A package key is required so the read is an index lookup. Each item carries
`status`, `verified`, `pairOutcome`, `badVersion`, `goodVersion`,
`environments` (per-environment outcome and boundary), `failureFingerprint`,
`sampleId`, `evidence` (receipt ids), `upstream` (type, url, references,
claim, confidence) and `reproducer`. `GET /v1/fix-claims/metrics` (writer
session) is the Phase 0 measurement: extraction precision, reproducer rate,
reproduced rate, confirmed rate, incorrect-claim rate, sample reuse rate and
Farm seconds per verified fix, each with its denominator.

## Phase 0, as measured so far

Collected 2026-09-19 from the ten sources in `seeds/fix-claims/phase0.json`,
five stable releases each (`seeds/fix-claims/phase0-candidates.json`):

| Number | Value |
| --- | --- |
| release-note lines that read as a fix | 143 |
| kept as executable candidates | 64 (79 skipped: 41 `no-executable-target`, 23 `non-executable-claim`, 15 `release-cap`) |
| submitted after the round-robin limit | 50 across 9 packages (serde_json's five releases carried no fix line) |
| confidence | 27 high, 18 medium, 5 low |
| validator rejections at the door | 0 (`TestPhase0CandidatesEnterTheQueueAndComeOutAsWork`) |

Everything from the reproducer onward -- usable reproducers, bugs
reproduced, fixes confirmed, incorrect claims, Farm minutes per verified fix
-- is a number this repo cannot produce: it needs the Farm lane
(CodeSampleX-Farm) to run `csx fix-claims next` and file receipts. The
metrics endpoint reads those numbers from the queue once they exist;
nothing here estimates them.

## Non-goals, still

No changelog is indexed whole; no matrix is run whole; no model-written
reproducer is stored without building and running; no maintainer's claim is
presented as this network's verification.
