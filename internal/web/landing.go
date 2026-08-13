package web

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// netStats mirrors the materialized NetworkStats JSON (plan P5.5). The
// reasoning-avoided figure is an estimate by construction and is always
// rendered with an "estimated" label (goal.md §14.5).
type netStats struct {
	Peers              int64   `json:"peers"`
	Packages           int64   `json:"packages"`
	Symbols            int64   `json:"symbols"`
	Evidence           int64   `json:"evidence"`
	VerifiedSamples    int64   `json:"verifiedSamples"`
	PostHitSuccessRate float64 `json:"postHitSuccessRate"`
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
	Label     string
	Value     string
	Estimated bool
}

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
	return []statTile{
		{Label: i18n.T(lang, "stats.peers"), Value: num(st.Peers)},
		{Label: i18n.T(lang, "stats.packages"), Value: num(st.Packages)},
		{Label: i18n.T(lang, "stats.symbols"), Value: num(st.Symbols)},
		{Label: i18n.T(lang, "stats.evidence"), Value: num(st.Evidence)},
		{Label: i18n.T(lang, "stats.verified_samples"), Value: num(st.VerifiedSamples)},
		{Label: i18n.T(lang, "stats.post_hit_success"), Value: pct(st.PostHitSuccessRate)},
		{Label: i18n.T(lang, "stats.reasoning_avoided"), Value: num(st.EstimatedReasoningAvoided.Value), Estimated: true},
	}
}

type landingPage struct {
	basePage
	Tiles     []statTile
	InstallPS string
	InstallSH string
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

	s.render(w, "landing", http.StatusOK, landingPage{
		basePage:  b,
		Tiles:     buildTiles(lang, s.loadStats(r)),
		InstallPS: "irm " + base + "/install.ps1 | iex",
		InstallSH: "curl -fsSL " + base + "/install.sh | sh",
	})
}

// statsPage is kept as a permanent redirect: the network counters moved
// onto /explore, which is the page that shows the network itself. Old
// links, bookmarks and indexed URLs still land somewhere correct.
func (s *site) statsPage(w http.ResponseWriter, r *http.Request) {
	target := "/explore"
	if lang := r.URL.Query().Get("lang"); lang != "" {
		target += "?lang=" + url.QueryEscape(lang)
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

