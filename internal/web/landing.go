package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// netStats mirrors the materialized NetworkStats JSON (plan P5.5). The
// homepage deliberately presents only packages, evidence and verified
// samples, but keeping the complete shape here lets it decode the producer's
// document without changing the raw stats contract.
type netStats struct {
	Peers              int64   `json:"peers"`
	ProjectsMonth      int64   `json:"projectsMonth"`
	Packages           int64   `json:"packages"`
	Symbols            int64   `json:"symbols"`
	Evidence           int64   `json:"evidence"`
	VerifiedSamples    int64   `json:"verifiedSamples"`
	PostHitSuccessRate float64 `json:"postHitSuccessRate"`
	// PostHitBuildsReported is the denominator. Deciding "not measured" from
	// the RATE hid a real result: adoption reports arrive, every reported
	// build fails, the rate is a genuine 0% — and the page rendered an em
	// dash, which here means "nobody has told us". The one number worth
	// showing was the one it refused to show.
	PostHitBuildsReported int64 `json:"postHitBuildsReported"`
	// EstimatedReasoningAvoided is an estimate carried WITH its reasoning:
	// the producer emits {value, formula, estimated, assumptions} so the
	// figure can never be mistaken for a measurement. Decoding it as a bare
	// number silently failed the whole document and blanked every counter.
	EstimatedReasoningAvoided estimatedNumber `json:"estimatedReasoningAvoided"`
	Estimated                 bool            `json:"estimated"`
	GeneratedAt               string          `json:"generatedAt"`
}

// estimatedNumber decodes either a plain number or the richer
// {value, estimated, …} object the aggregator writes.
type estimatedNumber struct {
	Value     int64
	Estimated bool
}

func (e *estimatedNumber) UnmarshalJSON(b []byte) error {
	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		e.Value, e.Estimated = n, true
		return nil
	}
	var obj struct {
		Value     float64 `json:"value"`
		Estimated bool    `json:"estimated"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	e.Value, e.Estimated = int64(obj.Value), obj.Estimated
	return nil
}

func (s *site) loadStats(r *http.Request) *netStats {
	raw, ok := s.d.Store.LatestStatsJSON(r.Context())
	if !ok {
		return nil
	}
	var st netStats
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return nil
	}
	return &st
}

// statTile is one homepage network counter. Value is compact for scanning;
// Exact is the locale-formatted raw count exposed to assistive technology
// and pointer users. Sub says what the number MEANS — a count without its
// meaning is decoration.
type statTile struct {
	Label string
	Value string
	Exact string
	Sub   string
}

func buildTiles(lang string, st *netStats) []statTile {
	have := st != nil
	if st == nil {
		st = &netStats{}
	}
	counter := func(key string, n int64) statTile {
		tile := statTile{
			Label: i18n.T(lang, key),
			Sub:   i18n.T(lang, key+"_sub"),
		}
		if !have {
			tile.Value = "—"
			return tile
		}
		tile.Value = i18n.FormatCompactInt(lang, n)
		tile.Exact = i18n.FormatInt(lang, n)
		return tile
	}
	// Coverage, not volume. The middle tile used to be a count of recorded
	// build runs, and a volume number means whatever its population means:
	// the same figure is a thriving network or one developer's afternoon,
	// and the reader cannot tell which. Reporters are anonymous by design, so
	// the page cannot resolve that even in principle.
	//
	// What the network actually knows is a coverage question, and it answers
	// honestly however few machines drew the map: an API covered by ten
	// machines is still an API covered. Symbols are recorded package-
	// qualified ("axios.post"), so distinct symbols are distinct APIs.
	// Coverage on the outside, our own output in the middle.
	//
	// The verified-sample count is the one number here that measures US
	// rather than the ecosystem, and it is the largest, so ending the row on
	// it let it read as the conclusion of a coverage story. In the middle it
	// is plainly the thing between what exists and what we have measured of
	// it, which is what a sample is.
	return []statTile{
		// What the network measured, first: builds that really ran. It came
		// off this row this morning because the figure was wrong three ways
		// — the same build counted once per symbol found in it, presence
		// records that cannot fail folded in, and events named as though
		// they were people. All three are fixed, and it is the plainest true
		// thing this project can say about itself.
		counter("stats.observations", st.Evidence),
		// Our own output stays in the middle, which is where it has been
		// since the row was three. The findings count sat here as a fourth
		// card; it has a page of its own with a tab, and a four-card row
		// made the reader weigh four different units at once.
		counter("stats.verified_samples", st.VerifiedSamples),
		counter("stats.packages", st.Packages),
	}
}

// ---------------------------------------------------------------------------
// Hero compatibility matrix: the landing's primary element. It is a real
// slice — runtime × OS — of the most-measured package's cube, and every
// cell drills into that package's explorer.

// heroMatrixTabs is how many hot packages the switcher offers;
// heroMatrixTries bounds how many are probed for a renderable grid.
const (
	heroMatrixTabs  = 6
	heroMatrixTries = 6
)

type heroTab struct {
	Label    string // "axios · npm" — data values, never translated
	Href     string
	Selected bool
}

type heroMatrixData struct {
	Package string
	Eco     string
	Href    string // the package's cube explorer
	Grid    pivotGrid
	Tabs    []heroTab
	// XLabel/YLabel name the slice's axes in the page language
	// (columns × rows), so the grid says what it is a map OF.
	XLabel, YLabel string
}

// heroAxisPairs are the slices the hero considers, best-first.
//
// Version × symbol leads. An OS axis looks like the natural map and is the
// worst choice on this corpus: every observation is recorded on Windows and
// every verification runs on Linux, so it files the two halves into separate
// rows and no cell can ever hold both. Version × symbol is also the question
// the site exists to answer -- does this API work in this release.
var heroAxisPairs = [][2]string{
	{"version", "symbol"},
	{"version", "runtime"},
	{"symbol", "runtime"},
	{"runtime", "os"},
	{"version", "os"},
}

// heroGridScore ranks candidate grids. Observed volume leads, then cells that
// actually carry usage, then merely filled cells and a real 2D spread.
//
// Volume leads because the front page is a demonstration, and a grid whose
// cells rest on thousands of real runs demonstrates something a grid of
// single measurements does not -- both look identical if you only count
// which cells are non-empty.
func heroGridScore(g pivotGrid, pairRank int) int {
	if g.Empty() {
		return 0
	}
	nonEmpty := 0
	withUsage := 0
	observed := int64(0)
	for _, r := range g.Rows {
		for _, c := range r.Cells {
			if c.Class != "empty" {
				nonEmpty++
			}
			// A cell reading "✓ —" is not empty and carries no usage. Counting
			// it as density picked a grid of six rows where exactly one cell
			// had a number in it, over a slice where most cells did.
			if c.Runs > 0 {
				withUsage++
			}
			observed += c.Runs
		}
	}
	// Damped so a single very busy cell cannot beat a genuine spread.
	volume := 0
	for v := observed; v > 0; v /= 10 {
		volume += 15
	}
	score := volume + withUsage*25 + nonEmpty*4 + len(g.Rows)*len(g.Cols)
	if len(g.Rows) >= 2 && len(g.Cols) >= 2 {
		score += 40
	}
	score += (len(heroAxisPairs) - pairRank) * 10
	return score
}

// heroMatrixTTL bounds how long a finished matrix is served as-is. The cubes
// beneath it hold for five minutes; a minute here keeps the page fresh while
// a busy landing pays the pivot once, not per view.
//
// heroStaleTTL is how long past that a finished matrix may still be shown
// while its refresh runs. A healthy process needs at most heroWarmTimeout
// plus heroWarmRetryDelay of it; the hour is slack for a slow database, and
// it is bounded so a store that stopped answering cannot keep an ancient
// slice on the front page forever.
const (
	heroMatrixTTL      = time.Minute
	heroStaleTTL       = time.Hour
	heroWarmTimeout    = time.Minute
	heroWarmRetryDelay = 30 * time.Second
)

// heroMatrix picks the featured package and slice: the ?m= selection when
// it is one of the hot packages (never an arbitrary store read), else the
// probed package whose best axis pair yields the richest grid.
func (s *site) heroMatrix(r *http.Request, lang string, hits []PackageHit) *heroMatrixData {
	if len(hits) == 0 {
		return nil
	}
	key := func(h PackageHit) string { return h.Ecosystem + "/" + h.Name }

	ordered := make([]PackageHit, 0, len(hits))
	selected := false
	sel := r.URL.Query().Get("m")
	if sel != "" {
		for _, h := range hits {
			if key(h) == sel {
				ordered = append(ordered, h)
				selected = true
				break
			}
		}
	}
	if !selected {
		ordered = hits
		// A ?m= that names no hot package is the unselected view; keying the
		// memo on it would let arbitrary query strings grow the cache without
		// bound, when the result is identical anyway.
		sel = ""
	}

	memoKey := lang + heroMemoKey(sel)
	s.heroMu.Lock()
	memo, memoized := s.heroCache[memoKey]
	s.heroMu.Unlock()
	if memoized && time.Since(memo.at) < heroMatrixTTL {
		return memo.data
	}

	// A finished cube can still be pivoted on this request. A cold cube is a
	// database fan-out, though, and the landing has an honest empty state for
	// exactly this moment. Schedule one bounded assembly and write the page
	// now instead of making the first visitor after a restart wait for it.
	data, complete := s.buildHeroMatrix(r, lang, hits, ordered, false)
	if !complete {
		s.warmHeroMatrix(r, lang, memoKey, hits, ordered)
		// The empty state belongs to a process that has never rendered a
		// grid, not to one whose candidate cubes happen to be cold right
		// now. Polled once every 8s for 10.7 minutes on production
		// 2026-08-29 (v0.1.62, 3f6ad8d), 11 of 80 landing responses carried
		// landing.matrix_empty — "the first compatibility grids appear here
		// as soon as the network records enough environment evidence" —
		// under this page's own counters reading 92,472 observations across
		// 1,980 packages. The misses land at a strict ~66s cadence: the memo
		// expires, the six probed cubes are no longer all warm, and the
		// request cannot assemble one, so every visitor in that window is
		// told the network is empty. The last grid this process rendered is
		// the honest thing to show meanwhile — it is one refresh older at
		// worst and carries its own observation date in the corner.
		if memoized && memo.data != nil && time.Since(memo.at) < heroStaleTTL {
			return memo.data
		}
		// A selection that has never rendered has no memo of its own, and a
		// selected build narrows `ordered` to that single package -- so one
		// cold cube aborts it and the reader who just clicked a category is
		// told the network is empty. Reported from the live homepage as the
		// grid occasionally vanishing while browsing the categories; it is not
		// occasional, it is every package this process has not assembled yet.
		//
		// The unselected grid is what to show meanwhile, for the same reason
		// the stale memo above is: it is a grid this process really rendered,
		// it contains the clicked package among its rows, and it carries its
		// own observation date. One click behind is not the same claim as "the
		// network has no evidence".
		if sel != "" {
			s.heroMu.Lock()
			base, ok := s.heroCache[lang+heroMemoKey("")]
			s.heroMu.Unlock()
			if ok && base.data != nil && time.Since(base.at) < heroStaleTTL {
				return base.data
			}
		}
		return nil
	}
	s.cacheHeroMatrix(memoKey, data)
	return data
}

// heroMemoKey renders a selection as the tail of a hero cache key.
//
// One place, because the key is built twice now: once for the selection being
// rendered, and once to reach the unselected grid a cold selection falls back
// to. Two hand-written separators is how those two quietly stop pointing at
// the same map entry.
func heroMemoKey(sel string) string { return "\x00" + sel }

func (s *site) cacheHeroMatrix(key string, data *heroMatrixData) {
	s.heroMu.Lock()
	defer s.heroMu.Unlock()
	if s.heroCache == nil {
		s.heroCache = map[string]heroCacheEntry{}
	}
	s.heroCache[key] = heroCacheEntry{data: data, at: time.Now()}
}

func (s *site) warmHeroMatrix(r *http.Request, lang, key string, hits, ordered []PackageHit) {
	s.heroMu.Lock()
	now := time.Now()
	if s.heroLoading[key] || now.Before(s.heroRetryAt[key]) {
		s.heroMu.Unlock()
		return
	}
	if s.heroLoading == nil {
		s.heroLoading = map[string]bool{}
	}
	if s.heroRetryAt == nil {
		s.heroRetryAt = map[string]time.Time{}
	}
	s.heroLoading[key] = true
	s.heroMu.Unlock()

	// The handler owns its request and slices. Clone them before it returns so
	// the background worker never observes caller-owned state changing.
	warmRequest := r.Clone(context.Background())
	warmHits := append([]PackageHit(nil), hits...)
	warmOrdered := append([]PackageHit(nil), ordered...)
	go func() {
		ctx, cancel := context.WithTimeout(warmRequest.Context(), heroWarmTimeout)
		defer cancel()
		request := warmRequest.Clone(ctx)
		var (
			data     *heroMatrixData
			complete bool
		)
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					log.Printf("web: panic warming landing hero: %v", recovered)
					complete = false
				}
			}()
			data, complete = s.buildHeroMatrix(request, lang, warmHits, warmOrdered, true)
		}()

		s.heroMu.Lock()
		defer s.heroMu.Unlock()
		delete(s.heroLoading, key)
		if !complete {
			s.heroRetryAt[key] = time.Now().Add(heroWarmRetryDelay)
			return
		}
		delete(s.heroRetryAt, key)
		if s.heroCache == nil {
			s.heroCache = map[string]heroCacheEntry{}
		}
		s.heroCache[key] = heroCacheEntry{data: data, at: time.Now()}
	}()
}

// buildHeroMatrix does the probing and pivoting behind heroMatrix; hits is
// the full hot list (for the tabs), ordered the candidates to probe.
func (s *site) buildHeroMatrix(r *http.Request, lang string, hits, ordered []PackageHit, loadCold bool) (*heroMatrixData, bool) {
	homePath := "/"
	if lang != i18n.Default {
		homePath = "/" + lang + "/"
	}
	key := func(h PackageHit) string { return h.Ecosystem + "/" + h.Name }

	now := time.Now()
	var best *heroMatrixData
	bestScore := 0
	consider := func(h PackageHit, facts []cubeFact) {
		if len(facts) == 0 {
			return
		}
		pagePath := pkgHref(h.Ecosystem, h.Name)
		hasSymbols := cubeHasRealSymbol(facts)
		for rank, pair := range heroAxisPairs {
			x, y := pair[0], pair[1]
			// Same guard the explorer uses: a symbol axis with only the
			// package-level aggregate is one row reading "whole package".
			if (x == "symbol" || y == "symbol") && !hasSymbols {
				continue
			}
			pin := func(dims map[string]string) string {
				return cubeHref(pagePath, cubeQuery(dims, "", "", lang))
			}
			links := pivotLinks{
				Cell: func(row, col string) string { return pin(map[string]string{y: row, x: col}) },
				Row:  func(row string) string { return pin(map[string]string{y: row}) },
				Col:  func(col string) string { return pin(map[string]string{x: col}) },
			}
			// The showcase drops the rows and columns that stand for a gap.
			// The full explorer keeps them — there completeness is the point —
			// but a front page whose grid has a row labelled "node (version
			// not recorded)" is answering a question about our instrument
			// instead of "does it run there".
			grid := dropUnrecordedAxes(buildCubeGrid(facts, x, y, links, now, false))
			labelSampleMarks(&grid, lang)
			if score := heroGridScore(grid, rank); score > bestScore {
				bestScore = score
				best = &heroMatrixData{
					Package: h.Name, Eco: h.Ecosystem,
					Href:   cubeHref(pagePath, cubeQuery(nil, "", "", lang)),
					Grid:   grid,
					XLabel: i18n.T(lang, "cube.dim_"+x),
					YLabel: i18n.T(lang, "cube.dim_"+y),
				}
			}
		}
	}

	// Candidates stay in rank order, and warmth is never a selection input.
	// Which cubes are warm is a function of what other visitors happened to
	// browse in the last five minutes, and letting that decide made the featured
	// package a function of other people's traffic.
	//
	// The cost stays bounded: a cached cube is free, and a cold one is warmed
	// only until something renders. Probing all six to pick the richest grid
	// was what made this the most expensive URL on the site.
	//
	// What changed is when to stop. Stopping at the first candidate that
	// rendered anything meant the featured slice was simply the first hot
	// package -- and on this corpus that is a grid where one cell in eighteen
	// has a number in it, because symbol-level observations exist for 138
	// packages out of 2,729. The scan now continues until a grid actually
	// carries usage, and keeps the first renderable one as the fallback for
	// when none does.
	var fallback *heroMatrixData
	for i, h := range ordered {
		if i >= heroMatrixTries {
			break
		}
		facts, ok := s.cubeFactsCached(h.Ecosystem, h.Name)
		if !ok {
			if !loadCold {
				return nil, false
			}
			if r.Context().Err() != nil {
				return nil, false
			}
			facts, _ = s.heroCubeFacts(r.Context(), h.Ecosystem, h.Name)
			// heroCubeFacts deliberately leaves no cache entry on an assembly
			// error. Re-read it to distinguish that failure from a successful
			// empty cube, which is a complete and cacheable result.
			if cached, loaded := s.cubeFactsCached(h.Ecosystem, h.Name); loaded {
				facts = cached
			} else {
				return nil, false
			}
			if r.Context().Err() != nil {
				return nil, false
			}
			// heroCubeFacts reapplies this deadline on a detached shared-load
			// context. Its timer can fire just before the parent context's Done
			// channel is closed, so Err alone has a narrow false-negative race.
			if deadline, ok := r.Context().Deadline(); ok && !time.Now().Before(deadline) {
				return nil, false
			}
		}
		consider(h, facts)
		if best != nil && fallback == nil {
			fallback = best
		}
		// "Carries any usage at all" was too low a bar: the top hit clears it
		// with two cells in eighteen and the scan stopped there. Half was too
		// low as well — on this corpus a grid can reach it and still be half
		// dashes, and the reader should meet the network where it has the
		// most to show rather than at the first slice that was not
		// embarrassing. Three quarters is the bar; a grid that clears it is
		// dense enough that continuing buys little.
		//
		// Worst case is one assembly per candidate on a cold cache. One
		// coalesced background warm pays that cost; landing requests keep
		// rendering the honest empty state until a finished matrix is ready.
		if best != nil && gridUsageCells(best.Grid)*4 >= gridCells(best.Grid)*3 {
			break
		}
	}
	if best == nil {
		best = fallback
	}
	if best == nil {
		return nil, true
	}
	for j, tab := range hits {
		if j >= heroMatrixTabs {
			break
		}
		best.Tabs = append(best.Tabs, heroTab{
			Label: tab.Name + " · " + tab.Ecosystem,
			// Anchored so switching the featured package keeps the grid in
			// view instead of returning the reader to the top of the page.
			Href:     homePath + "?m=" + url.QueryEscape(key(tab)) + "#matrix",
			Selected: tab.Ecosystem == best.Eco && tab.Name == best.Package,
		})
	}
	return best, true
}

type landingPage struct {
	basePage
	Tiles []statTile
	// Ecos links each measured ecosystem into its filtered records
	// inventory, right under the counters.
	Ecos      []heroEco
	InstallPS string
	InstallSH string
	// LLMPrompt is the ready-to-paste instruction for the coding agent the
	// visitor already has open. It is rendered visibly AND carried in the
	// copy button's data attribute, from this one field, so the text a
	// reader checks is byte-for-byte the text their agent receives.
	LLMPrompt string
	// Findings are the few measured contradictions shown on the home page.
	//
	// The rest of this page EXPLAINS why the network is needed; these PROVE
	// it. One line of "the documentation says X, the contract measured Y"
	// does more than every paragraph above it, because a reader recognises
	// the shape of it from their own week.
	Findings []homeFinding
	// Coverage is the instrument describing its own shape. An observatory's
	// failure mode is not a false claim but an unstated skew, and this one is
	// extreme enough to publish rather than let a reader discover it.
	// Matrix is the hero compatibility grid — the page's protagonist. nil
	// when the network has no renderable cube yet; the page stays honest
	// and simply says what will appear here.
	Matrix *heroMatrixData
}

// homeFinding is one measured contradiction, trimmed for the home page.
type homeFinding struct {
	Ecosystem string
	Subject   string
	Believed  string
	Measured  string
	Href      string
}

// heroEco is one measured ecosystem, linked straight into the filtered
// records inventory. The landing names them without capability claims —
// the honest per-adapter matrix stays in docs/adapters.md and
// GET /v1/adapters.
type heroEco struct {
	Name string
	Href string
}

// landingEcosystems is the fixed display order of the record-backed
// ecosystems (the knownEcosystems route set).
var landingEcosystems = []string{
	"npm", "pypi", "golang", "cargo", "maven", "gem", "composer", "hex", "pub",
}

func buildHeroEcos(lang string) []heroEco {
	ecos := make([]heroEco, 0, len(landingEcosystems))
	for _, e := range landingEcosystems {
		ecos = append(ecos, heroEco{Name: e, Href: recordsHref(RecordFilter{Ecosystem: e}, 1, lang)})
	}
	return ecos
}

func (s *site) landingRoot(w http.ResponseWriter, r *http.Request) {
	s.landing(w, r, s.negotiate(w, r))
}

func (s *site) landing(w http.ResponseWriter, r *http.Request, lang string) {
	base := s.base(r)
	title := "CodeSampleX — " + i18n.T(lang, "landing.tagline")
	b := s.page(r, lang, title, i18n.T(lang, "site.meta_description"))
	b.IsLanding = true
	b.OGImage = base + "/static/inspector-hero-v1.webp"
	b.Alternates = landingAlternates(base)
	if lang == i18n.Default {
		b.Canonical = base + "/"
	} else {
		b.Canonical = base + "/" + lang + "/"
	}
	b.JSONLD = landingJSONLD(base, lang)

	st := s.loadStats(r)
	hits, err := s.d.Store.HotPackages(r.Context(), 12)
	if err != nil {
		hits = nil // an empty map is still a usable landing page
	}
	s.render(w, "landing", http.StatusOK, landingPage{
		basePage:  b,
		Tiles:     buildTiles(lang, st),
		Ecos:      buildHeroEcos(lang),
		InstallPS: "irm " + base + "/install.ps1 | iex",
		InstallSH: "curl -fsSL " + base + "/install.sh | sh",
		LLMPrompt: llmPrompt(lang, base),
		Findings:  homeFindings(lang),
		Matrix:    s.heroMatrix(r, lang, hits),
	})
}

// llmPrompt renders the "let your agent install it" prompt for one locale.
//
// The two %s are this deployment's own origin, so the installer URLs a
// visitor hands to an agent are the ones they are reading the page on
// rather than a hard-coded hostname. Everything the prompt asks the agent
// to run is a published CodeSampleX command; it names no third-party
// client that csx has not been measured against, asks for no credential,
// and tells the agent to ADD to an MCP config rather than replace it.
func llmPrompt(lang, base string) string {
	return i18n.T(lang, "landing.llm_prompt", base, base)
}

// homeFindingsShown is how many measured contradictions the front page
// carries. Three: enough that the shape is unmistakable, few enough that
// the page still opens with what this is.
const homeFindingsShown = 3

// homeFindings picks the contradictions that land fastest on a stranger.
//
// They are chosen by hand rather than by recency, because what makes one
// land is not how new it is: it is whether the reader has been bitten by it
// — an empty form field arriving as 0, a password silently truncated at 72
// bytes. Every one of them links to the sample whose contract measured it,
// so the claim is checkable in one click.
func homeFindings(lang string) []homeFinding {
	want := []string{"zod", "bcryptjs", "jose"}
	var out []homeFinding
	for _, w := range want {
		for _, f := range append(append([]finding{}, documentedFindings...), believedFindings...) {
			if !strings.HasPrefix(f.Subject, w) {
				continue
			}
			href := "/findings"
			if f.SampleID != "" {
				href = "/samples/" + f.SampleID
			}
			out = append(out, homeFinding{
				Ecosystem: f.Ecosystem,
				Subject:   f.Subject,
				Believed:  f.Believed,
				Measured:  f.Measured,
				Href:      href,
			})
			break
		}
		if len(out) == homeFindingsShown {
			break
		}
	}
	return out
}

// statsPage and adaptersPage are permanent redirects. Both pages folded
// into the front page — the counters and the ecosystem support rows —
// and old links, bookmarks and indexed URLs still land somewhere correct.
// The full capability data stays published at GET /v1/adapters and in
// docs/adapters.md, which is what goal.md §13.1 actually requires.
// statsPage points at /records directly. It used to redirect to /explore,
// which is itself a 301 to /records: measured on the live site, /stats was
// a two-hop chain (301 → 301), and a chain is a link that only partly
// arrives.
func (s *site) statsPage(w http.ResponseWriter, r *http.Request) {
	s.redirectTo(w, r, "/records")
}

func (s *site) adaptersPage(w http.ResponseWriter, r *http.Request) {
	s.redirectTo(w, r, "/")
}

func (s *site) redirectTo(w http.ResponseWriter, r *http.Request, target string) {
	if lang := r.URL.Query().Get("lang"); lang != "" {
		if l, ok := i18n.Canonical(lang); ok {
			target += "?lang=" + url.QueryEscape(l)
		}
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

// gridUsageCells counts cells that carry an observed run count. A cell
// reading "✓ —" is not empty and holds no usage, and the difference is
// what separates a grid that demonstrates something from one that does not.
func gridUsageCells(g pivotGrid) int {
	n := 0
	for _, r := range g.Rows {
		for _, c := range r.Cells {
			if c.Runs > 0 {
				n++
			}
		}
	}
	return n
}

// gridCells counts every cell in the grid, empty ones included.
func gridCells(g pivotGrid) int {
	n := 0
	for _, r := range g.Rows {
		n += len(r.Cells)
	}
	return n
}
