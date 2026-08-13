// Package web is the server-rendered public website: landing page,
// Compatibility Explorer, sample/seeder pages, install scripts and SEO
// surface (plan P6.1–P6.3, goal.md §14.5/§14.6).
//
// Everything is read-only and snapshot-backed: pages consume the Store
// interface below, which returns materialized snapshot/stats JSON — the
// site never aggregates raw evidence at request time.
//
// All assets are embedded; the pages reference no external host, so the
// site works over plain http, behind Caddy, and offline.
package web

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

//go:embed install/install.ps1 install/install.sh
var installFS embed.FS

// Store is the website's own consumer-side view of server data. The real
// serverstore is adapted to this interface by the integrator; tests use
// an in-memory fake. All snapshot/stats payloads arrive as JSON strings
// exactly as materialized (goal.md §14.5: web reads snapshots only).
type Store interface {
	// LatestStatsJSON returns the newest NetworkStats JSON, false when no
	// stats have been materialized yet.
	LatestStatsJSON(ctx context.Context) (string, bool)
	// SnapshotJSON returns the compatibility snapshot for (purl, symbol);
	// symbol "" addresses the package-level snapshot.
	SnapshotJSON(ctx context.Context, purl, symbol string) (string, bool)
	// PackageVersions lists known versions of a package, newest first.
	PackageVersions(ctx context.Context, ecosystem, name string) ([]string, error)
	// PackageSymbols lists symbol families with evidence for one version.
	PackageSymbols(ctx context.Context, ecosystem, name, version string) ([]string, error)
	// SampleMeta returns published-sample metadata by content id.
	SampleMeta(ctx context.Context, id string) (SampleMeta, bool)
	// SampleReceipts returns the verification-receipt JSON documents of a sample.
	SampleReceipts(ctx context.Context, id string) ([]string, error)
	// SeederSamples lists samples published under a seeder login.
	SeederSamples(ctx context.Context, login string) ([]SampleListItem, error)
	// SearchPackages searches packages by name fragment.
	SearchPackages(ctx context.Context, q string, limit int) ([]PackageHit, error)
	// HotPackages returns the highest-traffic packages for sitemap/landing use.
	HotPackages(ctx context.Context, limit int) ([]PackageHit, error)
	// FailureClusters returns failure-cluster JSON documents for a package.
	FailureClusters(ctx context.Context, ecosystem, name string) ([]string, error)
}

// SampleMeta is the sample header the sample page renders.
type SampleMeta struct {
	SampleID     string
	Status       string // PUBLISHED | CROSS_PASS | MATRIX_PASS | STABLE | …
	License      string
	OriginSeeder string // "" ⇒ anonymous
	CreatedAt    string // RFC3339
	ManifestJSON string // csx.json content
	Files        []string
}

// SampleListItem is one row in a seeder's published-samples list.
type SampleListItem struct {
	SampleID  string
	Goal      string
	Status    string
	License   string
	CreatedAt string
}

// PackageHit is one package search/hot result.
type PackageHit struct {
	Ecosystem     string
	Name          string
	LatestVersion string
	Symbols       int
	EvidenceCount int64
}

// Deps wires the site to the rest of the server.
type Deps struct {
	Store     Store
	PublicURL string // canonical origin, e.g. https://codesamplex.dev; "" ⇒ derive from request
	Version   string // csx release version shown in the footer / JSON-LD
	DistDir   string // directory with release binaries served under /dl/; "" ⇒ /dl 404s
}

const langCookie = "csx_lang"

// knownEcosystems guards the /{ecosystem}/... routes against junk paths.
var knownEcosystems = map[string]bool{"npm": true, "pypi": true, "cargo": true, "golang": true}

type site struct {
	d    Deps
	tmpl map[string]*template.Template
}

// Register mounts every website route on mux.
func Register(mux *http.ServeMux, d Deps) {
	s := &site{d: d, tmpl: parseTemplates()}

	mux.HandleFunc("GET /{$}", s.landingRoot)
	mux.HandleFunc("GET /{seg}", s.oneSegment)
	// Locale landings are registered as literal patterns ("GET /ko/{$}") so
	// they never conflict with /static/ or the package wildcard routes.
	// /en/ serves English rather than redirecting to "/": the root
	// re-negotiates Accept-Language, so redirecting there sent every
	// non-English browser straight back to its own language and made
	// English unreachable. Visiting an explicit locale is a deliberate
	// choice, so it is remembered like the ?lang= switch.
	for _, code := range i18n.Supported {
		lang := code
		mux.HandleFunc("GET /"+code+"/{$}", func(w http.ResponseWriter, r *http.Request) {
			setLangCookie(w, lang)
			s.landing(w, r, lang)
		})
	}
	mux.HandleFunc("GET /explore", s.explore)
	mux.HandleFunc("GET /stats", s.statsPage)
	mux.HandleFunc("GET /adapters", s.adaptersPage)
	mux.HandleFunc("GET /samples/{id}", s.samplePage)
	mux.HandleFunc("GET /seeders/{login}", s.seederPage)
	mux.HandleFunc("GET /robots.txt", s.robots)
	mux.HandleFunc("GET /sitemap.xml", s.sitemap)
	mux.HandleFunc("GET /install.ps1", s.installScript("install/install.ps1"))
	mux.HandleFunc("GET /install.sh", s.installScript("install/install.sh"))
	mux.HandleFunc("GET /dl/{file}", s.download)
	mux.Handle("GET /static/", cacheControl(http.FileServerFS(staticFS)))
	mux.HandleFunc("GET /{ecosystem}/{rest...}", s.packageRoutes)
}

func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		next.ServeHTTP(w, r)
	})
}

func parseTemplates() map[string]*template.Template {
	pages := []string{"landing", "explore", "package", "version", "symbol",
		"sample", "seeder", "stats", "adapters", "error"}
	out := make(map[string]*template.Template, len(pages))
	for _, p := range pages {
		out[p] = template.Must(template.ParseFS(templateFS,
			"templates/base.html", "templates/"+p+".html"))
	}
	return out
}

// base returns the canonical origin without a trailing slash.
func (s *site) base(r *http.Request) string {
	if s.d.PublicURL != "" {
		return strings.TrimSuffix(s.d.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// setLangCookie remembers an explicit language choice for a year.
func setLangCookie(w http.ResponseWriter, lang string) {
	http.SetCookie(w, &http.Cookie{
		Name: langCookie, Value: lang, Path: "/",
		MaxAge: int((365 * 24 * time.Hour).Seconds()), SameSite: http.SameSiteLaxMode,
	})
}

// negotiate resolves the request language for non-landing pages:
// ?lang= query (persisted in a cookie) → cookie → Accept-Language → en.
func (s *site) negotiate(w http.ResponseWriter, r *http.Request) string {
	if q := r.URL.Query().Get("lang"); q != "" {
		if l, ok := i18n.Canonical(q); ok {
			setLangCookie(w, l)
			return l
		}
	}
	if c, err := r.Cookie(langCookie); err == nil {
		if l, ok := i18n.Canonical(c.Value); ok {
			return l
		}
	}
	return i18n.Match(r.Header.Get("Accept-Language"))
}

// alternate is one hreflang link.
type alternate struct{ Lang, URL string }

// langLink is one language-switcher entry.
type langLink struct{ Code, Name, URL string }

// basePage carries everything base.html needs; page structs embed it so
// {{.T "key"}} and the SEO fields are available everywhere.
type basePage struct {
	Lang        string
	Title       string
	Description string
	Canonical   string
	Alternates  []alternate
	JSONLD      []template.JS
	Version     string
	IsLanding   bool
	path        string
	query       url.Values // current query without lang
}

// T translates a UI string into the page language.
func (b basePage) T(key string, args ...any) string { return i18n.T(b.Lang, key, args...) }

// N formats an integer with the page locale's digit grouping.
func (b basePage) N(n int64) string { return i18n.FormatInt(b.Lang, n) }

// Pct formats a 0..1 ratio as a percentage.
func (b basePage) Pct(f float64) string { return i18n.FormatPercent(b.Lang, f) }

// Project links shown in the header and footer. The client is open source
// (goal.md §3.14) and the network is funded by sponsorship, so both belong
// on every page.
const (
	repoURL    = "https://github.com/r2cuerdame/CodeSampleX"
	sponsorURL = "https://github.com/sponsors/r2cuerdame"
)

// RepoURL is the public source repository.
func (b basePage) RepoURL() string { return repoURL }

// SponsorURL is the GitHub Sponsors page funding the public network.
func (b basePage) SponsorURL() string { return sponsorURL }

// HomeHref is the landing URL for the page language.
func (b basePage) HomeHref() string {
	if b.Lang == i18n.Default {
		return "/"
	}
	return "/" + b.Lang + "/"
}

// WithLang decorates an internal link so the chosen language follows the
// visitor across non-landing pages (?lang= + cookie, plan P6.3).
func (b basePage) WithLang(path string) string {
	if b.Lang == i18n.Default {
		return path
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "lang=" + url.QueryEscape(b.Lang)
}

// LangLinks builds the footer language switcher. Every entry — English
// included — carries an explicit locale: linking English to bare "/" made
// the switcher a no-op for anyone whose browser asks for another language,
// because "/" re-runs Accept-Language negotiation and lands right back on
// the language they were trying to leave.
func (b basePage) LangLinks() []langLink {
	links := make([]langLink, 0, len(i18n.Supported))
	for _, code := range i18n.Supported {
		var href string
		if b.IsLanding {
			href = "/" + code + "/"
		} else {
			q := url.Values{}
			for k, vs := range b.query {
				q[k] = vs
			}
			q.Set("lang", code)
			href = b.path + "?" + q.Encode()
		}
		links = append(links, langLink{Code: code, Name: i18n.NativeName[code], URL: href})
	}
	return links
}

// page assembles the shared page chrome. Every indexable page gets a
// canonical URL and a full hreflang cluster (9 locales + x-default).
func (s *site) page(r *http.Request, lang, title, desc string) basePage {
	base := s.base(r)
	path := r.URL.Path
	q := url.Values{}
	for k, vs := range r.URL.Query() {
		if k != "lang" {
			q[k] = vs
		}
	}
	b := basePage{
		Lang:        lang,
		Title:       title,
		Description: desc,
		Canonical:   base + path,
		Version:     s.d.Version,
		path:        path,
		query:       q,
	}
	b.Alternates = queryAlternates(base, path)
	return b
}

// queryAlternates builds hreflang links for ?lang=-negotiated pages.
func queryAlternates(base, path string) []alternate {
	alts := make([]alternate, 0, len(i18n.Supported)+1)
	for _, code := range i18n.Supported {
		u := base + path
		if code != i18n.Default {
			u += "?lang=" + url.QueryEscape(code)
		}
		alts = append(alts, alternate{Lang: code, URL: u})
	}
	alts = append(alts, alternate{Lang: "x-default", URL: base + path})
	return alts
}

// landingAlternates builds the path-prefix hreflang cluster for the landing.
func landingAlternates(base string) []alternate {
	alts := make([]alternate, 0, len(i18n.Supported)+1)
	for _, code := range i18n.Supported {
		u := base + "/"
		if code != i18n.Default {
			u = base + "/" + code + "/"
		}
		alts = append(alts, alternate{Lang: code, URL: u})
	}
	alts = append(alts, alternate{Lang: "x-default", URL: base + "/"})
	return alts
}

func (s *site) render(w http.ResponseWriter, name string, status int, data any) {
	t, ok := s.tmpl[name]
	if !ok {
		http.Error(w, "template missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "base.html", data); err != nil {
		// Headers are already written; nothing safe to add.
		return
	}
}

// errorPage renders the localized error template.
type errorPage struct {
	basePage
	Status int
}

func (s *site) notFound(w http.ResponseWriter, r *http.Request, lang string) {
	b := s.page(r, lang, i18n.T(lang, "error.not_found")+" — CodeSampleX", i18n.T(lang, "error.not_found"))
	b.Alternates = nil // error pages are not indexable
	b.Canonical = ""
	s.render(w, "error", http.StatusNotFound, errorPage{basePage: b, Status: http.StatusNotFound})
}

func (s *site) unavailable(w http.ResponseWriter, r *http.Request, lang string) {
	b := s.page(r, lang, i18n.T(lang, "error.unavailable")+" — CodeSampleX", i18n.T(lang, "error.unavailable"))
	b.Alternates = nil
	b.Canonical = ""
	s.render(w, "error", http.StatusServiceUnavailable, errorPage{basePage: b, Status: http.StatusServiceUnavailable})
}

// oneSegment handles bare single-segment paths: /ko → /ko/ (canonical
// locale landing); anything else is not a page.
func (s *site) oneSegment(w http.ResponseWriter, r *http.Request) {
	seg := r.PathValue("seg")
	if i18n.Has(seg) {
		target := "/" + seg + "/"
		if seg == i18n.Default {
			target = "/"
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
	}
	s.notFound(w, r, s.negotiate(w, r))
}

// robots allows everything and advertises the sitemap.
func (s *site) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("User-agent: *\nAllow: /\n\nSitemap: " + s.base(r) + "/sitemap.xml\n"))
}

// installScript serves an embedded installer with the deployment's real
// origin substituted, so one-line installs work on any host.
func (s *site) installScript(path string) http.HandlerFunc {
	raw, err := installFS.ReadFile(path)
	if err != nil {
		panic("web: missing embedded installer " + path)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		body := strings.ReplaceAll(string(raw), "__CSX_BASE_URL__", s.base(r))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}
}

// download serves release binaries from DistDir. The file name is
// reduced to a clean basename; anything else 404s (no traversal).
func (s *site) download(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if s.d.DistDir == "" || name == "" ||
		strings.Contains(name, "/") || strings.Contains(name, "\\") ||
		strings.Contains(name, "..") || name != filepath.Base(name) {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(s.d.DistDir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, name, info.ModTime(), f)
}
