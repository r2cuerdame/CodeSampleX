package web

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// samplesPerPage is how many samples one screen of the collection holds.
const samplesPerPage = 24

// maxSamplesPage bounds ?page= before it is multiplied. Atoi happily returns
// 9223372036854775807, and (page-1)*samplesPerPage overflows to a negative
// offset that the store would slice with — the same URL-crashes-the-page shape
// the records collection was fixed for.
const maxSamplesPage = 500

// sampleCard is one row of the collection.
//
// It leads with what the sample ANSWERS, not with what it is called. A sample
// id is a content hash; a reader scanning a list of them learns nothing, and
// the whole reason to browse this collection is to find an answer worth
// reusing.
type sampleCard struct {
	SampleID string
	Href     string
	// Goal is the sentence the sample was written to answer.
	Goal string
	// Subject is the release it answers for, and Symbols the APIs.
	Subject string
	Symbols []string
	// Context is the execution context it was published from ("node 22").
	Context string
	Kind    string
	// Verified says a contract passed for it. The collection only carries
	// published samples, so this is a statement about evidence rather than a
	// filter — a card that cannot say it does not claim it.
	Verified  bool
	CreatedAt string
}

type samplesView struct {
	basePage
	Cards    []sampleCard
	Total    string
	From     string
	To       string
	Page     int
	Pages    int
	PrevHref string
	NextHref string
	Empty    bool
	Query    string
	Searched bool
}

func samplesHref(query string, page int, lang string) string {
	v := url.Values{}
	if query != "" {
		v.Set("q", query)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if lang != "en" {
		v.Set("lang", lang)
	}
	if len(v) == 0 {
		return "/samples"
	}
	return "/samples?" + v.Encode()
}

// samples is the browsable collection.
//
// Until this route existed the only way to a sample was a link from the
// package it happens to be about, so a reader who wanted to see what this
// network had actually built had nowhere to go. R2C-136.
func (s *site) samples(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		page = min(p, maxSamplesPage)
	}

	// A reader looking for a reusable answer types the package, the API or a
	// word from the goal. All three live in the manifest, so one box searches
	// all of them rather than making them choose a field first.
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 128 {
		query = query[:128]
	}
	var rows []SampleListItem
	var total int
	var err error
	if query != "" {
		rows, total, err = s.d.Store.SearchSamples(r.Context(), query, (page-1)*samplesPerPage, samplesPerPage)
	} else {
		rows, total, err = s.d.Store.SamplesPage(r.Context(), (page-1)*samplesPerPage, samplesPerPage)
	}
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	// A page number past the end is a stale link, not an error. Send the
	// reader to the LAST real page, the way /records, /findings and /wanted
	// all do: a bookmark into a collection that has since grown belongs near
	// where its owner was standing, and page 1 is the one place they can
	// always get to anyway. The query is kept -- dropping it would answer
	// "page 40 of your search" with the whole collection, a different
	// question.
	if len(rows) == 0 && page > 1 {
		last := 1
		if total > 0 {
			last = (total + samplesPerPage - 1) / samplesPerPage
		}
		if last > maxSamplesPage {
			last = maxSamplesPage
		}
		http.Redirect(w, r, samplesHref(query, last, lang), http.StatusFound)
		return
	}

	n := func(v int) string { return i18n.FormatInt(lang, int64(v)) }
	view := samplesView{
		Page:     page,
		Total:    n(total),
		From:     n((page-1)*samplesPerPage + 1),
		To:       n((page-1)*samplesPerPage + len(rows)),
		Empty:    len(rows) == 0,
		Query:    query,
		Searched: query != "",
	}
	if view.Empty {
		view.From = n(0)
	}
	for _, row := range rows {
		view.Cards = append(view.Cards, sampleCard{
			SampleID:  row.SampleID,
			Href:      sampleHref(row.SampleID),
			Goal:      sampleGoalHeadline(row.Goal),
			Subject:   row.Version,
			Symbols:   row.Symbols,
			Context:   row.Context,
			Kind:      row.Kind,
			Verified:  row.Status == "CROSS_PASS" || row.Status == "PUBLISHED",
			CreatedAt: row.CreatedAt,
		})
	}
	if page > 1 {
		view.PrevHref = samplesHref(query, page-1, lang)
	}
	if len(rows) == samplesPerPage && total > page*samplesPerPage {
		view.NextHref = samplesHref(query, page+1, lang)
	}

	b := s.page(r, lang, i18n.T(lang, "samples.title")+" — CodeSampleX", i18n.T(lang, "samples.sub"))
	// One canonical URL per language. A page of the same collection is the
	// collection sliced, not a different page; the language is a different
	// page, and dropping it here would point every translation at the English
	// one.
	b.Canonical = b.canonicalURL(s.base(r), "/samples")
	view.basePage = b
	// Only on the canonical view: the canonical drops the query and the page
	// number, so an ItemList emitted from a search or a later page would
	// describe rows that URL does not serve.
	if query == "" && page == 1 {
		entries := make([]collectionEntry, 0, len(view.Cards))
		for _, c := range view.Cards {
			entries = append(entries, collectionEntry{Name: c.Goal, URL: s.base(r) + c.Href})
		}
		view.JSONLD = []template.JS{collectionJSONLD(b.Canonical,
			i18n.T(lang, "samples.title"), i18n.T(lang, "samples.sub"), entries)}
	}
	s.render(w, "samples", http.StatusOK, view)
}

// sampleGoalHeadline is the goal with the coordinate taken off the end.
//
// The queue asks a worker to verify one symbol at one release, so the goal it
// writes back reads "verify <symbol> in pkg:<eco>/<name>@<version>" — and the
// card already prints that release and that symbol on the line below. The
// suffix was therefore saying the same thing twice, in the one place a reader
// scans first, and it is where the overflow came from: 175 characters with no
// space to break at ran straight out of the card.
//
// A goal an author wrote themselves has no such suffix and is left alone.
func sampleGoalHeadline(goal string) string {
	if i := strings.LastIndex(goal, " in pkg:"); i > 0 {
		return goal[:i]
	}
	return goal
}
