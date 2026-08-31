package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// gapsPerPage and maxGapsPage match /records and the atlas, because a reader
// who has learned one list's shape should not have to learn another's.
const (
	gapsPerPage = 50
	maxGapsPage = 200
)

// gapAxis is one of the three assets, as it renders: whether the coordinate
// holds it, what the answer was, and whether it can ever be closed.
type gapAxis struct {
	Label  string
	Held   bool
	Answer string
	// Unaskable carries the reason nothing here can produce this axis. A row
	// with one is still shown -- the census counts it too -- but it is not
	// work anybody can pick up.
	Unaskable string
}

type gapItem struct {
	Ecosystem string
	Name      string
	Version   string
	Coord     string
	Href      string
	// State is the census cell this row falls in, so a row on the page can be
	// traced to a column in the matrix on /stats.
	State string
	Axes  [3]gapAxis
	// Closeable is false when every missing axis is unaskable: the row is
	// there for completeness, not as an invitation.
	Closeable bool
}

type gapsPage struct {
	basePage
	Items       []gapItem
	Total       int
	Query       string
	HasQuery    bool
	ClearHref   string
	Page, Pages int
	RangeText   string
	PageText    string

	PrevHref, NextHref string
}

// gaps renders the work this network has left, one coordinate at a time.
//
// It replaces /wanted, which ranked what people searched for and missed.
// Demand is a real signal and it is not this one: a coordinate nobody has ever
// asked about can be the largest hole in the corpus, and one asked about daily
// can already be finished. What a contributor needs to see is which of Sample,
// Evidence and Dependency a coordinate is missing -- the same three axes the
// census on /stats counts, listed instead of summed.
//
// The page is deliberately not a ranking of importance. It is ordered by how
// little is known, which is a fact about the corpus; calling one gap more
// important than another would be this network grading work it has not done.
func (s *site) gaps(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		page = min(p, maxGapsPage)
	}

	rows, total, err := s.d.Store.CompletenessGaps(r.Context(), query,
		(page-1)*gapsPerPage, gapsPerPage)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	pages := (total + gapsPerPage - 1) / gapsPerPage
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		http.Redirect(w, r, gapsHref(query, pages, lang), http.StatusFound)
		return
	}

	n := func(v int) string { return i18n.FormatInt(lang, int64(v)) }
	view := gapsPage{
		basePage: s.page(r, lang, i18n.T(lang, "gaps.title")+" — CodeSampleX",
			i18n.T(lang, "meta.gaps")),
		Total: total, Query: query, HasQuery: query != "",
		ClearHref: gapsHref("", 1, lang),
		Page:      page, Pages: pages,
		PageText: i18n.T(lang, "records.page", n(page), n(pages)),
	}
	if total > 0 {
		from := (page-1)*gapsPerPage + 1
		to := from + len(rows) - 1
		view.RangeText = i18n.T(lang, "records.range", n(from), n(to), n(total))
	}
	for _, row := range rows {
		view.Items = append(view.Items, gapItemFor(lang, view.basePage, row))
	}
	if page > 1 {
		view.PrevHref = gapsHref(query, page-1, lang)
	}
	if page < pages {
		view.NextHref = gapsHref(query, page+1, lang)
	}
	s.render(w, "gaps", http.StatusOK, view)
}

// gapItemFor renders one gap's three axes.
//
// The dependency axis is the only one with three answers rather than two, and
// it is rendered as three: a release a resolver read and found empty is
// finished on that axis, and showing it as the same blank as one nobody has
// opened would put a closed coordinate back on the work list forever.
func gapItemFor(lang string, base basePage, row CompletenessGap) gapItem {
	item := gapItem{
		Ecosystem: row.Ecosystem, Name: row.Name, Version: row.Version,
		Coord: row.Name + "@" + row.Version,
		Href:  base.WithLang(versionHref(row.Ecosystem, row.Name, row.Version)),
		State: gapState(row),
	}
	item.Axes[0] = gapAxis{
		Label: i18n.T(lang, "gaps.axis_sample"), Held: row.HasSample,
		Answer:    i18n.T(lang, boolKey(row.HasSample, "gaps.sample_have", "gaps.sample_missing")),
		Unaskable: row.SampleNAReason,
	}
	item.Axes[1] = gapAxis{
		Label: i18n.T(lang, "gaps.axis_evidence"), Held: row.HasEvidence,
		Answer: i18n.T(lang, boolKey(row.HasEvidence, "gaps.evidence_have", "gaps.evidence_missing")),
	}
	dep := gapAxis{
		Label: i18n.T(lang, "gaps.axis_dependency"),
		Held:  row.Dependency != GapDependencyUnknown,
		// Default is the honest one: nothing has looked.
		Answer:    i18n.T(lang, "gaps.dep_unknown"),
		Unaskable: row.DependencyNAReason,
	}
	switch row.Dependency {
	case GapDependencyGraph:
		dep.Answer = i18n.T(lang, "gaps.dep_graph")
	case GapDependencyProvenNone:
		dep.Answer = i18n.T(lang, "gaps.dep_none")
	}
	item.Axes[2] = dep

	for _, a := range item.Axes {
		if !a.Held && a.Unaskable == "" {
			item.Closeable = true
		}
	}
	return item
}

// gapState renders the three axes as the census cell name.
func gapState(row CompletenessGap) string {
	key := []byte("---")
	if row.HasSample {
		key[0] = 'S'
	}
	if row.HasEvidence {
		key[1] = 'E'
	}
	if row.Dependency != GapDependencyUnknown {
		key[2] = 'D'
	}
	return string(key)
}

func boolKey(v bool, yes, no string) string {
	if v {
		return yes
	}
	return no
}

// gapsHref builds a link back into the list, keeping the query and language.
func gapsHref(query string, page int, lang string) string {
	v := url.Values{}
	if query != "" {
		v.Set("q", query)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if lang != "" && lang != i18n.Default {
		v.Set("lang", lang)
	}
	if len(v) == 0 {
		return "/gaps"
	}
	return "/gaps?" + v.Encode()
}

// wantedGone sends the retired request board to the gap list.
//
// /wanted is in old READMEs, old MCP replies and external links. Retiring the
// concept is a decision about what the site says; breaking the URL is a
// decision about whether people arrive at all, and that one was not made.
func wantedGone(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/gaps", http.StatusMovedPermanently)
}
