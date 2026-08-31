package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// dependenciesPerPage is how many subjects one page shows, and maxDependenciesPage
// bounds how deep a crawler may walk. Both match /wanted, because a reader who
// has learned one list's shape should not have to learn another's.
const (
	dependenciesPerPage = 50
	maxDependenciesPage = 200
)

type dependencySubjectItem struct {
	Ecosystem    string
	Name         string
	Version      string
	Coord        string
	Href         string
	PackageHref  string
	ParentsText  string
	ProjectsText string
	Selected     bool
}

type dependencyParentItem struct {
	Name         string
	Version      string
	Coord        string
	Href         string
	ProjectsText string
}

type dependenciesPage struct {
	basePage
	Items       []dependencySubjectItem
	Total       int
	Query       string
	HasQuery    bool
	ClearHref   string
	Page, Pages int
	RangeText   string
	PageText    string

	// Selected is the subject whose parents are shown, empty when none was
	// asked for.
	SelectedCoord string
	SelectedHref  string
	Parents       []dependencyParentItem
	ParentsEmpty  bool

	PrevHref, NextHref string
}

// dependencies renders the atlas: the dependency graph read from the child's
// side.
//
// Every other surface reads it from the parent's. `Dependencies` answers "what
// did this package pull", which requires already knowing a parent, and the
// package page shows the same thing under "ships with". The question a reader
// actually arrives with — who pulls this release, and at which version — had
// no entry point at all, so a dependency map of 14,000 edges could only be
// entered through a package somebody already suspected.
//
// What it deliberately does not say: that any of this is a compatibility
// claim. An edge records that a resolver placed one release beside another on
// some machine. It is not evidence that they work together, and the page says
// so rather than letting a reader infer it from the presence of a row.
func (s *site) dependencies(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		page = min(p, maxDependenciesPage)
	}

	rows, total, err := s.d.Store.DependencySubjects(r.Context(), query,
		(page-1)*dependenciesPerPage, dependenciesPerPage)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	pages := (total + dependenciesPerPage - 1) / dependenciesPerPage
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		http.Redirect(w, r, dependenciesHref(query, pages, lang), http.StatusFound)
		return
	}

	// The subject whose parents to show. Named by its three parts rather than
	// a purl, because a purl in a query string has to survive two levels of
	// escaping to come back the same and an npm scope starts with the one
	// character that does not.
	selEco := strings.TrimSpace(r.URL.Query().Get("eco"))
	selName := strings.TrimSpace(r.URL.Query().Get("name"))
	selVer := strings.TrimSpace(r.URL.Query().Get("ver"))
	selected := selEco != "" && selName != "" && selVer != ""

	n := func(v int) string { return i18n.FormatInt(lang, int64(v)) }

	view := dependenciesPage{
		basePage: s.page(r, lang, i18n.T(lang, "dependencies.title")+" — CodeSampleX",
			i18n.T(lang, "meta.dependencies")),
		Total: total, Query: query, HasQuery: query != "",
		ClearHref: dependenciesHref("", 1, lang),
		Page:      page, Pages: pages,
		PageText: i18n.T(lang, "records.page", n(page), n(pages)),
	}
	if total > 0 {
		from := (page-1)*dependenciesPerPage + 1
		to := from + len(rows) - 1
		view.RangeText = i18n.T(lang, "records.range", n(from), n(to), n(total))
	}
	for _, row := range rows {
		coord := row.Name + "@" + row.Version
		item := dependencySubjectItem{
			Ecosystem: row.Ecosystem, Name: row.Name, Version: row.Version,
			Coord:        coord,
			Href:         dependencySubjectHref(query, page, lang, row),
			PackageHref:  view.WithLang(versionHref(row.Ecosystem, row.Name, row.Version)),
			ParentsText:  i18n.Plural(lang, "dependencies.n_parents", row.Parents),
			ProjectsText: i18n.Plural(lang, "dependencies.n_projects", row.Projects),
			Selected: selected && row.Ecosystem == selEco &&
				row.Name == selName && row.Version == selVer,
		}
		view.Items = append(view.Items, item)
	}

	if selected {
		view.SelectedCoord = selName + "@" + selVer
		view.SelectedHref = view.WithLang(versionHref(selEco, selName, selVer))
		parents, err := s.d.Store.DependencyParents(r.Context(), selEco, selName, selVer)
		if err != nil {
			s.unavailable(w, r, lang)
			return
		}
		for _, e := range parents {
			view.Parents = append(view.Parents, dependencyParentItem{
				Name: e.ParentName, Version: e.ParentVersion,
				Coord:        e.ParentName + "@" + e.ParentVersion,
				Href:         view.WithLang(versionHref(selEco, e.ParentName, e.ParentVersion)),
				ProjectsText: i18n.Plural(lang, "dependencies.n_projects", e.Projects),
			})
		}
		view.ParentsEmpty = len(view.Parents) == 0
	}

	if page > 1 {
		view.PrevHref = dependenciesHref(query, page-1, lang)
	}
	if page < pages {
		view.NextHref = dependenciesHref(query, page+1, lang)
	}
	s.render(w, "dependencies", http.StatusOK, view)
}

// dependenciesHref builds a list URL, omitting every part that is at its
// default so the canonical form of the first page is just /dependencies.
func dependenciesHref(query string, page int, lang string) string {
	q := url.Values{}
	if query != "" {
		q.Set("q", query)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if lang != "en" {
		q.Set("lang", lang)
	}
	if len(q) == 0 {
		return "/dependencies"
	}
	return "/dependencies?" + q.Encode()
}

// dependencySubjectHref keeps the reader's place: selecting a subject must not
// silently reset the search or throw them back to page one.
func dependencySubjectHref(query string, page int, lang string, row DependencySubject) string {
	q := url.Values{}
	if query != "" {
		q.Set("q", query)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if lang != "en" {
		q.Set("lang", lang)
	}
	q.Set("eco", row.Ecosystem)
	q.Set("name", row.Name)
	q.Set("ver", row.Version)
	return "/dependencies?" + q.Encode()
}

// dependencyAtlasHref points at one release in the atlas, with its parents
// already open — the other side of the edge the reader is looking at.
func dependencyAtlasHref(ecosystem, name, version string) string {
	q := url.Values{}
	q.Set("eco", ecosystem)
	q.Set("name", name)
	q.Set("ver", version)
	return "/dependencies?" + q.Encode()
}
