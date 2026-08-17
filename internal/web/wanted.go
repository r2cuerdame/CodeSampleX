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

// WantedItem is one unanswered question on the board.
type WantedItem struct {
	Ecosystem string
	Name      string
	Version   string
	Symbol    string
	Asks      int64
	AsksText  string
	RankText  string
	Href      string
}

type wantedPage struct {
	basePage
	Items              []WantedItem
	Total              int
	Query              string
	HasQuery           bool
	ClearHref          string
	Page, Pages        int
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

	rows, total, err := s.d.Store.WantedRows(r.Context(), query, (page-1)*wantedPerPage, wantedPerPage)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
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
			Ecosystem: row.Ecosystem,
			Name:      row.Name,
			Version:   row.Version,
			Symbol:    row.Symbol,
			Asks:      row.Asks,
			AsksText:  i18n.FormatInt(lang, row.Asks),
			RankText:  i18n.FormatInt(lang, int64((page-1)*wantedPerPage+i+1)),
			// Every supported row has a stable, honest wanted-only page even
			// before compatibility evidence exists.
			Href: b.WithLang(pkgHref(row.Ecosystem, row.Name)),
		})
	}
	n := func(v int) string { return i18n.FormatInt(lang, int64(v)) }
	view := wantedPage{
		basePage: b, Items: items, Total: total, Query: query, HasQuery: query != "",
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
