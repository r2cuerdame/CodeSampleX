# Compatibility-Matrix Repositioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reposition the CodeSampleX homepage and README as a *Developer Compatibility Testing Network* ("Tested. Not guessed.") — matrix-first, CLI-first, MCP-as-adapter — using only data the backend already serves.

**Architecture:** All changes live in `internal/web` (Go server-rendered templates + one CSS file + 9 locale JSON catalogs), `README.md`, and `docs/i18n/README.*.md`. A new pure pivot engine turns existing snapshot JSON (`Store.SnapshotJSON`) into OS × version grids; the landing page renders it from the already-fetched-but-unrendered `HotPackages` data. **No change to `internal/web.Store`, no change to any package outside `internal/web`, no API/schema/worker/pipeline change.**

**Tech Stack:** Go 1.x `html/template`, embedded static CSS, table-driven Go tests, no JS framework (native `<details>`/GET forms only).

**Spec:** `reneware.md` (repo root) + user addendum: (1) the landing matrix features the most widely used / most-sampled packages, (2) package detail pages are matrix-based down to *symbol × OS*, (3) anomalies are co-marked with `!` and `?` glyphs.

## Global Constraints

- Backend untouched: nothing outside `internal/web/`, `README.md`, `docs/i18n/` may change (exception: none anticipated; report first if one appears).
- `internal/web.Store` interface must NOT change — only these existing methods may be used: `LatestStatsJSON, SnapshotJSON, PackageVersions, PackageSymbols, SampleMeta, SampleManifest, SampleReceipts, SeederSamples, ListSamples, PackageSamples, SearchPackages, HotPackages, RecordPackages, FailureClusters, TopWanted, WantedRows, WantedForPackage, DerivedFindings`.
- Observations and verifications are NEVER summed (goal.md §3.5); a compile pass is never presented as a symbol working.
- Every new `{{.T "key"}}` needs an entry in ALL NINE locale files `internal/web/i18n/locales/{en,ko,ja,zh-CN,es,fr,de,pt-BR,ru}.json` — `TestI18nLocaleCompleteness` and `TestEveryTemplateKeyExists` enforce it; no empty values.
- Only real CLI commands may appear anywhere: `config, daemon, init, login, mcp, mcp-config, run, sample, sample-worker, scan, search, stats, sync, ui, update, version, worker` (16, verified in `internal/cli`).
- Cell/chip data values (PASS, FAIL, HIGH, MEDIUM, node 22, linux) stay English/mono — the i18n package doc says data values are never translated.
- Homepage keeps exactly three counters (Packages / Evidence / Verified Samples) — `landing_honesty_test.go` guards this; subtitles may be added, a fourth counter may not.
- New hero copy (en, verbatim): tagline `Does it run there?`; lead `CodeSampleX is an open compatibility testing network: it runs real builds and contract tests across real developer environments and records what actually happened. Tested. Not guessed.`
- Marker semantics (everywhere a pivot cell renders): `!` = measured anomaly (elevated failure or a FAIL among verifications), `?` = weak/uncertain evidence (observation-only, LOW confidence, or stale). STALE = `lastSeen` older than 90 days (mirrors the 90-day `RecencyDecay` half-life in `internal/compatibility/confidence.go`).
- Verification: `go build ./... && go vet ./internal/web/... && go test ./internal/web/... ./internal/web/i18n/...` then full `go test ./...`. Prefer csx MCP `run_observed_command` if the csx MCP server is reachable this session; plain `go` otherwise.

---

### Task 1: Pivot engine (`internal/web/pivot.go`)

**Files:**
- Create: `internal/web/pivot.go`
- Test: `internal/web/pivot_test.go`

**Interfaces:**
- Consumes: `snapshotRow`, `snapshotDoc`, `stageCount` (already in `internal/web/explorer.go`), `domain.EnvironmentFingerprint`.
- Produces (used by Tasks 2–4):

```go
// pivotCell is one (row, column) verdict of a pivoted compatibility grid.
type pivotCell struct {
    State   string // "PASS" | "FAIL" | "MIXED" | "OBSERVED" | "" (empty = no evidence)
    Class   string // "pass" | "fail" | "mixed" | "observed" | "empty"
    Glyph   string // "✓" | "✕" | "◐" | "○" | "—"
    Bang    bool   // render " !": elevated failure or any verification FAIL
    Maybe   bool   // render " ?": observation-only, LOW confidence, or stale
    Stale   bool
    Cross   bool   // ≥2 distinct verifying peers → cross-checked
    Href    string // drill-down link ("" = plain cell)
    Tip     string // title attribute: full evidence detail, English data values
    Obs     int64  // observation events (USED/PROJECT_*)
    Ver     int64  // verification events (SYMBOL_*/RESOLVE..CONTRACT)
}

// pivotGrid is a rendered OS × context grid.
type pivotGrid struct {
    Cols     []string              // column labels, version-sorted ("node 20", "node 22")
    Rows     []pivotGridRow
    HasBang  bool                  // any cell carries "!" (legend shows the marker note)
    HasMaybe bool
}
type pivotGridRow struct {
    Label string      // "linux", "linux musl", "windows", "macos"
    Cells []pivotCell // one per Cols entry, aligned by index
}

// buildPivot pivots snapshot rows into rowKey × colKey cells.
// rowKey/colKey choose the axes; cellHref may be nil (no links).
func buildPivot(rows []snapshotRow, rowKey, colKey func(r snapshotRow) string,
    cellHref func(row, col string) string, now time.Time) pivotGrid

// osRowKey buckets a snapshot row by OS (+libc when set): "linux musl".
// Returns "" (skip) when the env has no OS.
func osRowKey(r snapshotRow) string

// contextColKey buckets by runtime/browser line + MAJOR version: "node 22".
// Falls back to the row's ContextLabel; "" when nothing is known.
func contextColKey(r snapshotRow) string
```

Aggregation rules (binding):
- A cell may absorb several snapshot rows. Per cell: `verPass/verFail` from non-observation stages, `obsPass/obsFail` from `USED`/`PROJECT_*` stages (reuse `splitStageCounts`'s stage classification — extract the stage-class predicate into `isObservationStageName(stage string) bool` so both share it).
- State: `verPass>0 && verFail==0` → PASS(✓). `verFail>0 && verPass==0` → FAIL(✕). both → MIXED(◐). `ver==0 && obs>0` → OBSERVED(○) with `Maybe=true`. none → empty(—).
- `Bang` = any absorbed row has `ElevatedFailure` OR `verFail>0` (also on PASS-dominant cells absorbing a FAIL... impossible by state rule — so effectively MIXED/FAIL cells and elevated observation cells).
- `Maybe` = observation-only OR max confidence across rows is LOW/empty OR stale.
- `Stale` = newest `LastSeen` across absorbed rows older than `now - 90*24h` (missing LastSeen ⇒ not stale, but Maybe if it is also observation-only).
- `Cross` = any absorbed row `VerificationCounts["distinctVerifyingPeers"] >= 2`.
- `Tip` (English, mono data): `"<obs> observed · <ver> verified · pass <rate%> · <CONF> · last seen <date>"` — omit fragments with no data; append `" · cross-checked"` when Cross, `" · stale"` when Stale.
- Column sort: group by line name (node, python…), numeric-descending by major within a line, lines alphabetical. Row sort: alphabetical with `linux` first, then `macos`/`darwin`, then `windows`, then the rest (fixed familiar order: linux, macos, windows, others alphabetical). Map `darwin` → display `macos`.
- Cap: at most 8 columns and 6 rows (drop lowest-evidence extras, count dropped in `pivotGrid` NOT needed — instead never silently drop: prefer merging by taking the highest-evidence columns and note is unnecessary because detail tables below show everything).

- [ ] **Step 1: Write failing tests** in `internal/web/pivot_test.go` (package `web`), table-driven:

```go
func TestPivotSeparatesObservationFromVerification(t *testing.T) {
    // one row: PROJECT_COMPILE 10✓ + CONTRACT 2✓, os=linux, runtime node 22
    // → single cell State PASS, Obs 10, Ver 2 — never 12 of anything.
}
func TestPivotObservationOnlyCellIsMarkedUncertain(t *testing.T) {
    // PROJECT_COMPILE only → State OBSERVED, Maybe true, Glyph "○"
}
func TestPivotVerificationFailureIsMarkedBang(t *testing.T) {
    // CONTRACT 1✓ 1✕ → State MIXED, Bang true
}
func TestPivotStaleCellIsMarkedMaybe(t *testing.T) {
    // lastSeen 91 days before now → Stale true, Maybe true
}
func TestPivotCrossCheckedNeedsTwoPeers(t *testing.T) {
    // distinctVerifyingPeers 2 → Cross true; 1 → false
}
func TestPivotAxisBucketing(t *testing.T) {
    // node 22.18 + node 22.4 → one "node 22" column; linux+musl → "linux musl" row
}
func TestPivotColumnOrderIsVersionDescendingWithinLine(t *testing.T) {}
func TestPivotSkipsRowsWithoutTheAxis(t *testing.T) {
    // env without OS contributes to no row (osRowKey "")
}
```
Each test hand-builds `snapshotRow` values (see `envrow_test.go` for the existing construction style) and calls `buildPivot(rows, osRowKey, contextColKey, nil, fixedNow)`.

- [ ] **Step 2: Run tests, verify they fail** — `go test ./internal/web/ -run TestPivot -v` → compile error (`buildPivot` undefined).
- [ ] **Step 3: Implement `internal/web/pivot.go`** per the rules above; extract `isObservationStageName` and reuse it in `splitStageCounts`.
- [ ] **Step 4: Run tests, verify pass** — `go test ./internal/web/ -run TestPivot -v` AND `go test ./internal/web/` (splitStageCounts refactor must not break `explorer_test.go`).
- [ ] **Step 5: Commit** — `feat(web): pivot engine for OS × version compatibility grids`

### Task 2: Landing featured-matrix data (`internal/web/landing.go`)

**Files:**
- Modify: `internal/web/landing.go` (handler + new `heroMatrix` builder)
- Modify: `internal/web/web.go` (`site` struct: add `heroMu sync.Mutex; heroCache *heroMatrixData; heroAt time.Time` following the `derived*` cache pattern)
- Modify: `internal/web/fake_test.go` only if the fake lacks needed fixtures (it already implements the full Store)
- Test: `internal/web/landing_matrix_test.go`

**Interfaces:**
- Consumes: `Store.HotPackages`, `Store.PackageVersions`, `Store.SnapshotJSON`, `Store.ListSamples`, Task 1's `buildPivot/osRowKey/contextColKey`, `pivotGrid`.
- Produces (consumed by Task 3's template):

```go
type heroTab struct {
    Label    string // "axios · npm"
    Href     string // "/?m=npm%2Faxios" (WithLang applied in template via .WithLang)
    Selected bool
}
type heroMatrixData struct {
    Package  string    // "axios"
    Eco      string    // "npm"
    Version  string    // "1.12.2" — the latest version with a snapshot
    Href     string    // "/npm/axios/1.12.2" — "view full report"
    Grid     pivotGrid
    Tabs     []heroTab
    Generated string   // snapshot generatedAt date part
}
// landingPage gains: Matrix *heroMatrixData, VerifiedSamples []SampleListItem
```

Behavior (binding):
- `?m=<eco>/<name>` selects a package **only if it is in the current `HotPackages(12)` list** (guards against arbitrary-store-read URLs); invalid `m` falls back to default.
- Default featured package: first hot package whose latest version yields a non-empty grid; try at most the first 6 hot packages; each try = `PackageVersions` (use `[0]`) + `SnapshotJSON(purl,"")`.
- Default result cached 5 minutes behind `heroMu` (pattern: `derivedCache`); explicit `?m=` selections are built per-request (still ≤2 store reads, webstore snapshot cache 30s absorbs them).
- Tabs: first 6 hot packages, current selection marked.
- Cell Href: `func(row, col string) string` → version page `versionHref(eco,name,ver)` (all cells share the drill-down; per-cell env anchors come in Task 4's symbol page, not here).
- `VerifiedSamples`: `ListSamples(ctx, 24)` filtered to status ∈ {`CROSS_PASS`,`MATRIX_PASS`,`STABLE`}, first 3; if fewer than 3 verified exist, top up with the newest remaining samples so the section never renders empty while samples exist at all.
- Nil-safety: no stats, no hot packages, no snapshots → `Matrix` nil → template omits the section (landing must render fine on an empty store — existing tests demand it).

- [ ] **Step 1: Write failing tests** in `landing_matrix_test.go`:

```go
func TestLandingRendersHotPackageMatrix(t *testing.T) {
    // fake store: hot package npm/axios, versions ["1.12.2"], snapshot with
    // linux/node22 CONTRACT 2✓ and windows/node24 CONTRACT 1✕ rows.
    // GET / → body contains "node 22", "node 24", "linux", "windows",
    // a PASS cell linking /npm/axios/1.12.2, and a FAIL cell with "!".
}
func TestLandingMatrixSelectionIsBoundedToHotPackages(t *testing.T) {
    // GET /?m=npm/otherpkg (not hot) → falls back to default matrix,
    // and the store records no PackageVersions call for otherpkg.
}
func TestLandingSkipsMatrixWhenNoSnapshots(t *testing.T) {
    // empty store → page 200, no <table class="pivot"> present.
}
func TestLandingShowsVerifiedSamplesFirst(t *testing.T) {
    // ListSamples returns PUBLISHED newer + CROSS_PASS older → the
    // CROSS_PASS sample appears in the home samples section.
}
```
- [ ] **Step 2: Run, verify fail** — `go test ./internal/web/ -run TestLanding.*Matrix -v`.
- [ ] **Step 3: Implement** builder + handler wiring (template renders in Task 3; for this task's tests to pass, Task 3's template section must exist — implement Tasks 2 and 3 as one commit if needed, but keep the Go-side tests written first).
- [ ] **Step 4: Run tests, verify pass.**
- [ ] **Step 5: Commit** — `feat(web): landing hero compatibility matrix from hot packages`

### Task 3: Landing template + CSS + en/ko locale keys

**Files:**
- Modify: `internal/web/templates/landing.html` (full IA reorder), `internal/web/templates/base.html` (new `{{define "pivot"}}` + tiles `Sub` line), `internal/web/static/site.css` (pivot table + hero + ladder styles), `internal/web/landing.go` (`statTile` gains `Sub string`; `buildTiles` fills it), `internal/web/i18n/locales/en.json`, `internal/web/i18n/locales/ko.json`
- Modify: `internal/web/seo.go` (landing JSON-LD/FAQ copy to match the new claim), `internal/web/i18n/i18n_test.go` (`TestI18nTagline` asserts the new tagline)
- Modify: existing landing tests that assert the old order (`llminstall_test.go`, `landing_honesty_test.go` untouched unless the 3-counter markup moves, `seo_test.go` if FAQ text changes)
- Test: `internal/web/landing_ia_test.go`

New landing IA (top→bottom, binding):
1. **Hero** — h1 `landing.tagline` = `Does it run there?`; lead `site.meta_description` (new value = Global Constraints hero lead); sub-line `landing.tested_line` = `Tested across real environments. Not guessed from documentation.`; three stat tiles WITH meaning subtitles (`stats.packages_sub` = `coverage`, `stats.evidence_sub` = `measured observations`, `stats.verified_samples_sub` = `usable answers`). The search form MOVES out of the hero into a small bar under the matrix (`landing.matrix_search_hint`). Primary hero action: anchor to `#matrix` (`landing.see_matrix` = `See the compatibility map`); secondary: `#install` (`landing.cli_install` = `Install the CLI`).
2. **`<section id="matrix">` Compatibility Matrix** — heading `landing.matrix_heading` = `Where it actually ran`; package tabs; `{{template "pivot" ...}}`; footer row: legend link (`#evidence`), "Full report →" (Matrix.Href), snapshot date; then the demoted search form.
3. **Findings** — existing section, heading key `landing.findings_heading` REVALUED to `Where does it break?` (sub stays believed/measured framing).
4. **Verified samples** — new section, heading `landing.samples_heading` = `Then how do I use it?`, sub `landing.samples_sub` = `Every answer here ran in a pinned container and passed its own assertions before it was published.`; renders the 3 `VerifiedSamples` via existing `{{template "samplelist"}}`.
5. **`<section id="evidence">` Evidence ladder + legend** — static; five ladder steps mapping REAL grades: Observed (`USAGE_OBSERVATION` — build/test results from real projects), Verified (`SAMPLE_VERIFICATION` / L3 contract pass in a pinned container), Cross-checked (`CROSS_PASS` — an independent peer re-ran it), Multi-environment (`MATRIX_PASS` — ≥2 OS/runtime boundaries), Stable (`STABLE` — ≥3 peers, no failure for 30 days). Legend block explains `✓ ◐ ○ ✕ — ! ?` and STALE. Keys `landing.ladder_*`, `landing.legend_*`.
6. **Install (CLI-first)** — manual install commands FIRST (heading `landing.install_heading` = `Install the tester`), then `landing.cli_can_heading` = `What the CLI does` with four real one-liners: `csx run -- npm test` (`landing.cli_can_run` = `wrap any build — its result becomes evidence`), `csx search "axios multipart upload"`, `csx scan`, `csx stats`. THEN the agent path as a demoted `<details>` (`landing.agents_heading` reworded `Secondary: agent integration (MCP adapter)` — key `landing.agents_heading` revalue), keeping the LLM dialog + `csx mcp-config` untouched inside.
7. **Differentiation strip** — small static 4-liner `landing.diff_docs/code/community/csx` = `Documentation — what should work` / `Code search — how somebody used it` / `Community — what somebody says worked` / `CodeSampleX — what was actually tested`.
8. Keep: ecosystem support block, how-it-works `<details>` (`landing.for_agents` revalue to de-center MCP: `Your coding agent can consume the same network through the MCP adapter; the CLI and web report read identically.`), contract `<details>` unchanged.

`{{define "pivot"}}` (base.html) renders `pivotGrid`: `<div class="tablewrap"><table class="pivot">` — header row = Cols; each body row = Label + cells `<td class="pv {{.Class}}">` containing `<a>`/`<span>` with `<span class="glyph">{{.Glyph}}</span>{{.State}}{{if .Bang}} <b class="mark bang">!</b>{{end}}{{if .Maybe}} <b class="mark maybe">?</b>{{end}}{{if .Cross}}<span class="cross" title="cross-checked">✓✓</span>{{end}}` and `title="{{.Tip}}"`. Empty cells render `—` dim. Never color-only: state word + glyph always present.

CSS (site.css append, using existing custom properties): `.pivot th/.pivot td` mono compact; `.pv.pass` tint `color-mix`-free (use existing --ok/--bad/--mid with low-opacity backgrounds via rgba equivalents consistent with current palette); `.mark.bang{color:var(--bad)}`, `.mark.maybe{color:var(--mid)}`; `.hero-matrix` grid layout; `.ladder` numbered rail; `.stat .sub` small dim line; tabs `.mtabs a[aria-current="true"]`.

- [ ] **Step 1: Write failing IA tests** in `landing_ia_test.go`:

```go
func TestLandingHeroLeadsWithTheQuestion(t *testing.T) {
    // GET / → "Does it run there?" appears before any "MCP" occurrence
    // and before the install commands.
}
func TestLandingMatrixPrecedesInstall(t *testing.T) { /* index compare */ }
func TestLandingEvidenceLadderNamesRealGrades(t *testing.T) {
    // body contains CROSS_PASS, MATRIX_PASS, STABLE and the observed/verified split note
}
func TestLandingCLIIsPrimaryAgentIsSecondary(t *testing.T) {
    // "csx run" appears before "csx mcp-config"; the agents block is a <details>
}
func TestLandingSearchIsNotInHero(t *testing.T) {
    // the <form class="home-search"> appears after the matrix section start
}
```
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** templates + CSS + `en.json`/`ko.json` keys (full list of new/revalued keys — implement exactly; other 7 locales in Task 5): new `landing.tested_line, landing.see_matrix, landing.cli_install, landing.matrix_heading, landing.matrix_search_hint, landing.matrix_full_report, landing.samples_heading, landing.samples_sub, landing.ladder_heading, landing.ladder_observed, landing.ladder_verified, landing.ladder_cross, landing.ladder_multi, landing.ladder_stable, landing.legend_heading, landing.legend_pass, landing.legend_fail, landing.legend_mixed, landing.legend_observed, landing.legend_empty, landing.legend_bang, landing.legend_maybe, landing.legend_stale, landing.legend_cross, landing.cli_can_heading, landing.cli_can_run, landing.cli_can_search, landing.cli_can_scan, landing.cli_can_stats, landing.diff_docs, landing.diff_code, landing.diff_community, landing.diff_csx, stats.packages_sub, stats.evidence_sub, stats.verified_samples_sub, pivot.col_env`; revalued `landing.tagline, site.meta_description, landing.findings_heading, landing.agents_heading, landing.for_agents`. Update `TestI18nTagline` and any seo/llminstall assertions that pinned old copy.
- [ ] **Step 4: Run** `go test ./internal/web/...` — expect ONLY `TestI18nLocaleCompleteness`/`TestEveryTemplateKeyExists` failures for the 7 untranslated locales (Task 5); everything else green. If practical, stub the 7 locales with correct translations immediately instead of leaving red (preferred: do Task 5's key work in the same commit to keep CI-green).
- [ ] **Step 5: Commit** — `feat(web): matrix-first landing — tested-not-guessed positioning`

### Task 4: Matrix-based package / version / symbol pages

**Files:**
- Modify: `internal/web/explorer.go` (three handlers + page structs), `internal/web/templates/package.html`, `version.html`, `symbol.html`, `internal/web/i18n/locales/en.json` + `ko.json` (keys below)
- Test: `internal/web/explorer_pivot_test.go`

Behavior (binding):
- **Symbol page** (`/npm/axios/1.12.2/axios.post`): above the existing detail table, render `{{template "pivot"}}` of the SAME snapshot pivoted OS × context (`buildPivot(doc.Rows, osRowKey, contextColKey, nil, now)`); cells anchor `#env-detail` (set `Href` to `"#env-detail"` for non-empty cells); existing detailed matrix table gets `id="env-detail"` and stays as the drill-down. Skip the pivot when it would have <2 rows AND <2 cols (a 1×1 pivot adds nothing over the detail table).
- **Version page** (`/npm/axios/1.12.2`): new "Symbols × OS" grid replacing the plain symbol link list as the primary element (keep the link list below for symbols beyond the cap): for the first `versionSymbolPivotLimit = 10` symbols, `SnapshotJSON(purl, symbol)`; grid rows = symbols (linked to symbol page), cols = OS union. Implemented with a second axis pair: rowKey = constant symbol name (build per-symbol then merge), i.e. a dedicated helper `buildSymbolOSGrid(ctx, store, purl, eco, name, ver, symbols, lang, now) pivotGrid` in explorer.go that calls `buildPivot` per symbol with `osRowKey` as COLUMN key and collapses each symbol's cells row-wise. Cells link to the symbol page. When >10 symbols, note `version.more_symbols` (count) above the full link list. Package-level env matrix stays below (`version.env_heading`).
- **Package page** (`/npm/axios`): new "Versions × OS" overview above samples: for the first `packageVersionPivotLimit = 6` entries of `PackageVersions`, `SnapshotJSON(purl@v, "")`; rows = versions (linked to version pages), cols = OS union; cells state-aggregated exactly like other pivots. Versions without snapshots render as empty cells (still listed — absence is honest data). Existing versions list stays (full enumeration).
- All three pages append a compact shared legend line (`{{template "pivotlegend" .}}` in base.html: glyph/marker explanations reusing the `landing.legend_*` keys).
- New keys: `pkg.matrix_heading` = `Compatibility by version and OS`, `version.matrix_heading` = `Which symbol ran where`, `version.env_heading` = `Environment detail`, `version.more_symbols` = `+ %s more symbols below`, `symbol.pivot_heading` = `OS × runtime at a glance`, `symbol.env_detail` = `Environment detail` (reuse where sensible).

- [ ] **Step 1: Write failing tests** in `explorer_pivot_test.go`:

```go
func TestSymbolPageShowsOSPivotAboveDetail(t *testing.T) {
    // snapshot with linux/node22 pass + windows/node22 fail →
    // pivot table present, FAIL cell carries "!", anchor #env-detail exists
}
func TestVersionPageSymbolByOSGrid(t *testing.T) {
    // two symbols with different OS results → grid rows are symbol links,
    // columns are the OS union, cells link to the symbol pages
}
func TestPackagePageVersionByOSGrid(t *testing.T) {
    // two versions, one with a snapshot → grid renders; the snapshotless
    // version row is present with empty cells
}
func TestVersionPageCapsSymbolPivot(t *testing.T) {
    // 12 symbols → 10 pivot rows + "more symbols" note + full list below
}
```
- [ ] **Step 2: Run, verify fail.** — [ ] **Step 3: Implement.** — [ ] **Step 4: Run `go test ./internal/web/...`, verify pass (same locale-test caveat as Task 3).**
- [ ] **Step 5: Commit** — `feat(web): matrix-based package, version and symbol pages`

### Task 5: Locale catalogs ×9 complete

**Files:**
- Modify: `internal/web/i18n/locales/{ja,zh-CN,es,fr,de,pt-BR,ru}.json` (+ en/ko if any key from Tasks 3–4 is still missing)

- [ ] **Step 1:** Enumerate every key added/revalued in Tasks 3–4 from `en.json` git diff.
- [ ] **Step 2:** Translate each into the 7 remaining locales (native quality, full diacritics; keep data values — command names, PASS/FAIL, grade names — in English; follow each catalog's existing tone). Fan out one workflow agent per locale if run under orchestration; otherwise translate inline.
- [ ] **Step 3:** `go test ./internal/web/i18n/... ./internal/web/ -run 'TestI18n|TestEveryTemplateKey|TestNoForeignScript' -v` → PASS.
- [ ] **Step 4: Commit** — `feat(web): nine-locale catalog for the matrix-first landing`

### Task 6: README.md rewrite

**Files:**
- Modify: `README.md`

Structure (binding, reneware ordering; all commands/claims verified against Task-0 research):
1. `# CodeSampleX` — tagline blockquote `> **Tested. Not guessed.**`; hero image kept; Languages row kept; first paragraph verbatim: `CodeSampleX is an open compatibility testing network for developer libraries, runtimes and toolchains. It does not summarize documentation and it does not collect anecdotes: it runs real builds and contract tests in real, recorded environments — then shows you where things actually worked, where they broke, and how sure it is of both.`
2. `## Does it run there?` — the question list (OS/runtime/version) + an ASCII matrix example. Use the axios example the site itself uses (`axios.post · axios 1.12.2 · Node 22/24 · Linux and Windows`); ATTEMPT to fetch live values (`curl https://codesamplex.dev/v1/registry/packages/pkg:npm%2Faxios@1.12.2`) and embed real PASS/FAIL cells with a "measured <date>" line + link to the live page; if unreachable, label the matrix explicitly as "the shape of an answer" with a link to the live matrix, never invented values.
3. `## Why testing matters` — docs/code-search/community/CodeSampleX differentiation block (same 4 lines as the landing strip); the honesty principles (compile ≠ works, UNKNOWN stays UNKNOWN, NO_SAFE_MATCH is an answer).
4. `## Install the CLI` — existing real one-liners + `csx init` contract question + existing caveat block (curl/CA/PATH), trimmed.
5. `## Check and use` — real workflow: `csx run -- <build>`, `csx search`, `csx scan`, `csx stats`, `csx ui`, `csx sync`.
6. `## Verified samples` — sample statuses + `csx sample propose/create/preview/verify/publish` clean-room loop, typed-`yes` publish, leakage hard-refuse.
7. `## Findings` — believed vs measured, live /findings link, derived findings from `believed` fields.
8. `## Evidence and grading` — evidence classes ladder, L0–L5, confidence HIGH/MEDIUM/LOW rules, statuses CROSS_PASS/MATRIX_PASS/STABLE, 90-day decay, observed/verified never summed; stats JSON table + existing anti-claims kept verbatim.
9. `## Contributor worker` — condensed current section (Docker-only, budgets, receipts).
10. `## API` — real public endpoints table (stats, search, registry, shards, samples, wanted, adapters).
11. `## Agent adapter (MCP)` — SHORT: one paragraph framing MCP as the adapter agents use to consume the network; `csx mcp-config`; the 8 tool names in one line; link `llms-install.md` + MCPB. Explicit architecture snippet: CLI primary / API automation / Web report / MCP adapter.
12. `## The contract` — privacy block verbatim (ASCII table) + sanitizer facts.
13. `## Architecture`, `## Building from source`, `## License` — condensed current content.

- [ ] **Step 1:** Rewrite README.md fully per skeleton; every command checked against the Task-0 CLI list; every claim traceable to code/docs findings.
- [ ] **Step 2:** Self-review against reneware.md `Do Not Do` list (no invented features, MCP not top, no mock-as-measured).
- [ ] **Step 3: Commit** — `docs: reposition README as compatibility testing network`

### Task 7: i18n READMEs ×8

**Files:**
- Modify: `docs/i18n/README.{ko,ja,zh-CN,es,fr,de,pt-BR,ru}.md`

- [ ] **Step 1:** Translate the NEW root README per locale (full structure parity — these are currently two revisions stale; this task also fixes that drift). Keep code blocks/commands/ASCII tables in English, translate prose; keep relative links (`../../README.md` style) and the hero image reference. Fan out one workflow agent per locale when orchestrated.
- [ ] **Step 2:** Spot-check ko + one other locale for structure parity (same heading count/order as root).
- [ ] **Step 3: Commit** — `docs: retranslate all eight README locales`

### Task 8: Full verification + review

- [ ] **Step 1:** `go build ./... ` then `go vet ./internal/web/...` then `go test ./...` — ALL green (via csx `run_observed_command` when available).
- [ ] **Step 2:** Render-inspect: run the server or use `dump_for_review_test.go`-style output to eyeball the landing HTML; verify acceptance criteria (5-second identity, matrix visible, `!`/`?` markers, CLI-first, MCP demoted).
- [ ] **Step 3:** Adversarial review pass (code-review workflow): correctness of pivot aggregation (obs/ver separation!), template escaping, i18n completeness, README claim-vs-code audit.
- [ ] **Step 4:** Fix findings; re-run tests; final commit.

## Amendment A (user, mid-implementation): Compatibility Cube Explorer

The 2D matrix is a VIEW; the data is an N-dimensional cube. Requirements:
- Package pages become an **N-dimensional explorer**: X Axis ▼ / Y Axis ▼ selectors on top; every dimension not on an axis is a filter chip (`os=windows`, `runtime=node 22`, …). Clicking a cell pins one more filter per axis value and drills into the next slice.
- Aggregated cells show depth honestly: `15/18` verified passes (ratio + the pct already in the tooltip), not a flat PASS, whenever the slice hides variation.
- The final drill-down (≤1 varying combination left) renders the **exact contract** leaf: full environment facts, stage verdicts, counts, confidence, last seen, links to the symbol detail page and the package's samples/failure clusters.

### Task A1: cube engine (`internal/web/cube.go`)
- `cubeFact` = one snapshot row tagged with its source dims: `version`, `symbol` (`""` → shown as `(package)`), plus bucketed env dims `os`, `arch`, `runtime` (line+major), `tool` (packageManager), `context` (executionContext/browser), `libc`; carries the same agg fields as `pivotAgg`.
- `loadCubeFacts(ctx, store, eco, name)`: versions cap 6 (newest), per version `PackageSymbols` cap 10 + the package-level symbol `""` snapshot; per (version, symbol) `SnapshotJSON`. Site-level cache: 5-min TTL, ≤64 packages, `cubeMu`.
- `buildCubeGrid(facts, x, y, href, now) pivotGrid` reusing `pivotAgg`/`buildPivotCell`/axis sorters; dim-aware ordering (version axis: newest first).
- `pivotCell` gains `Ratio string` (`"15/18"` when ver>1, else obs>1 observation ratio).
- Axis/filter model: query params `x`, `y`, `f_<dim>=<value>`; default axes `os × runtime`, else the two highest-cardinality free dims by priority `os, runtime, version, symbol, arch, tool, context, libc`. Cell href pins `f_<xdim>`/`f_<ydim>` and lets the handler pick the next default axes; when ≤1 dim still varies the page renders the leaf panel instead of a grid.
- Tests: fact assembly caps + no double count (symbol `""` is disjoint), slicing respects filters, ratio cells, drill-down href chain, leaf detection, axis defaulting.

### Task A2: package-page explorer UI
- `package.html` gains the cube section as its primary element: axis selector GET form, filter chips with remove links, the grid (shared `pivot` template + `Ratio` rendering), the trimmed note, and the leaf "exact contract" panel when the slice bottoms out. Landing hero matrix cells link INTO the cube (`/npm/axios?f_os=linux&f_runtime=node+22`) so the landing → package → … → symbol chain is the drill-down the user described.
- New i18n keys (×9): `cube.heading, cube.x_axis, cube.y_axis, cube.apply, cube.filters, cube.clear, cube.leaf_heading, cube.leaf_result, cube.leaf_env, cube.leaf_links, cube.dim_version, cube.dim_symbol, cube.dim_os, cube.dim_arch, cube.dim_runtime, cube.dim_tool, cube.dim_context, cube.dim_libc, cube.package_level`.

## Self-Review

- Spec coverage: hero/matrix/findings/samples/evidence/CLI ordering → Task 3; matrix real-data + axes → Tasks 1–2; cell states + `!`/`?` + tooltips + cell links → Task 1/3/4; metrics with meanings → Task 3; package-internal matrices (user addendum) → Task 4; README ordering + competitive separation + privacy → Task 6; scope words ("libraries, runtimes and toolchains") → Tasks 3/6; i18n → Tasks 5/7. STALE state → pivot `Stale`. CROSS-CHECKED → pivot `Cross`. No backend change: verified — every data need maps to an existing Store method.
- Type consistency: `pivotGrid/pivotCell` names used identically in Tasks 1–4; `heroMatrixData` only in Tasks 2–3.
- Known risk: existing copy-pinned tests (`llminstall_test.go`, `seo_test.go`, `TestI18nTagline`) enumerated for update in Task 3 Step 3.
