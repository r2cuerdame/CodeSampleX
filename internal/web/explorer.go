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
		parts = append(parts, e.OS)
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

func looksLikeVersion(seg string) bool { return versionRe.MatchString(seg) }

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
		if looksLikeVersion(segs[i]) {
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
	Clusters  []clusterView
}

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
	if len(versions) == 0 && len(clusters) == 0 {
		s.notFound(w, r, lang)
		return
	}
	base := s.base(r)
	title := name + " " + eco + " compatibility — CodeSampleX"
	b := s.page(r, lang, title, i18n.T(lang, "meta.explorer", name+" ("+eco+")"))
	b.JSONLD = []template.JS{breadcrumbJSONLD([][2]string{
		{"CodeSampleX", base + "/"},
		{eco, base + "/explore?q=" + url.QueryEscape(eco)},
		{name, base + pkgHref(eco, name)},
	})}
	s.render(w, "package", http.StatusOK, packagePage{
		basePage: b, Ecosystem: eco, Name: name,
		Versions: versions, Clusters: clusters,
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
	title := name + " " + version + " compatibility — CodeSampleX"
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
	title := strings.Join(titleParts, " ") + " compatibility — CodeSampleX"

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
// Explore.

type explorePage struct {
	basePage
	Query string
	Hits  []PackageHit
	Hot   bool
}

func (s *site) explore(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var hits []PackageHit
	var err error
	hot := q == ""
	if hot {
		hits, err = s.d.Store.HotPackages(r.Context(), 30)
	} else {
		hits, err = s.d.Store.SearchPackages(r.Context(), q, 30)
	}
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	title := i18n.T(lang, "explore.title") + " — CodeSampleX"
	b := s.page(r, lang, title, i18n.T(lang, "site.meta_description"))
	b.Canonical = s.base(r) + "/explore"
	s.render(w, "explore", http.StatusOK, explorePage{
		basePage: b, Query: q, Hits: hits, Hot: hot,
	})
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

type samplePageData struct {
	basePage
	Meta     SampleMeta
	Manifest *domain.SampleManifest
	Level    string
	Context  string
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

	title := i18n.T(lang, "sample.title") + " " + shortHash(meta.SampleID) + " — CodeSampleX"
	desc := i18n.T(lang, "site.meta_description")
	if manifest != nil && manifest.Case.Goal != "" {
		desc = manifest.Case.Goal
	}
	b := s.page(r, lang, title, desc)
	ctx := ""
	if manifest != nil {
		ctx = manifest.Environment.ContextLabel()
	}
	s.render(w, "sample", http.StatusOK, samplePageData{
		basePage: b, Meta: meta, Manifest: manifest,
		Level: levelBadge(meta.Status), Context: ctx, Receipts: receipts,
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

type adapterRow struct {
	adapterEntry
	Levels []bool // aligned with AllCapabilityLevels
}

type adaptersPageData struct {
	basePage
	Doc    *adaptersDoc
	Rows   []adapterRow
	Levels []string
}

func (s *site) adaptersPage(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	doc := loadAdapters()
	if doc == nil {
		s.unavailable(w, r, lang)
		return
	}
	rows := make([]adapterRow, 0, len(doc.Adapters))
	for _, a := range doc.Adapters {
		has := make([]bool, len(AllCapabilityLevels))
		for i, lvl := range AllCapabilityLevels {
			for _, c := range a.Capabilities {
				if c == lvl {
					has[i] = true
				}
			}
		}
		rows = append(rows, adapterRow{adapterEntry: a, Levels: has})
	}
	title := i18n.T(lang, "adapters.title") + " — CodeSampleX"
	b := s.page(r, lang, title, i18n.T(lang, "site.meta_description"))
	s.render(w, "adapters", http.StatusOK, adaptersPageData{
		basePage: b, Doc: doc, Rows: rows, Levels: AllCapabilityLevels,
	})
}
