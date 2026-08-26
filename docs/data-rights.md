# Data rights: evidence, compatibility data, samples, code

Four different things in this project carry four different sets of rights, and
until now only two of them were stated anywhere. This document is the map: what
each layer is, what the repository currently says about it, and what the owner
decided on 2026-08-23 (R2C-63). It is not itself a licence, and no wording here
is in force until the open item in §6 is answered.

## 1. The four layers

| Layer | What it is | Where it lives | Rights today |
|---|---|---|---|
| **Code** | the `csx` and `csx-server` source | this repository | Apache-2.0 ([`LICENSE`](../LICENSE)) |
| **Sample source** | clean-room projects a human published with `csx sample publish` | `samples` table, `/v1/samples/{id}/artifact` | per-sample, default MIT-0, chosen from a fixed permissive set (`permissiveLicenses`, `internal/httpapi/samples.go`) |
| **Evidence and compatibility data** | anonymous observations and everything aggregated from them — snapshots, shards, the matrix, `/v1/*` responses | `evidence_agg`, `compatibility_snapshots`, `wanted`, `adoptions`, `search_hits`, `receipts` | **unstated** |
| **Personal and operational data** | optional GitHub identity records; IP-derived, epoch-scoped activity pseudonyms; and private authoring-session metadata including refresh IP and computer name | `identities`, `activity_buckets`, `authoring_sessions`; see [`PRIVACY.md`](../PRIVACY.md) | governed by the privacy policy and operator controls, not by the public-data licence |

The third row is the gap R2C-63 exists to close, and the fourth row is the one
most easily confused with it. A privacy policy says what is collected and why;
it says nothing about what a reader may do with what is published. The two
documents answer different questions and are kept separate on purpose.

## 2. What the owner decided

Recorded on the issue on 2026-08-23:

- Public evidence and aggregated compatibility data may be reused publicly.
- Commercial reuse is allowed.
- A Community Peer, on submitting evidence, grants CodeSampleX the right to
  store, aggregate and redistribute it.
- The rights in the public data and the operating policy of the API service are
  separate. Rate limits, quotas, authentication and any paid or API-only plan
  are operational controls and do not narrow the data licence.
- Sample source licensing, evidence/metadata terms and the privacy policy stay
  separate documents.
- Rights are not extended retroactively over contributions already made; where
  it matters, name the effective date and the boundary of consent.

The last point is the one that collides with how the data is stored. §3 is the
measurement.

## 3. The current evidence schema has no reliable contribution cutoff

Observation evidence is not stored as contributions. `evidence_agg` is keyed on
the coordinate `(purl, symbol, env_hash, stage, result, error_fp)`, and every
reporter that ever reaches that coordinate merges into the same row with
`observation_count` as a running sum (`internal/serverstore/pg.go`, the
`ON CONFLICT … DO UPDATE` upsert).

`evidence_dedup` preserves a split by the **client-supplied observation day**,
but that is not a server receipt or terms-acceptance timestamp. A client may
upload durable pending observations after the day in `epoch`, and
`validEpoch` checks only that the value has `YYYY-MM-DD` syntax. The ledger has
no `created_at`/`received_at` column and no terms-version dimension. Likewise,
`evidence_agg.first_seen` and `last_seen` belong to the merged coordinate, not
to an individual contribution. Consequently, even an intact ledger cannot
prove which contributions were submitted or accepted before a legal cutoff.

The schema comments and goal.md §14.4 describe an intended bounded retention
policy, and the store exposes `PurgeDedupOlderThan`. The inspected repository,
however, has no non-test caller that schedules that method and no deployment
step that deletes from `evidence_dedup`. A direct test call proves what the
capability does; it is not evidence that production currently runs it.

Measured in `internal/serverstore/datarights_test.go`
(`TestEvidenceRightsCutoffIsNotRepresentedByClientEpochs`), against a real
PostgreSQL: one ingestion request supplies two contributions of 7 and 5 to one
coordinate while claiming observation days 120 days apart. They merge into a
single row of 12 whose server timestamp is current. The intact ledger can
recover 7 and 5 by the two claimed days, but it cannot turn either claim into a
receipt or consent date. When the test itself calls `PurgeDedupOlderThan(30)`,
the older ledger row disappears while the aggregate remains 12, showing the
additional information loss that would occur if the intended maintenance were
wired into production.

What that means per layer:

| Layer | Per-contribution row? | Separable by a cutoff date? |
|---|---|---|
| `evidence_agg` | no — merged by coordinate | **no** — neither the aggregate nor its dedup ledger records a server receipt or accepted terms version |
| `wanted` | no — `asks` counter; `wanted_dedup` is keyed by a client-supplied epoch | no reliable server-side contribution cutoff |
| `adoptions`, `search_hits`, `receipts` | yes, with `created_at` | yes |
| `samples` | yes, with `created_at` and a per-row `license` | yes |

So the existing compatibility aggregate cannot be partitioned by a provable
submission or acceptance cutoff, even while the client-epoch ledger is intact.
Only two mechanisms remain, and choosing between them is a legal question, not
a schema question:

- **Prospective application.** Publish the terms with an effective date and
  apply them to the aggregate as published from that date, resting on the fact
  that the contract screen shown at collection time
  (`internal/cli/agentassets/contract.txt`) already told every contributor the
  exchange was "Public compatibility knowledge" for anonymous public-package
  facts — a public-redistribution purpose, disclosed before collection. This is
  the only option that preserves the existing corpus.
- **Discard and restart.** Truncate the pre-cutoff evidence so the licensed
  corpus begins clean. Mechanically possible and destroys the network's
  accumulated data; `last_seen`-based deletion would not even be clean, since a
  row touched after the cutoff keeps its earlier counts.

Adding provenance so that *future* cutoffs are expressible is a separate,
optional change: record a server receipt boundary and the accepted terms
version before merging a contribution into the aggregate. It does not recover
what is already merged, and any additional linkage needs a privacy review.

## 4. Surfaces that state or imply rights today

Every place a reader currently learns something about licensing, and what each
one omits.

| Surface | Location | Says today | Gap |
|---|---|---|---|
| README licence section | [`README.md`](../README.md) §License, line 267 | "Code: Apache-2.0. Published samples default to MIT-0." | silent on evidence and compatibility data |
| README licence badge | [`README.md`](../README.md) line 5 | shields.io renders the repo licence, "Apache-2.0" | reads as though it covers everything the project publishes |
| Localized READMEs | `docs/i18n/README.{de,es,fr,ja,ko,pt-BR,ru,zh-CN}.md`, line 239 (ko: 251) | same two sentences, translated | same gap, ×8 |
| Site footer | `internal/web/templates/landing.html` line 116 | links `LICENSE` as "Apache-2.0" | same as the badge |
| Privacy policy | [`PRIVACY.md`](../PRIVACY.md) §7 line 225 | "Published sample source is public, MIT-0 by default" | describes evidence as "the product" without stating reuse terms |
| Privacy policy | [`PRIVACY.md`](../PRIVACY.md) §4.7 line 172 | sample upload is "under MIT-0" | the only contributor-facing grant stated anywhere, and it covers sample source only |
| Contribution consent | `internal/cli/agentassets/contract.txt` | what is shared and what never is | states no grant of rights over what is shared |
| Public API reference | `internal/web/apiref.go` | ten read endpoints, no account needed | no terms attached to the data served |
| MCPB manifest | `scripts/make-mcpb.py` line 91 | `"license": "Apache-2.0"` | correct for the bundle; a directory may render it as the data licence |
| Sample pages | `internal/web/templates/sample.html`, `base.html` | per-sample SPDX chip | correct and already layered |

## 5. Follow-up implementation scope

Once §6 is answered, the change set is:

1. **A data terms document** at the repository root, versioned like `PRIVACY.md`
   rather than served from an editable page. It states: the licence over
   evidence and aggregated compatibility data including bulk and database use;
   the contributor grant and its effective date; that operational API controls
   are separate from the licence and cannot narrow it; and the correction,
   removal and abuse-report route, explicitly distinguished from the privacy
   policy's (uploaded evidence carries no identifier, so there is nothing to
   look up on request — `PRIVACY.md` §10 already says this and the data terms
   must not contradict it).
2. **README licence section**, English and the eight translations: three named
   layers instead of two.
3. **Badge disambiguation**, README and the site footer: label the badge as the
   code licence so it stops standing for the whole project.
4. **`PRIVACY.md` §7**, one cross-reference: what the server stores is
   published under the data terms. No policy text moves into it.
5. **The API reference page**, one line naming the terms the served data comes
   under.
6. **The contribution consent screen.** The verbatim goal.md §5.4 block is
   pinned by `internal/cli/init_test.go` and should stay verbatim; the grant
   belongs on a line printed after it by `askContract`, pointing at the data
   terms.
7. **A doc regression test** in the repository's existing idiom
   (`internal/daemon/localonlydoc_test.go`, `internal/mcp/mcpbcatalog_test.go`):
   pin the three-layer statement across README and its translations so a
   future edit cannot silently collapse it back to two.

## 6. Open item

The instrument has not been chosen. All three candidates satisfy the owner's
decision — public reuse, commercial reuse, bulk and database use — and they
differ in what they ask of a downstream reuser.

| Candidate | Attribution | Notes for this project |
|---|---|---|
| **CC0-1.0** | none | no obligation reaches an agent that answers from the data; nothing credits the network back |
| **CC-BY-4.0** | required | licenses sui generis database rights explicitly; the obligation lands on every downstream consumer including coding agents, where it is largely unenforceable |
| **CDLA-Permissive-2.0** | notice on redistribution of the data itself, none on results computed from it | written for data rather than adapted to it; less widely recognised |

## 7. Legal review items

- Whether the contract screen in force before the effective date is a
  sufficient basis for publishing the pre-cutoff aggregate under the new terms,
  given §3 shows the current schema has no reliable submission/acceptance
  boundary with which to separate it.
- Whether EU sui generis database rights are engaged by this corpus, and
  whether the chosen instrument disposes of them (CC-BY-4.0 does so explicitly;
  CC0-1.0 waives them; CDLA-Permissive-2.0 grants them).
- Whether the contributor grant needs to survive a contributor's later switch
  to `local-only`, and how that reads against §3.
- Whether "commercial reuse allowed" plus a future paid API needs an explicit
  no-exclusivity statement so the two cannot be read as contradicting.
