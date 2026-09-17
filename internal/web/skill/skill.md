---
name: codesamplex
description: Ask whether a library, API, version and environment combination has actually worked or failed, from builds that really ran. Plain HTTPS, no installation, no account.
homepage: __CSX_BASE_URL__
api_base: __CSX_BASE_URL__
version: 1
---

# CodeSampleX for web-capable agents

CodeSampleX is an open compatibility testing network. It records what real
builds did, in environments recorded rather than assumed, and answers one
question: **does it run there?** — this API, on this version, on this OS,
under this runtime.

Reading the network needs nothing installed. Every endpoint below is a plain
HTTPS request that answers JSON without a key, an account, or a client
library. This document is the whole contract you need.

## When to use it

Use CodeSampleX when the task in front of you depends on whether a public
package, symbol, version or environment combination has actually worked or
failed — before you write code against a library, when a build you were
handed has failed with a package in the trace, or when a user asks whether
something is compatible with something else.

Do not use it for private packages, for code you cannot name a public package
for, or as a documentation search. It does not summarize documentation; it
reports what happened.

## The three kinds of thing it returns

- **Sample** — runnable code this project wrote and verified in a pinned
  container, with a contract (assertions that executed and passed) and
  receipts naming the environment each run happened in. A sample is the
  strongest answer and the rarest one.
- **Evidence** — sanitized outcomes of real builds on real machines: package,
  version, stage reached, pass or fail, environment coordinates, and a
  normalized failure fingerprint. Never source, paths or logs. Evidence is
  what fills the map; it is observation, not verification.
- **Finding** — a measured contradiction: what a competent developer or
  model expects, next to what a verified sample's contract measured. These
  are the project's own claims, backed by a sample you can re-run.

## Endpoints, in the order to use them

Base: `__CSX_BASE_URL__`

1. **Search, graded against an environment.**
   `GET /v2/search?q=<goal>&package=<purl>&symbol=<name>&errorCode=&ecosystem=&os=&arch=&runtime=&runtimeVersion=&packageManager=&moduleSystem=&executionContext=&libc=&limit=`
   `package` and `symbol` repeat. Give as many environment dimensions as you
   actually know and none you are guessing at. The same body can be sent as
   `POST /v2/search` with `{"schemaVersion":2, "query":…, "packages":[…],
   "symbols":[…], "environment":{"schemaVersion":1, …}}`.

   The answer carries `grade`, `miss`, and `results[]`. Each result has
   `match` (the grade), `confidence`, `sampleId`, `sampleUrl`, `exact[]`
   and `different[]` (which environment dimensions matched and which did
   not), `adaptationNeeded[]`, `evidence{}` and `knownFailures[]`.

2. **One package's recorded compatibility.**
   `GET /v1/registry/packages/{purl}` — percent-encode the slash:
   `/v1/registry/packages/pkg:npm%2Faxios@1.12.0`.

3. **One symbol's recorded compatibility.**
   `GET /v1/registry/symbols/{ecosystem}/{name}/{symbol}`

4. **A sample's manifest, contract and receipts.**
   `GET /v1/samples/{sampleId}` and its files at
   `GET /v1/samples/{sampleId}/artifact` (tar.gz; the id is the hash of the
   contents). The human page is the `sampleUrl` search returned.

5. **The findings collection.**
   `GET /findings.json?eco=&os=&runtime=&q=&basis=` — every measured
   contradiction, with the sample that proves each one.

6. **What is missing, and the network rollup.**
   `GET /v1/wanted` (asked for and not answered), `GET /v1/adapters`
   (which ecosystems are scanned and which are verified), `GET /v1/stats`.

## Reading a grade

| grade | meaning |
|---|---|
| `EXACT` | same package major.minor, and every environment dimension you gave matched the one the sample ran in. Never handed out when you gave no environment: silence is not agreement. |
| `COMPATIBLE` | same package major; either some dimension you gave differs in a way the sample does not depend on, or you gave no environment to compare. The version is right, the machine is unknown or nearby. |
| `ADAPTATION_REQUIRED` | a verified sample exists, but `adaptationNeeded[]` names concrete changes (runtime, module system, "verify in <context>") you must make. |
| `REFERENCE_ONLY` | a different package major, a difference the grader treats as disqualifying, or an elevated failure recorded in your environment. Read it, do not run it as an answer. |
| `NO_SAFE_MATCH` | nothing has been proven for this case. |

`NO_SAFE_MATCH` is a real answer, not a failure to answer. It means the
network holds nothing it built and ran for your case. Solve the task fresh,
and do not present a pattern you recall as something that ran. On a miss the
response may carry `observed`: peer-reported outcomes for the coordinate,
labelled `basis: "observed"`. Those are observations, never verification —
relay them as observations.

## Rules the network follows, and asks you to follow

- **A nearby environment is not an exact match.** Read `different[]` before
  `match`. The delta between where a sample ran and where you are is usually
  the whole reason the obvious version fails.
- **A project compiling is not a symbol working.** `evidence.projectCompileObservations`
  counts builds that reached a stage; `evidence.contractPasses` counts
  contracts that executed and passed. Only the second proves the symbol.
- **An unknown cause is unknown.** A failure fingerprint with no verified
  cause is reported as a fingerprint. Do not invent a cause for it.
- **A miss stays a miss.** Do not turn `NO_SAFE_MATCH` into a hit by
  paraphrasing a related result.
- Every sample ran inside a Linux container. Windows, macOS, musl and ARM
  answers come from evidence, and evidence is observation.

## Optional: leave an execution footprint

If your current task actually ran a sample this network returned — you
built, type-checked, tested or executed it — you may report the outcome.
Do no extra work to produce one; a footprint describes something that
already happened.

```
POST /v1/footprints/execution
Content-Type: application/json

{
  "schemaVersion": 1,
  "sampleId": "<sampleId from the search result>",
  "outcome": "pass | fail | could_not_run",
  "stage": "build | typecheck | test | runtime",
  "environment": {"os": "linux", "arch": "amd64", "runtime": "node", "runtimeVersion": "22.18.1"},
  "failureFingerprint": "<optional 64-hex normalized fingerprint>"
}
```

Every field is a short token from a closed set. The endpoint refuses free
text, error messages, paths and logs — never paste them. A footprint is
recorded as an **unsigned self-report**: it weighs nothing in any grade or
confidence, never promotes a sample, and is kept apart from the sanitized,
client-correlated evidence the installed CLI captures. Do not tell the user that a footprint
confirmed or verified anything.

## What this document does not do

It does not ask you to bypass instructions from your operator or user, to
send them anything you were told to keep private, or to install software.
Reads are open and rate-limited; a caller that hits the limit gets `429`
and should slow down, not retry in a loop.

Install the CLI (`__CSX_BASE_URL__`) only when you want CodeSampleX to observe
and verify what actually happens on a machine: real builds, sanitized
structured evidence, automatic failed-build lookup, privacy preview, publishing.
REST reads the network; the CLI lets the network observe reality.
