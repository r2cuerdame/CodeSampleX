# #149 — What one hard coordinate costs the farm, measured and bounded

*The production measurement below was captured read-only on 2026-09-23 before
the policy changed. Its committed JSON therefore labels the former 8.3-hour
rule as current-at-capture. The 2026-09-24 decision record and implementation
at the end of this document use that measurement; they do not rewrite the
historical observation.*

## Source and method

- **Input.** Every row of production `authoring_attempts` (5,609 coordinates)
  and `authoring_sessions` (189 writer sessions), dumped with `SELECT`-only
  `COPY` over the documented psql path at 2026-09-23 06:53 UTC. Dump digests:
  ledger `sha256:5284a550de9c141348b1636eafb4a583b4dc329f1c9f3ef34c1c973ef3cefa64`,
  sessions `sha256:0fa99bea71db752eb4e341ac58e5f7edba4913f62d03878d5b3dbba42028ee80`.
- **Window.** Ledger history from 2026-08-23 04:20 UTC to 2026-09-21 13:22 UTC
  (13,160 events). The last ledger write is 2026-09-21 13:22; nothing newer
  existed.
- **Tool.** `csx-server authoring-budget-report --ledger F --sessions F`
  (`internal/serverstore/authoring_budget_report.go`). It reads files only,
  never the database, and the full output is
  [`issue-149-authoring-budget-report.json`](issue-149-authoring-budget-report.json).
  The dump SQL is in [`../authoring-quarantine.md`](../authoring-quarantine.md#measuring-the-authoring-budget-149).
- **Episode.** The handouts of one coordinate on one axis up to and including
  the one whose writer authored a draft. Episodes are rebuilt from the bounded
  history (10 entries per coordinate). 50 rows had older handouts outside the
  window; those count exactly when no success fell out with them, and the 21
  episodes that cannot be counted exactly are left out of the
  attempts-to-success histogram.
- **Slot time.** An attempt's slot time is known only when the same writer's
  next event closes it: authored draft, a reported outcome, or its own next
  handout. It is capped at the 50-minute `agy --print-timeout`. An attempt
  closed by a *different* writer or never closed is charged 50 minutes as an
  upper bound. Every saving below is given as **observed** (known closes only)
  and **upper bound**.
- **What the ledger cannot see.** It records AUTHORED only for Sample drafts.
  Evidence and Dependency answers complete elsewhere, so the 257 episodes on
  those axes are reported but are **not** priced as failures.

## 1. Attempts to success

6,008 episodes: 5,553 authored, 101 withheld, 354 without a recorded end
(239 of them Evidence/Dependency).

| Handouts to success | Episodes | Share of 5,537 exact successes |
| ---: | ---: | ---: |
| 1 | 5,107 | 92.2 % |
| 2 | 325 | 5.9 % |
| 3 | 24 | 0.4 % |
| 4–6 | 45 | 0.8 % |
| 7–12 | 36 | 0.7 % |

p50 1, p90 1, p99 5, max 12.

By ecosystem (Sample-producing episodes):

| Ecosystem | Episodes | Authored | Withheld | First-try share | p99 handouts | Success slot-min p50 / p90 / p99 / max | Failed slot-min (upper) p50 / p90 / max |
| --- | ---: | ---: | ---: | ---: | ---: | --- | --- |
| npm | 3,199 | 2,974 | 29 | 94.5 % | 2 | 2.7 / 6.2 / 45.7 / 71.8 | 50 / 92.7 / 235.8 |
| golang | 2,202 | 2,045 | 39 | 90.8 % | 7 | 3.6 / 11.8 / 165 / 268.2 | 60.2 / 197.8 / 337.4 |
| pypi | 294 | 268 | 7 | 90.3 % | 11 | 3.0 / 10.8 / 48.8 / 56.8 | 50 / 116 / 182.7 |
| cargo | 283 | 255 | 9 | 80.8 % | 5 | 5.6 / 12.8 / 57.9 / 74.0 | 63.1 / 125.1 / 127.3 |
| maven | 20 | 11 | 8 | 54.5 % | 6 | 21.3 / 24.9 / 114.3 / 114.3 | 32.2 / 125.9 / 125.9 |
| pub | 10 | 0 | 9 | — | — | — | 89.1 / 128.3 / 133.8 |

By work type:

| Kind | Episodes | Authored | Withheld | First-try share | p99 handouts | Success slot-min p50 / p90 / p99 / max |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| EXPANSION | 3,451 | 3,311 | 21 | 93.0 % | 2 | 3.5 / 8.8 / 37.2 / 268.2 |
| FINDING | 1,225 | 1,213 | 9 | 98.7 % | 2 | 2.2 / 3.3 / 7.8 / 55.7 |
| WANTED | 1,097 | 1,029 | 56 | 82.3 % | 11 | 3.7 / 21.7 / 201.1 / 249.9 |
| DEPENDENCY | 235 | (not recorded by the ledger) | 15 | — | — | — |

The expensive tail is almost entirely **Go WANTED/EXPANSION coordinates in
the OpenTelemetry module family**: all ten of the costliest episodes are
`go.opentelemetry.io/...`, 7–12 handouts over 2–5 sessions, 122–250 observed
slot-minutes each (up to 381 upper bound). Eight of those ten did author in
the end.

## 2. Duration and timeouts

| Attempt ends in | Count |
| --- | ---: |
| AUTHORED (same writer) | 5,546 |
| same writer's next handout (implicit no output) | 918 |
| another writer's handout (lease moved on, slot time unobserved) | 319 |
| no close recorded | 351 |
| reported INFRASTRUCTURE / NO_CALLABLE_SYMBOL / TRANSIENT / UNSUPPORTED_ENVIRONMENT | 107 / 114 / 5 / 5 |

Duration of the attempts that authored (the throughput every option must
protect): **p50 3.0 min, p90 6.8, p99 21.8, max 50** (the cap). Authored
attempts longer than 10 / 15 / 20 / 30 / 45 minutes: 262 / 118 / 64 / 26 / 7
of 5,546.

**Timeouts are not terminal evidence, measured.** 775 attempts ended without a
sample after at least 45 minutes in the slot (or with an unobserved close
charged at 50). In **153** episodes such an attempt was later followed by a
success on the same coordinate. A timeout says the writer ran out of time, not
that the coordinate cannot be written.

Worst single episode in the window: **4.5 slot-hours observed, 6.4 upper
bound**. The rules allow (6 charged + 4 excused handouts) × 50 min =
**8.3 slot-hours** for one no-output coordinate. The issue's "about 5 hours"
estimate is inside what the data shows.

## 3. Independence: sessions, not machines

- Every session that appears in the ledger ran on **one machine,
  `csx-farm-linux-1`** (188 of 189 sessions; the other, labelled
  `ip-172-26-1-17`, appears in no ledger event). **All 299 episodes that reached a second
  writer reached a second *session of the same machine*.** Every one of the
  6,001 episodes with a handout in the window had exactly one peer.
- The "two independent writers" behind `AuthoringNoOutputQuarantine = 6`, and
  behind the permanent `NO_CALLABLE_SYMBOL` (44 rows withheld) and
  `UNSUPPORTED_ENVIRONMENT` (3 rows withheld) withholdings, have therefore always
  been two sessions of one machine running the same agent (`agy`).
- Even so, the second session is not useless. It is a fresh agent run with a
  fresh context. Successes by writer ordinal: 1st 5,379, **2nd 116, 3rd 19,
  4th 2**. That is 137 successes (2.5 %) that the first session did not
  produce. 53 of them came after one session had used its three handouts.
- Scheduler re-assignment across local slots: 5,773 episodes stayed on one
  slot, 192 moved across two, 36 across three. Handouts of one coordinate are
  exclusive (one `authoring_assignments` row per coordinate and axis), so no
  coordinate ever ran on two slots *at once*. The cost is sequential.

## 4. The options, priced on the same history

Each rule is replayed over the Sample episodes. "Lost" is a success that
happened but that the rule would have stopped first. Today's thresholds,
replayed over a history partly made under earlier rules (before #366's
per-writer refund bound) and operator reopenings, "lose" 34. That is the
replay's error bar, so every option is also given **beyond** it. Savings are
slot-minutes over the ~29-day window on episodes that never authored.

| Option | Lost | Lost beyond current | Saved, observed | Saved, upper | Worst episode (slot-h) |
| --- | ---: | ---: | ---: | ---: | ---: |
| current (6 charged, 3 per session) | 34 | — | 24 | 224 | 5.6 |
| **peer independence**: 3 charged per machine, park when every machine is exhausted | 76 | 42 | 1,607 | 4,710 | 3.1 |
| slot budget 30 min / episode | 165 | 131 | 2,370 | 5,632 | 1.3 |
| slot budget 60 | 76 | 44 | 1,665 | 4,627 | 1.8 |
| slot budget 90 | 50 | 25 | 1,119 | 2,436 | 2.3 |
| slot budget 120 | 38 | 15 | 810 | 1,802 | 2.8 |
| slot budget 180 | 26 | 4 | 488 | 988 | 3.8 |
| initial timeout 10 m, one escalation to 50 m | 0* | 0* | 1,266 | 4,857 | 5.2 |
| initial timeout 15 m, one escalation | 0* | 0* | 1,354 | 4,495 | 5.2 |
| initial timeout 20 m, one escalation | 0* | 0* | 1,202 | 3,892 | 5.3 |
| initial timeout 30 m, one escalation | 0* | 0* | 769 | 2,559 | 5.5 |
| timeout weighting (a timeout-like attempt counts 2) | 47 | 13 | 671 | 3,213 | 3.9 |

\* Assumes the long first attempt, cut short and run again at 50 minutes,
authors as it did the first time. The option is charged the wasted cut run
(timeout × the authored first attempts that ran longer). It is not charged a
failed rerun. At 15 minutes that is 118 of 5,546 authored attempts running
twice.

Reading the table:

- **Peer independence on a one-machine farm is effectively a 3-handout cap.**
  It prices almost exactly like a 60-minute slot budget: 42 successes lost
  beyond today (among them `npm:plist@3.1.0`, `npm:postcss@8.5.26` and
  `npm:bare-os@3.9.3`). It only means "independent" once a second machine writes.
- **A slot-minute ceiling is the only lever that directly bounds the worst
  case.** 120–180 minutes keeps worst-case cost at 2.8–3.8 slot-hours, down
  from 5.6, and loses 4–15 successes beyond today, most of them the Go
  OpenTelemetry tail.
- **A shorter first timeout is the cheapest lever for throughput, but it does
  not bound the worst case.** It leaves the worst episode near 5.2 h, because
  every later attempt still runs at 50 m.
- **Timeout weighting** is dominated. It loses more than a 120-minute budget
  and bounds the worst case less.

## 5. Source decision and implementation (2026-09-24)

Source decision: Luna `DLG-20260924-004`, represented by
[LoopOffice #349](https://github.com/r2cuerdame/LoopOffice/issues/349), and
Chief directive `CD-159a8662c75ed0b9d973`. Worker job: `CodeSampleX#149`.
The representative decision requires a measured per-coordinate time ceiling
and requeue, with no paid capacity or new spend. The four decisions requested
by the earlier worker are recorded individually:

1. **Independence semantics — evidence/provenance, decided.** An independent
   writer for `NO_CALLABLE_SYMBOL` and `UNSUPPORTED_ENVIRONMENT` is a distinct
   machine/peer identity (`computer_name`, with the pre-field `-slotN` label
   fallback), not a session. Multiple sessions on one machine count once.
   Session diversity remains useful for non-terminal authoring attempts: it
   rescued 137 measured successes, so it is not removed.
2. **Episode budget — operational implementation, decided.** Stop dispatching
   new attempts once the episode has charged 120 slot-minutes. Because a turn
   admitted just below that line can use the existing 50-minute print timeout,
   the strict coordinate ceiling is **170 slot-minutes (2h50m)**. Fixture
   coverage exercises the 169-minute boundary and proves the next handout is
   refused. This chooses the measured 120-minute option: p50/p90 successful
   attempts are 3.0/6.8 minutes, and replay loses 15 successes beyond the old
   policy error versus 25 at 90 minutes. The 180-minute option preserves 11
   more replayed successes but permits another hour of tail cost.
3. **First timeout / escalation — operational implementation, decided.** Keep
   `agy --print-timeout 50m` for every turn. The apparent zero-loss result for
   a 15–20 minute first timeout assumes a cut attempt succeeds when rerun; 118
   real successful attempts exceeded 15 minutes. The cumulative budget bounds
   the tail without relying on that assumption.
4. **Rollout timing — release operation, decided.** Ship through the normal
   CodeSampleX release/deploy path after merge; do not add Farm capacity or
   spend. After the deployed Farm produces post-change ledger events, rerun
   the report and require worst-episode cost at or below 2h50m with successful
   attempt p50/p90 not regressing from 3.0/6.8 minutes.

Budget exhaustion uses the same visible, reversible 30-day cooldown mechanism
as repeated no output, but has its own reason: `slot budget exhausted: deferred
for a later episode; not terminal evidence`. It never increments peer terminal
measurements and never labels a timeout/no-output as unsupported. On cooldown
expiry the coordinate is requeued with a fresh episode budget; attempts and
bounded history remain for audit.
