# Whole-site SEO opportunity map — #192, third wave (2026-09-19)

This continues [`opportunity-map-2026-09-05.md`](opportunity-map-2026-09-05.md).
That document holds the Search Console query/page evidence
(2026-08-06..2026-09-02) and the first wave of fixes; this one records the
indexing evidence gathered since, the second and third waves, the state of
every indexable route family as of production `v0.1.199` (`ebde5fc`), and
the comparison plan rebased to three deploy dates instead of one.

Nothing here claims uplift. Every number below is either a pre-change
measurement or a count of what production serves today.

## What the web is for

The public site is an acquisition and proof surface, not the product. Every
change in all three waves was judged by one test: does it help a search
engine or an LLM find, understand and cite CodeSampleX, and does the page it
lands on lead to installing `csx`? Checked on production today:

- the landing's only primary action is **Install the CLI** (`#install`); the
  agent-driven install prompt (`csx init`) is on the same page;
- `SoftwareApplication` JSON-LD names `csx`, free, Windows/macOS/Linux;
- evidence pages (package, release, symbol, sample) are indexable,
  self-canonical and carry the breadcrumb hierarchy from #395.

## Three waves

| deployed | PR | server | what changed |
|---|---|---|---|
| 2026-09-05 | #199 | — | package/release descriptions lead with the page's own facts; canonical is a function of the address, never of `Accept-Language`; "Verified" → "Published" samples; package `CollectionPage`+`ItemList` JSON-LD |
| 2026-09-14 | #395 | v0.1.179 `282df87` | `/samples` hub links the readable canonical, not the digest; `noindex` on 404/503 and on searched `/samples?q=`; empty seeder → 404; `/samples` + hreflang clusters in `static-1.xml`; 5-tier `BreadcrumbList` on every explorer tier |
| **D** (this PR) | — | — | `releases-1.xml` sitemap shard; `/llms.txt`; one product statement on the homepage `<meta name=description>`, `WebSite` JSON-LD, `skill.md` and `llms.txt`; `csx-seo-report` gains a `release` cohort |

**D** is the production deploy date of this PR. Fill it in on the issue
when `GET /version` reports the merged SHA, and every "D+n" below moves
with it.

## Indexing evidence — Search Console Pages report, 2026-09-14

Stored as [`index-coverage-2026-09-14.json`](index-coverage-2026-09-14.json).

| bucket | count | route family (inferred) | cause found | shipped |
|---|---:|---|---|---|
| Indexed | ~3,650 | — | — | — |
| Discovered – currently not indexed | 6,399 | samples (6,867 of 9,852 advertised URLs) | sample pages were orphan URLs: in the sitemap, linked from nowhere but their own release page | #395 — `/samples` hub cards and `ItemList` link the canonical readable URL |
| Crawled – currently not indexed | 2,694 | samples, releases | thin crawl context, no shared hierarchy across tiers | #395 — one `BreadcrumbList` on package, version, symbol, sample |
| Alternate page with proper canonical tag | 371 | `/samples/sha256:…` digest addresses | hub linked the digest, page canonicalised to the readable URL | #395 — hub links the canonical directly |
| Not found (404) | 12 | mixed | error pages indexable | #395 — `noindex, follow` on 404 |
| Page with redirect | 9 | `/records`, `/wanted`, `/stats`, `/adapters`, trailing slashes, bare Go versions | intentional 301s | none needed; they are not in the sitemap |
| Server error (5xx) | 2 | transient during builder passes | 503 pages indexable | #395 — `noindex` on 503 |
| Soft 404 | 1 | `/seeders/{login}` with zero samples | 200 with an empty body | #395 — 404 |

The recheck for this table is **2026-10-12** (28 days after the 09-14
deploy); see the plan below. Reading the Pages report earlier than that
tells you about Google's crawl queue, not about the fix.

## Route-family map, production 2026-09-19 (`v0.1.199`)

Counts are from the served sitemap (`X-Sitemap-Urls: 10837`, built
2026-09-19T11:52:57Z) and from spot checks of served pages.

| family | example | in sitemap | canonical | indexable | state |
|---|---|---:|---|---|---|
| landing cluster | `/`, `/ko/` … 9 locales | 9 | self, per address; full hreflang cluster | yes | ✅ head now states the product (this PR) |
| collection pages | `/compatibility`, `/findings`, `/samples`, `/gaps`, `/dependencies`, `/features` | 6 | self, drops every query parameter | yes | ✅ |
| filtered / paged / searched variants | `/compatibility?eco=npm&page=2`, `/samples?q=x` | 0 | the unfiltered page | searched `/samples?q=` is `noindex`; others canonicalise | ✅ |
| package hub | `/npm/axios` | 3,135 | self | yes | ✅ description leads with releases + sample count (#199) |
| **release** | `/npm/axios/1.19.0` | **0 → ~2,383 at D** | self; bare Go version 301s to `v`-prefixed | yes | 🚀 this PR: every release a published sample names, at zero extra store cost |
| release with evidence but no sample | `/npm/axios/1.11.0` (snapshot only) | 0 | self | yes | ⛔ gated — see below |
| symbol / API | `/npm/axios/1.19.0/get`, `?symbol=` form | 0 | self | yes | reachable from the release page; not advertised (one symbol page per API × release would triple the map for pages whose query demand GSC has not shown) |
| sample, readable | `/npm/robots-parser/3.0.1/samples/robotsparser-81f8b1e3` | 7,677 | self | yes | ✅ (#68, #395) |
| sample, digest-only | `/samples/sha256:…` (Go stdlib `net/http`, no routable package) | 10 | self | yes | ✅ correct: these name no release, so the digest is their only address |
| failure issue | `/npm/axios?issue={id}` | 0 | built from the path — the hub, so the issue view is one indexed page with its package | yes, as the hub | ✅ query variant of the hub; not a separate indexable page |
| agent documents | `/skill.md`, `/llms.txt`, `/findings.json` | 0 | — | plain text/markdown/JSON | ✅ `/llms.txt` was a 404 on production; served from D |
| error pages | any 404, 503 | — | — | `noindex, follow` | ✅ (#395) |
| retired addresses | `/records`, `/wanted`, `/contribute`, `/stats`, `/adapters` | 0 | — | 301 | ✅ |

Spot checks today (all HTTP 200, all `rel=canonical` equal to the
requested URL, no `robots` meta): 8 random `samples-1.xml` entries across
npm, golang and pypi; 4 of the 10 digest-only entries.

## Wave three — what shipped in this PR and why

### 1. Release pages join the sitemap (`releases-1.xml`)

The 09-05 map gated this: three of its five converting queries were release
lookups (`eslint 9.39.5`, `express 4.21.2 npm`, `axios 1.19.0`), yet no
`/{eco}/{name}/{version}` page was advertised, because the only per-release
store read was per-package — an N+1 on every 15-minute rebuild on a 2-vCPU
host.

The gate is lifted without that read. `versionPage` renders any release
that has at least one published sample (the rule that let Go modules
published as both `1.6.0` and `v1.6.0` keep their samples readable), and
the sitemap already reads every published sample. The distinct
`(ecosystem, name, version)` those rows name are therefore routable pages,
derived from data already in hand. On today's corpus that is **2,383**
releases (npm 1,464 · golang 562 · pypi 131 · cargo 126 · gem 32 ·
composer 22 · pub 21 · maven 15 · hex 10). `lastmod` is the newest sample
published against the release — the day the page last gained an answer.
The health log line and `X-Sitemap-Urls` count the section; `X-Sitemap-Urls`
should read ≈13,200 after D.

Expect the Pages report's "Discovered – currently not indexed" to **rise**
for a few weeks after D as these URLs enter the queue. That is the map
working, not regressing; the cohort to read is the release cohort below.

### 2. `/llms.txt`

Production answered 404. The file follows the llmstxt.org shape — H1, one
blockquote stating what the product is, then linked sections — and links
only pages this server serves, with the deployment origin substituted the
way `skill.md` and the installers are. Its test fetches every same-origin
link it names.

### 3. One product statement on every head surface

The homepage `<meta name="description">` and `WebSite` JSON-LD said
"CodeSampleX is an open compatibility testing network…" while the page body
(#204) said "Install csx to add a real execution experience layer to your
AI". A crawler met two products; a branded snippet quoted the old one. All
nine locales now carry the statement the body makes — upgrade pack for AI
coding agents, an execution memory of which package versions and APIs
actually built or failed, and where — within the 158-character budget in
English. `skill.md` opens with the same sentence. The FAQ copy and README
were left to #204's owner; they do not contradict the new statement.

### 4. `csx-seo-report` reads releases as their own cohort

`internal/seoreport` classified package hubs, releases and symbol pages as
one `package` class. A change made for release pages that is averaged into
that class cannot be falsified, so `release` is now its own cohort: a path
ending in a version segment (`v?N.N…`); a Go module's `/v5` suffix stays a
package path. Baselines written before this PR have no `release` cohort
row; regenerate them from the original CSV before comparing that cohort.

## Gated, not fixed

- **Releases with snapshot or symbol evidence but no published sample**
  are still not advertised. They render 200 and are linked from their
  package hub, so they are indexable; advertising them needs a bulk store
  read returning routable `(ecosystem, name, version)` rows with evidence
  attached. `sitemapHealth.releases` versus a future corpus count is where
  the gap will be visible.
- **Symbol pages** are not advertised. GSC has shown no symbol-shaped
  query demand; one entry per API × release would roughly triple the map
  for pages nobody has asked for yet. Revisit when the Queries report shows
  `package symbol` shapes landing on release pages.
- **Search Console access is human-only.** No agent on this project holds
  the property's credentials, and the OAuth connectors available to agents
  are not authorised. Every measurement step below is therefore an
  operator action; the agent's part is the report tool and this plan.

## Post-deploy comparison plan (rebased)

Three changes, three clocks. Do not read a cohort before its date; the
property is small (5,153 impressions in the 28-day baseline) and a week of
data is noise.

### Cohort A — snippet (waves 1): package and release pages

- Baseline: [`serp-baseline-2026-09-02.json`](serp-baseline-2026-09-02.json)
  (site row and the five query rows are established; regenerate from the
  original CSV for band rows).
- **2026-09-19 (today, 14 days)** — recrawl check only. In URL Inspection,
  `/npm/nanoid`, `/npm/axios/1.19.0` and `/` should report a
  self-referential Google-selected canonical, and "Duplicate, Google chose
  a different canonical" should have stopped accruing against `?lang=`
  URLs. *Not performed by the agent: needs the property.*
- **2026-10-03 (28 days)** — export Pages.csv and Queries.csv for
  2026-09-05..2026-10-02 and run

  ```
  go run ./cmd/csx-seo-report -pages Pages.csv -queries Queries.csv \
      -label "2026-09-05..2026-10-02 export" \
      -baseline docs/seo/serp-baseline-2026-09-02.json \
      -out docs/seo/serp-2026-10-02.json
  ```

  Working: the five named queries convert at all (0 → >0 clicks on ≈92
  impressions) and the `package` and `release` 4-10 bands' CTR rises.
  Falsified: positions and impressions hold and CTR does not move — then
  the snippet was not the binding constraint.

### Cohort B — indexing (wave 2): Pages report buckets

- Baseline: [`index-coverage-2026-09-14.json`](index-coverage-2026-09-14.json).
- **2026-10-12 (28 days after 09-14)** — read the Pages report and record
  the seven buckets beside the baseline. Working: "Alternate page with
  proper canonical tag" falls toward zero (the hub no longer links digest
  URLs); "Discovered – currently not indexed" falls for sample URLs
  (filter the bucket's URL list on `/samples/`); Soft 404 and 5xx stay at
  zero. Confounder: the release shard from wave 3 adds ≈2,400 URLs to the
  queue from D — read the sample-URL share of the bucket, not the total.

### Cohort C — release pages (wave 3)

- Baseline: zero release URLs advertised; the count of indexed release
  URLs on the day of D (Pages report, URL filter matching
  `^https://codesamplex\.dev/[a-z]+/.+/v?[0-9]+\.[0-9]+[^/]*$`).
- **D+14** — Sitemaps report: `releases-1.xml` shows Discovered ≈ its URL
  count. Crawl-only check.
- **D+28** — Pages.csv for D..D+27 through `csx-seo-report`; the `release`
  cohort row is the reading. Working: release-cohort impressions rise and
  the 4-10 band holds CTR; "Crawled – currently not indexed" does not grow
  by the number of release URLs. Falsified: the shard is Discovered but
  the indexed count and impressions of release URLs do not move by D+56 —
  then release pages need more than a sitemap entry (unique on-page
  evidence, internal links from samples up to their release).

### Cohort D — branded demand (the issue's mature-stage signal)

- Baseline: Queries.csv for the 28 days before D, filtered on the regex
  `codesamplex|code ?sample ?x|\bcsx\b` (case-insensitive). Record
  impressions and clicks for that set; today's 09-02 baseline holds no
  branded row, so the honest baseline is whatever that export says.
- Every 28 days from D: the same filter. Working: branded impressions and
  clicks grow, and branded queries land on `/` or `/features` rather than
  on a long-tail sample page. Referrals from LLM front-ends (`chatgpt.com`,
  `perplexity.ai`, `claude.ai`, `gemini.google.com`) are not in Search
  Console; if they are ever wanted, they need a referrer dimension the
  anonymous-analytics design deliberately does not record, and that is a
  Source decision, not an SEO task.

### Cohort E — machine readability (wave 3, non-GSC)

- `/llms.txt` and `/skill.md` answer 200 with the deployment origin in
  every link (`curl -s https://codesamplex.dev/llms.txt | grep -c
  codesamplex.dev`), and `<meta name="description">` on `/` and `/ko/`
  carries the new statement. Check once at D; a regression here is a
  build defect, not an SEO signal.

## Summary against the issue's acceptance list

- whole-site GSC opportunity map — this file + the 09-05 file + two stored
  baselines;
- highest-impact safe fixes shipped — three waves, last one in this PR;
- #68 reconciled — its sample-page work is referenced, not redone;
- technical indexing/canonical issues — fixed (canonical, hub links,
  noindex, soft 404, release shard) or explicitly gated (evidence-only
  releases, symbol pages);
- public web as acquisition — one CTA, one product statement, `/llms.txt`;
- LLM discovery/citation readiness — `/llms.txt`, `/skill.md`, consistent
  statement, stable self-canonical evidence URLs;
- post-deploy comparison plan — five cohorts, each with a baseline, a
  date, a working reading and a falsifying reading.
