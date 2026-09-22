# CodeSampleX Public Evidence and Compatibility Data Terms

**Effective Date:** September 21, 2026  
**License:** [CDLA-Permissive-2.0](https://cdla.dev/permissive-2-0/) (Community Data License Agreement – Permissive – Version 2.0)

This document establishes the reuse, redistribution, and contribution terms for Community Peer Evidence and aggregated compatibility data published by CodeSampleX.

---

## 1. The Four Rights Layers

CodeSampleX consists of four distinct layers, each governed by its own terms:

| Layer | What it covers | Canonical terms |
|---|---|---|
| **Code** | The `csx` CLI, daemon, MCP adapter, and `csx-server` source code | Apache-2.0 ([`LICENSE`](LICENSE)) |
| **Sample source** | Runnable code samples published via `csx sample publish` | Per-sample SPDX license, default MIT-0 ([`samples` manifest](docs/distribution.md)) |
| **Evidence and compatibility data** | Anonymous observations, compatibility matrix, snapshots, shards, and `/v1/*` & `/v2/*` data | **CDLA-Permissive-2.0** (this document) |
| **Personal and operational metadata** | Optional GitHub authoring sign-in tokens, edge rate-limiting logs, pseudonymous client analytics | Governed by [`PRIVACY.md`](PRIVACY.md) |

---

## 2. Scope of Public Compatibility Data

The terms in this document apply to:
- Observation batches, build outcomes, stage results, and sanitized failure fingerprints uploaded by Community Peers.
- Aggregated compatibility records and matrices stored in `evidence_agg` and exposed via `/v1/*` or `/v2/*`.
- Offline compatibility shards (`GET /v1/shards/{ecosystem}/{name}/{major}`).
- Machine-readable findings (`GET /findings.json`) and wanted boards (`GET /v1/wanted`).
- Compatibility snapshots, search hit summaries, and public verification receipts (`GET /v1/samples/{id}`).
- Execution footprints recorded as unsigned self-reports (`POST /v1/footprints/execution`).

---

## 3. Contributor Grant

When you run CodeSampleX in **Community mode** and contribute observations, build results, failure fingerprints, wanted requests, or execution footprints, you grant CodeSampleX and its users a perpetual, irrevocable, worldwide, royalty-free, non-exclusive license to:
1. Store, aggregate, index, deduplicate, and analyze the submitted anonymous records.
2. Publish, share, and redistribute the resulting aggregated compatibility data and snapshots under the [CDLA-Permissive-2.0](https://cdla.dev/permissive-2-0/) license.
3. Compute derived answers, compatibility grades, adaptation requirements, and machine-readable models from that data.

### Effective Date and Pre-Cutoff Aggregate
- This grant applies to all contributions submitted on or after the Effective Date (September 21, 2026).
- Compatibility aggregates merged prior to the Effective Date are published prospectively under these terms, resting on the disclosure made to every contributor at collection time in `internal/cli/agentassets/contract.txt`: the exchange was "Public compatibility knowledge" for anonymous public-package facts.
- The contributor grant survives any subsequent local configuration change (such as later running `csx init --local-only`), because submitted observations merge into anonymous aggregate coordinates (`(purl, symbol, env_hash, stage, result, error_fp)`) without individual author identifiers.

---

## 4. Reuse, Redistribution, and Commercial Use

Under [CDLA-Permissive-2.0](https://cdla.dev/permissive-2-0/):
- **Public reuse:** You may freely access, search, query, cache, and mirror the public data.
- **Commercial reuse:** Commercial use is permitted without fee or royalty. You may incorporate CodeSampleX compatibility data into commercial developer tools, IDE extensions, CI/CD platforms, coding assistants, and APIs.
- **Redistribution of Data:** If you publish, distribute, or share the Data itself (or a modified version of the Data, or extracts of the dataset), you must:
  1. Make the Data available under CDLA-Permissive-2.0.
  2. Retain all copyright, trademark, and attribution notices accompanying the Data, or credit the source:
     ```text
     Data from CodeSampleX (https://codesamplex.dev) — licensed under CDLA-Permissive-2.0
     ```
- **No Attribution Required on Results:** You are **not** required to provide notice or attribution on **Results** computed from or informed by the Data. Answers, code adaptations, environment checks, and diagnostic explanations returned to users by coding agents, IDEs, or automated pipelines carry no downstream licensing burden.

---

## 5. Bulk Access and Database Extraction

- Bulk extracts, database dumps, shard archives, and full snapshot datasets share the exact same CDLA-Permissive-2.0 license as individual HTTP responses.
- **Operational Controls vs. Data License:** Operational constraints—including rate limits, HTTP status `429 Too Many Requests`, request quotas, edge DDoS protections, authentication requirements for write endpoints, and any future hosted or high-throughput API tiers—are infrastructure access controls, not restrictions on the data license. They cannot narrow or revoke your CDLA-Permissive-2.0 rights in data you have already received.

---

## 6. Privacy and Content Boundaries

CodeSampleX enforces strict privacy boundaries prior to ingest:
- **Never submitted:** Real project source code, repository names, local file paths, code snippets, environment variables, credentials/secrets, private packages, and raw compiler or runtime logs are never transmitted (see [`PRIVACY.md`](PRIVACY.md) §1–§4).
- **No Stable Identity in Evidence:** Evidence payloads contain no user names, email addresses, or persistent hardware identifiers. Observations are grouped using rotating daily pseudonyms (`anonId`) and salted one-way project hashes (`projectBucket`) that rotate monthly.
- **Public Packages Only:** Automatic evidence collection is strictly restricted to packages verified to exist on public package registries (npm, PyPI, crates.io, Go module proxy, RubyGems, Packagist, pub.dev, Hex).

---

## 7. Corrections, Abuse, and Tainted Data

Because submitted evidence is merged into anonymous coordinate sums and carries no contributor identity:
- There is no contributor account or record from which an individual historical observation can be selectively looked up or deleted (as documented in [`PRIVACY.md`](PRIVACY.md) §10).
- If poisoned, incorrect, or corrupted evidence is identified for any package or coordinate:
  - Users and agents can file a verification request via `report_anomaly` (or `csx anomaly`). An anomaly report does not immediately alter the public aggregate; it queues an independent run on container verifiers. Only an objective passing or failing receipt from an independent run can confirm or reject the report.
  - Security reports regarding maliciously spoofed coordinates or tainted public packages should be directed to the security contact named in [`SECURITY.md`](SECURITY.md).
