# Monetization: what CSX can meter, and what it must not

> **Status: proposal, not a shipped decision.** The standing operating model is
> [README](../README.md) §Operating model — the public network is free to join
> and operation is funded by sponsorship. Nothing in this document changes that
> until the project owner accepts it. No price is set here, and none should be
> read out of here; §7 says why, and what to measure instead.

This document answers [#205](https://github.com/r2cuerdame/CodeSampleX/issues/205):
a Context7-like tier structure that does not punish the one behavior the whole
product depends on — an agent calling `search_known_solution` without thinking
about it first.

It reaches a different meter than the question assumed, and §1–§3 are the
reason. They are measurements from this repository, not preferences.

---

## 1. Recall is answered locally, so recall is not a cost

The obvious pricing model — meter lookups, sell more lookups — is borrowed from
services that serve every lookup from their own servers. CSX does not.

An agent's search runs against the corpus already on the machine
(`internal/mcp/deps.go:146`, `engine.Search(ctx, req)` over the local DB). The
server is contacted only when that local search comes up short: `FetchMissing`
pulls the missing shards and the engine re-runs locally
(`internal/mcp/deps.go:150-152`). The corpus it reads is a cached artifact —
1,770 sample manifests and 974 shard rows, about 12 MB of JSON on the install
this was measured on (`internal/search/corpuscache.go:9-14`).

So the marginal server cost of the *n*-th recall is zero, and the server cannot
observe it in the first place. What the server actually serves is periodic shard
synchronisation, and most of that is already free: of 4,074 shard requests in one
production window, 2,606 were conditional revalidations answered `304`
(`internal/httpapi/ratelimit.go`, the `refund` comment) — the limiter now refunds
those precisely because charging them was exhausting honest clients' budgets.

**Consequence.** A recall meter would bill for something that costs nothing,
cannot be seen, and would have to be *added to the wire* to be billed. The free
tier is not a generosity decision to be tuned. It is structural, and it is a
better free tier than a server-side competitor can afford to offer.

## 2. There is no per-user meter, and adding one breaks a published promise

Even if recall were worth metering, there is nothing to meter it against.

The reporter id rotates every day by construction:
`hex(HMAC-SHA256(seed, "anon|"+epochDay))[:16]` (`internal/identity/identity.go:182-184`).
Two uploads a day apart are deliberately not linkable, and
[docs/activation-funnel.md](activation-funnel.md) §"Two structural facts" treats
that as the design rather than a gap — the funnel is local precisely because the
server cannot follow one install through time.

Accounts exist, but only for one action. The `identities` table holds
`login, github_id, display, token_hash, api_token_hash, created_at`
(`internal/serverstore/migrations/0001_init.sql:121`) — no plan, no quota, no
billing column — and it is consulted on the sample-publish path alone
(`internal/httpapi/publishgate.go`, `internal/httpapi/samples.go`).
`publishgate.go` states the rule in the code itself: the split is **by ACTION,
not by identity**, and evidence and search stay anonymous "with no account
anywhere near them."

README:294 makes that a public promise: *"search, evidence, receipts and the
wanted board are open without an account."*

**Consequence.** Metering recall per user requires attaching a durable identity
to search. That contradicts a documented non-negotiable principle, and README
says breaking one needs the project owner's explicit approval. This proposal
does not ask for it, and §9 records it as a gate rather than routing around it.

## 3. What the project has already decided

Three prior decisions constrain the answer and mostly agree with it.

| Source | Decision | Effect here |
|---|---|---|
| README §Operating model | Free to join; sponsorship-funded. A paid **Hosted API** is reserved for "API-only consumers who contribute no evidence, storage, or verification" | The reserved meter is already *non-contribution*, not usage |
| README §Out of scope for Public v1 | "API-only billing", "enterprise/private package networks, SSO/SLA/on-premise" are out of Public v1 | Tiers 2–4 below are post-v1 by the project's own scope decision |
| [docs/data-rights.md](data-rights.md) §2 (owner, 2026-08-23) | "Rate limits, quotas, authentication and any paid or API-only plan are **operational controls** and do not narrow the data licence" | A paid tier may gate *service*, never *rights to the data* |

The third is the load-bearing one. It is what lets a paid tier exist at all
without turning contributed evidence into a paywalled asset — which #205 lists
as a constraint and which the corpus could not survive anyway.

---

## 4. The proposal: the meter is identity, not recall

> **You never pay to ask. You pay when you want CSX to know who you are.**

Free is unmetered anonymous recall — CLI, MCP, local corpus, evidence
contribution, the wanted board — because §1 says it costs nothing and §2 says it
cannot be counted honestly. That is not a trial. It is the product, permanently.

Everything worth charging for is something a user *asks* to be identified for.
An individual on one machine wants anonymity and gets it free. A team wants
attribution, pooling, visibility and guarantees — and every one of those is
impossible without an account, so the boundary the customer wants to cross is
exactly the boundary the free tier cannot cross. Nobody is throttled into
upgrading; they upgrade when they stop being one anonymous machine.

This keeps one mental model. There is no "MCP plan" or "API plan" — #205's
guardrail — because the transports are not the units. Identity is.

## 5. Tier shape

Prices are deliberately absent (§7). What each tier *unlocks* is the proposal.

| Tier | Who | What it adds | Identity needed |
|---|---|---|---|
| **1. Free / individual** | every developer and agent | unmetered local recall, evidence contribution, wanted board, attributed publishing via `csx login github` | none (login optional, for attribution only) |
| **2. Pro / heavy individual** | CI, automation, servers | sustained programmatic `POST /v1/search` throughput above the anonymous read budget; higher publish ceiling; priority shard freshness | API token |
| **3. Team / org** | teams | pooled ceilings across members, shared visibility into what the team asked and adopted, seat/billing admin | org account |
| **4. Enterprise** | regulated / private | SSO, audit, support and SLA-shaped guarantees; private package networks | org + contract |

Two notes on where the ceilings sit. Today's anonymous read budget is 300
requests/minute and the per-credential ceiling is 20/second
(`internal/httpapi/ratelimit.go`) — Pro sells sustained headroom above the first,
not relief from the second, which is an abuse control and should stay one.
And the seeded-publish precedent already works this way: an identified publisher
gets 300/hour against an anonymous 10/hour, keyed by login, and the comment
explains that the strict number "was not protecting anything, it was only capping
the people doing the work." Paid tiers should read as more of that, not as a
new kind of thing.

## 6. Against the acceptance test

#205 asks for one sentence each.

**Why a free individual can use CSX enough to feel the upgrade:** their recall is
answered on their own machine and is never counted, so the habit forms with no
budget to deliberate about — and they feel the ceiling only when they leave the
single-machine case, which is the moment a paid tier starts being about
something they want rather than something withheld.

**Why a heavy or team user naturally pays more:** everything that makes CSX
valuable at scale — sustained programmatic throughput, pooled team ceilings,
attribution, controls, guarantees — requires an account, and the free tier's
defining property is that it has none, so the upgrade is a change of identity
rather than a change of product.

## 7. What must be benchmarked at implementation time

#205 requires benchmarking against the then-current Context7 model rather than
freezing an old snapshot. Writing today's competitor prices into this file would
create exactly the stale snapshot it warns against, so this section is the
procedure and the price cells stay empty until someone runs it.

At implementation time, record with the date observed:

1. Context7's then-current free ceiling, paid tier prices and the unit each is
   denominated in.
2. Whether their free tier is per-account or per-key, since ours is neither.
3. Our own numbers to price against, which are already collected and are not
   competitor-dependent: `POST /v1/search` volume by client class, shard-sync
   egress and its 304 ratio, and the per-request cost of the routes in §5.
4. The floor: what tier 2 must earn to cover a heavy API consumer's server and
   egress cost, since §1 means everyone else is already near-free to serve.

Set prices from 3 and 4; use 1 and 2 to check the shape is not alien to what
developers already expect. A tier that cannot be justified from 3 is a tier that
exists to hit a number, which is what §8's third bullet forbids.

## 8. What this must not become

- **No metering of anonymous recall**, ever. It is the product principle and,
  per §1, it would also mean adding to the wire something that today does not
  need to be there.
- **No paywall on contribution.** Evidence, receipts and the wanted board stay
  open (#205; README:294). Contribution is how the corpus gets better; charging
  for it inverts the incentive.
- **No optimizing around raw record count.** README §Success metrics is explicit
  that success is not judged by sample counts or sign-ups, and #205 says the moat
  is normalization quality and evidence trust.
- **No fragmenting by transport.** MCP, CLI and API are one product.
- **No narrowing of data rights** to make a tier attractive
  ([data-rights.md](data-rights.md) §2).

## 9. Gates this proposal does not clear

Recorded rather than assumed, and none of them is an engineering decision:

1. **Pricing itself is the owner's call.** Nothing here sets an amount.
2. **Tiers 2–4 are outside Public v1** by README's own scope list, including the
   words "API-only billing". Shipping them is a scope change the owner makes.
3. **Any durable identity on the search path** would break a non-negotiable
   principle and needs explicit owner approval. §4 is built to avoid needing it.
4. **Payment processing, tax and terms** need external accounts and legal review;
   [data-rights.md](data-rights.md) §7 already carries the open question of
   whether "commercial reuse allowed" plus a paid API needs an explicit
   no-exclusivity statement.

## 10. If accepted, what would have to be built

Not built here; this is the inventory the estimate would come from.

- A plan column and a usage counter attached to `identities`, which today has
  neither (`0001_init.sql:121`).
- A tier-aware limiter selection, extending the pattern `limitPublish` already
  establishes — resolve identity first, then choose the budget, keyed by login
  rather than address (`internal/httpapi/ratelimit.go`).
- Org accounts and seat mapping; nothing in the schema models a group today.
- Billing integration, and an admin surface for it.
- A doc regression test pinning "search and evidence need no account" across
  README and its translations, in the idiom `internal/daemon/localonlydoc_test.go`
  already uses — so a later pricing edit cannot quietly delete the promise §2
  depends on.

## 11. Open items

- Whether tier 2's unit should be sustained request rate or shard-sync freshness.
  Rate is easier to explain; freshness is closer to what a heavy consumer
  actually wants and is cheaper to serve.
- Whether an org tier should pool ceilings or grant per-seat ones. Pooling suits
  teams with uneven usage and is harder to abuse-bound.
- Whether sponsorship (README §Operating model) remains primary with tiers
  supplementing it, or tiers replace it. This proposal assumes the former and
  does not depend on the answer.
- Whether a paid tier changes anything about verification priority. It should
  probably not: verification order is an evidence-quality decision, and selling
  positions in it would put a thumb on the corpus.
