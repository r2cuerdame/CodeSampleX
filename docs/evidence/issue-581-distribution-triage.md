# Issue #581: active-installation decline triage

Checked 2026-10-15 UTC. This is an evidence record, not a distribution
campaign or a claim that the target was met.

## What the available records establish

- [Issue #581](https://github.com/r2cuerdame/CodeSampleX/issues/581) reports
  `installs.active_1d` recent-window mean 2.52 versus prior-window mean 5,
  from eight daily readings, last observed 2026-10-15T00:18:10Z. It provides
  neither the individual dated readings nor their source-level channel data.
  The source and owner/test population filter are marked unknown.
- A read of the existing public [`GET /v1/stats`](https://codesamplex.dev/v1/stats)
  returned `generatedAt=2026-10-15T01:23:44Z`,
  `activeInstallations1d=2`, `activeInstallations7d=2`, and
  `activeInstallations30d=3`. This is one snapshot, not another daily series.
  The [producer](../../internal/serverstore/pg.go) counts distinct anonymous
  presence tokens in aligned UTC epochs for `ordinary` or `external` clients;
  the [daemon](../../internal/daemon/presence.go) reports at most once per day
  and silently tolerates reporting failures. These are neither successful
  installs nor identified people. The public rollup has no acquisition channel.
- [PR #566](https://github.com/r2cuerdame/CodeSampleX/pull/566) merged the
  zero-spend README first-use links on 2026-10-12T16:37:25Z. The repository's
  [release history](https://github.com/r2cuerdame/CodeSampleX/releases) shows
  multiple releases across October 9–14. Neither record says which anonymous
  installation first found the project through which route, or supplies a
  matched daily series of channel exposure and presence. Temporal overlap
  therefore cannot identify a cause or show whether the README change helped.
- The existing [activation contract](../activation-funnel.md) says installer
  requests are excluded from edge logs, pre-consent first run is local, and
  the anonymous identity rotates by day. The older
  [distribution dossier](../distribution.md) also marks its directory survey
  as dated; its August listing state is not evidence of a change in this
  October window.

## Measurement gap and smallest zero-spend observation

The missing evidence is the dated eight-reading series, a dated record of
changes to each distribution surface in the same interval, and a
source-measured acquisition signal. Existing `codesamplex-stats` readings
cannot recover an acquisition channel or separate owner/test installations.
No channel-specific intervention or explanation of the 49.5% decline is
supported by these records.

For the next decision, retain each already collected daily
`installs.active_1d` reading with its observation timestamp and source, and
record the time and target surface of any actual distribution edit beside it.
Cross-check against existing GitHub PR, release, and Actions timestamps and
the public `/v1/stats` `generatedAt` value. Use the existing Growth-page
seven-reading follow-up after an approved change; do not turn the epoch
counter into a unique-user or channel-conversion measure. A matching timeline
can reveal a candidate to investigate, but causal attribution still requires
source-measured channel evidence. This observation uses existing records and
no new collector, identifier, infrastructure, or spend.

Decision required: obtain dated source/channel evidence before choosing a
targeted zero-spend distribution lever, or decline this causal recovery
proposal as not planned. The target `installs.active_1d >= 5` has not been
demonstrated and no growth action has been implemented for #581.
