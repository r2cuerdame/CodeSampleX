# Measurement Layers: Separating Retrieval Quality from User Outcome Value

**GitHub Issue #206 Contract**  
Related: [PRIVACY.md](../PRIVACY.md) · [README.md](../README.md) · [docs/activation-funnel.md](activation-funnel.md) · [docs/operations.md](operations.md)

---

## 1. Product decision and core principles

CodeSampleX measurement must have **two distinct layers**. They are related, but they are **never substitutes**:

```text
Layer 1: Retrieval / Memory Quality
  │  "Is CSX returning the right execution memory?"
  ▼
Better Evidence Surfaced
  │  "Sanitized contracts, exact deltas, and boundary proofs"
  ▼
Layer 2: User / Agent Outcome Value
     "Did that memory actually improve the user's/agent's result?"
```

Treat the causal relationship as:

$$\text{retrieval/memory quality} \longrightarrow \text{better evidence surfaced} \longrightarrow \text{better user/agent outcome}$$

### Why the distinction matters

1. **A high hit rate is not product success.** A search retrieval can be technically exact, perfectly normalized, and supported by three independent peer receipts, yet completely useless or distracting to the user's immediate task.
2. **Search quality explains *why* the product works, not *whether* the user succeeded.** Diagnosing indexing recall, ranking precision, and shard warming is vital for system engineering, but it is internal product quality.
3. **User outcome is the north star.** The sole reason CodeSampleX exists is to help human developers and autonomous coding agents reach working execution paths with less wasted work, avoiding known-dead ends.
4. **Outcome claims require controlled or observed evidence.** An outcome claim must never be asserted from retrieval volume alone, and percentage uplift figures must never be invented.

---

## 2. Layer 1: Retrieval / memory quality (internal product quality)

Layer 1 evaluates the execution-memory search engine. It answers:

> **"Is CSX returning the right execution memory?"**

These metrics diagnose and guide improvements to ingestion, normalization, ranking, and synchronization. They explain system mechanics, but they are **not the end value by themselves**.

### Measured & diagnostic dimensions

| Dimension | What it measures | Why it is Layer 1 |
|---|---|---|
| **Exact-coordinate relevance** | Whether candidate matches the exact package, version, and symbol requested | Internal search accuracy; does not guarantee the user needed that sample |
| **Nearby-coordinate relevance** | Precision of version-range and compatible-minor fallbacks | Ranking candidate selection |
| **Normalization precision** | Canonicalization of PURL versions, command arguments, and environment dimensions | Input hygiene and recall consistency |
| **Failure-family separation** | Grouping distinct stack traces into verified error fingerprints without cross-contamination | Diagnostic precision for error matching |
| **Provenance completeness** | Proportion of candidates backed by signed v2 receipts, reproducible container digests, and independent peer verification | Internal evidence reliability |
| **Boundary & contradiction surfacing** | Surfacing environment differences (`different`), adaptation needs, and explicit `NO_SAFE_MATCH` | Prevents bad suggestions; still an internal filtering property |
| **Hits / Misses & Hit rate** | Search requests returning `match != NO_SAFE_MATCH` vs misses | Retrieval volume; high volume does not imply user task completion |
| **Exact failure matches** | Sanitized error fingerprint matched a recorded failure for a passing detour | Memory match quality |
| **Verified detours offered** | Passing contract offered with no undisclosed environment delta | Search recommendation candidate quality |
| **Index & corpus coverage** | Known packages, symbols, shards, and cache bytes | System memory capacity |

### Role and governance
- **Internal diagnostic only:** Used by engineers and automated schedulers (`docs/coverage-scheduler.md`) to find coverage gaps, improve scoring weights, and fix normalization edge cases.
- **Never external value proof:** Raw corpus size or lookup count must never be cited as proof that CSX improves an LLM.

---

## 3. Layer 2: User & agent outcome value (core product value)

Layer 2 evaluates real-world results experienced by developers and coding agents. It answers:

> **"Did that memory actually improve the user's/agent's result?"**

This is the actual reason CodeSampleX exists. These are the metrics that matter to users and must drive product claims once sufficient evidence is gathered.

### Outcome metrics and validation targets

| Metric / Dimension | What it measures | How it is validated |
|---|---|---|
| **Repeated known-bad path avoided** | An agent or developer was prevented from executing a known fatal command or package version | Exact failure match surfaced, verified detour applied, and subsequent run succeeded |
| **Task success rate improved** | The rate at which builds/tests pass after consulting CSX vs baseline | Measured post-hit build reports: `PostHitBuildPassRate = PASS / (PASS + FAIL)` |
| **Reported failures avoided** | Complete four-stage verified detour funnel: exact failure matched $\to$ detour offered $\to$ applied $\to$ build passed | All 4 measured stages completed locally in `interventions` (`ReportedFailuresAvoided`) |
| **Adoptions applied** | An agent or developer actively integrated the suggested code or detour | `report_sample_adoption` with `applied=true` |
| **Fewer dead-end attempts & retries** | Reduction in futile build/fix loops on known breaking changes | Rework tracking (`PostHitBuildReports - PASS`) and retry counts |
| **Reduced time-to-resolution** | Total elapsed duration from first failure to passing test suite | Controlled session time comparisons |
| **Wasted reasoning work reduced** | Avoided LLM token consumption and unnecessary reasoning turns | `EstimatedReasoningAvoided` (fixed assumption: ~3 turns saved per applied hit minus rework; always flagged as estimated) |

### Role and governance
- **North star:** Every product claim, benchmark report, and marketing statement must be rooted in Layer 2.
- **Strict evidentiary standards:** A high number of Layer 1 hits with low Layer 2 adoptions or low post-hit pass rates indicates a product failure, not a success.

---

## 4. Field-first guardrails & no invented uplift

To prevent misrepresenting internal metrics as user value, the following six guardrails are mandatory across all code, reports, schemas, and documentation:

### Guardrail 1: No vanity metrics as proof of LLM improvement
Raw corpus size (e.g., "over 500,000 packages indexed"), raw shard counts, and raw lookup counts are capacity metrics. They must **never** be cited or badged as evidence that CodeSampleX improves agent performance.

### Guardrail 2: No reduction to search-quality metrics
Product evaluation must never be reduced to retrieval precision, recall, or hit rate alone. Optimizing search hit rate without verifying whether applied samples passed user builds creates deceptive incentives.

### Guardrail 3: No invented uplift percentages
Uplift percentages (e.g., "+35% coding accuracy", "saves 45% of agent time") must **never** be published, displayed, or claimed without:
1. A credible, documented measurement design.
2. A controlled counterfactual baseline (e.g., randomized A/B trial or matched cohort).
3. A statistically sufficient sample size (minimum $N \ge 50$ completed task sessions).

Unverified uplift claims are rejected at build time and test time by `internal/metricname` (`RuleInventedUplift`) and `internal/measurement` (`ErrInventedUplift`).

### Guardrail 4: Field-first measurement precedence
Real-world developer and agent outcomes observed in the field outrank synthetic benchmarks and internal dogfooding.
- **Field-observed evidence** (`EvidenceFieldObserved`) is primary.
- **Synthetic benchmarks** (`EvidenceSyntheticBenchmark`) and dogfood runs (`EvidenceInternalDogfood`) are supporting evidence only, and cannot serve as the sole justification for product outcome claims.

### Guardrail 5: Explicit two-layer reporting on all surfaces
Dashboards (`csx ui`), CLI commands (`csx stats`), JSON APIs (`GET /local/v1/stats`, `GET /v1/stats`), and schemas must make the distinction between Layer 1 and Layer 2 visually and structurally explicit. They must not blend search volume and outcome success into a single unqualified list.

### Guardrail 6: Strict metric classification and naming rules
Inherited from [docs/activation-funnel.md](activation-funnel.md) §6 and enforced by `internal/metricname`:
- **Measured:** Direct counts of records in the store (`hits`, `adoptions`, `postHitBuildReports`). Named for records, never actors (`RuleForbiddenActor`).
- **Estimated:** Computed by formula from measured inputs. Must carry the `estimated` prefix, an `estimated: true` sibling boolean, and documented assumptions (`RuleUnlabelledEstimate`).
- **Unmeasured:** Displayed as `—` (em dash) or a descriptive note (`PlaceholderStat`), never as `0`.
- **No invented uplift tokens:** Field names containing `uplift` or `speedup` are forbidden on public or local documents unless explicitly authorized by a controlled measurement receipt (`RuleInventedUplift`).

---

## 5. Concrete instrumentation & schema mapping

The two layers map to existing and extended CodeSampleX data structures as follows:

| Layer | Measurement Concept | CLI `csx stats` | Daemon `daemon.Stats` | Public `compatibility.StatsDoc` | Local DB `interventions` |
|---|---|---|---|---|---|
| **L1** | Search Hits / Misses | `Hits / Misses` | `st.RetrievalQuality.Hits`, `Misses`, `HitRate` | — | `hits` table count |
| **L1** | Exact Failure Matches | `Exact failure matches` | `st.RetrievalQuality.ExactFailureMatches` | — | `interventions.exact_failure_matched = 1` |
| **L1** | Verified Detours Offered | `Verified detours offered` | `st.RetrievalQuality.VerifiedDetoursOffered` | — | `interventions.verified_offer = 1` |
| **L1** | Known Packages / Shards | `Known packages` | `st.RetrievalQuality.KnownPackages` | `Packages`, `Symbols` | `packages` table count |
| **L1** | Local / Network Evidence | `Automatic evidence sent` | `st.RetrievalQuality.EvidenceBatchesSent`, `OriginSeeds`, `CrossVerifications` | `Evidence`, `VerifiedSamples`, `Peers`, `ProjectsMonth` | `evidence_dedup`, `upload_queue` |
| **L2** | Applied Adoptions | `Adoptions` | `st.OutcomeValue.Adoptions` | `hitsAdopted` | `interventions.applied = 1` |
| **L2** | Post-Hit Build Pass Rate | `Post-hit build pass` | `st.OutcomeValue.PostHitBuildPassRate` | `PostHitSuccessRate` | `hits.post_build_pass` |
| **L2** | Post-Hit Measured Reports | `(N reports)` | `st.OutcomeValue.PostHitBuildReports` | `PostHitBuildsReported` | `post_build_pass IS NOT NULL` |
| **L2** | Reported Failures Avoided | `Reported failures avoided` | `st.OutcomeValue.ReportedFailuresAvoided` | — | All 4 stages: match + offer + applied + PASS |
| **L2** | Estimated Reasoning Avoided | `Estimated reasoning avoided` | `st.OutcomeValue.EstimatedReasoningAvoided` (`estimated=true`) | `EstimatedReasoningAvoided` | Formula: `adoptions * 3 - rework` |

---

## 6. Acceptance checklist (Issue #206)

A consumer or dashboard inspecting CodeSampleX can unambiguously answer:
1. **`Is CSX returning the right execution memory?`**  
   Answered by Layer 1: retrieval hit rate, exact failure matches, verified offers, coverage completeness, and provenance receipts.
2. **`Did that memory actually improve the user's/agent's result?`**  
   Answered by Layer 2: adoptions applied, post-hit build pass rate, reported failures avoided, and measured rework avoidance.

Product claims are strictly governed by Layer 2, while Layer 1 serves as the diagnostic foundation.
