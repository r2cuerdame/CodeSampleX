# Activation: what can be measured between install and first proven value

CodeSampleX records misses, hits, adoptions, evidence and network activity.
It does not record whether an install ever worked. Nothing in this repository
says how many binaries were run, how many `csx init` runs finished, whether
the MCP path is live, or how long it takes to reach a first useful answer.

This document is the contract for that gap. It states, stage by stage, what
signal exists today, what could exist without changing what leaves the
machine, what may only ever be local, and what must never be collected at
all. It also fixes the naming rules that keep a count of records from being
read as a count of people, because the counters this product already
publishes are the ones most likely to be misread that way.

Two structural facts drive everything below, and they are not obstacles to
route around — they are the design:

1. **The early stages happen before consent exists.** Downloading a binary
   and running it for the first time both precede `csx init`, which is where
   the user chooses community or local-only. An install that reports its own
   first run has transmitted before anyone agreed to transmission, and an
   install that later chooses `local-only` has transmitted in the mode whose
   entire promise is that it does not
   ([PRIVACY.md](../PRIVACY.md) §2). So the top of the funnel is not merely
   unmeasured; it is unmeasurable server-side without breaking the mode
   contract.
2. **The anonymous id rotates daily, on purpose.** `anonId` is
   `HMAC(seed, "anon|" + day)` (`internal/identity`), so two uploads a day
   apart are not linkable. A funnel is a claim about one install moving
   through stages over time. Nothing the server holds can express that, and
   nothing should be added to make it, unless the fork in §9 is decided the
   other way.

The honest consequence: **the funnel is local. What the server can see is
same-day stage presence, not conversion.** The rest of this document is that
distinction, applied.

---

## 1. The stages, and what each one can be read from

`Local signal` is what the machine itself can know. `Network signal` is what
the server can see today, without any new field on the wire.

| # | Stage | Local signal today | Network signal today |
|---|---|---|---|
| S0 | download | — | GitHub release asset counter (public, already badged in [README](../README.md)); `GET /install.sh` and `/install.ps1` are served by the edge but **not** logged — both paths fall outside the `csx_route` allowlist in [`deploy/caddy/Caddyfile`](../deploy/caddy/Caddyfile) and are `log_skip`'d |
| S1 | binary first run | **none** — nothing in `$CSX_HOME` records a first execution | none, and none is possible: this precedes the mode choice |
| S2 | `csx init` complete | `config.json` `mode` is no longer `""` (`internal/config`) | none, and none is possible: the mode choice is the consent itself |
| S3 | first sync complete | `shards` rows in `csx.db`; `SyncResult.WarmedKeys` from `csx sync` — neither is stamped with a time | `GET /v1/shards/*` is a `meaningfulRoute`, so it produces an IP-derived activity bucket (`internal/activity`) and a `csx_route shards` edge-log line. Both are network-level, not install-level |
| S4 | MCP connected | **none** — `csx mcp` opens the local store at startup (`internal/cli/mcp.go` → `mcp.NewDeps`) and writes no mark | none: MCP is a stdio transport with no request of its own. MCP-originated searches and evidence are indistinguishable from CLI ones |
| S5 | hook active / approval pending | `csx hook status` — six states, per agent, bound to a definition fingerprint (`internal/cli/hookready.go`, R2C-18) | none, and none is wanted (§3) |
| S6 | first hit | `hits` table in `csx.db`, with `ts` | `search_hits(epoch, anon_id, …)` — deduplicated per reporter per offer per UTC day (`0020_search_hits.sql`) |
| S7 | first adoption report | `hits.adopted` / `hits.post_build_pass` | `adoptions(sample_id, epoch, anon_id, …)` (`0005_adoptions.sql`) |

Two stages sit alongside rather than inside the sequence, and both are
already measured:

* **evidence filed** — `evidence_dedup(bucket_kind='peer', bucket, epoch)`,
  where `bucket` is the daily `anonId` (`internal/serverstore/pg.go`). This is
  what `/v1/stats` publishes as `peers`.
* **searched and found nothing** — `search_misses(epoch, anon_id, dedup_key)`
  (`0023_search_misses.sql`), one row per question per reporter per day.

### What the table says

S1, S2, S4 and S5 have **no signal at all**, locally or on the network. S3
has a local fact with no timestamp. So the four stages where setup actually
loses people are exactly the four with nothing recorded, and three of them
(S1, S2, S4) can be fixed with a local stamp that costs nothing and
transmits nothing.

S6 and S7 are measured on both sides. The gap is not at the far end.

---

## 2. Three classifications, and the rule for each

Every activation signal falls into exactly one of these. The classification
is a property of the signal, not of the mode it happens to be observed in.

### 2.1 Local status — never transmitted, in any mode

Facts about this machine's setup. They answer "is my install working", which
is the user's question and support's question, and they answer it without
anyone else needing to know.

* first run, init completion, first sync completion, first MCP session
* hook registration state, its definition fingerprint, and the config path it
  was read from
* which coding agents are installed, and which of them accepted registration
* elapsed time from any of these to any other

These stay in `$CSX_HOME`. The `csx ui` privacy preview renders the exact
pending upload payloads, so a reader can confirm by inspection that none of
this is in them.

### 2.2 Aggregatable in community mode — already transmitted

Signals that already leave the machine under the §4 contract in
[PRIVACY.md](../PRIVACY.md), and that a rollup may therefore read without any
new collection:

* observation evidence (`evidence_dedup`, keyed by daily `anonId` and monthly
  `projectBucket`)
* search hits (`search_hits`) and search misses (`search_misses`)
* adoption reports (`adoptions`)

An activation rollup built only from these adds **zero** to what is
collected. It is a query over data already held, and that is the whole reason
§8 recommends it.

### 2.3 Never — regardless of mode or consent

* Project identity, path, name, or the prose of any query. Already forbidden
  and already enforced; activation is not a reason to reopen it.
* Which agent tooling the user runs, and the fingerprints of their agent
  config. A hook-readiness upload would be a durable statement about a
  developer's private toolchain, sent because they wanted a compatibility
  answer. It stays local (§2.1).
* Any pre-consent signal (S0 download attribution, S1, S2). Transmitting
  these is transmitting before the mode question has been asked.
* Any identifier that links one install's activity across UTC days, unless
  §9 is decided otherwise and [PRIVACY.md](../PRIVACY.md) is amended in the
  same change.

---

## 3. Why "where setup loses users" has two different answers

The question has a per-machine answer and a fleet answer, and they are not
the same question.

**Per machine**, the sequence is fully knowable. The install knows it ran,
knows it initialized, knows whether it synced, knows whether an MCP process
ever started, knows its hook state, and knows whether it has ever had a hit.
It can present all of that, in order, with the exact next action for the
first row that is not ready. That is §7.

**Across the fleet**, the sequence is not knowable and should not be made
knowable cheaply. What is knowable is which stages were *present* on the same
UTC day, across the anonymous buckets that reported anything at all — and
that population excludes, by construction, every install that stalled before
it ever transmitted. A rollup that divides adopters by evidence reporters is
not measuring activation; it is measuring the shape of the population that
already activated far enough to appear.

So the fleet answer is bounded to this, and must be labelled as it:

> Of the anonymous reporter buckets seen on day *d*, how many appear in
> evidence, in search, and in adoption — and in which combinations.

That is a **stage presence** figure. It is not a conversion rate, it has no
denominator of installs, and it says nothing about anyone who never reached
day *d*'s wire at all.

---

## 4. Retries, reinstalls and multiple machines

The ticket asks how these avoid inflating the funnel. Each has a different
answer, and one of them is "it doesn't, and the number must say so".

**Retries.** Already handled on every uploaded surface. `search_hits` is keyed
`(epoch, anon_id, dedup_key)` where the dedup key is the offer id, so a
search retried all afternoon is one hit. `search_misses` keys the digest of
the whole coordinate set, so a retried miss collapses the same way — and a
report naming ten packages is one question, not ten. `adoptions` is keyed
`(sample_id, epoch, anon_id)`. `evidence_dedup` is keyed by bucket and epoch.
Nothing new is needed for retries.

**Reinstalls.** A reinstall that keeps `$CSX_HOME` keeps its local ledger and
its `identity.json` seed, so it is continuous on both sides. A reinstall that
wipes it starts over: new seed, new buckets, a fresh local ledger. That is
not a defect to correct. The only way to recognise the returning machine is
to derive an identity from the machine, and `identity.json` is deliberately
random rather than machine-derived ([PRIVACY.md](../PRIVACY.md) §5). A
reinstall therefore counts as a new participant, and any rollup that could be
read as an install count must carry that caveat rather than silently absorb
it.

**Multiple machines.** Two machines are two seeds and two bucket streams, and
nothing links them. One person with a laptop, a desktop and a CI runner is
three. This is why *users* is not a unit this product has, and why §6 forbids
the word outright.

**CI.** The environment fingerprint carries a `ci` flag, described in
[`schemas/v1/environment.json`](../schemas/v1/environment.json) as: "Automated
runner. CI fleets are clones, so they must not be counted as many independent
developer environments." An activation rollup must exclude CI-flagged
evidence or report it separately; a fleet of identical runners is the single
largest available way to inflate any of these figures.

---

## 5. Time to first useful answer

The elapsed time from S2 to S6 is the one duration worth having, and it is
computable **only locally**: both endpoints are on the machine, and the
server holds neither in a form that survives a day boundary.

It is a local number, rendered locally, and it is not uploaded. Uploading a
duration would mean uploading a pair of timestamps or their difference, which
is a cross-day fact about one install — precisely the linkability §9 exists
to decide about.

---

## 6. Naming rules: measured, estimated, unmeasured

A count of records read as a count of people is the specific failure this
document exists to prevent. These rules are how a field name is prevented
from making a claim the data does not support. They apply to every field of
the two published stats documents — `compatibility.StatsDoc` (public
`GET /v1/stats`) and `daemon.Stats` (local `GET /local/v1/stats` and
`csx stats --json`) — and to every surface that renders them.

**Measured.** A count of records the store actually holds. It is named for
the record it counts, never for the actor behind it: `evidence`,
`searchHits`, `postHitBuildsReported`. `evidence` is observation records; the
struct comment already says a big number there means "widely used" and never
"widely verified", and that is the pattern.

**Estimated.** A figure derived by formula from measured inputs. It carries
the `estimated` prefix, it carries `estimated: true`, and it carries its
formula and its assumptions. `EstimatedStat` in `internal/compatibility`
exists for exactly this and its `Estimated` field is always true. A struct
holding an `estimated*` field must also carry an `estimated` flag, so a
consumer that reads only the top level still cannot present it as measured.
`csx stats` prints the suffix `(Estimated — never measured)` for the same
reason.

**Unmeasured.** A thing we have decided not to measure, or cannot. It renders
as `—` with a note, never as `0`. `PlaceholderStat` carries the note; the
homepage renders an em dash rather than "0%" when nothing has been collected;
the admin flow KPIs render `표본 없음` for an empty window
([docs/operations.md](operations.md)). A zero is a measurement. A gap is not,
and the two must never look alike.

**Bucket nouns.** `peers` and `projectsMonth` are counts of rotating
anonymous buckets, not of people or machines. `peers` is today's distinct
daily buckets and therefore genuinely cannot be summed over time — an install
active all month appears as thirty. `projectsMonth` rotates monthly and is
the only participation figure that does not reset at midnight. Both are
allowed, both are terms of art defined in the README and PRIVACY, and both
must be rendered with their unit visible.

**Forbidden.** No field on either document may be named for a person, a
machine, an install or a session: `users`, `activeUsers`, `dau`, `mau`,
`installs`, `devices`, `machines`, `people`, `accounts`, `seats`,
`subscribers`, `visitors`. Not because the words are imprecise, but because
none of them has a defensible method behind it here — and a name is a claim
that survives every caveat placed next to it. The README states this in
plain text today: CodeSampleX does not yet measure reliable unique/active
users, live MCP processes, or successful installs. That sentence stays true
until a method exists, not until a number does.

These rules are held by a test rather than by this paragraph.
`internal/metricname` is the rule as code, and
`internal/compatibility/statsnaming_test.go` and
`internal/daemon/statsnaming_test.go` apply it to the two documents. A new
bucket noun has to be added to the allowlist deliberately, next to the
comment saying what it counts.

---

## 7. Proposal: the local readiness view

A panel at the top of `csx ui`, and the same rows as text in `csx stats`.
One row per stage, each carrying its state, where the state was read from,
and the exact next action when it is not ready.

```
Readiness                                        (nothing on this panel is uploaded)
  Mode            COMMUNITY                       config.json
  Shard cache     807 keys · synced 2h ago        csx.db
  MCP             registered: Claude Code, Codex  last session 4m ago
  Hook            Claude Code  verified 2026-08-24
                  Codex        registered — approval not verifiable
                                               → run /hooks in Codex and trust the lookup
  First answer    2026-08-20 (2h after init)
  First adoption  — never reported               → report_sample_adoption after using a sample
```

Rules the panel obeys, inherited from `csx hook status` because they were
right there:

* It may only say what it can show. `registered` is a fact about a config
  file and is never a claim that anything ran; `verified` requires a smoke
  check that actually exercised the registered command.
* Where a state is not readable, it says so in those words —
  `approval not verifiable` — and never guesses in either direction.
* `never seen` is distinct from `not working`. An MCP row that has never seen
  a session on a machine whose agent is registered means the agent has not
  been started, not that the path is broken.
* Every not-ready row carries the command that fixes it.

Backing store: first-occurrence stamps in the local `meta` table
(`internal/storage/localdb`, `stat:` namespace) — `firstRunAt`, `initAt`,
`firstSyncAt`, `mcpFirstSeenAt`, `mcpLastSeenAt`, `firstHitAt`,
`firstAdoptionAt`. Write-once for the `first*` keys. Each is one line at a
site that already opens the store: `csx init` after `cfg.Save`, `csx mcp` at
`newDeps`, the sync path after a successful warm, the hit writer, the
adoption writer.

`firstRunAt` (S1) is the only one with no existing site, because there is no
first-run path — the CLI opens the store lazily. It belongs wherever
`config.EnsureHome` first creates the directory tree, which is the earliest
moment the binary provably ran.

This panel is what makes S1–S5 answerable at all. It is also the entire
answer for those stages: they are §2.1 signals and they do not have a network
form.

---

## 8. Proposal: the minimal server rollup

Admin-side, not public. The public `/v1/stats` document is a coverage and
evidence rollup and the homepage deliberately renders three tiles
(`internal/web/landing_honesty_test.go`); activation health is an operator
question and belongs on 운영 요약 next to the flow KPIs it resembles.

One daily row, computed from tables that already exist, with no new field on
the wire and no new collection:

| Field | Query | Unit |
|---|---|---|
| `reportersEvidence` | `COUNT(DISTINCT bucket)` from `evidence_dedup` where `bucket_kind='peer'` and `epoch = d` | daily anonymous buckets |
| `reportersSearch` | `COUNT(DISTINCT anon_id)` over `search_hits ∪ search_misses` at `epoch = d` | daily anonymous buckets |
| `reportersAdoption` | `COUNT(DISTINCT anon_id)` from `adoptions` at `epoch = d` | daily anonymous buckets |
| `reportersSearchWithEvidence` | the intersection of the first two | daily anonymous buckets |
| `reportersSearchWithAdoption` | the intersection of the second and third | daily anonymous buckets |

The join key is the same `anonId` on all three sides, which is already the
dedup key on each table. Computing the intersection reads what is stored and
stores nothing further; the output is five integers.

Every one of them is rendered with the same sentence attached: *same-day
anonymous reporter buckets — not users, not installs, and not a conversion
rate. An install that never transmitted appears in none of these.* Under the
§6 rules these are measured counts of buckets, so they are named
`reporters*`, and none of them may be divided by anything to produce a
percentage labelled activation.

`reportersEvidence` is numerically the same query as `/v1/stats.peers`. It is
restated here under a name that says what it is, because `peers` next to
`reportersAdoption` invites exactly the reading this document is trying to
prevent.

**Excluded from the rollup:** CI-flagged evidence, counted separately (§4).

**Two additional measured counts, kept apart and never divided:**

* GitHub release asset downloads — already public, counts mirrors, CI and
  re-downloads, and is not an install count.
* `GET /install.sh` and `/install.ps1` request counts — **not collected
  today**. Adding `csx_route install` to the Caddy allowlist would collect a
  route label, a status and a coarse method bucket, with `remote_ip` already
  dropped and the whole request object deleted before bytes reach disk. That
  is the smallest defensible top-of-funnel signal available, it carries no
  identity, and it is still not an install count.

The two are in different units and describe different populations. A ratio
between either of them and `reportersEvidence` would be a fabricated
conversion rate; the rollup renders them as counts, side by side, and never
as a ratio.

---

## 9. What is worth implementing now

**Now — no privacy change, no new field on the wire:**

1. The local activation ledger and the `csx ui` readiness panel (§7). It is
   the only thing that makes S1–S5 answerable, it is local-only, and it is
   the answer a user and a support conversation actually need.
2. The admin daily stage-presence rollup (§8), from existing tables.
3. The naming-rule guard (§6). Shipped with this document.
4. The `csx_route install` edge-log label (§8), as a measured count that is
   never divided by anything.

**Not now — this is the fork:**

5. Any *new uploaded* activation event. Including a coarsened, one-shot
   "reached stage N" ping. It cannot cover S1 or S2 without collecting before
   the mode question has been asked, and it cannot produce a real funnel
   without an identifier that links one install across UTC days. Either is an
   amendment to [PRIVACY.md](../PRIVACY.md) §4 and §5 and to the contract
   printed in the README, and neither is an implementation detail.

**Never:**

6. Hook state, agent inventory, config paths, MCP client identity, project
   identity, or query text, in any aggregated or hashed form (§2.3).

Until item 5 is decided, the sentence the README already carries stays exactly
as it is: unique users, active users, live MCP processes and successful
installs are **not measured**, and no field on any surface may be named as
though they were.

---

## Related

* [PRIVACY.md](../PRIVACY.md) — §2 modes, §4 exactly what community mode
  transmits, §5 identifiers and what they can and cannot link.
* [docs/operations.md](operations.md) — the flow KPIs, the windowing rule,
  and `표본 없음` for an empty window.
* [docs/schema.md](schema.md) — evidence quality, and the rule that a legacy
  row is never promoted to a modern claim.
* `internal/cli/hookready.go` — the readiness vocabulary §7 reuses.
