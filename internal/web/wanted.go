package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// wantedPerPage keeps the ranking readable without hiding requests below a
// fixed top-N cutoff.
const wantedPerPage = 40

// maxWantedPage prevents a hostile page number from overflowing the offset
// multiplication. It is deliberately aligned with /records.
const maxWantedPage = 1 << 20

// WantedItem is one unanswered coordinate on the board, with the separate
// questions asked about it folded in behind.
type WantedItem struct {
	Ecosystem string
	Name      string
	Version   string
	Asks      int64
	AsksText  string
	// DetailText says what the fold covered — how many distinct APIs were
	// asked about and on which platforms. Without it the board hid the fact
	// that four separate questions had been reported.
	DetailText string
	RankText   string
	Href       string
}

type wantedPage struct {
	basePage
	Items       []WantedItem
	Total       int
	Query       string
	HasQuery    bool
	ClearHref   string
	Page, Pages int
	// Windowed says the fold ran over a bounded read, so a package may have
	// rows beyond it. An absent row must never read as "nobody asked".
	Windowed           bool
	RangeText          string
	PageText           string
	PrevHref, NextHref string
}

// wanted renders the questions the network could not answer, most-asked
// first.
//
// This is the only ranking on the site that is not a description of what
// exists. Everything else says "here is what we know"; this says "here is
// what nobody has written down yet", which is the one list a contributor
// can act on. It is also the honest reading of a miss: NO_SAFE_MATCH is a
// feature, but a miss that teaches nobody anything is just a dead end.
//
// The questions themselves are not here and never will be. What was asked
// is a sentence a person typed, and it stays on their machine (goal.md
// §8.5); what is counted is the package, which was public before anyone
// asked about it.
func (s *site) wanted(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		page = min(p, maxWantedPage)
	}

	// The board reads by package version, but the stored row is a work unit
	// down to symbol and platform, so it has to be folded before it can be
	// paged — folding one page at a time would split a package across the
	// boundary. The window is read whole and the page taken from the fold.
	raw, _, err := s.d.Store.WantedRows(r.Context(), query, 0, wantedRollupWindow)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	rolled, windowed := rollUpWanted(raw, wantedRollupWindow)
	total := len(rolled)
	start := (page - 1) * wantedPerPage
	if start > total {
		start = total
	}
	end := min(start+wantedPerPage, total)
	rows := rolled[start:end]
	pages := (total + wantedPerPage - 1) / wantedPerPage
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		http.Redirect(w, r, wantedHref(query, pages, lang), http.StatusFound)
		return
	}

	b := s.page(r, lang, i18n.T(lang, "wanted.title")+" — CodeSampleX",
		i18n.T(lang, "meta.wanted"))
	items := make([]WantedItem, 0, len(rows))
	for i, row := range rows {
		items = append(items, WantedItem{
			Ecosystem:  row.Ecosystem,
			Name:       row.Name,
			Version:    row.Version,
			Asks:       row.Asks,
			AsksText:   i18n.FormatInt(lang, row.Asks),
			DetailText: wantedDetail(lang, row),
			RankText:   i18n.FormatInt(lang, int64((page-1)*wantedPerPage+i+1)),
			// Every supported row has a stable, honest wanted-only page even
			// before compatibility evidence exists.
			//
			// To the RELEASE when the row names one. The label reads
			// "npm/three@0.180.0" and this used to drop the version, so rows
			// asking about different releases of one package all resolved to
			// the same destination -- 72 rows onto 60 URLs in production --
			// and a reader who clicked the release they cared about got the
			// package, with nothing on the page saying it had moved them.
			Href: b.WithLang(wantedRowHref(row)),
		})
	}
	n := func(v int) string { return i18n.FormatInt(lang, int64(v)) }
	view := wantedPage{
		basePage: b, Items: items, Total: total, Query: query, HasQuery: query != "",
		Windowed:  windowed,
		ClearHref: wantedHref("", 1, lang), Page: page, Pages: pages,
		PageText: i18n.T(lang, "records.page", n(page), n(pages)),
	}
	if total > 0 {
		from := (page-1)*wantedPerPage + 1
		to := (page-1)*wantedPerPage + len(items)
		view.RangeText = i18n.T(lang, "records.range", n(from), n(to), n(total))
	}
	if page > 1 {
		view.PrevHref = wantedHref(query, page-1, lang)
	}
	if page < pages {
		view.NextHref = wantedHref(query, page+1, lang)
	}
	s.render(w, "wanted", http.StatusOK, view)
}

// wantedRowHref points at the exact coordinate the row advertises.
//
// A row without a version is a question about the package itself, and the
// package page is the honest answer to it. A row WITH one is a question about
// that release, and answering it with the package would quietly discard the
// half the reader chose.
func wantedRowHref(row WantedRollupRow) string {
	if row.Version == "" {
		return pkgHref(row.Ecosystem, row.Name)
	}
	return versionHref(row.Ecosystem, row.Name, row.Version)
}

// wantedHref keeps the active search and language while moving through the
// ranking. Page one stays clean, matching /records and /findings.
func wantedHref(query string, page int, lang string) string {
	v := url.Values{}
	if query = strings.TrimSpace(query); query != "" {
		v.Set("q", query)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if lang != i18n.Default {
		v.Set("lang", lang)
	}
	if len(v) == 0 {
		return "/wanted"
	}
	return "/wanted?" + v.Encode()
}
