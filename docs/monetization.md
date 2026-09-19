# Monetization: one upgrade pack, capacity that grows with the user

> **Decision record for issue #205.** This document defines the tier model and
> launch guardrails. It does not activate billing, set a launch price, or change
> today's rate limits. Prices and numeric limits must pass the dated benchmark
> in §6 immediately before a paid plan ships.

CodeSampleX should be easy for an individual developer or coding agent to call
habitually, while users who need sustained hosted capacity, shared
administration, or contractual guarantees pay for the additional service.
There is one product—the **CSX upgrade pack**—regardless of which client or
transport reaches it.

## 1. The pricing invariant

> **A free individual gets unmetered local recall and enough shared-service
> capacity to make CSX habitual; heavy and team users pay for more sustained,
> pooled, and governed capacity without changing products or losing the basic
> evidence model.**

That is the acceptance sentence for this proposal. It also defines the test for
future pricing changes: if an agent must deliberate before an ordinary lookup,
or if a customer must understand transport internals to choose a plan, the
change is wrong.

The durable rules are:

- Local recall of the public corpus is unmetered.
- Evidence contribution, receipts, and the wanted board remain available
  without a paid plan.
- Every tier receives the same evidence semantics: recorded environment,
  contract assertions, differences, provenance, and honest misses.
- Paid plans buy service capacity and operations, not more trustworthy answers.
- Raw record count is not a billing unit. Normalization quality and evidence
  trust are the product's value.
- MCP, HTTP, CLI, editor integrations, and future adapters are access paths to
  one upgrade pack, never separately priced products.

## 2. Why ordinary recall is not the paid meter

CSX answers an agent's normal search from the corpus on its own machine. The
MCP path calls the local search engine first (`internal/mcp/deps.go`); only a
miss can fetch missing public shards and repeat that local search. The marginal
cost of another warm lookup is therefore effectively zero, and the service does
not observe it.

The anonymous reporter identifier also rotates daily
(`internal/identity/identity.go`). Search and evidence deliberately do not
require a durable account, and the README promises that search, evidence,
receipts, and the wanted board stay open without one. Adding durable identity
to ordinary recall merely to count it would weaken that privacy boundary and
manufacture a billing event where none exists.

CSX can honestly meter service it actually performs for an identified customer:
sustained hosted search, shard delivery and freshness, automation, pooled team
capacity, administrative visibility, and contracted operations. The
customer-facing name for that meter is **hosted capacity**. Internal request
classes and protocols remain implementation details.

Anonymous abuse controls are not product quotas. They continue to protect the
shared service, and conditional requests that do negligible work should keep
being refunded as they are today (`internal/httpapi/ratelimit.go`).

## 3. Tier model

Prices and quantitative ceilings are deliberately marked **set at launch**.
This avoids turning a competitor snapshot or an unmeasured cost assumption into
a permanent promise.

| Tier | Best for | Included product | What naturally creates the upgrade |
|---|---|---|---|
| **Free / individual** | one developer and their agents | unmetered local public-corpus recall; generous recurring hosted capacity and shard freshness; evidence contribution, receipts, wanted reports, and optional attributed publishing | no forced upgrade for ordinary recall; upgrade only when sustained automation or shared ownership becomes valuable |
| **Pro / heavy individual** | CI, automation, and developers with sustained use | substantially higher hosted-capacity ceiling; priority throughput and freshness where capacity is scarce; higher identified publishing ceiling | one person's automation needs predictable capacity beyond the generous individual envelope |
| **Team / org** | groups sharing agents and workflows | pooled hosted capacity; member roles; usage visibility; budgets, billing, and administrative controls | uneven usage can be pooled and governed instead of purchasing disconnected individual products |
| **Enterprise** | regulated or operationally critical organizations | negotiated capacity; SSO; security and audit controls; dedicated support and SLA-style guarantees; private-network or deployment options if demand justifies them | contractual assurance and organization-wide controls, not a different evidence model |

### Free-tier behavior

Free is a recurring plan, not a short trial. Local recall never consumes its
hosted allowance. The allowance must cover a normal individual developer's
misses and synchronization with comfortable headroom; reaching it should be an
exception caused by automation or abuse, not a daily workflow. If a free user
is temporarily constrained by shared-service pressure, cached recall and
contribution remain available.

### Paid-tier behavior

Pro raises sustained capacity for one identified account. Team changes the
capacity owner from a person to an organization and pools it so uneven use is
not penalized seat by seat. Enterprise adds controls and guarantees only when
customers demonstrate demand. All use one account entitlement across every
supported access path.

Paid overage, if offered, must be predictable and capped or alertable. A hard
stop should not be the default for an in-good-standing paid user. Abuse and
security ceilings remain independent of commercial capacity.

## 4. Contribution and evidence stay common

Field evidence contribution cannot become a paid entitlement. Broad real-world
participation is how the corpus learns which package, version, API, and
environment combinations actually work. Charging to contribute would reduce
the value of every tier.

Likewise, the core evidence model is not a feature ladder. A Free answer must
not omit provenance or uncertainty that a paid answer includes. Higher tiers
may receive more throughput, fresher delivery, team visibility, and operational
guarantees; they do not receive a more flattering verdict.

The data-rights boundary in `docs/data-rights.md` remains authoritative: rate
limits, quotas, authentication, and paid plans are operational controls and do
not narrow the corpus licence.

## 5. Current Context7 benchmark

Observed **2026-09-19** from Context7's official
[Plans & Pricing](https://context7.com/plans) and
[usage documentation](https://context7.com/docs/howto/usage):

| Context7 plan | Current public shape | Lesson for CSX |
|---|---|---|
| Free | $0; 1,000 included requests per month; public repositories; 20 bonus requests each day after the monthly ceiling | the free envelope is large enough to establish recurring agent use, and exhaustion does not mean total abandonment |
| Pro | $10 per seat/month; 5,000 included requests per seat; $10 per additional 1,000; private repositories and collaboration | heavy use pays for capacity, while collaboration makes identity useful |
| Enterprise | custom/scale pricing; security, SSO, support, SLA, and self-hosted options | enterprise value is controls and assurance rather than fragmenting the core lookup product |

This is a benchmark, not CSX's price card. Context7 serves remote lookups and
can count them; CSX normally recalls locally and cannot honestly use the same
request counter. The reusable shape is a useful free tier followed by more
capacity, collaboration, and guarantees—not the competitor's transport unit.

The benchmark must be refreshed from the same first-party sources before any
price or numeric ceiling is approved. Record the observation date, currency,
billing period, included capacity, overage behavior, pooling behavior, and what
happens at the free limit. Do not copy numbers from this dated section into
billing configuration.

## 6. Launch calibration

Exact CSX prices and limits require a measurement window and owner approval.
Immediately before launch, use at least 30 representative days to calculate:

1. The distribution of shard fetches, remote searches, bytes served, and
   database work for anonymous individuals, identified heavy users, and teams.
2. The free ceiling that covers at least the 95th percentile of non-automated
   individual hosted consumption, excluding confirmed abuse. Local recall does
   not enter this calculation.
3. A Pro ceiling materially above Free and sufficient for typical individual
   automation, with clear alerts before paid overage or throttling.
4. A genuinely pooled Team allowance sized from aggregate organization use,
   not `seats × isolated per-seat quota` disguised as pooling.
5. Unit economics for the measured service consumption, including egress,
   storage, database work, support, payment fees, and a safety margin.
6. A fresh Context7 comparison using §5's checklist, plus at least one other
   current developer-tool benchmark if it materially changes the decision.

Publish the resulting plan in customer language: monthly hosted capacity,
pooling, roles, and guarantees. The enforcement layer may count requests and
bytes internally, but neither the price card nor limit errors should ask an
ordinary user to reason about protocol calls or adapters.

## 7. Launch gates and verification

This proposal is complete without provisioning billing. Shipping paid plans is
a separate product and operational change and requires all of the following:

- owner approval of the measured prices, ceilings, grace, and overage policy;
- legal and tax review plus approved payment processing;
- an entitlement model that does not put durable identity on local recall or
  anonymous evidence submission;
- usage visibility, alerts, budget controls, downgrade behavior, and support
  procedures before enforcement starts;
- tests proving one entitlement works consistently across access paths and that
  Free retains local recall and evidence contribution after its hosted ceiling;
- a staged rollout with measurement of lookup success, free-limit encounters,
  contribution rate, paid conversion, and support incidents.

The proposal should be rejected or recalibrated if the measured free tier makes
agents hesitate before ordinary recall, if paid conversion depends on
withholding evidence quality, or if the price card exposes internal transport
complexity as separate products.
