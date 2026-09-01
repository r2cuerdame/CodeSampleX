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
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/buildinfo"
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
	// SymbolPackageSpread reports how many DISTINCT packages of one ecosystem
	// carry evidence for each symbol.
	//
	// One build's detected symbols are attributed to every package in its
	// closure, so commons-logging's page listed MockHttpServletRequest — a
	// Spring Test class — as its own API. 563 of the corpus's 4,165 symbols
	// are claimed by more than one package, worst case twenty-one. A count
	// above one means this evidence cannot say whose the symbol is.
	SymbolPackageSpread(ctx context.Context, ecosystem string, symbols []string) (map[string]int, error)
	// SampleMeta returns published-sample metadata by content id.
	SampleMeta(ctx context.Context, id string) (SampleMeta, bool)
	// SampleManifest returns only the stored manifest. Collection pages use
	// this instead of SampleMeta so they do not open and decompress the
	// artifact merely to learn its recorded environment.
	SampleManifest(ctx context.Context, id string) (manifestJSON string, ok bool)
	// SampleReceipts returns the verification-receipt JSON documents of a sample.
	SampleReceipts(ctx context.Context, id string) ([]string, error)
	// SampleSource returns the readable files of a sample's artifact.
	//
	// Naming the files without offering them left a visitor able to see that a
	// sample exists and not what it says. The archive was the only way in, and
	// downloading a tarball to read forty lines of Go is not inspection -- it
	// is a barrier with a link on it.
	SampleSource(ctx context.Context, id string) ([]SampleFile, error)
	// SeederSamples lists samples published under a seeder login.
	SeederSamples(ctx context.Context, login string) ([]SampleListItem, error)
	// ListSamples lists published samples, newest first, for the sitemap.
	// Quarantined samples must not appear.
	ListSamples(ctx context.Context, limit int) ([]SampleListItem, error)
	// SamplesPage is one page of the /samples collection, newest first, with
	// the total so the page can say where the reader is. A sample nobody can
	// reach is a sample nobody can reuse, and until this route existed the
	// only way to a sample was a link from the package it happens to be
	// about.
	SamplesPage(ctx context.Context, offset, limit int) ([]SampleListItem, int, error)
	// SearchSamples is the same collection narrowed by what a reader typed.
	// The query matches the manifest — goal, packages, symbols — because that
	// is what somebody looking for a reusable answer knows to type.
	SearchSamples(ctx context.Context, query string, offset, limit int) ([]SampleListItem, int, error)
	// PackageSamples lists published samples whose manifest names this
	// package, newest first. This is what puts a link to a sample page on
	// a page a crawler already reaches.
	PackageSamples(ctx context.Context, ecosystem, name string, limit int) ([]SampleListItem, error)
	// ReleaseSamples lists published samples whose manifest names one EXACT
	// release. It is what resolves a sample's human-readable URL back to its
	// content address, so unlike PackageSamples its bound is per release
	// rather than per package: a newest-N window over a package would stop
	// resolving the older samples of a busy package, and those are URLs the
	// sitemap has already advertised.
	//
	// Quarantined samples must not appear. Verification must NOT be
	// required: a readable address is addressing, and whether a contract ran
	// is a separate fact the page states for itself.
	ReleaseSamples(ctx context.Context, ecosystem, name, version string, limit int) ([]SampleListItem, error)
	// PackageCodeCounts returns exhaustive verified-sample counts by release
	// and API. It is deliberately separate from the bounded PackageSamples
	// display list: a newest-N page cannot prove that older code does not
	// exist.
	PackageCodeCounts(ctx context.Context, ecosystem, name string) ([]PackageCodeCount, error)
	// SearchPackages searches packages by name fragment.
	SearchPackages(ctx context.Context, q string, limit int) ([]PackageHit, error)
	// HotPackages returns the highest-traffic packages for sitemap use.
	HotPackages(ctx context.Context, limit int) ([]PackageHit, error)
	// RecordPackages returns one ranked page of the packages the network
	// has evidence for, plus the total so the page can be navigated.
	RecordPackages(ctx context.Context, filter RecordFilter, offset, limit int) (hits []PackageHit, total int, err error)
	// FailureClusters returns failure-cluster JSON documents for a package.
	// FailureClusters returns at most a page of clusters plus how many the
	// package actually has. pgx/v5 carries 133; rendering all of them was a
	// wall, and truncating without the total would read as "this is all".
	FailureClusters(ctx context.Context, ecosystem, name string) (clusters []string, total int, err error)
	// TopWanted lists the most-asked packages the network still has no
	// sample for, most wanted first.
	TopWanted(ctx context.Context, limit int) ([]WantedRow, error)
	// Coverage reports the network's own coverage per (platform, ecosystem).
	// A nil slice is not an error: the section simply does not render, which
	// is honest — an instrument that cannot describe itself should say
	WantedForPackage(ctx context.Context, ecosystem, name string) ([]WantedRow, error)
	// Dependencies lists what shipped ALONGSIDE each version of one package —
	// its first-level children, which is all anyone needs: the version that
	// moved under an upgrade is the one that broke the build.
	Dependencies(ctx context.Context, ecosystem, name string) ([]DependencyEdge, error)
	// DependencySubjects browses the graph from the CHILD's side -- one ranked,
	// searchable page of releases other packages resolved onto, and how many
	// match in total. Dependencies needs a parent named up front, so "who pulls
	// this" had no entry point at all.
	DependencySubjects(ctx context.Context, query string, offset, limit int) (rows []DependencySubject, total int, err error)
	// DependencyParents lists the exact releases that resolved onto one exact
	// release.
	DependencyParents(ctx context.Context, ecosystem, name, version string) ([]DependencyEdge, error)
	// DependencyResolvedNone reports whether a resolution measured this exact
	// release to declare nothing. An answer, as opposed to a release nothing
	// has read -- which is a gap, and must not render as the same blank.
	DependencyResolvedNone(ctx context.Context, ecosystem, name, version string) (bool, error)
	// PackageAssets reports, per package, how many of its releases this
	// network has proven and how many have an answered dependency question.
	//
	// Ratios rather than flags: "this package has a sample" is true of one
	// sample across fifty releases, and a reader takes it to mean covered.
	PackageAssets(ctx context.Context) ([]PackageAsset, error)
	// CompletenessGaps lists coordinates missing at least one of Sample,
	// Evidence and Dependency -- the census's eight cells, listed rather than
	// counted, emptiest first. The matrix could say two thirds of the corpus
	// was incomplete and not which two thirds, so the number was true and
	// nobody could act on it.
	CompletenessGaps(ctx context.Context, query string, offset, limit int) (rows []CompletenessGap, total int, err error)
	// DerivedFindings returns published samples that state the belief they
	// correct, newest first. These grow the /findings page without anyone
	// editing Go source.
	DerivedFindings(ctx context.Context) ([]DerivedFinding, error)
}

// DerivedFinding is a finding the network produced rather than one a
// person wrote: a published sample whose case declares what was believed,
// paired with the contract line that measured otherwise.
//
// The hand-written lists in findings.go are the reason this exists. They
// are good entries, but they are twenty-nine entries in a Go literal, and
// the storehouse they are drawn from passed three hundred samples and is
// aimed at ten thousand. The most persuasive page on the site was the only
// one that could not grow on its own.
type DerivedFinding struct {
	Ecosystem string
	Subject   string // "axios@1.12.0", from the first package purl
	Believed  string // Case.Believed, written by the sample's author
	Measured  string // the contract line that contradicts it
	SampleID  string
	// OS and Runtime come from the sample manifest's recorded environment.
	// They are empty when the manifest did not establish that dimension;
	// the findings page never guesses them from the ecosystem.
	OS          string
	Runtime     string
	Environment string
}

// RecordFilter is the user-visible slice of /records. Environment and
// evidence-basis dimensions are matched against materialized snapshot rows;
// an absent dimension does not count as a match.
type RecordFilter struct {
	Query     string
	Ecosystem string
	OS        string
	Runtime   string
	Basis     string // observed | verified | ""
}

// WantedRow is one unanswered question: a package people asked about that
// nothing in the network answers yet.
type WantedRow struct {
	Ecosystem string
	Name      string
	Version   string
	Symbol    string
	Asks      int64
	// TargetOS is the platform the miss was reported from, empty when the
	// reporter named none. A report about a platform is a different question
	// from a report about the package, which is why it is part of the row
	// rather than a note beside it.
	TargetOS string
	// HasPage reports whether an explorer page exists for this package.
	// A wanted package usually has neither sample nor evidence, so linking
	// every row made a board of 404s.
	HasPage bool
}

// SampleFile is one readable file of a sample, as the page shows it.
//
// Binary files are absent rather than mangled: a page of replacement
// characters is not source anybody can read or copy. The file LIST still names
// them, which is where a reader learns they exist.
type SampleFile struct {
	Name string
	Body string
	// Truncated says this is the first part of a longer file, and the page
	// says so rather than letting a reader copy half a file believing it is
	// whole.
	Truncated bool
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

// SampleListItem is one row of a published-samples list (a seeder's page,
// a package page, the sitemap).
type SampleListItem struct {
	SampleID string
	Goal     string
	Status   string
	License  string
	// Context is the execution context the sample was published from
	// ("node 22"), so a list row says which environment it answers for.
	Context   string
	CreatedAt string
	// Version is the package version this sample is about, and Symbols the
	// APIs it answers for. A sample answers one version of one API; without
	// these a package's list is an undifferentiated pile — uuid alone has
	// 96 published samples.
	Version string
	Symbols []string
	// Kind is the case kind: HOW | FIX | MIGRATION | CONFIG.
	Kind string
	// Ecosystem and Name complete the release coordinate Version starts.
	// They come from the same manifest purl, and they are what lets a list
	// row — and the sitemap — name the sample's canonical human-readable
	// URL without opening the sample.
	Ecosystem string
	Name      string
}

// Href is where this sample should be linked FROM: its human-readable
// canonical URL when it has one, and its content address otherwise.
//
// Internal links and the sitemap both use it. A site that links a page at
// one address and declares another canonical is telling a crawler to ignore
// every link it was given.
func (s SampleListItem) Href() string {
	slug := sampleSlug(s.SampleID, sampleSubject(s.Name, s.Goal, s.Symbols))
	if href := semanticSampleHref(s.Ecosystem, s.Name, s.Version, slug); href != "" {
		return href
	}
	return sampleHref(s.SampleID)
}

// Headline is the link text for this sample: the release and what it
// answers for. It is the anchor text on every page that lists samples, and
// it used to be the raw goal — which for most of the corpus is the line the
// authoring worker printed, so package and version pages linked their own
// samples as "verify pkg:npm/browserslist@4.28.7".
func (s SampleListItem) Headline() string {
	headline := serpHeadline(s.Name, s.Version, sampleSubject(s.Name, s.Goal, s.Symbols))
	if headline == "" {
		return s.SampleID
	}
	return headline
}

// PackageCodeCount is one authoritative code-availability aggregate. An
// empty Symbol is the whole-release count; a non-empty Symbol is one API.
type PackageCodeCount struct {
	Version string
	Symbol  string
	Samples int64
}

// DependencyEdge is one "this package pulled that version" relationship, as
// the machine holding the lockfile saw it.
//
// VersionCoresidence says two versions were installed together; this says who
// wanted each, which is the half a person can act on.
type DependencyEdge struct {
	ParentName    string
	ParentVersion string
	ChildName     string
	ChildVersion  string
	// int64 so the template's number formatter takes it directly. A mismatch
	// here does not fail loudly: html/template aborts mid-render and the page
	// simply stops, which looks like a missing item rather than an error.
	Projects int64
}

// The dependency axis's three answers, mirroring serverstore's constants.
//
// "Resolved and it declares nothing" and "nobody has ever looked" are the same
// blank on screen and opposite facts, and the page has to render them apart.
const (
	GapDependencyUnknown    = "unknown"
	GapDependencyGraph      = "graph"
	GapDependencyProvenNone = "none"
)

// PackageAsset is what this network holds for one package, counted over its
// releases rather than over its snapshot entries.
//
// /records ranked packages by entry count, which is a fact about this
// network's bookkeeping and not about the package: a library with 400 entries
// and no contract is less proven than one with three entries and a passing
// one.
type PackageAsset struct {
	Ecosystem      string
	Name           string
	Releases       int
	WithSample     int
	WithDependency int
}

// CompletenessGap is one coordinate with an axis still missing, and the reason
// an axis cannot be closed at all when that is the answer.
type CompletenessGap struct {
	Ecosystem string
	Name      string
	Version   string

	HasSample   bool
	HasEvidence bool
	// Dependency is one of the GapDependency* constants.
	Dependency string

	// Non-empty when this network cannot produce that axis for this
	// coordinate -- the authoring queue's own sentence, so a contributor is
	// not handed work every poll will decline.
	SampleNAReason     string
	DependencyNAReason string
}

// DependencySubject is one release other packages resolved onto, as the
// atlas ranks it.
//
// Parents counts distinct parent RELEASES, not names: "four releases of one
// library pulled this" and "four different libraries pulled this" are
// different facts, and the second is the one a reader is looking for.
type DependencySubject struct {
	Ecosystem string
	Name      string
	Version   string
	Parents   int64
	Projects  int64
}

// PackageHit is one package search/hot result.
type PackageHit struct {
	Ecosystem     string
	Name          string
	LatestVersion string
	Symbols       int
	EvidenceCount int64
	// UpdatedAt is when this package's compatibility snapshot was last
	// materialized — the date the records inventory orders by and prints.
	// "Most symbols first" answered "what does the network know most
	// about"; a log of measurements is read newest first.
	UpdatedAt string
	// Filter dimensions are populated by test stores and are not rendered.
	// The production adapter matches the same dimensions directly against
	// snapshot rows before it builds a PackageHit.
	OperatingSystems []string
	Runtimes         []string
	EvidenceBases    []string
}

// Deps wires the site to the rest of the server.
type Deps struct {
	Store     Store
	PublicURL string // canonical origin, e.g. https://codesamplex.dev; "" ⇒ derive from request
	// Build is the identity of the server process rendering the page. It is
	// resolved from the stamps the deployment put on this artifact, never
	// from a string written into a template: a hand-maintained version is
	// exactly the thing that keeps saying the old number after a rollback.
	Build   buildinfo.Info
	DistDir string // directory with release binaries served under /dl/; "" ⇒ /dl 404s
}

const langCookie = "csx_lang"

// knownEcosystems guards the /{ecosystem}/... routes against junk paths.
//
// Automatic project observation started with four ecosystems, but verified
// samples and compatibility snapshots now cover all nine. Keeping the old
// four-name route guard made every Gem, Composer, Hex and pub package URL in
// the sitemap a guaranteed 404 even though the data behind it existed.
var knownEcosystems = map[string]bool{
	"npm": true, "pypi": true, "cargo": true, "golang": true,
	"gem": true, "composer": true, "hex": true, "pub": true,
	"maven": true,
}

type site struct {
	d    Deps
	tmpl map[string]*template.Template

	// derived* cache the machine-derived findings. The scan reads every
	// recent manifest, which is fine on a timer and not fine per request.
	derivedMu    sync.Mutex
	derivedCache []finding
	derivedAt    time.Time
	// A cold or stale findings cache refreshes out of band. The public page
	// must never wait behind the whole-corpus scan that fills it, especially
	// while a fresh production builder is using the background DB lanes.
	derivedRefreshing bool
	derivedRetryAt    time.Time

	// assets caches the per-package three-axis rollup behind /compatibility.
	// It classifies every public release, which is a timer job and not a
	// per-request one.
	assets assetCache

	// hand* caches environment decoration for the static findings. Their
	// sample IDs are immutable, but the linked sample may arrive after a
	// deployment, so a short TTL avoids both permanent misses and 29 store
	// reads on every public request.
	handMu         sync.Mutex
	handDocumented []finding
	handBelieved   []finding
	handAt         time.Time
	handRefreshing bool
	handRetryAt    time.Time

	// cube* caches assembled compatibility cubes per package (cube.go):
	// one assembly reads dozens of snapshots, which is fine on a timer and
	// not fine per request.
	cubeMu       sync.Mutex
	cubeCache    map[string]cubeCacheEntry
	cubeRevision uint64
	// cubeLoading holds one channel per package currently being assembled,
	// closed after that foreground assembly publishes. Concurrent foreground
	// readers wait on it rather than each running the same fan-out.
	cubeLoading map[string]chan struct{}
	// heroCubeLoading is a separate singleflight lane for landing background
	// fills. Foreground readers never wait on it, so a slow post-restart hero
	// cannot move the public timeout from / to the next package page.
	heroCubeLoading map[string]chan struct{}
	// pinnedCube caches the repair read for coordinates the browse window
	// skipped, keyed by package and pin. It is small — one release, or one
	// symbol across the window's releases — and it is on the request path of
	// exactly the URLs people share, which are the ones that get linked from
	// somewhere and then hit repeatedly.
	pinnedCube map[string]pinnedCubeEntry

	// sitemap* caches the built sitemap index and shards. One rebuild reads
	// the whole indexable corpus, which is fine once per freshness window
	// and not fine per crawler request; every request inside the window is
	// served from memory (sitemap.go).
	sitemapMu    sync.Mutex
	sitemapCache *sitemapSnapshot
	sitemapAt    time.Time

	// hero* memoizes the landing's finished hero matrix per (language,
	// selection). Assembly is cached above; the PIVOTING was not, and a warm
	// landing still built up to thirty grids per view — six candidates by
	// five axis pairs, pure CPU on the most-requested URL of the site — to
	// arrive at the same matrix every time.
	heroMu    sync.Mutex
	heroCache map[string]heroCacheEntry
	// heroLoading is one background assembly per bounded landing view key.
	// heroCubeLoading is authoritative per package, so different locales
	// cannot multiply the database fan-out for the same cold cube.
	heroLoading map[string]bool
	heroRetryAt map[string]time.Time
}

type heroCacheEntry struct {
	data *heroMatrixData
	at   time.Time
}

// Register mounts every website route on mux.
func Register(mux *http.ServeMux, d Deps) {
	s := &site{d: d, tmpl: parseTemplates()}
	// handle registers a page behind a recover guard.
	//
	// The /v1 API has had one since the beginning; the website was mounted
	// bare, so a panic in any page handler killed the connection outright
	// and the visitor got no response at all -- not a 500, nothing. The
	// first one found was reachable from a URL: /records?page=<huge>
	// overflowed page*perPage into a negative offset and panicked on the
	// slice, from any browser, with no evidence rows needed.
	handle := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Printf("web: panic serving %s: %v", r.URL.Path, rec)
					s.unavailable(w, r, s.negotiate(w, r))
				}
			}()
			h(w, r)
		})
	}

	handle("GET /{$}", s.landingRoot)
	handle("GET /{seg}", s.oneSegment)
	// Locale landings are registered as literal patterns ("GET /ko/{$}") so
	// they never conflict with /static/ or the package wildcard routes.
	// /en/ serves English rather than redirecting to "/": the root
	// re-negotiates Accept-Language, so redirecting there sent every
	// non-English browser straight back to its own language and made
	// English unreachable. Visiting an explicit locale is a deliberate
	// choice, so it is remembered like the ?lang= switch.
	for _, code := range i18n.Supported {
		lang := code
		handle("GET /"+code+"/{$}", func(w http.ResponseWriter, r *http.Request) {
			setLangCookie(w, lang)
			s.landing(w, r, lang)
		})
	}
	// /records named this network's bookkeeping, not the reader's question.
	// The page answers "what does CodeSampleX know about this package", so it
	// is /compatibility; the old address keeps working because it is in old
	// READMEs, old MCP replies and external links.
	handle("GET /records", recordsGone)
	handle("GET /compatibility", s.records)
	handle("GET /findings", s.findings)
	// /wanted ranked what people searched for and missed. That is demand, and
	// the page it belonged on claimed to be the work left over -- a coordinate
	// nobody has ever asked about can be the largest hole in the corpus. /gaps
	// is the same question answered from the completeness census instead.
	handle("GET /wanted", wantedGone)
	handle("GET /gaps", s.gaps)
	handle("GET /dependencies", s.dependencies)
	handle("GET /features", s.features)
	// One rule for a trailing slash, applied everywhere: redirect to the
	// slashless form. It was inconsistent — /records/ and /findings/ hard
	// 404'd while a package page happily served /npm/zod/ as a second 200
	// that canonicalized to itself, so the same page existed at two indexed
	// URLs. Both halves of that are fixed by picking one form and sending
	// the other to it.
	handle("GET /records/{$}", redirectToSlashless)
	handle("GET /compatibility/{$}", redirectToSlashless)
	handle("GET /findings/{$}", redirectToSlashless)
	handle("GET /wanted/{$}", redirectToSlashless)
	handle("GET /gaps/{$}", redirectToSlashless)
	handle("GET /dependencies/{$}", redirectToSlashless)
	handle("GET /features/{$}", redirectToSlashless)
	// /explore was the old name for the same page.
	handle("GET /explore", s.explorePage)
	// /contribute is retired, and its address survives in old READMEs, old
	// MCP replies and external links. A URL people were sent to gets a
	// redirect, not a 404; the nearest living answer to "how do I
	// contribute" is the gap list — installing csx already contributes
	// evidence, and /gaps is what is left to write.
	contributeGone := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/gaps", http.StatusMovedPermanently)
	}
	handle("GET /contribute", contributeGone)
	handle("GET /contribute/{$}", contributeGone)
	handle("GET /stats", s.statsPage)
	handle("GET /adapters", s.adaptersPage)
	handle("GET /samples", s.samples)
	handle("GET /samples/{id}", s.samplePage)
	handle("GET /seeders/{login}", s.seederPage)
	handle("GET /robots.txt", s.robots)
	// /sitemap.xml is the index robots.txt advertises; the shards it names
	// live under /sitemaps/. Both serve from the same cached snapshot.
	handle("GET /sitemap.xml", s.sitemapIndex)
	handle("GET /sitemaps/{file}", s.sitemapShardPage)
	handle("GET /install.ps1", s.installScript("install/install.ps1"))
	handle("GET /install.sh", s.installScript("install/install.sh"))
	handle("GET /dl/{file}", s.download)
	// The asset version is the deployed build's short revision, the same
	// token every page appends to its stylesheet link.
	assetVersion := ""
	if line := buildLineFor(d.Build, i18n.Default); line != nil {
		assetVersion = line.ShortRevision
		if assetVersion == "" {
			assetVersion = line.Version
		}
	}
	mux.Handle("GET /static/", staticCache(assetVersion, http.FileServerFS(staticFS)))
	handle("GET /{ecosystem}/{rest...}", s.packageRoutes)
}

// staticCache decides how long a static asset may be kept, and gives it a
// validator so that revalidating costs a header rather than the file.
//
// Every page links the stylesheet as /static/site.css?v=<short revision>, so
// that URL's bytes never change: it can be cached for a year and never asked
// about again. It was served with max-age=3600 and no ETag and no
// Last-Modified, which made a returning visitor re-fetch 26KB every hour over
// whatever link they have, and made the revalidation a full re-send because
// there was nothing to revalidate against.
//
// The same file WITHOUT the token is a different promise. Nothing makes that
// URL change, so it gets a short life -- long enough to help a burst of
// requests, short enough that a deploy is picked up.
//
// An unstamped build has no version, and then there is nothing honest to
// promise: no ETag, short life for everything.
func staticCache(version string, next http.Handler) http.Handler {
	etag := ""
	if version != "" {
		etag = `"` + version + `"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if etag != "" {
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			if r.URL.Query().Get("v") == version {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		next.ServeHTTP(w, r)
	})
}

func parseTemplates() map[string]*template.Template {
	pages := []string{"landing", "compatibility", "findings", "samples", "gaps", "dependencies", "features", "package", "version",
		"symbol", "sample", "seeder", "error"}
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
	// The body varies by both of these, and without saying so a shared cache
	// is entitled to hand one visitor's language to the next.
	w.Header().Add("Vary", "Accept-Language")
	w.Header().Add("Vary", "Cookie")
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
	// Build is nil when this process carries no build identity, and the
	// footer then says nothing rather than inventing one.
	Build     *buildLine
	IsLanding bool
	OGImage   string
	// OGType is the og:type of the page. Empty renders "website"; pages
	// that are a dated document rather than a site section set "article".
	OGType string
	// Related is the way out of this collection into the next one. Empty on
	// every page that is not one of the five collections.
	Related []relatedLink
	path    string
	query   url.Values // current query without lang
}

// buildLine is the server-identity line in the footer: which build of this
// server answered this request, and which deployment it belongs to.
//
// The visible part is deliberately the short form. The full revision is what
// a deploy log and an image label carry, so it belongs in the hover detail
// where an operator can copy it, not in forty characters of page chrome.
type buildLine struct {
	Version       string
	ShortRevision string
	Environment   string
	// Detail is the title attribute: the full revision and, when the build
	// was stamped with one, the time the artifact was built.
	Detail string
}

// buildLineFor renders info for lang, or nil when there is nothing to say.
func buildLineFor(info buildinfo.Info, lang string) *buildLine {
	if !info.Known() {
		return nil
	}
	line := &buildLine{
		Version:       info.Version,
		ShortRevision: info.ShortRevision(),
		Environment:   info.Environment,
	}
	if line.Version == "" {
		// A revision with no version still answers the operational question.
		line.Version = line.ShortRevision
		line.ShortRevision = ""
	}
	parts := []string{i18n.T(lang, "footer.build"), info.Environment}
	if info.Revision != "" {
		parts = append(parts, info.Revision)
	}
	if !info.BuiltAt.IsZero() {
		parts = append(parts, info.BuiltAt.UTC().Format(time.RFC3339))
	}
	line.Detail = strings.Join(parts, " · ")
	return line
}

// AssetVersion is the cache-busting token on the stylesheet. It has to
// change exactly when the deployed build does, and never otherwise: the
// short revision is unique per deployment, and an unstamped build has no
// token rather than a constant one.
func (b basePage) AssetVersion() string {
	if b.Build == nil {
		return ""
	}
	if b.Build.ShortRevision != "" {
		return b.Build.ShortRevision
	}
	return b.Build.Version
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
// P renders a count together with the noun it counts, inflected for the
// locale. Templates used to write the number and the noun separately, which is
// how "1 findings across 1 ecosystems", "1 tools" and "1 anonymous daily
// reports" reached the live pages: the number was right and nothing could make
// the word agree with it.
func (b basePage) P(key string, n any) string {
	var v int64
	switch t := n.(type) {
	case int:
		v = int64(t)
	case int64:
		v = t
	case int32:
		v = int64(t)
	}
	return i18n.Plural(b.Lang, key, v)
}

func (b basePage) WithLang(path string) string {
	if b.Lang == i18n.Default {
		return path
	}
	// A same-page fragment needs no locale decoration: the current document
	// is already in this language. Appending `?lang=` after `#` changes the
	// fragment identifier itself ("#failures?lang=ko") and breaks the jump.
	if strings.HasPrefix(path, "#") {
		return path
	}
	fragment := ""
	if i := strings.IndexByte(path, '#'); i >= 0 {
		fragment, path = path[i:], path[:i]
	}
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	return path + sep + "lang=" + url.QueryEscape(b.Lang) + fragment
}

// LangName is the current language in its own name, for the header picker's
// closed state. A picker labelled in a language the reader cannot read is the
// one control on the page they most need to recognise without reading it.
func (b basePage) LangName() string { return i18n.NativeName[b.Lang] }

// LangLinks builds the language switcher. Every entry — English
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
	// The canonical must carry the language. Built as base+path it named the
	// ENGLISH url on every localized page, so all nine languages disavowed
	// themselves in favour of one — nine translations collapsing into a
	// single indexed page, which is the opposite of what translating them
	// was for. It matches the hreflang set below now, so each language is
	// self-canonical.
	canonical := base + path
	if lang != i18n.Default {
		canonical += "?lang=" + url.QueryEscape(lang)
	}
	b := basePage{
		Lang:        lang,
		Title:       title,
		Description: desc,
		Canonical:   canonical,
		Build:       buildLineFor(s.d.Build, lang),
		path:        path,
		query:       q,
	}
	b.Alternates = queryAlternates(base, path)
	// Derived from the path rather than set by each handler: the five
	// collections were reachable from one another only through the header,
	// and two of them are deliberately not in the header.
	b.Related = relatedCollections(lang, path)
	return b
}

// queryAlternates builds hreflang links for ?lang=-negotiated pages.
func queryAlternates(base, path string) []alternate {
	return queryAlternatesWithQuery(base, path, nil)
}

// queryAlternatesWithQuery is used when a query value identifies the
// resource rather than filtering it (currently symbol names that cannot be
// represented safely as one URL path segment). It preserves that identity
// while changing only the locale.
func queryAlternatesWithQuery(base, path string, values url.Values) []alternate {
	alts := make([]alternate, 0, len(i18n.Supported)+1)
	for _, code := range i18n.Supported {
		q := url.Values{}
		for key, list := range values {
			q[key] = append([]string(nil), list...)
		}
		if code != i18n.Default {
			q.Set("lang", code)
		}
		u := base + path
		if encoded := q.Encode(); encoded != "" {
			u += "?" + encoded
		}
		alts = append(alts, alternate{Lang: code, URL: u})
	}
	q := url.Values{}
	for key, list := range values {
		q[key] = append([]string(nil), list...)
	}
	u := base + path
	if encoded := q.Encode(); encoded != "" {
		u += "?" + encoded
	}
	alts = append(alts, alternate{Lang: "x-default", URL: u})
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

// redirectToSlashless sends /path/ to /path, keeping the query so a
// language choice survives the hop.
func redirectToSlashless(w http.ResponseWriter, r *http.Request) {
	target := strings.TrimSuffix(r.URL.Path, "/")
	if target == "" {
		target = "/"
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}
