package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// netStats mirrors the materialized NetworkStats JSON (plan P5.5). The
// reasoning-avoided figure is an estimate by construction and is always
// rendered with an "estimated" label (goal.md §14.5).
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

// statTile is one network counter. The reasoning-avoided tile always
// carries Estimated=true — the figure is an estimate by definition.
type statTile struct {
	Label string
	Value string
	// Note carries the denominator when a rate has one. "100%" from a
	// single report is arithmetically true and tells a reader nothing;
	// beside "of 1 reported build" it tells them exactly what it is worth.
	Note      string
	Estimated bool
}

// Thresholds below which a figure says less than nothing.
const (
	// minPeersToShow: a peer count is a participation statistic, and the
	// operator's own machines are always in it. Two is what one person
	// running a laptop and a test container produces, so the tile only
	// starts carrying information well above that.
	minPeersToShow = 5
	// minReportsForARate: the smallest number of reported builds a
	// percentage may be computed from. One build reported as "100%" is an
	// anecdote wearing a percentage sign.
	minReportsForARate = 20
)

func buildTiles(lang string, st *netStats) []statTile {
	have := st != nil
	if st == nil {
		st = &netStats{}
	}
	num := func(n int64) string {
		if !have {
			return "—"
		}
		return i18n.FormatInt(lang, n)
	}
	pct := func(f float64) string {
		if !have {
			return "—"
		}
		return i18n.FormatPercent(lang, f)
	}
	// notYetMeasured renders a figure that has no data behind it yet as an
	// em dash rather than a zero. Both of the tiles below are derived from
	// ADOPTION reports — a real user applying a sample and telling us how it
	// went — and the producer marks them as a placeholder until one arrives.
	// "0" reads as "we measured, and nobody was helped"; the truth is that
	// nothing has been collected, and those are different claims.
	notYetMeasured := func(v int64, rendered string) string {
		if v == 0 {
			return "—"
		}
		return rendered
	}
	// A RATE needs enough behind it to be a rate. "100%" over one reported
	// build is not a measurement, it is one anecdote wearing a percentage
	// sign — and a percentage implies a precision the sample size cannot
	// support, which is a stronger claim than the raw count would make.
	//
	// Below the floor the tile is not shown at all: a reader deciding
	// whether to install should see the numbers that mean something
	// (packages, evidence, verified samples) rather than three tiles that
	// quietly say "almost nobody has used this yet".
	measured := func(n int64) bool { return n >= minReportsForARate }

	tiles := []statTile{}
	// Projects-this-month leads WHEN it means something. Peer buckets
	// rotate daily, so the peer tile resets every midnight and reads as an
	// empty network even when it is not; project buckets rotate monthly,
	// which makes this the longest window the identity scheme can count
	// honestly.
	//
	// But a project bucket is a DIRECTORY, not a person. With one peer on
	// the network every one of them is the same machine, so "73 projects
	// this month" is one operator's folder count wearing the clothes of a
	// participation statistic — and a reader asked whether it meant
	// seventy-three people. The peer tile is already hidden for this exact
	// reason; this number needed the same rule, and it comes back on its
	// own as soon as a second peer makes it mean what it says.
	if st.Peers >= minPeersToShow {
		tiles = append(tiles,
			statTile{Label: i18n.T(lang, "stats.projects_month"), Value: num(st.ProjectsMonth)})
	}
	// The peer tile is omitted while the count cannot mean anything.
	//
	// Buckets rotate daily and the operator's own machine is always one of
	// them, so "1" is indistinguishable from no external activity at all —
	// and rendered as a network statistic it implies activity that is not
	// there. The adoption detector uses exactly this reading: its recorded
	// baseline is one peer today, meaning us. Above the baseline the number
	// starts carrying information, and the tile comes back on its own.
	if st.Peers >= minPeersToShow {
		tiles = append(tiles, statTile{Label: i18n.T(lang, "stats.peers"), Value: num(st.Peers)})
	}
	tiles = append(tiles,
		statTile{Label: i18n.T(lang, "stats.packages"), Value: num(st.Packages)},
		statTile{Label: i18n.T(lang, "stats.symbols"), Value: num(st.Symbols)},
		statTile{Label: i18n.T(lang, "stats.evidence"), Value: num(st.Evidence)},
		statTile{Label: i18n.T(lang, "stats.verified_samples"), Value: num(st.VerifiedSamples)},
		// Both this and the tile below come from ADOPTION reports, and both
		// have to say "not measured" the same way. "0%" next to "Post-hit
		// success rate" is a claim: we watched, and none of it worked. What
		// is true is that nobody has reported yet, and a visitor deciding
		// whether to install reads the first one.
	)
	if measured(st.PostHitBuildsReported) {
		tiles = append(tiles, statTile{
			Label: i18n.T(lang, "stats.post_hit_success"),
			Value: notYetMeasured(st.PostHitBuildsReported, pct(st.PostHitSuccessRate)),
			Note:  buildsNote(lang, st.PostHitBuildsReported),
		})
	}
	if measured(st.PostHitBuildsReported) {
		tiles = append(tiles, statTile{
			Label:     i18n.T(lang, "stats.reasoning_avoided"),
			Value:     notYetMeasured(st.EstimatedReasoningAvoided.Value, num(st.EstimatedReasoningAvoided.Value)),
			Estimated: true,
		})
	}
	return tiles
}

type landingPage struct {
	basePage
	Tiles       []statTile
	GeneratedAt string
	Hits        []PackageHit
	Support     []supportRow
	InstallPS   string
	InstallSH   string
	// Findings are the few measured contradictions shown on the home page.
	//
	// The rest of this page EXPLAINS why the network is needed; these PROVE
	// it. One line of "the documentation says X, the contract measured Y"
	// does more than every paragraph above it, because a reader recognises
	// the shape of it from their own week.
	Findings []homeFinding
}

// homeFinding is one measured contradiction, trimmed for the home page.
type homeFinding struct {
	Ecosystem string
	Subject   string
	Believed  string
	Measured  string
	Href      string
}

// supportRow says what CodeSampleX can observe in one ecosystem, in plain
// words. The A0–A4 codes and the long per-adapter caveats live in
// GET /v1/adapters and docs/adapters.md; a visitor deciding whether to
// install needs "does it see my stack, and how much can I trust it".
type supportRow struct {
	Ecosystem  string
	Managers   string
	Can        []string
	Missing    []string
	Confidence string // EXACT | PROBABLE | UNKNOWN
	// ConfidenceClass styles the chip; ConfidenceTip explains the value
	// on hover so the word is never a bare label.
	ConfidenceClass string
	ConfidenceTip   string
}

func buildSupport(lang string) []supportRow {
	doc := loadAdapters()
	if doc == nil {
		return nil
	}
	// Level → the plain-language thing it lets the network observe.
	labels := []struct{ level, key string }{
		{"A0", "support.packages"},
		{"A1", "support.builds"},
		{"A2", "support.symbols"},
		{"A4", "support.samples"},
	}
	rows := make([]supportRow, 0, len(doc.Adapters))
	for _, a := range doc.Adapters {
		has := map[string]bool{}
		for _, c := range a.Capabilities {
			has[c] = true
		}
		row := supportRow{
			Ecosystem:  a.Ecosystem,
			Managers:   strings.Join(a.PackageManagers, ", "),
			Confidence: a.SymbolConfidence,
		}
		if key, ok := confidenceKey[strings.ToUpper(a.SymbolConfidence)]; ok {
			row.ConfidenceClass = strings.ToLower(a.SymbolConfidence)
			row.ConfidenceTip = i18n.T(lang, key)
		}
		for _, l := range labels {
			if has[l.level] {
				row.Can = append(row.Can, i18n.T(lang, l.key))
			} else {
				row.Missing = append(row.Missing, i18n.T(lang, l.key))
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *site) landingRoot(w http.ResponseWriter, r *http.Request) {
	s.landing(w, r, s.negotiate(w, r))
}

func (s *site) landing(w http.ResponseWriter, r *http.Request, lang string) {
	base := s.base(r)
	title := "CodeSampleX — " + i18n.T(lang, "landing.tagline")
	b := s.page(r, lang, title, i18n.T(lang, "site.meta_description"))
	b.IsLanding = true
	b.Alternates = landingAlternates(base)
	if lang == i18n.Default {
		b.Canonical = base + "/"
	} else {
		b.Canonical = base + "/" + lang + "/"
	}
	b.JSONLD = landingJSONLD(base, s.d.Version, lang)

	st := s.loadStats(r)
	generated := ""
	if st != nil {
		generated = st.GeneratedAt
	}
	hits, err := s.d.Store.HotPackages(r.Context(), 12)
	if err != nil {
		hits = nil // an empty map is still a usable landing page
	}

	s.render(w, "landing", http.StatusOK, landingPage{
		basePage:    b,
		Tiles:       buildTiles(lang, st),
		GeneratedAt: generated,
		Hits:        hits,
		Support:     buildSupport(lang),
		InstallPS:   "irm " + base + "/install.ps1 | iex",
		InstallSH:   "curl -fsSL " + base + "/install.sh | sh",
		Findings:    homeFindings(lang),
	})
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

// buildsNote renders the denominator behind the post-hit rate, or "" when
// there is nothing to qualify.
func buildsNote(lang string, reported int64) string {
	switch reported {
	case 0:
		return ""
	case 1:
		// English needs the singular, and several other locales inflect
		// differently at one too. A number this small is exactly when the
		// note matters most, so it should not read like a template.
		return i18n.T(lang, "stats.of_one_build")
	}
	return i18n.T(lang, "stats.of_n_builds", i18n.FormatInt(lang, reported))
}
