package web

import (
	"net/http"
	"net/url"
	"strconv"

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
}

func samplesHref(page int, lang string) string {
	href := "/samples"
	sep := "?"
	if page > 1 {
		href += sep + "page=" + strconv.Itoa(page)
		sep = "&"
	}
	if lang != "en" {
		href += sep + "lang=" + lang
	}
	return href
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

	rows, total, err := s.d.Store.SamplesPage(r.Context(), (page-1)*samplesPerPage, samplesPerPage)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	// A page number past the end is a stale link, not an error.
	if len(rows) == 0 && page > 1 {
		http.Redirect(w, r, samplesHref(1, lang), http.StatusFound)
		return
	}

	n := func(v int) string { return i18n.FormatInt(lang, int64(v)) }
	view := samplesView{
		Page:  page,
		Total: n(total),
		From:  n((page-1)*samplesPerPage + 1),
		To:    n((page-1)*samplesPerPage + len(rows)),
		Empty: len(rows) == 0,
	}
	if view.Empty {
		view.From = n(0)
	}
	for _, row := range rows {
		view.Cards = append(view.Cards, sampleCard{
			SampleID:  row.SampleID,
			Href:      sampleHref(row.SampleID),
			Goal:      row.Goal,
			Subject:   row.Version,
			Symbols:   row.Symbols,
			Context:   row.Context,
			Kind:      row.Kind,
			Verified:  row.Status == "CROSS_PASS" || row.Status == "PUBLISHED",
			CreatedAt: row.CreatedAt,
		})
	}
	if page > 1 {
		view.PrevHref = samplesHref(page-1, lang)
	}
	if len(rows) == samplesPerPage && total > page*samplesPerPage {
		view.NextHref = samplesHref(page+1, lang)
	}

	b := s.page(r, lang, i18n.T(lang, "samples.title")+" — CodeSampleX", i18n.T(lang, "samples.sub"))
	// One canonical URL per language. A page of the same collection is the
	// collection sliced, not a different page; the language is a different
	// page, and dropping it here would point every translation at the English
	// one.
	b.Canonical = s.base(r) + "/samples"
	if lang != i18n.Default {
		b.Canonical += "?lang=" + url.QueryEscape(lang)
	}
	view.basePage = b
	s.render(w, "samples", http.StatusOK, view)
}
