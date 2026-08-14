package web

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// ---------------------------------------------------------------------------
// Snapshot documents (materialized by internal/compatibility, read here as
// JSON — the site never aggregates raw evidence at request time).

type stageCount struct {
	Pass int64 `json:"pass"`
	Fail int64 `json:"fail"`
}

type snapshotRow struct {
	ContextLabel      string                         `json:"contextLabel"`
	EnvLabel          string                         `json:"envLabel"`
	Env               *domain.EnvironmentFingerprint `json:"env"`
	Confidence        string                         `json:"confidence"`
	ElevatedFailure   bool                           `json:"elevatedFailure"`
	PassRate          float64                        `json:"passRate"`
	UniquePeerBuckets int64                          `json:"uniquePeerBuckets"`
	LastSeen          string                         `json:"lastSeen"`
	ByStage           map[string]stageCount          `json:"byStage"`
}

type failureCluster struct {
	Symbol              string                     `json:"symbol"`
	Stage               string                     `json:"stage"`
	ErrorCode           string                     `json:"errorCode"`
	Fingerprint         string                     `json:"fingerprint"`
	Count               int64                      `json:"count"`
	ObservationCount    int64                      `json:"observationCount"`
	EnvSummary          map[string]string          `json:"envSummary"`
	Hypotheses          []domain.FailureHypothesis `json:"hypotheses"`
	RegressionCandidate bool                       `json:"regressionCandidate"`
	Versions            []string                   `json:"versions"`
}

type snapshotDoc struct {
	SchemaVersion int              `json:"schemaVersion"`
	PURL          string           `json:"purl"`
	Symbol        string           `json:"symbol"`
	GeneratedAt   string           `json:"generatedAt"`
	Rows          []snapshotRow    `json:"rows"`
	Failures      []failureCluster `json:"failures"`
}

// ---------------------------------------------------------------------------
// View models.

// matrixRow is one execution-context row of the compatibility matrix.
// Observations (USED/PROJECT_*) and verifications (SYMBOL_*/verification
// stages/CONTRACT) are carried separately and never summed (§3.5).
type matrixRow struct {
	Context        string // leading dimension: "node 22", "safari 19"
	Detail         string // remaining env dims: "TS 5.9 · pnpm · windows"
	Chip           string // HIGH | MEDIUM | LOW | ELEVATED FAILURE | UNKNOWN
	ChipClass      string // high | medium | low | elevated | unknown
	Glyph          string // non-color marker for the chip
	NoEvidence     bool
	Observations   int64
	Verifications  int64
	ObservedStages string // "PROJECT_COMPILE 100✓ 4✕"
	VerifiedStages string // "CONTRACT 6✓ 1✕"
	PassRate       string // formatted, "" when no evidence
	Peers          int64
	LastSeen       string // date part
}

type hypothesisView struct {
	Domain string
	Pct    string
}

type clusterView struct {
	Symbol              string
	Stage               string
	ErrorCode           string
	Fingerprint         string
	Count               int64
	EnvSummary          string
	Hypotheses          []hypothesisView
	RegressionCandidate bool
	Versions            string
}

func chipFor(row snapshotRow, obs, ver int64) (chip, class, glyph string, noEvidence bool) {
	conf := strings.ToUpper(strings.TrimSpace(row.Confidence))
	if row.ElevatedFailure || conf == "ELEVATED_FAILURE" || conf == "ELEVATED FAILURE" {
		return "ELEVATED FAILURE", "elevated", "▲", false
	}
	switch conf {
	case "HIGH":
		return "HIGH", "high", "✓", false
	case "MEDIUM":
		return "MEDIUM", "medium", "◐", false
	case "LOW":
		return "LOW", "low", "○", false
	}
	return "UNKNOWN", "unknown", "?", obs+ver == 0
}

// languageShort renders "typescript 5.9" as the conventional "TS 5.9".
func languageShort(lang, version string) string {
	short := map[string]string{
		"typescript": "TS", "javascript": "JS", "python": "Python",
		"go": "Go", "rust": "Rust",
	}
	l := short[strings.ToLower(lang)]
	if l == "" {
		l = lang
	}
	if version != "" {
		return l + " " + version
	}
	return l
}

// rowLabels derives the leading context and the detail cell of a row.
func rowLabels(row snapshotRow) (ctx, detail string) {
	ctx = row.ContextLabel
	if ctx == "" && row.Env != nil {
		ctx = row.Env.ContextLabel()
	}
	if ctx == "" {
		ctx = "unknown"
	}
	if row.EnvLabel != "" {
		return ctx, row.EnvLabel
	}
	if row.Env == nil {
		return ctx, ""
	}
	e := row.Env.Bucketed()
	var parts []string
	if e.Language != "" {
		parts = append(parts, languageShort(e.Language, e.LanguageVersion))
	}
	if e.PackageManager != "" {
		parts = append(parts, e.PackageManager)
	}
	if e.OS != "" {
		os := e.OS
		// musl vs glibc decides whether a native module loads at all, so
		// it belongs next to the OS rather than hidden in the raw JSON.
		if e.Libc != "" {
			os += " " + e.Libc
		}
		parts = append(parts, os)
	}
	// A container or VM run proves something about that sandbox, not
	// about the host that started it — say so on the row.
	if e.Virtualization != "" {
		v := e.Virtualization
		if e.ContainerRuntime != "" {
			v = e.ContainerRuntime
		}
		parts = append(parts, v)
	}
	if e.CI {
		parts = append(parts, "ci")
	}
	return ctx, strings.Join(parts, " · ")
}

// splitStageCounts separates weak project observations from strong
// verification evidence and renders each group's per-stage counts.
func splitStageCounts(byStage map[string]stageCount) (obs, ver int64, obsText, verText string) {
	names := make([]string, 0, len(byStage))
	for k := range byStage {
		names = append(names, k)
	}
	sort.Strings(names)
	var obsParts, verParts []string
	for _, stage := range names {
		c := byStage[stage]
		txt := stage + " " + i18n.FormatInt("en", c.Pass) + "✓"
		if c.Fail > 0 {
			txt += " " + i18n.FormatInt("en", c.Fail) + "✕"
		}
		if stage == string(domain.StageUsed) || strings.HasPrefix(stage, "PROJECT_") {
			obs += c.Pass + c.Fail
			obsParts = append(obsParts, txt)
		} else {
			ver += c.Pass + c.Fail
			verParts = append(verParts, txt)
		}
	}
	return obs, ver, strings.Join(obsParts, " · "), strings.Join(verParts, " · ")
}

func buildMatrix(lang string, doc snapshotDoc) []matrixRow {
	rows := make([]matrixRow, 0, len(doc.Rows))
	for _, r := range doc.Rows {
		obs, ver, obsText, verText := splitStageCounts(r.ByStage)
		chip, class, glyph, noEvidence := chipFor(r, obs, ver)
		ctx, detail := rowLabels(r)
		row := matrixRow{
			Context: ctx, Detail: detail,
			Chip: chip, ChipClass: class, Glyph: glyph, NoEvidence: noEvidence,
			Observations: obs, Verifications: ver,
			ObservedStages: obsText, VerifiedStages: verText,
			Peers:    r.UniquePeerBuckets,
			LastSeen: datePart(r.LastSeen),
		}
		if obs+ver > 0 {
			row.PassRate = i18n.FormatPercent(lang, r.PassRate)
		}
		rows = append(rows, row)
	}
	return rows
}

func buildClusters(clusters []failureCluster) []clusterView {
	out := make([]clusterView, 0, len(clusters))
	for _, c := range clusters {
		count := c.Count
		if count == 0 {
			count = c.ObservationCount
		}
		keys := make([]string, 0, len(c.EnvSummary))
		for k := range c.EnvSummary {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		envParts := make([]string, 0, len(keys))
		for _, k := range keys {
			envParts = append(envParts, k+"="+c.EnvSummary[k])
		}
		hyps := make([]hypothesisView, 0, len(c.Hypotheses))
		for _, h := range c.Hypotheses {
			hyps = append(hyps, hypothesisView{
				Domain: string(h.Domain),
				Pct:    i18n.FormatPercent("en", h.Confidence),
			})
		}
		out = append(out, clusterView{
			Symbol: c.Symbol, Stage: c.Stage, ErrorCode: c.ErrorCode,
			Fingerprint: shortHash(c.Fingerprint), Count: count,
			EnvSummary: strings.Join(envParts, " · "), Hypotheses: hyps,
			RegressionCandidate: c.RegressionCandidate,
			Versions:            strings.Join(c.Versions, " → "),
		})
	}
	return out
}

func datePart(rfc3339 string) string {
	if len(rfc3339) >= 10 {
		return rfc3339[:10]
	}
	return rfc3339
}

func shortHash(h string) string {
	if len(h) > 19 {
		return h[:19] + "…"
	}
	return h
}

// ---------------------------------------------------------------------------
// Routing: /{ecosystem}/{name...}[/{version}[/{symbol}]] with multi-segment
// names (golang module paths, npm scopes).

var versionRe = regexp.MustCompile(`^v?\d+(\.\d+)*([-+.][0-9A-Za-z.+-]*)?$`)

// goMajorSuffixRe matches the major-version element of a Go module path:
// the "/v5" in github.com/golang-jwt/jwt/v5. It is part of the import path,
// not a version, and a released Go module version always carries a full
// major.minor.patch — so a bare vN in the golang namespace is never one.
var goMajorSuffixRe = regexp.MustCompile(`^v[0-9]+$`)

// looksLikeVersion reports whether a URL segment ends the package name.
//
// The golang exception is not a nicety: without it every module at v2 or
// above had no package page at all. /golang/github.com/golang-jwt/jwt/v5
// split as the package "github.com/golang-jwt/jwt" at version "v5", which
// does not exist, and dropping the suffix does not help either because the
// module really is named with it. Both spellings 404'd, so chi/v5, jwt/v5
// and every other v2+ module was unreachable while decimal, which has no
// suffix, was fine.
func looksLikeVersion(ecosystem, seg string) bool {
	if ecosystem == "golang" && goMajorSuffixRe.MatchString(seg) {
		return false
	}
	return versionRe.MatchString(seg)
}

// splitPackagePath resolves the rest of a package URL into name, version
// and symbol. The first version-looking segment after the minimum name
// length ends the name; golang names may span many segments.
func splitPackagePath(ecosystem, rest string) (name, version, symbol string, ok bool) {
	segs := strings.Split(strings.Trim(rest, "/"), "/")
	if len(segs) == 0 || segs[0] == "" {
		return "", "", "", false
	}
	minName := 1
	if ecosystem == "npm" && strings.HasPrefix(segs[0], "@") {
		minName = 2
	}
	if len(segs) < minName {
		return "", "", "", false
	}
	verIdx := -1
	for i := minName; i < len(segs); i++ {
		if looksLikeVersion(ecosystem, segs[i]) {
			verIdx = i
			break
		}
	}
	if verIdx == -1 {
		return strings.Join(segs, "/"), "", "", true
	}
	name = strings.Join(segs[:verIdx], "/")
	version = segs[verIdx]
	tail := segs[verIdx+1:]
	if len(tail) > 1 {
		return "", "", "", false
	}
	if len(tail) == 1 {
		symbol = tail[0]
		if symbol == "" {
			return "", "", "", false
		}
	}
	return name, version, symbol, true
}

func (s *site) packageRoutes(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	eco := r.PathValue("ecosystem")
	if !knownEcosystems[eco] {
		s.notFound(w, r, lang)
		return
	}
	name, version, symbol, ok := splitPackagePath(eco, r.PathValue("rest"))
	if !ok {
		s.notFound(w, r, lang)
		return
	}
	switch {
	case version == "":
		s.packagePage(w, r, lang, eco, name)
	case symbol == "":
		s.versionPage(w, r, lang, eco, name, version)
	default:
		s.symbolPage(w, r, lang, eco, name, version, symbol)
	}
}

func pkgHref(eco, name string) string {
	return "/" + eco + "/" + escapePathSegments(name)
}

// ---------------------------------------------------------------------------
// Package page.

type packagePage struct {
	basePage
	Ecosystem string
	Name      string
	Versions  []string
	Samples   []SampleListItem
	Clusters  []clusterView
}

// packageSampleLimit bounds the samples listed on a package page. It is a
// reading list, not an archive; the sitemap is what guarantees every
// sample is reachable.
const packageSampleLimit = 25

func (s *site) loadClusters(r *http.Request, eco, name string) []clusterView {
	raw, err := s.d.Store.FailureClusters(r.Context(), eco, name)
	if err != nil {
		return nil
	}
	clusters := make([]failureCluster, 0, len(raw))
	for _, doc := range raw {
		var c failureCluster
		if json.Unmarshal([]byte(doc), &c) == nil {
			clusters = append(clusters, c)
		}
	}
	return buildClusters(clusters)
}

func (s *site) packagePage(w http.ResponseWriter, r *http.Request, lang, eco, name string) {
	versions, err := s.d.Store.PackageVersions(r.Context(), eco, name)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	clusters := s.loadClusters(r, eco, name)
	// Samples are listed here because this is the page a crawler already
	// reaches from the sitemap: without a link from somewhere indexed, a
	// sample page exists but is never visited.
	samples, err := s.d.Store.PackageSamples(r.Context(), eco, name, packageSampleLimit)
	if err != nil {
		samples = nil // the rest of the page is still worth serving
	}
	if len(versions) == 0 && len(clusters) == 0 && len(samples) == 0 {
		s.notFound(w, r, lang)
		return
	}
	base := s.base(r)
	// Translated: the <html lang> said one language while the title was
	// always English, which is the first thing a search result shows.
	title := i18n.T(lang, "title.compatibility", name, eco) + " — CodeSampleX"
	b := s.page(r, lang, title, i18n.T(lang, "meta.explorer", name+" ("+eco+")"))
	b.JSONLD = []template.JS{breadcrumbJSONLD([][2]string{
		{"CodeSampleX", base + "/"},
		{eco, base + "/explore?q=" + url.QueryEscape(eco)},
		{name, base + pkgHref(eco, name)},
	})}
	s.render(w, "package", http.StatusOK, packagePage{
		basePage: b, Ecosystem: eco, Name: name,
		Versions: versions, Samples: samples, Clusters: clusters,
	})
}

// ---------------------------------------------------------------------------
// Version page.

type versionPage struct {
	basePage
	Ecosystem string
	Name      string
	Ver       string
	Symbols   []string
	Matrix    []matrixRow
}

func (s *site) versionPage(w http.ResponseWriter, r *http.Request, lang, eco, name, version string) {
	purl := domain.PURL{Ecosystem: eco, Name: name, Version: version}.String()
	symbols, err := s.d.Store.PackageSymbols(r.Context(), eco, name, version)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	var matrix []matrixRow
	if raw, ok := s.d.Store.SnapshotJSON(r.Context(), purl, ""); ok {
		var doc snapshotDoc
		if json.Unmarshal([]byte(raw), &doc) == nil {
			matrix = buildMatrix(lang, doc)
		}
	}
	if len(symbols) == 0 && len(matrix) == 0 {
		s.notFound(w, r, lang)
		return
	}
	base := s.base(r)
	title := i18n.T(lang, "title.compatibility", name, version) + " — CodeSampleX"
	b := s.page(r, lang, title, i18n.T(lang, "meta.explorer", name+"@"+version))
	b.JSONLD = []template.JS{breadcrumbJSONLD([][2]string{
		{"CodeSampleX", base + "/"},
		{name, base + pkgHref(eco, name)},
		{version, base + pkgHref(eco, name) + "/" + url.PathEscape(version)},
	})}
	s.render(w, "version", http.StatusOK, versionPage{
		basePage: b, Ecosystem: eco, Name: name, Ver: version,
		Symbols: symbols, Matrix: matrix,
	})
}

// ---------------------------------------------------------------------------
// Symbol page: the compatibility matrix with execution context as the
// leading row dimension (docs/execution-context.md §6).

type symbolPage struct {
	basePage
	Ecosystem string
	Name      string
	Ver       string
	Symbol    string
	PURL      string
	Matrix    []matrixRow
	Clusters  []clusterView
	Generated string
}

func (s *site) symbolPage(w http.ResponseWriter, r *http.Request, lang, eco, name, version, symbol string) {
	purl := domain.PURL{Ecosystem: eco, Name: name, Version: version}.String()
	raw, ok := s.d.Store.SnapshotJSON(r.Context(), purl, symbol)
	if !ok {
		s.notFound(w, r, lang)
		return
	}
	var doc snapshotDoc
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		s.unavailable(w, r, lang)
		return
	}
	matrix := buildMatrix(lang, doc)
	clusters := buildClusters(doc.Failures)

	// Descriptive title leads with the strongest context row:
	// "axios.post axios 1.12.0 node 22 compatibility — CodeSampleX".
	titleParts := []string{symbol, name, version}
	if len(matrix) > 0 && matrix[0].Context != "unknown" {
		titleParts = append(titleParts, matrix[0].Context)
	}
	title := i18n.T(lang, "title.compatibility_one", strings.Join(titleParts, " ")) + " — CodeSampleX"

	base := s.base(r)
	b := s.page(r, lang, title, i18n.T(lang, "meta.explorer", symbol+" — "+name+"@"+version))
	verHref := base + pkgHref(eco, name) + "/" + url.PathEscape(version)
	b.JSONLD = []template.JS{breadcrumbJSONLD([][2]string{
		{"CodeSampleX", base + "/"},
		{name, base + pkgHref(eco, name)},
		{version, verHref},
		{symbol, verHref + "/" + url.PathEscape(symbol)},
	})}
	s.render(w, "symbol", http.StatusOK, symbolPage{
		basePage: b, Ecosystem: eco, Name: name, Ver: version, Symbol: symbol,
		PURL: purl, Matrix: matrix, Clusters: clusters,
		Generated: datePart(doc.GeneratedAt),
	})
}

// ---------------------------------------------------------------------------
// Records: everything the network has evidence for, searchable and paged.

// recordsPerPage keeps the page readable. A dependency tree runs to
// hundreds of packages, and a single unbounded list is unusable.
const recordsPerPage = 40

type recordsPage struct {
	basePage
	Query string
	Hits  []PackageHit
	Total int
	// Page numbers are 1-based for the reader. RangeText and PageText are
	// rendered here rather than in the template so the numbers get the
	// locale's own grouping ("1–40 of 1,204").
	Page, Pages        int
	RangeText          string
	PageText           string
	PrevHref, NextHref string
}

// explorePage redirects the former URL; the page is now /records.
func (s *site) explorePage(w http.ResponseWriter, r *http.Request) {
	target := "/records"
	if q := r.URL.RawQuery; q != "" {
		target += "?" + q
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

func (s *site) records(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		page = p
	}

	hits, total, err := s.d.Store.RecordPackages(r.Context(), q, (page-1)*recordsPerPage, recordsPerPage)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	pages := (total + recordsPerPage - 1) / recordsPerPage
	if pages == 0 {
		pages = 1
	}
	// A page number past the end is a stale link, not an error: show the
	// last real page instead of an empty screen.
	if page > pages {
		http.Redirect(w, r, recordsHref(q, pages, lang), http.StatusFound)
		return
	}

	from := (page-1)*recordsPerPage + 1
	to := (page-1)*recordsPerPage + len(hits)
	if total == 0 {
		from = 0
	}
	n := func(v int) string { return i18n.FormatInt(lang, int64(v)) }
	view := recordsPage{
		Query: q, Hits: hits, Total: total,
		Page: page, Pages: pages,
		RangeText: i18n.T(lang, "records.range", n(from), n(to), n(total)),
		PageText:  i18n.T(lang, "records.page", n(page), n(pages)),
	}
	if page > 1 {
		view.PrevHref = recordsHref(q, page-1, lang)
	}
	if page < pages {
		view.NextHref = recordsHref(q, page+1, lang)
	}

	title := i18n.T(lang, "records.title") + " — CodeSampleX"
	b := s.page(r, lang, title, i18n.T(lang, "meta.explore"))
	// One canonical URL PER LANGUAGE for the record: paged and searched
	// views are the same collection sliced differently, and indexing each
	// slice separately would just split the page's signal — but the language
	// is not a slice of the same page, it is a different page, and dropping
	// it here made every translation point at the English one.
	b.Canonical = s.base(r) + "/records"
	if lang != i18n.Default {
		b.Canonical += "?lang=" + url.QueryEscape(lang)
	}
	view.basePage = b
	s.render(w, "records", http.StatusOK, view)
}

// recordsHref builds a /records link that keeps the query, page and
// language the reader is on.
func recordsHref(q string, page int, lang string) string {
	v := url.Values{}
	if q != "" {
		v.Set("q", q)
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	if lang != i18n.Default {
		v.Set("lang", lang)
	}
	if len(v) == 0 {
		return "/records"
	}
	return "/records?" + v.Encode()
}

// ---------------------------------------------------------------------------
// Sample page.

type receiptView struct {
	Context    string
	Capability string
	Stages     string
	Verifier   string
	CreatedAt  string
	PeerID     string
}

// descPackageLimit caps how many packages the meta description names.
// Measured over the 117 seed samples: 89 name one package, 15 name two,
// and six name three or more (the largest names six).
const descPackageLimit = 3

// pkgRef is one package a sample is about. Href is empty when the site
// has no page for that ecosystem (samples exist for ecosystems the
// explorer does not route, and a link into a 404 is worse than text).
type pkgRef struct {
	Label string // "axios 1.12.2"
	PURL  string // "pkg:npm/axios@1.12.2"
	Href  string // "/npm/axios"
}

// packageRefs resolves the manifest's purls into display labels and, where
// the explorer routes that ecosystem, links.
//
// The link is the package page, not the version page. A version page only
// exists when that exact version string has a snapshot, and manifest
// versions do not always agree: pkg:golang/github.com/shopspring/decimal
// is published both as "@1.4.0" and "@v1.4.0", and only the second has a
// page (measured on the live site — the first answers 404). The package
// page always exists for a package a published sample names, because that
// sample is now one of the things the page lists.
func packageRefs(purls []string) []pkgRef {
	refs := make([]pkgRef, 0, len(purls))
	for _, p := range purls {
		ref := pkgRef{Label: strings.TrimPrefix(p, "pkg:"), PURL: p}
		if parsed, err := domain.ParsePURL(p); err == nil {
			ref.Label = parsed.Name + " " + parsed.Version
			if knownEcosystems[parsed.Ecosystem] {
				ref.Href = pkgHref(parsed.Ecosystem, parsed.Name)
			}
		}
		refs = append(refs, ref)
	}
	return refs
}

type samplePageData struct {
	basePage
	Meta     SampleMeta
	Manifest *domain.SampleManifest
	Level    string
	Context  string
	Goal     string
	Packages []pkgRef
	Receipts []receiptView
}

// levelBadge maps a sample status to the honest verification level
// (goal.md §6.2): publication implies a local contract pass, nothing more.
func levelBadge(status string) string {
	switch status {
	case "LOCAL":
		return string(domain.L0SourceOnly)
	case "LOCAL_PASS", "PUBLISHED":
		return string(domain.L3ContractPass)
	case "CROSS_PASS":
		return string(domain.L4CrossPass)
	case "MATRIX_PASS", "STABLE":
		return string(domain.L5MatrixPass)
	}
	return string(domain.L0SourceOnly)
}

func (s *site) samplePage(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	id := r.PathValue("id")
	meta, ok := s.d.Store.SampleMeta(r.Context(), id)
	if !ok {
		s.notFound(w, r, lang)
		return
	}
	var manifest *domain.SampleManifest
	var m domain.SampleManifest
	if json.Unmarshal([]byte(meta.ManifestJSON), &m) == nil {
		manifest = &m
	}

	var receipts []receiptView
	if docs, err := s.d.Store.SampleReceipts(r.Context(), id); err == nil {
		for _, doc := range docs {
			var rec domain.VerificationReceipt
			if json.Unmarshal([]byte(doc), &rec) != nil {
				continue
			}
			stages := make([]string, 0, len(rec.Stages))
			for k := range rec.Stages {
				stages = append(stages, k)
			}
			sort.Strings(stages)
			parts := make([]string, 0, len(stages))
			for _, st := range stages {
				parts = append(parts, st+":"+rec.Stages[st])
			}
			receipts = append(receipts, receiptView{
				Context:    rec.Environment.ContextLabel(),
				Capability: string(rec.SandboxCapability),
				Stages:     strings.Join(parts, " · "),
				Verifier:   rec.VerifierAdapter,
				CreatedAt:  datePart(rec.CreatedAt),
				PeerID:     rec.PeerID,
			})
		}
	}

	var (
		goal  string
		ctx   string
		refs  []pkgRef
		env   domain.EnvironmentFingerprint
		purls []string
		syms  []string
	)
	if manifest != nil {
		goal = strings.TrimSpace(manifest.Case.Goal)
		env = manifest.Environment
		ctx = env.ContextLabel()
		purls = manifest.Packages
		syms = manifest.Symbols
		refs = packageRefs(purls)
	}

	// The title and description are the whole visible surface of this page
	// in a search result, and the question the page answers is the goal.
	// Titling it with the content address instead ("Sample sha256:9d1d…")
	// gave every sample page a title nobody can search for.
	title := i18n.T(lang, "sample.title") + " " + shortHash(meta.SampleID) + " — CodeSampleX"
	if goal != "" {
		title = goal
		if len(refs) > 0 {
			title += " · " + refs[0].Label
		}
		title += " — CodeSampleX"
	}
	// Description: the goal, then the facts that decide whether this
	// answer applies to the reader — which packages, which environment.
	// No adjective about how well it works; the level badge and the
	// receipts on the page carry that, and they carry it exactly.
	desc := i18n.T(lang, "site.meta_description")
	if goal != "" {
		desc = goal
		var facts []string
		for i, ref := range refs {
			// A search snippet is ~160 characters and the goal already
			// spends most of it. The page lists every package; the
			// description names the ones that identify the sample.
			if i == descPackageLimit {
				facts = append(facts, "…")
				break
			}
			facts = append(facts, strings.TrimPrefix(ref.PURL, "pkg:"))
		}
		if ctx != "" {
			facts = append(facts, ctx)
		}
		if len(facts) > 0 {
			desc += " — " + strings.Join(facts, " · ")
		}
	}

	b := s.page(r, lang, title, desc)
	b.OGType = "article"
	base := s.base(r)
	pageURL := base + sampleHref(meta.SampleID)
	crumbs := [][2]string{{"CodeSampleX", base + "/"}}
	if len(refs) > 0 && refs[0].Href != "" {
		if parsed, err := domain.ParsePURL(refs[0].PURL); err == nil {
			crumbs = append(crumbs, [2]string{parsed.Name, base + pkgHref(parsed.Ecosystem, parsed.Name)})
		}
	}
	crumbName := goal
	if crumbName == "" {
		crumbName = shortHash(meta.SampleID)
	}
	crumbs = append(crumbs, [2]string{crumbName, pageURL})
	b.JSONLD = []template.JS{breadcrumbJSONLD(crumbs)}
	if goal != "" {
		b.JSONLD = append(b.JSONLD,
			sampleJSONLD(pageURL, goal, desc, meta.CreatedAt, meta.License, purls, syms, env))
	}

	s.render(w, "sample", http.StatusOK, samplePageData{
		basePage: b, Meta: meta, Manifest: manifest,
		Level: levelBadge(meta.Status), Context: ctx, Goal: goal,
		Packages: refs, Receipts: receipts,
	})
}

// ---------------------------------------------------------------------------
// Seeder page.

type seederPageData struct {
	basePage
	Login   string
	Samples []SampleListItem
}

func (s *site) seederPage(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	login := r.PathValue("login")
	samples, err := s.d.Store.SeederSamples(r.Context(), login)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	title := login + " — " + i18n.T(lang, "seeder.title") + " — CodeSampleX"
	b := s.page(r, lang, title, i18n.T(lang, "site.meta_description"))
	s.render(w, "seeder", http.StatusOK, seederPageData{
		basePage: b, Login: login, Samples: samples,
	})
}

// ---------------------------------------------------------------------------
// Adapters page: renders schemas/v1/adapters.json. The file is read from
// the deployment's schemas dir (or the repo during tests) and cached.

type adapterEntry struct {
	Ecosystem        string   `json:"ecosystem"`
	Name             string   `json:"name"`
	Version          string   `json:"version"`
	PackageManagers  []string `json:"packageManagers"`
	Capabilities     []string `json:"capabilities"`
	SymbolConfidence string   `json:"symbolConfidence"`
	Notes            string   `json:"notes"`
}

type adaptersDoc struct {
	SchemaVersion          int            `json:"schemaVersion"`
	Description            string         `json:"description"`
	Adapters               []adapterEntry `json:"adapters"`
	RuntimeInstrumentation string         `json:"runtimeInstrumentation"`
}

var (
	adaptersOnce   sync.Once
	adaptersCached *adaptersDoc
)

// AllCapabilityLevels is the fixed A0–A4 column set of the matrix.
var AllCapabilityLevels = []string{"A0", "A1", "A2", "A3", "A4"}

func loadAdapters() *adaptersDoc {
	adaptersOnce.Do(func() {
		candidates := []string{
			filepath.Join("schemas", "v1", "adapters.json"),
			filepath.Join("..", "..", "schemas", "v1", "adapters.json"),
		}
		if exe, err := os.Executable(); err == nil {
			candidates = append(candidates,
				filepath.Join(filepath.Dir(exe), "schemas", "v1", "adapters.json"))
		}
		for _, p := range candidates {
			raw, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			var doc adaptersDoc
			if json.Unmarshal(raw, &doc) == nil && len(doc.Adapters) > 0 {
				adaptersCached = &doc
				return
			}
		}
	})
	return adaptersCached
}

// confidenceKey maps a published symbol-confidence value to its i18n key.
var confidenceKey = map[string]string{
	"EXACT":    "adapters.conf_exact",
	"PROBABLE": "adapters.conf_probable",
	"UNKNOWN":  "adapters.conf_unknown",
}
