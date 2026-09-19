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
csx fix-claims work --repro-root seeds/fix-claims/reproducers --server URL   # the loop a Farm lane runs
csx fix-claims probe --repro seeds/fix-claims/reproducers/zod-462-prefault-undefined --version 4.6.1 --server URL
```

`work` polls until the queue answers `NO_WORK`. For each lease it finds the
reproducer directory whose `fix-claim.json` names the candidate (by package,
claimed-fixed release and normalized claim), and for each probe the planner
asked for -- plus each environment the reproducer's author listed -- it
copies the directory, repins the candidate's package to the probe's release
(`fixclaims.Repin`), regenerates the lockfile with the ecosystem's own tool
(`Relock`; a release the registry cannot resolve is `UNRUNNABLE`, not a
verdict), creates the sample, uploads it under the lease, verifies it in the
local Docker sandbox, posts the signed receipt, and files the runs. The code
and the contract are never edited; only the pin moves. `probe` is one step of
that by hand; without `--id` it executes everything and files nothing, which
is how a reproducer is checked before it is committed.

```bash
csx fix-claims next --server URL                     # ASSIGNED | NO_WORK | BUDGET_EXHAUSTED
csx fix-claims runs --id 17 runs.json --server URL   # {"runs":[{version, environment, verdict, failureFingerprint, receiptId, sampleId, farmSeconds}]}
csx fix-claims report --id 17 --outcome no-reproducer --server URL   # or infrastructure | transient | no-output
```

`POST /v1/fix-claims/{id}/reproducer` records what was resolved. `POST
/v1/fix-claims/{id}/samples` is where a probe's sample goes: multipart like
`/v1/authoring/drafts`, but gated on the lease rather than on a WANTED
assignment, refused unless the manifest pins a release of the candidate's
package, and stored as a quarantined `DRAFT` whatever the receipt will say
-- a failing reproducer is the evidence this lane exists to keep. It never
enters cross-verification and never reaches the authoring inbox; it is read
only through the fix record that cites it. `runs` answers with the
re-evaluated record and `nextProbes`; the lease is released with the runs,
so a worker takes the next probes on its next poll. A poll that finds the
planner has nothing more to ask of a record closes it (`complete`) and looks
again, up to five such rows per poll.

Two consecutive `INFRASTRUCTURE` outcomes stop `work`: the same machine
failing the same way twice will not succeed a third time, and since the
outcome refunds the attempt, a worker that kept polling would spin on one
candidate.

A reproducer directory is `csx.json` plus `fix-claim.json` (`source`, the
`candidate` or `candidates` it answers, optional `environments` the author
wants probed beyond the planner's, `notes`) and the sample's own files. One
directory may answer the same advisory on several release lines
(`candidates`), each with its own pair of runs.

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

## Phase 0, as measured

Collected 2026-09-19 from the ten sources in `seeds/fix-claims/phase0.json`,
five stable releases each (`seeds/fix-claims/phase0-candidates.json`):

| Number | Value |
| --- | --- |
| release-note lines that read as a fix | 143 |
| kept as executable candidates | 64 (79 skipped: 41 `no-executable-target`, 23 `non-executable-claim`, 15 `release-cap`) |
| submitted after the round-robin limit | 50 across 9 packages (serde_json's five releases carried no fix line) |
| confidence | 27 high, 18 medium, 5 low |
| validator rejections at the door | 0 (`TestPhase0CandidatesEnterTheQueueAndComeOutAsWork`) |

### Executed

Sixteen reproducers were written for 21 of those candidates
(`seeds/fix-claims/reproducers/`; the undici advisories are one reproducer
each across the 6.x, 7.x and 8.x lines). `TestPhase0ReproducersRunEndToEnd`
(`internal/cli/fix_claims_phase0_test.go`, gated on `CSX_TEST_DOCKER=1`)
ran all of them on 2026-09-19 through the real server mux -- lease, sample
upload, signed receipt, receipt-checked run admission, evaluator -- in the
local Docker sandbox, probing `linux` from a Windows host. The record of that
run, every fix record with its runs, receipt ids and sample ids, is
`docs/evidence/issue-444-phase0-runs.json`.

| Number | Value |
| --- | --- |
| candidates executed end to end | 21 (across axios, express, pydantic, requests, tokio, undici, zod) |
| usable reproducers | 21 / 21 (16 directories; 0 reused an existing sample) |
| bug reproduced on the bad release | 21 / 21 |
| `VERIFIED_FIX` | 15 |
| `REGRESSED` | 4 (see below) |
| `REPRODUCED_BUG` / `AMBIGUOUS` | 2 |
| `CLAIM_NOT_REPRODUCED`, `PARTIAL_FIX`, `UNRUNNABLE` | 0 |
| runs filed | 69 (42 pair probes, 4 author environments, 23 boundary probes) |
| Farm seconds | 566 total; 37.7 per verified fix over the lane, 27.7 over the verified records' own runs |
| wall clock | 10 min 5 s, one worker, one probe at a time |

What the run found that the release notes do not say:

- **pydantic 2.13.3** ("Handle AttributeError subclasses with
  `from_attributes`") reproduces and is fixed on Python 3.12, and does not
  reproduce at all on Python 3.14: the record is `VERIFIED_FIX` with
  `pairOutcome: ENVIRONMENT_DEPENDENT` and the 3.14 environment reads
  `NOT_REPRODUCED`. The note names no runtime.
- **axios 1.18.1** ("AxiosError#cause non-enumerable"): 1.18.0 fails as
  described; 1.18.1 fails at a different place under the same contract
  (`AMBIGUOUS`, `verified: false`). Either the reproducer reads the claim
  too widely or the fix is narrower than the note; the record says which
  fingerprint each side produced and stops there.
- **undici 6.28.1** (GHSA-rfgv-xxqx-mfg5, WebSocket subprotocol): 6.28.0
  throws the advisory's `TypeError`; 6.28.1 neither throws nor closes with
  1002 within the contract's timeout. `AMBIGUOUS`. The 7.29.1 and 8.10.2
  patches of the same advisory pass.
- **undici 7.29.1** (all four GHSA patches): the pair is clean, and the
  planner's `regression-watch` probe at 8.10.1 -- the next known release
  above the fix -- fails with the bug's fingerprint, so the evaluator reads
  the record as `REGRESSED` at 8.10.1. That is literally true along
  version order and the 8.10.2 record beside it is `VERIFIED_FIX`; whether
  a maintenance-line patch should instead be read within its own line is a
  product decision this run surfaces rather than makes.
- The boundary walk moved `firstObservedBad` below the claimed bad release
  for pydantic 2.13.2 (to 2.13.0), requests 2.34.0 (to 2.33.0) and zod 4.6.3
  (to 4.6.1): the reproducer failed the same way there.

One defect in the product fell out of the run. The sandbox names each
container after a hash of its workspace, the stage log opens with that
docker command line, and the sanitizer did not normalize a sixteen-digit
hex run -- so every contract-failure fingerprint the verifier had ever
produced was unique per run, and the same assertion at undici 7.29.0 and
8.10.1 carried two digests. `REPRODUCED_NOT_FIXED`, `REGRESSED` and the
fingerprint-hint priority were unreachable. The name is now `csx-<token>`
(`internal/sanitizer`, `TestFingerprintIgnoresTheSandboxContainerName`);
nothing that clustered before splits, because nothing did.

Not measured here: extraction precision beyond the validator's door (the 21
reproducers were chosen by hand from the 50 accepted candidates, so
"candidates with a usable reproducer" is 21/50 = 42 % by that choice, not by
an automated resolver); reuse of existing samples (none of the ten packages
had a published sample exercising the claimed symbol); cost on the Farm's
own hosts. The `exhausted` metric reads 9 because a poll closes at most
five planner-complete rows before answering `NO_WORK`; the remaining rows
close on the next poll.

## Non-goals, still

No changelog is indexed whole; no matrix is run whole; no model-written
reproducer is stored without building and running; no maintainer's claim is
presented as this network's verification.
