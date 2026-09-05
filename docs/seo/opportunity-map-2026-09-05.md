# Whole-site SEO opportunity map — #192

Evidence window: **2026-08-06 .. 2026-09-02**, Google Search Console, whole
property. Totals: **5,153 impressions, 9 clicks, CTR 0.1747%, average
position 15.19**. Stored as a comparable baseline in
[`serp-baseline-2026-09-02.json`](serp-baseline-2026-09-02.json).

The shape of the problem is not ranking. Every query below already sits in
the first ten results and none of them converts. A page that Google shows to
fifty-four people at position seven and that nobody clicks is losing on the
result line, not in the index.

## Relationship to #68

#68 owns the **Sample detail** SERP surface and has shipped it: `serpcopy.go`
rebuilds sample titles and descriptions from package/release/API rather than
from an internal purl, and `semanticSampleHref` gives every sample a
human-readable canonical so the content-addressed URL and the readable one
are one indexed page. None of that is redone here.

What #192 adds is the route family one level up — the **package and release
pages**, which are what the queries in the table below actually asked for —
and the site-wide canonical rule that was quietly working against both.

## Evidence table

| query | route family | imp | clicks | pos | diagnosis | change shipped |
|---|---|---:|---:|---:|---|---|
| `nanoid npm` | `/npm/nanoid` — package | 54 | 0 | 7.31 | Description was one sentence with only the package name substituted in, identical across the whole corpus. Nothing in the snippet said what this page holds. | Description leads with the releases this page carries evidence for and the published sample count, then the generic sentence, capped at the rendered budget |
| `fastapi httpx2` | package / sample | 15 | 0 | 5.80 | Same generic snippet | Same |
| `eslint 9.39.5` | `/npm/eslint/9.39.5` — release | 9 | 0 | 5.33 | 223-char description whose first ~130 characters were corpus-wide boilerplate; the recorded environments and the sample count — the only distinguishing half — sat past Google's ~160-char cut and were never rendered | Facts first, generic after, truncated on a word boundary at `descriptionBudget` |
| `express 4.21.2 npm` | release | 8 | 0 | 8.88 | Same | Same |
| `axios 1.19.0` | release | 6 | 0 | 6.33 | Same | Same |

Verified against production before the change:

```
$ curl -s https://codesamplex.dev/npm/axios/1.19.0 | grep 'name="description"'
Real-environment compatibility evidence for axios@1.19.0: execution contexts,
confidence, failure clusters and samples that built. Recorded environments:
node 22 · node 24.13. Verified samples written against this release: 6.
```

## Technical findings

### 1. rel=canonical varied with a request header — fixed

`GET /npm/nanoid` with `Accept-Language: ko` answered, on production:

```
<link rel="canonical" href="https://codesamplex.dev/npm/nanoid?lang=ko">
```

The canonical was built from the *negotiated* language (`?lang=` → cookie →
`Accept-Language`), so one address declared different canonicals to different
callers. On every locale-adaptive crawl the canonical package page disavowed
itself in favour of its own query variant — which is the `?lang=`
cannibalization of package and version pages the issue reports.

The landing carried the worst instance of it: `GET /` with
`Accept-Language: ko` answered `canonical=https://codesamplex.dev/ko/`, so
the site's strongest URL handed its signal to a translation nobody had asked
for by name.

Fixed by making the canonical a function of the address and of nothing else
(`canonicalLangOf`, `basePage.canonicalURL`). Nothing is hidden: the page
still renders in the negotiated language, still advertises the full hreflang
cluster, and every `?lang=` URL is still indexable and still self-canonical.
The landing's cluster is path-prefixed, so `/?lang=ko` now resolves to `/ko/`
— the address the switcher, the hreflang cluster and the sitemap all name.

### 2. The release description claimed verification it did not have — fixed

`meta.verified_answers` counted every row `versionSamples` returns and called
them "Verified samples". `levelBadge` exists precisely because publication
does not imply a contract pass — `csx sample publish` requires no
`csx sample verify`, and a `POST /v1/samples` needs no receipt at all. The
badge on the page refuses that claim; the snippet that brings a reader to the
page was making it. Now "Published samples", in all nine locales.

### 3. Package pages had no structured statement of what they hold — fixed

The only JSON-LD on a package page was a `BreadcrumbList`, which says where
the page sits and nothing about the release pages it is the hub for. It now
also emits `CollectionPage` + `ItemList` over its releases, at the same
addresses the rows link to. No `Dataset`: that describes one measured
coordinate and belongs on the release page, which already has it.

### 4. Audited and left alone

- **hreflang**: bidirectional, 9 locales + `x-default`, matching each page's
  own canonical. Correct; the contradiction was in the canonical, not here.
- **robots.txt**: `Allow: /` plus the sitemap index. Correct — filter and
  page permutations are handled by canonicalisation, not by disallow, so
  their links still pass signal.
- **Filter / pagination / search URLs**: `s.page` drops every query parameter
  from the canonical, and the collection `ItemList` is emitted only on the
  unfiltered first page. Correct.
- **Sample pages**: #68's work. Not touched.
- **`/stats`, `/adapters`, `/records`, `/wanted`**: single-hop 301s, out of
  the sitemap. Correct.

### 5. Explicitly gated, not fixed

**Release pages are not in the sitemap.** The map advertises the landing
cluster, the collection pages, every package page and every sample page — but
no `/{eco}/{name}/{version}` page, even though three of the five queries above
are release lookups. They are indexed today only through the package page's
internal links.

Not fixed here on purpose. Building that section needs the release list for
every package, and the only store call that returns it
(`ListPackageVersions`) is per-package — an N+1 read on every 15-minute
sitemap rebuild, on the same 2-vCPU host #190/#193/#195 just finished
clearing slow paths off. `PackageHit.LatestVersion` would be one cheap entry
per package, but `versionPage` 404s a release with no symbols, no matrix and
no samples, and the sitemap's own rule is that it may not advertise a 404.

The prerequisite is a bulk store read that returns routable `(ecosystem,
name, version)` rows with evidence attached. Tracked for the follow-up rather
than guessed at.

## Post-deploy comparison plan

**Change deployed: 2026-09-05.** Nothing below may be read as uplift before
the dates given.

Search Console needs a full recrawl plus a settled window before any of this
is measurable, and the property is small enough (5,153 impressions in 28
days) that a week of data is noise. So:

- **2026-09-19 (14 days)** — recrawl check only, no CTR reading. Confirm in
  the URL Inspection tool that `/npm/nanoid`, `/npm/axios/1.19.0` and `/` now
  report a *self-referential* Google-selected canonical, and that
  "Duplicate, Google chose a different canonical" has stopped accruing
  against `?lang=` variants in the Pages report.
- **2026-10-03 (28 days, the matched cohort)** — the real comparison. Export
  Pages.csv and Queries.csv for **2026-09-05..2026-10-02** and run:

  ```
  go run ./cmd/csx-seo-report -pages Pages.csv -queries Queries.csv \
      -label "2026-09-05..2026-10-02 export" \
      -baseline docs/seo/serp-baseline-2026-09-02.json
  ```

  Regenerate the baseline from the original CSV first if the cohort and band
  rows are wanted — the stored file is `partial: true` and only its site row
  and query rows are established.

What would count as the fix working, per band rather than in aggregate
(`seoreport` compares per band for a reason — a page that fell from 4 to 12
loses clicks for reasons that have nothing to do with its snippet):

- the five named queries hold position and start converting at all — from
  0 clicks on 92 impressions to anything non-zero;
- package and release cohort CTR rises inside the 4-10 band specifically;
- `?lang=` URLs stop appearing as separate rows for the same query as their
  canonical page.

What would falsify it: positions hold, impressions hold, and CTR does not
move by 2026-10-03. That would say the snippet was not the binding
constraint on these routes and the next lever is the page itself, not its
description.
