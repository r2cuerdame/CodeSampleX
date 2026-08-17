package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

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
// and pointer users.
type statTile struct {
	Label string
	Value string
	Exact string
}

func buildTiles(lang string, st *netStats) []statTile {
	have := st != nil
	if st == nil {
		st = &netStats{}
	}
	counter := func(key string, n int64) statTile {
		tile := statTile{Label: i18n.T(lang, key)}
		if !have {
			tile.Value = "—"
			return tile
		}
		tile.Value = i18n.FormatCompactInt(lang, n)
		tile.Exact = i18n.FormatInt(lang, n)
		return tile
	}
	return []statTile{
		counter("stats.packages", st.Packages),
		counter("stats.evidence", st.Evidence),
		counter("stats.verified_samples", st.VerifiedSamples),
	}
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
	b.OGImage = base + "/static/inspector-hero-v1.webp"
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
