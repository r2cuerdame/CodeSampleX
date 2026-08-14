package web

import (
	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// wantedLimit bounds the page. A ranking nobody can read to the end is not
// a ranking.
const wantedLimit = 60

// WantedItem is one unanswered question on the board.
type WantedItem struct {
	Ecosystem string
	Name      string
	Symbol    string
	Asks      int64
	AsksText  string
	Href      string
}

type wantedPage struct {
	basePage
	Items []WantedItem
	Total int
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
	b := s.page(r, lang, i18n.T(lang, "wanted.title")+" — CodeSampleX",
		i18n.T(lang, "meta.wanted"))

	rows, err := s.d.Store.TopWanted(r.Context(), wantedLimit)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	items := make([]WantedItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, WantedItem{
			Ecosystem: row.Ecosystem,
			Name:      row.Name,
			Symbol:    row.Symbol,
			Asks:      row.Asks,
			AsksText:  i18n.FormatInt(lang, row.Asks),
			Href:      b.WithLang(pkgHref(row.Ecosystem, row.Name)),
		})
	}
	s.render(w, "wanted", http.StatusOK, wantedPage{
		basePage: b, Items: items, Total: len(items),
	})
}
