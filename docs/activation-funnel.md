# Activation: what can be measured between install and first proven value

CodeSampleX records misses, hits, adoptions, evidence and network activity.
It did not record whether an install ever worked: nothing in this repository
said whether the binary had run, whether `csx init` finished, whether the MCP
path was live, or how long it took to reach a first useful answer.

Those five are now stamped, locally, by §7. Everything about **why they are
local and stay local** is unchanged, and is the rest of this document.

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
| S1 | binary first run | `stat:firstRunAt`, written at the top of `cli.Main` (§7) | none, and none is possible: this precedes the mode choice |
| S2 | `csx init` complete | `config.json` `mode` is no longer `""` (`internal/config`), and `stat:initAt` records when (§7) | none, and none is possible: the mode choice is the consent itself |
| S3 | first sync complete | `shards` rows in `csx.db`; `SyncResult.WarmedKeys` from `csx sync`; `stat:firstSyncAt` is the time of the first warm that succeeded (§7) | `GET /v1/shards/*` is a `meaningfulRoute`, so it produces an IP-derived activity bucket (`internal/activity`) and a `csx_route shards` edge-log line. Both are network-level, not install-level |
| S4 | MCP connected | `stat:mcpFirstReadyAt` / `stat:mcpLastReadyAt`, written on the protocol-lifecycle transition and never at startup (§7) | none: MCP is a stdio transport with no request of its own. MCP-originated searches and evidence are indistinguishable from CLI ones |
| S5 | hook active / approval pending | `csx hook status` — six states, per agent, bound to a definition fingerprint (`internal/cli/hookready.go`, R2C-18) | none, and none is wanted (§3) |
| S6 | first hit | `hits` table in `csx.db`, with `ts`; `stat:firstHitAt` is the first row's own `ts` (§7) | `search_hits(epoch, anon_id, …)` — deduplicated per reporter per offer per UTC day (`0020_search_hits.sql`) |
| S7 | first adoption report | `hits.adopted` / `hits.post_build_pass`; `stat:firstAdoptionAt` on the first report that said applied (§7) | `adoptions(sample_id, epoch, anon_id, …)` (`0005_adoptions.sql`) |

Two stages sit alongside rather than inside the sequence, and both are
already measured:

* **evidence filed** — `evidence_dedup(bucket_kind='peer', bucket, epoch)`,
  where `bucket` is the daily `anonId` (`internal/serverstore/pg.go`). This is
  what `/v1/stats` publishes as `peers`.
* **searched and found nothing** — `search_misses(epoch, anon_id, dedup_key)`
  (`0023_search_misses.sql`), one row per question per reporter per day.

### What the table says

When this document was written, S1, S2, S4 and S5 had **no signal at all**,
locally or on the network, and S3 had a local fact with no timestamp: the four
stages where setup actually loses people were exactly the four with nothing
recorded. S6 and S7 were measured on both sides. The gap was never at the far
end.

S1, S2, S3, S4, S6 and S7 now carry a local first-occurrence stamp (§7). S5
keeps the answer it already had — `csx hook status` — and gains nothing,
because hook state is §2.3 never-collected and a stamp would be the start of
collecting it. Nothing in this change adds a field to the wire; the whole
ledger stays in `$CSX_HOME`.

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
knows it initialized, knows whether it synced, knows whether an MCP client
completed the protocol handshake, knows its hook state, and knows whether it
has ever had a hit.
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

**Transport retries and repeated searches are different events.** A
`search_hits` row is keyed `(epoch, anon_id, dedup_key)`, with the offer ID as
its dedup key. Re-delivery of the same queued hit payload carries the same
offer ID and collapses as a transport retry. A later successful search — even
with the same query and result — calls `RecordSearchOffer` again, receives a
new random offer ID, and is a distinct hit. Hit-volume metrics may therefore
count successful searches, but must not describe the deduplication as merging
repeated searches.

Misses have a different limitation: `search_misses` keys the digest of the
whole coordinate set. A same-day repeat of the identical unsuccessful search
collapses whether it was a transport retry or a deliberate re-search, while a
report naming ten packages remains one question rather than ten. A miss row
count is therefore a count of distinct daily coordinate sets per reporter,
not a count of search attempts. `adoptions` is keyed
`(sample_id, epoch, anon_id)`, and `evidence_dedup` is keyed by bucket and
epoch. Any rollup must name these actual deduplicated units rather than claim
one universal retry rule.

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

## 7. The local readiness view — shipped, minus the hook rows

A panel at the top of `csx ui`, and the same rows as text in `csx stats`.
One row per stage, each carrying its state, where the state was read from,
and the exact next action when it is not ready.

What `csx stats` prints today on a machine that has initialized and had one
answer but never adopted anything:

```
Readiness                      (local only — nothing here is uploaded)
  First run                    2026-08-20T08:59:00Z  (csx.db)
  Initialized                  2026-08-20T09:00:00Z  (config.json)
  Shard cache warmed           2026-08-20T09:01:00Z  (csx.db)
  MCP handshake                —  never  → restart your coding agent, then use a csx tool
  First answer                 2026-08-20T11:00:00Z  (csx.db)
  First adoption               —  never  → report_sample_adoption after using a sample
  Time to first answer         2h0m0s after csx init
```

The per-agent hook rows are **not** built. `csx hook status` already answers
S5 with six states per agent, and folding it in is a rendering job, not a
measurement one — it is the one part of this panel that has no missing signal
behind it.

Rules the panel obeys, inherited from `csx hook status` because they were
right there:

* It may only say what it can show. `registered` is a fact about a config
  file and is never a claim that anything ran; `verified` requires a smoke
  check that actually exercised the registered command.
* Where a state is not readable, it says so in those words —
  `approval not verifiable` — and never guesses in either direction.
* `never seen` is distinct from `not working`. An MCP row that has never seen
  a completed handshake on a machine whose agent is registered means only
  that no client has completed the protocol lifecycle; process startup alone
  is not readiness, and the path is not inferred broken.
* Every not-ready row carries the command that fixes it.

Backing store: first-occurrence stamps in the local `meta` table
(`internal/storage/localdb`, `stat:` namespace) — `firstRunAt`, `initAt`,
`firstSyncAt`, `mcpFirstReadyAt`, `mcpLastReadyAt`, `firstHitAt`,
`firstAdoptionAt`. Write-once for the `first*` keys. Existing success sites
cover `csx init` after `cfg.Save`, the sync path after a successful warm, the
hit writer, and the adoption writer.

Implemented in `internal/storage/localdb/activation.go`. `StampFirst` is a
single `ON CONFLICT DO NOTHING` insert rather than a read-then-write, because
the daemon, an MCP server and the CLI share this database on purpose and two
of them reaching a stage in the same second must not race the earlier stamp
away. The write sites are `cli.Main`, `csx init` after `cfg.Save`,
`Daemon.SyncNow` when a warm actually succeeded, `RecordSearchOffer` (from the
hit row's own `ts`, so a backdated hit does not claim a first answer at the
moment it was written), and `CorrelateInterventionAdoption` when the report
said applied — an explicit "I did not use this" is a completed report and not
an adoption. Every stamp is best effort: a ledger that cannot be written must
never be the reason a command that worked stops working.

The MCP stamps do **not** belong in `newDeps`: opening the database proves
only process startup. A session becomes ready only after the same stdio
session has successfully answered a valid `initialize` request and then
received `notifications/initialized`. `mcpFirstReadyAt` and `mcpLastReadyAt`
are written on that state transition; launching `csx mcp`, closing stdin, or
failing between those two protocol messages records no ready session.

`firstRunAt` (S1) must be attempted at the top of `cli.Main`, before it
inspects or dispatches `argv`, so the no-argument, `help`, `--help`, `version`,
and unknown-command early returns are real first executions too. Recording it
from `config.EnsureHome` would instead mean "first command that needed the
home" and would understate time to value. This write remains local-only and a
failure to create the local ledger must remain visible as unmeasured rather
than being replaced with a later timestamp.

This panel is what makes S1–S5 answerable at all. It is also the entire
answer for those stages: they are §2.1 signals and they do not have a network
form.

That last sentence is held by a test rather than by this paragraph:
`TestNoActivationStampReachesTheWire` sets every stamp to an instant no other
field could produce, queues real evidence, runs a community sync against a
server that keeps every byte, and fails if any request body carries a stamp
key or its value.

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

1. **Shipped.** The local activation ledger and the readiness panel (§7), in
   `csx stats` and at the top of `csx ui`. It is the only thing that makes
   S1–S5 answerable, it is local-only, and it is the answer a user and a
   support conversation actually need. The per-agent hook rows are not built:
   S5 already has `csx hook status` behind it, so that part is rendering
   rather than measurement.
2. The admin daily stage-presence rollup (§8), from existing tables. **Not
   built.**
3. The naming-rule guard (§6). Shipped with this document.
4. The `csx_route install` edge-log label (§8), as a measured count that is
   never divided by anything. **Not built** — `/install.sh` and `/install.ps1`
   are still `log_skip`'d.

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
