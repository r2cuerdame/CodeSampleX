# Learning which coordinates cannot be authored, and withholding them

*R2C-23. Written against the incidents below; every number in it is a proposal
measured against them rather than a preference.*

## What went wrong

Three kinds of package turned out to be structurally impossible to write a
sample against, and each was found by hand, after it had already cost a worker
hours:

| Shape | Why nothing can be written | Found |
| --- | --- | --- |
| Gradle plugin marker (`*.gradle.plugin`) | a pom whose only job is to point at the artifact that does the work: no jar, no classes, no symbols | `eacb3be` |
| Maven pom-only coordinates (BOM, parent) | a BOM or parent POM declares versions for other modules and contains no classes; 404 for the jar, 200 for the pom | `82f2ba9` |
| Per-platform npm native builds | `main` is the `.node` binary the *parent* package selects internally — `@tailwindcss/oxide-linux-x64-gnu` is `tailwindcss-oxide.linux-x64-gnu.node` | `82f2ba9` |

The worst single case is measured: one worker was handed
`org.jetbrains.kotlin.plugin.serialization.gradle.plugin`, tried **22 times
across four hours**, got as far as disassembling the `csx` binary looking for
something to call, and was handed the same coordinate on every restart. Sample
production across the network fell from 33 an hour to nothing while it ran.

Two separate defects made that possible:

1. **Nothing remembered the attempts.** Every restart was a fresh start, so
   nothing accumulated and nothing could ever conclude.
2. **Nothing released a live claim.** Reclaim deletes an assignment whose
   *session* is revoked or idled out. That session was neither: it refreshed
   every 45 minutes, so it held the coordinate for the full 24-hour lease and
   the coordinate was off the board for everybody else too.

Enumerating shapes by hand will always lag the ecosystem. What generalises is
not the shape of the package — it is the attempt.

## The minimum schema

One row per public coordinate, `authoring_attempts`
(`0021_authoring_attempts.sql`), keyed
`(ecosystem, name, version, symbol)`.

The mutable state travels as one JSONB document rather than a column per
counter, because the transition rules live in Go
(`internal/serverstore/authoring_quarantine.go`) and are shared byte for byte
by the in-memory store and PostgreSQL. Expressing them twice — once in SQL,
once in Go — is exactly how the two implementations of the farm panel drifted
apart before. `quarantined_at` and `reopens_at` are also plain columns because
the operations panel and the picker both filter on them.

| Field | Meaning |
| --- | --- |
| `attempts` | handouts, ever |
| `noOutput` | handouts that produced nothing publishable and were not excused, since the last one that did |
| `authored` | samples actually written |
| `excused` | attempts refunded as somebody else's fault |
| `sessionsMeasuringImpossible` | distinct writers that reported no callable symbol |
| `firstAttemptAt` / `lastAttemptAt` | the age an operator reads |
| `quarantinedAt` / `quarantineReason` / `reopensAt` | why the work left the board, and whether it comes back on its own |
| `history` | the last 10 attempts, each with kind, session, outcome and the writer's note |
| `sessionHandouts` / `noSymbolBy` | per-writer bookkeeping, not evidence |

**Why the key is the coordinate and not (coordinate, work kind).** A package
with no callable symbol has none whichever queue picked it, and splitting the
key would let the same hopeless coordinate restart its count under another
kind. The kind is kept on every history entry, so the record still
distinguishes a WANTED attempt from an EXPANSION one.

## The classification

The server can observe exactly one thing unaided: that it gave a coordinate
out and got nothing back. It cannot tell a Docker daemon that died from a
registry that was down from an artifact that contains no code — and treating
those three as one is how a bad afternoon at a registry becomes a permanent
exclusion. So the classification comes from the writer, over
`POST /v1/authoring/work/outcome` (`csx sample-worker report --outcome`).

Withholding is scoped to the assignment's completeness axis. The coordinate's
bounded history is shared for audit, but retry counts, writer handout limits,
and quarantine gates reset when work moves between Sample, Evidence, and
Dependency. Otherwise two writers proving a Sample impossible could silently
starve a dependency graph that a resolver can still measure.

| Outcome | What it says | Effect |
| --- | --- | --- |
| `HANDED_OUT` | server bookkeeping: an attempt opened | `attempts++`, `noOutput++` |
| `AUTHORED` | server bookkeeping: a sample was attached | resets every counter that withholds work; history kept |
| `INFRASTRUCTURE` | the writer's own machine failed | refunded (bounded, below) |
| `TRANSIENT` | a registry or toolchain would not answer | refunded (bounded, below) |
| `NO_CALLABLE_SYMBOL` | measured: no symbol or project a contract could call exists here | counts the writer as one independent measurement, and takes that writer off the coordinate |
| `NO_OUTPUT` | gave up, cannot say which of the above | nothing beyond the handout it closes |

`HANDED_OUT` and `AUTHORED` are refused from a client. A writer that could
report them could mark a coordinate solved without writing anything.

## The thresholds

| Constant | Value | Why this number |
| --- | --- | --- |
| `AuthoringAttemptDebounce` | 5 min | Polling is not attempting. `csx sample-worker next` is a one-shot command an agent runs; asking twice in a minute is not two tries, and counting it as two would withhold a coordinate nobody worked on. Shorter than any real attempt, longer than any poll loop. |
| `AuthoringMaxSessionHandouts` | 3 | How many times ONE writer may be handed the same coordinate before it is moved on. This is the bound the 22-attempt incident needed. Three separate stretches of work is enough for a writer to have said what it knows, and small enough that being wrong costs one worker-hour rather than four. |
| `AuthoringNoOutputQuarantine` | 6 | Exactly two writers' worth. With the per-writer bound above, six unexcused attempts cannot be reached by one machine — which is the point: one writer failing is one writer's opinion. |
| `AuthoringNoSymbolQuarantine` | 2 distinct writers | Same principle at a far lower count: this outcome is a measurement of the artifact, not a report about the attempt, so it does not need six tries to be believed. It still needs two, because one writer's report is one writer's opinion. |
| `AuthoringExcusedAttempts` | 4 | Excusing has to be bounded or a writer looping on one excuse holds the network's attention forever. More than any real outage needs on a single coordinate, far fewer than a loop produces. |
| `AuthoringQuarantineCooldown` | 30 days | A withholding that never lapses is a deletion with better manners. Repeated no output is an inference about attempts, and what it most plausibly reflects — a broken image, a broken toolchain, a registry having a week — heals. |
| `AuthoringHistoryDepth` | 10 attempts | An operator needs the last few attempts to judge a withholding. Nobody needs the two hundredth. |

**Against the incident:** with these numbers the 22-attempt coordinate would
have been taken off that worker after 3 attempts (≥15 minutes rather than 4
hours), and off the board entirely after the second worker reached the same
place — or after the first `--outcome no-callable-symbol` report from each of
two writers, which is minutes.

**These numbers are the part most worth arguing with**, and they are safe to
argue with after the fact: nothing here deletes, every withholding is listed
with its reason and evidence, and one click puts the work back.

## What lapses and what does not

* `no callable symbol` — `reopensAt` is null. An artifact does not grow a jar
  later, so only an operator lifts it.
* `repeated no output` — `reopensAt` is 30 days out, and the coordinate is
  offered again by itself.

Reopening — by the operator or by the timer — resets `noOutput`, `excused`,
the per-writer handout counts and the impossible-measurement set, and keeps
`attempts`, `authored` and the history. A coordinate that genuinely cannot be
authored simply earns its withholding again; nothing is lost by being wrong.

## Where it shows

* `GET /admin/api/withheld-work` and the **보류된 좌표** list in the farm panel:
  coordinate, reason, age, attempt counts, the last few attempts with the
  writers' own notes, and whether it needs an operator.
* `POST /admin/api/withheld-work/reopen` and the **다시 배포** button.
* `GET /admin/api/farm` → `health.withheldCoordinates` and
  `health.withheldByReason`, read from the same predicate the picker uses.
  A withdrawn **sample** and a withheld **coordinate** are different acts — one
  takes back an answer, the other stops asking the question — so they are
  counted apart rather than pooled into one alarming number.

## What this does not do

* It does not replace the by-name rules for shapes that are provably
  unauthorable (`mavenPomOnlyByName`, `npmPackagePlatform`, the pom-only
  registry probe). Those refuse before a worker is ever sent, which is
  cheaper than learning it again; this ledger is the net underneath them.
* It does not withhold anything from the public `/wanted` board. A quarantined
  coordinate is still something people asked for; it is withheld from
  *authoring*, not from the record of demand.
