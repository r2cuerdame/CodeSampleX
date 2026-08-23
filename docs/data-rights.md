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
| **Personal data** | none is collected; the boundary itself | [`PRIVACY.md`](../PRIVACY.md) | not a licence, and must not be read as one |

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

## 3. A date-bounded grant is only expressible for 30 days

Observation evidence is not stored as contributions. `evidence_agg` is keyed on
the coordinate `(purl, symbol, env_hash, stage, result, error_fp)`, and every
reporter that ever reaches that coordinate merges into the same row with
`observation_count` as a running sum (`internal/serverstore/pg.go`, the
`ON CONFLICT … DO UPDATE` upsert). The one per-epoch record of who contributed
what is `evidence_dedup`, and that ledger is purged on the retention window
(goal.md §14.4) — deliberately, so a rotating anonymous id cannot be followed
across epochs.

Measured in `internal/serverstore/datarights_test.go`
(`TestEvidenceContributionsStopBeingSeparableWhenTheDedupLedgerIsPurged`), against
a real PostgreSQL: two contributions of 7 and 5 observations to one coordinate,
120 days apart, merge into a single row of 12. Before the purge the split is
recoverable from the ledger. After `PurgeDedupOlderThan(30)` the older epoch's
ledger rows are gone and the row still reads 12, with nothing left to say which
seven arrived first. `first_seen`/`last_seen` bracket the row, not a
contribution.

What that means per layer:

| Layer | Per-contribution row? | Separable by a cutoff date? |
|---|---|---|
| `evidence_agg` | no — merged by coordinate | **only within the 30-day retention window** |
| `wanted` | no — `asks` counter; `wanted_dedup` is per-epoch and not purged | by epoch, yes |
| `adoptions`, `search_hits`, `receipts` | yes, with `created_at` | yes |
| `samples` | yes, with `created_at` and a per-row `license` | yes |

So a cutoff that is more than 30 days old cannot be applied to the compatibility
data by partitioning it. Only two mechanisms remain, and choosing between them
is a legal question, not a schema question:

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
optional change (a terms-version dimension on the aggregate). It does not
recover what is already merged, and it would widen what the row records about a
contributor, which is a privacy cost paid for a legal convenience.

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
  given §3 shows it cannot be separated out.
- Whether EU sui generis database rights are engaged by this corpus, and
  whether the chosen instrument disposes of them (CC-BY-4.0 does so explicitly;
  CC0-1.0 waives them; CDLA-Permissive-2.0 grants them).
- Whether the contributor grant needs to survive a contributor's later switch
  to `local-only`, and how that reads against §3.
- Whether "commercial reuse allowed" plus a future paid API needs an explicit
  no-exclusivity statement so the two cannot be read as contradicting.
