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
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// ---------------------------------------------------------------------------
// Snapshot documents (materialized by internal/compatibility, read here as
// JSON — the site never aggregates raw evidence at request time).

type stageCount struct {
	Pass int64 `json:"pass"`
	Fail int64 `json:"fail"`
	// FailAttributed is the subset of Fail whose sanitizer named a cause. The
	// rest say a build containing this package broke and nothing about which
	// package broke it.
	FailAttributed int64 `json:"failAttributed"`
}

type snapshotRow struct {
	ContextLabel string `json:"contextLabel"`
	EnvLabel     string `json:"envLabel"`
	// The producer writes this field as "envBucket" (compatibility
	// SnapshotRow.EnvBucket). Reading it as "env" meant it never decoded:
	// every matrix row on every package page rendered with no environment
	// detail at all, so two buckets differing only by libc, container
	// runtime or OS appeared as two identical rows with different
	// confidence chips and no way to tell which was which — including the
	// musl/glibc distinction the label code calls decisive.
	//
	// "env" is kept as an alias so a snapshot written before this is still
	// readable; the fake in the web tests hand-wrote "envLabel", which is
	// why nothing caught it.
	Env               *domain.EnvironmentFingerprint `json:"envBucket"`
	EnvAlias          *domain.EnvironmentFingerprint `json:"env"`
	Confidence        string                         `json:"confidence"`
	ElevatedFailure   bool                           `json:"elevatedFailure"`
	PassRate          float64                        `json:"passRate"`
	UniquePeerBuckets int64                          `json:"uniquePeerBuckets"`
	// VerificationCounts carries the verification-side counts, including
	// "distinctVerifyingPeers". It was never decoded, so a row proved by
	// five independent peers -- with no usage evidence, which is the normal
	// shape for a freshly seeded package -- printed 0 under a column headed
	// "Peers". UniquePeerBuckets is an OBSERVATION-side count, as its
	// producer says in as many words, and independence is the one thing a
	// reader uses that column to judge.
	VerificationCounts map[string]int64      `json:"verificationCounts"`
	LastSeen           string                `json:"lastSeen"`
	ByStage            map[string]stageCount `json:"byStage"`
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
	Context      string // leading dimension: "node 22", "safari 19"
	Detail       string // remaining env dims: "TS 5.9 · pnpm · windows"
	Chip         string // HIGH | MEDIUM | LOW | ELEVATED FAILURE | UNKNOWN
	ChipClass    string // high | medium | low | elevated | unknown
	Glyph        string // non-color marker for the chip
	NoEvidence   bool
	Observations int64
	// Usage counts presence records: the package was installed, nothing was
	// exercised. It was inside Observations, which made a column headed
	// "observations" partly a count of installed dependencies — and since
	// presence has no failing form, it could only ever make a coordinate
	// look better than it measured.
	Usage          int64
	Verifications  int64
	ObservedStages string // "PROJECT_COMPILE 100✓ 4✕"
	UsageStages    string // "USED 229✓"
	VerifiedStages string // "CONTRACT 6✓ 1✕"
	PassRate       string // formatted, "" when no evidence
	Peers          int64
	// VerifyingPeers is the verification-side count. It is rendered beside
	// Peers, never added to it: an observation bucket and a verifying peer
	// are different classes of evidence and summing them would overstate
	// both (goal.md §10.2).
	VerifyingPeers int64
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
	if row.Env == nil {
		row.Env = row.EnvAlias
	}
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
type stageSplit struct {
	obs, used, ver             int64
	obsText, usedText, verText string
}

func splitStageCounts(byStage map[string]stageCount) stageSplit {
	names := make([]string, 0, len(byStage))
	for k := range byStage {
		names = append(names, k)
	}
	sort.Strings(names)
	var obsParts, usedParts, verParts []string
	var out stageSplit
	for _, stage := range names {
		c := byStage[stage]
		txt := stage + " " + i18n.FormatInt("en", c.Pass) + "✓"
		if c.Fail > 0 {
			txt += " " + i18n.FormatInt("en", c.Fail) + "✕"
		}
		switch {
		case isUsageStageName(stage):
			// Presence, not an outcome: kept and shown, never added to the
			// runs whose rate the row reports.
			out.used += c.Pass + c.Fail
			usedParts = append(usedParts, txt)
		case strings.HasPrefix(stage, "PROJECT_"):
			out.obs += c.Pass + c.Fail
			obsParts = append(obsParts, txt)
		default:
			out.ver += c.Pass + c.Fail
			verParts = append(verParts, txt)
		}
	}
	out.obsText = strings.Join(obsParts, " · ")
	out.usedText = strings.Join(usedParts, " · ")
	out.verText = strings.Join(verParts, " · ")
	return out
}

func buildMatrix(lang string, doc snapshotDoc) []matrixRow {
	rows := make([]matrixRow, 0, len(doc.Rows))
	for _, r := range doc.Rows {
		sp := splitStageCounts(r.ByStage)
		obs, ver := sp.obs, sp.ver
		chip, class, glyph, noEvidence := chipFor(r, obs+sp.used, ver)
		ctx, detail := rowLabels(r)
		row := matrixRow{
			Context: ctx, Detail: detail,
			Chip: chip, ChipClass: class, Glyph: glyph, NoEvidence: noEvidence,
			Observations: obs, Usage: sp.used, Verifications: ver,
			ObservedStages: sp.obsText, UsageStages: sp.usedText,
			VerifiedStages: sp.verText,
			Peers:          r.UniquePeerBuckets,
			VerifyingPeers: r.VerificationCounts["distinctVerifyingPeers"],
			LastSeen:       datePart(r.LastSeen),
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

// simpleSymbolPathRe is deliberately conservative. Symbols matching it keep
// the original, readable /version/symbol URL. Everything else travels in a
// query parameter: fragments, query delimiters, brackets and slashes either
// change URL meaning or are decoded by net/http before routing, so path
// escaping alone cannot represent every public API name losslessly.
var simpleSymbolPathRe = regexp.MustCompile(`^[0-9A-Za-z._:@$+\-]+$`)

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
	} else if ecosystem == "maven" {
		// Maven identity is groupId/artifactId. Artifact IDs are allowed to
		// look like versions (for example a library literally named "2.0"),
		// so the first segment can never terminate the coordinate.
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
	// A trailing slash served the same page at a second URL, and the
	// canonical echoed whichever one was asked for, so both got indexed.
	if strings.HasSuffix(r.URL.Path, "/") {
		redirectToSlashless(w, r)
		return
	}
	name, version, symbol, ok := splitPackagePath(eco, r.PathValue("rest"))
	if !ok {
		s.notFound(w, r, lang)
		return
	}
	if querySymbols, present := r.URL.Query()["symbol"]; present {
		// Two different symbol coordinates in one URL are ambiguous. The
		// query form is for symbols that cannot safely occupy one path
		// segment, and is accepted only on a version route.
		if version == "" || symbol != "" || len(querySymbols) != 1 ||
			querySymbols[0] == "" || len(querySymbols[0]) > 512 {
			s.notFound(w, r, lang)
			return
		}
		symbol = querySymbols[0]
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

// crumb is one step of the path back up from a tracked object. Href
// empty marks the page the reader is on.
type crumb struct {
	Label string
	Href  string
}

// recordCrumbs builds the chain records → ecosystem → package → version →
// symbol, stopping at the first empty coordinate. Every step is a real
// page, so a reader who arrived from a search result or an agent link can
// climb to the level they actually wanted instead of starting over.
func recordCrumbs(b basePage, eco, name, version, symbol string) []crumb {
	out := []crumb{{Label: i18n.T(b.Lang, "nav.records"), Href: b.WithLang("/records")}}
	if eco == "" {
		return out
	}
	out = append(out, crumb{Label: eco,
		Href: b.WithLang(recordsHref(RecordFilter{Ecosystem: eco}, 1, i18n.Default))})
	if name == "" {
		return out
	}
	out = append(out, crumb{Label: name, Href: b.WithLang(pkgHref(eco, name))})
	if version == "" {
		return out
	}
	out = append(out, crumb{Label: version, Href: b.WithLang(versionHref(eco, name, version))})
	if symbol == "" {
		return out
	}
	return append(out, crumb{Label: symbol, Href: b.WithLang(symbolHref(eco, name, version, symbol))})
}

// leaf marks the last crumb as the current page.
func leaf(crumbs []crumb) []crumb {
	if n := len(crumbs); n > 0 {
		crumbs[n-1].Href = ""
	}
	return crumbs
}

func pkgHref(eco, name string) string {
	return "/" + eco + "/" + escapePathSegments(name)
}

func versionHref(eco, name, version string) string {
	return pkgHref(eco, name) + "/" + url.PathEscape(version)
}

// symbolHref keeps established URLs for simple symbols and uses a query for
// names such as OpenStruct#[], Set#include? and slash-delimited API families.
func symbolHref(eco, name, version, symbol string) string {
	base := versionHref(eco, name, version)
	if simpleSymbolPathRe.MatchString(symbol) {
		return base + "/" + symbol
	}
	return base + "?" + url.Values{"symbol": {symbol}}.Encode()
}

// ---------------------------------------------------------------------------
// Package page.

type packagePage struct {
	basePage
	Ecosystem string
	Name      string
	Versions  []versionRow
	Clusters  []clusterView
	// ClusterTotal is how many the package has, so the page can say what it
	// did not show rather than letting a bounded list read as complete.
	ClusterTotal int
	Wanted       []WantedRow
	Crumbs       []crumb
	// Cube is the N-dimensional compatibility explorer: the page's primary
	// element. nil when the package has no snapshot evidence yet.
	Cube *cubeView
	// Deps are the first-level dependencies of ONE pinned release. Empty
	// without ?f_version, deliberately: across releases the same library
	// appears at several versions and the page would have to choose one.
	Deps []PackageDep
}

// packageDeps lists the first-level dependencies of one PINNED release.
//
// Without ?f_version it returns nothing, and that is the whole design: across
// releases the same library appears at several versions, so an unpinned page
// would have to choose which to show — a choice nobody asked for and one the
// reader cannot check.
func (s *site) packageDeps(r *http.Request, eco, name, version string) []PackageDep {
	if version == "" {
		return nil
	}
	rows, err := s.d.Store.Dependencies(r.Context(), eco, name)
	if err != nil {
		return nil
	}
	kept := make([]DependencyEdge, 0, len(rows))
	for _, e := range rows {
		if e.ParentVersion == version {
			kept = append(kept, e)
		}
	}
	return buildPackageDeps(eco, kept)
}

// packageSampleLimit bounds how many of a package's samples one page
// reads. It is not a display cap: the package page shows counts per
// version and the version page shows that version's samples, so the read
// only has to cover a package's realistic sample count (uuid alone has
// 96). The sitemap is what guarantees every sample is reachable.
const packageSampleLimit = 200

// renderedClusterLimit bounds one page of failure clusters. They arrive
// ordered by how many machines reported them, so the head is the story and
// the tail is single-report noise; the page says how many it did not show.
const renderedClusterLimit = 12

// loadClusters returns a page of failure clusters and how many the package
// actually has, so the page can say what it did not show. pgx/v5 carries 133;
// rendering all of them was a wall, and truncating silently would read as
// "this is all of them".
// decidedVersion is the one release the page is standing on: the reader's
// pin, or the only version there has ever been. Empty means the page covers
// several releases and cannot speak for any single one of them.
func decidedVersion(r *http.Request, cube *cubeView) string {
	if v := r.URL.Query().Get("f_version"); v != "" {
		return v
	}
	if cube != nil {
		return cube.Coord["version"]
	}
	return ""
}

// hasAnyClusters answers the 404 question, which is about the PACKAGE and
// not about the coordinate: a package whose only evidence is failures still
// has a page, even standing on an undecided slice that shows none of them.
func (s *site) hasAnyClusters(r *http.Request, eco, name string) bool {
	_, total, err := s.d.Store.FailureClusters(r.Context(), eco, name)
	return err == nil && total > 0
}

func (s *site) loadClusters(r *http.Request, eco, name string, coord map[string]string) ([]clusterView, int) {
	raw, total, err := s.d.Store.FailureClusters(r.Context(), eco, name)
	if err != nil {
		return nil, 0
	}
	clusters := make([]failureCluster, 0, len(raw))
	for _, doc := range raw {
		var c failureCluster
		if json.Unmarshal([]byte(doc), &c) == nil {
			clusters = append(clusters, c)
		}
	}
	if len(coord) > 0 {
		clusters = filterClustersToPins(clusters, coord)
		// total counted the package; it no longer describes what is on
		// screen, and printing "showing 3 of 133" beside three clusters
		// from one coordinate would invite the reader to look for 130 more
		// that were never about this place.
		total = len(clusters)
	}
	// Bounded HERE, after the coordinate has done its narrowing. Bounding
	// first — which is what the store used to do — meant a cluster recorded
	// on exactly the environment the reader had drilled to was cut for
	// ranking low across the whole package, and the coordinate that caused
	// it showed nothing at all.
	if len(clusters) > renderedClusterLimit {
		clusters = clusters[:renderedClusterLimit]
	}
	return buildClusters(clusters), total
}

// versionRow is one row of the package's version list: what the network
// measured for that version, and how many published answers were written
// against it.
//
// These used to be two lists — the versions with evidence, then a second
// "samples by version" summary — which named most versions twice and
// made the reader diff them by eye.
type versionRow struct {
	Version string
	Href    string
	Latest  bool
	Samples int64
}

// versionRows merges the versions the network measured with the versions
// its published answers were written against, newest first.
//
// The union matters: a golang module is published as both "1.6.0" and
// "v1.6.0" and only one spelling carries snapshot evidence, so a list of
// measured versions alone would drop the samples filed under the other.
// Every version named here has a page — the version handler renders for
// published answers as well as for evidence.
func versionRows(b basePage, eco, name string, versions []string, samples []SampleListItem) []versionRow {
	counts := map[string]int64{}
	for _, item := range samples {
		if item.Version != "" {
			counts[item.Version]++
		}
	}
	seen := map[string]bool{}
	out := make([]versionRow, 0, len(versions)+len(counts))
	add := func(version string) {
		if version == "" || seen[version] {
			return
		}
		seen[version] = true
		out = append(out, versionRow{
			Version: version,
			Href:    b.WithLang(versionHref(eco, name, version)),
			Samples: counts[version],
		})
	}
	for _, v := range versions {
		add(v)
	}
	for v := range counts {
		add(v)
	}
	sort.Slice(out, func(i, j int) bool {
		if c := domain.CompareVersions(out[i].Version, out[j].Version); c != 0 {
			return c > 0
		}
		return out[i].Version > out[j].Version
	})
	// "Latest with evidence" is a claim about measurement, so it marks the
	// newest version the network actually measured — not a sample-only
	// spelling that happens to sort first.
	for i := range out {
		if len(versions) > 0 && out[i].Version == versions[0] {
			out[i].Latest = true
			break
		}
	}
	return out
}

func (s *site) packagePage(w http.ResponseWriter, r *http.Request, lang, eco, name string) {
	versions, err := s.d.Store.PackageVersions(r.Context(), eco, name)
	if err != nil {
		s.unavailable(w, r, lang)
		return
	}
	// Samples are listed here because this is the page a crawler already
	// reaches from the sitemap: without a link from somewhere indexed, a
	// sample page exists but is never visited.
	samples, err := s.d.Store.PackageSamples(r.Context(), eco, name, packageSampleLimit)
	if err != nil {
		samples = nil // the rest of the page is still worth serving
	}
	// A package requested through NO_SAFE_MATCH has a useful, honest page
	// even before its first sample exists. It says exactly that the request
	// is queued; it does not manufacture a version, matrix or evidence row.
	var wanted []WantedRow
	if rows, err := s.d.Store.WantedForPackage(r.Context(), eco, name); err == nil {
		wanted = rows
	}
	// The cube is the page. Everything under it belongs to ONE coordinate, so
	// it is built from what the cube decided rather than from the package: on
	// an undecided slice there is no release whose dependencies these are and
	// no environment whose failures these are, and the page showed both
	// anyway. That pile is what a reader had to read past to find the grid.
	cube := buildCubeView(s, r, lang, eco, name)
	var clusters []clusterView
	var clusterTotal int
	var deps []PackageDep
	// A dependency list belongs to the RELEASE and to nothing else: the same
	// lockfile resolves the same way whichever symbol or runtime the reader is
	// looking at. Deciding the version is the whole requirement — by pinning
	// it, or by the package only ever having had one.
	deps = s.packageDeps(r, eco, name, decidedVersion(r, cube))
	// A failure cluster belongs to the whole coordinate — this release, this
	// runtime, this OS — so it waits until nothing is left to choose.
	if cube != nil && cube.Decided {
		clusters, clusterTotal = s.loadClusters(r, eco, name, cube.Coord)
	}
	if len(versions) == 0 && len(samples) == 0 && len(wanted) == 0 && !s.hasAnyClusters(r, eco, name) {
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
		{eco, base + recordsHref(RecordFilter{Ecosystem: eco}, 1, i18n.Default)},
		{name, base + pkgHref(eco, name)},
	})}
	s.render(w, "package", http.StatusOK, packagePage{
		basePage: b, Ecosystem: eco, Name: name,
		Versions: versionRows(b, eco, name, versions, samples),
		Clusters: clusters, ClusterTotal: clusterTotal, Wanted: wanted,
		Crumbs: leaf(recordCrumbs(b, eco, name, "", "")),
		Cube:   cube,
		Deps:   deps,
	})
}

// ---------------------------------------------------------------------------
// Version page.

type versionPage struct {
	basePage
	Ecosystem string
	Name      string
	Ver       string
	Symbols   []symbolLink
	Matrix    []matrixRow
	Crumbs    []crumb
	// Samples are the published answers written against THIS version.
	Samples []SampleListItem
	// SymbolGrid answers "which symbol ran on which OS" at a glance; its
	// cells drill into the package cube with version, symbol and OS pinned.
	SymbolGrid pivotGrid
	// Clusters are the failures recorded against THIS release. The page is
	// where a search result lands and where the cube's exact records link,
	// and it used to name none of them.
	Clusters     []clusterView
	ClusterTotal int
}

type symbolLink struct {
	Name string
	Href string
	// Samples is how many published answers this API has. It replaces the
	// flat list the version page used to print: the count is what a reader
	// scanning the symbols actually needs, and the answers themselves are
	// one click away under the API they answer for.
	Samples int
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
	// Published answers are reason enough for a version to have a page. A
	// golang module is published as both "1.6.0" and "v1.6.0" and only one
	// spelling carries snapshot evidence, so requiring evidence here left
	// the samples filed under the other spelling with nowhere to be read.
	samples := s.versionSamples(r, eco, name, version)
	if len(symbols) == 0 && len(matrix) == 0 && len(samples) == 0 {
		s.notFound(w, r, lang)
		return
	}
	base := s.base(r)
	title := i18n.T(lang, "title.compatibility", name, version) + " — CodeSampleX"
	b := s.page(r, lang, title, i18n.T(lang, "meta.explorer", name+"@"+version))
	b.JSONLD = []template.JS{breadcrumbJSONLD([][2]string{
		{"CodeSampleX", base + "/"},
		{name, base + pkgHref(eco, name)},
		{version, base + versionHref(eco, name, version)},
	})}
	links, residue := symbolLinks(b, eco, name, version, symbols, samples)
	clusters, clusterTotal := s.loadClusters(r, eco, name, map[string]string{"version": version})
	s.render(w, "version", http.StatusOK, versionPage{
		basePage: b, Ecosystem: eco, Name: name, Ver: version,
		Symbols: links, Matrix: matrix,
		Crumbs:     leaf(recordCrumbs(b, eco, name, version, "")),
		Samples:    residue,
		SymbolGrid: s.versionSymbolGrid(r, lang, eco, name, version),
		// A cluster names its own versions, so this release's failures can be
		// picked out exactly and the rest left to the package page.
		Clusters:     clusters,
		ClusterTotal: clusterTotal,
	})
}

// symbolLinks builds the version page's symbol list and returns the samples
// no entry on it claims.
//
// The list is the union of two sources: what the store observed, and the
// APIs the published samples name. They overlap heavily but spell things
// differently, so the union is taken by member — pgx v5.10.0's observed list
// held 146 entries that were only 84 distinct APIs, the same API listed
// twice under two spellings, and 85 of its 128 samples matched none of them
// exactly. That is why every one of those 85 was still being printed in a
// flat list on the version page.
//
// The residue is what remains: a sample that names no API at all. It is
// usually one or two, and it is listed on the version page because there is
// nowhere else for it to go — dropping it would publish a sample into a
// page nobody can reach.
func symbolLinks(b basePage, eco, name, version string, observed []string, samples []SampleListItem) ([]symbolLink, []SampleListItem) {
	counts := map[string]int{}
	display := map[string]string{}
	for _, sym := range observed {
		if m := symbolMember(sym); m != "" {
			display[m] = sym
		}
	}
	var residue []SampleListItem
	for _, item := range samples {
		named := false
		for _, sym := range item.Symbols {
			m := symbolMember(sym)
			if m == "" {
				continue
			}
			named = true
			counts[m]++
			if _, ok := display[m]; !ok {
				// The sample's own spelling is the only one on record for
				// this API, so it names the entry — but by its member, so
				// the link reads "CollectRows" beside the observed ones
				// rather than a module path nobody scans.
				display[m] = m
			}
		}
		if !named {
			residue = append(residue, item)
		}
	}
	members := make([]string, 0, len(display))
	for m := range display {
		members = append(members, m)
	}
	sort.Strings(members)
	links := make([]symbolLink, 0, len(members))
	for _, m := range members {
		links = append(links, symbolLink{
			// The observed spelling when there is one: an ecosystem that
			// records "axios.post" must keep saying so, not shrink to
			// "post". Only an API known solely from samples is labeled by
			// its member.
			Name:    display[m],
			Href:    b.WithLang(symbolHref(eco, name, version, display[m])),
			Samples: counts[m],
		})
	}
	return links, residue
}

// versionSymbolGrid builds the "which symbol ran on which OS" grid of one
// version from the package cube; cells drill into the cube explorer with
// version, symbol and OS pinned. Empty when the version is outside the
// cube's newest-versions window or a 1×1 grid would only repeat the
// detail table.
func (s *site) versionSymbolGrid(r *http.Request, lang, eco, name, version string) pivotGrid {
	allFacts, _ := s.cubeFacts(r.Context(), eco, name)
	facts := filterCubeFacts(allFacts, map[string]string{"version": version})
	if len(facts) == 0 {
		return pivotGrid{}
	}
	// With symbol as a row axis, the package-level row would repeat the same
	// receipts the symbol rows already show (the producer files each
	// receipt under "" AND every claimed symbol). Where a symbol row
	// carries the verification, the package-level fact keeps only its own
	// disjoint observations.
	facts = suppressDuplicatePackageVerifications(facts)
	pin := func(extra map[string]string) string {
		q := map[string]string{"version": version}
		for k, v := range extra {
			q[k] = v
		}
		return cubeHref(pkgHref(eco, name), cubeQuery(q, "", "", lang))
	}
	g := buildCubeGrid(facts, "os", "symbol", pivotLinks{
		Cell: func(row, col string) string { return pin(map[string]string{"symbol": row, "os": col}) },
		Row:  func(row string) string { return pin(map[string]string{"symbol": row}) },
		Col:  func(col string) string { return pin(map[string]string{"os": col}) },
	}, time.Now(), false)
	if len(g.Rows) <= 1 && len(g.Cols) <= 1 {
		return pivotGrid{}
	}
	return g
}

// suppressDuplicatePackageVerifications strips the verification side from
// package-level facts whose (version, env bucket) is already verified by a
// symbol fact — those are the same receipts filed twice by the producer.
// Package-level facts with the only verification in their bucket keep it.
func suppressDuplicatePackageVerifications(facts []cubeFact) []cubeFact {
	type key struct{ version, envHash string }
	symbolVerified := map[key]bool{}
	for _, f := range facts {
		if !f.PackageLevel && f.Agg.verPass+f.Agg.verFail > 0 {
			symbolVerified[key{f.Dims["version"], f.EnvHash}] = true
		}
	}
	out := make([]cubeFact, 0, len(facts))
	for _, f := range facts {
		if f.PackageLevel && symbolVerified[key{f.Dims["version"], f.EnvHash}] {
			f.Agg.verPass, f.Agg.verFail = 0, 0
			f.Agg.cross = false
			f.Agg.verLastSeen = ""
			if f.Agg.events() == 0 {
				continue
			}
		}
		out = append(out, f)
	}
	return out
}

// symbolSamples lists the published samples that answer one exact symbol of
// one exact version. A sample names the APIs it was written against, so this
// is a filter over the version's list rather than a separate read.
func (s *site) symbolSamples(r *http.Request, eco, name, version, symbol string) []SampleListItem {
	var out []SampleListItem
	for _, item := range s.versionSamples(r, eco, name, version) {
		if sampleNamesSymbol(item.Symbols, symbol) {
			out = append(out, item)
		}
	}
	return out
}

// sampleNamesSymbol reports whether a sample answers for one symbol.
//
// It compares the member rather than the whole string because authors write
// the same API three ways and all three are correct: the full module path
// with the member ("github.com/jackc/pgx/v5.Batch"), the import alias with
// the member ("pgx.Batch"), and the member alone ("Batch"). An exact match
// claimed only the last, which is the rarest — pgx v5.10.0 carried 192 of
// the first spelling against 22 of the third — so the symbol pages showed
// almost nothing and the version page showed everything in one flat list.
//
// The comparison stays on the whole member. Prefix matching would hand a
// reader who followed a link to Batch the samples for BatchResults, and a
// sample filed under the wrong API is worse than one that is hard to find.
func sampleNamesSymbol(named []string, symbol string) bool {
	want := symbolMember(symbol)
	if want == "" {
		return false
	}
	for _, n := range named {
		if symbolMember(n) == want {
			return true
		}
	}
	return false
}

// symbolMember takes the last dot- or slash-separated segment of a symbol
// name. Both separators appear: Go and Python qualify with dots, and the Go
// module path in front of the dot carries slashes of its own.
func symbolMember(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexAny(s, "./"); i >= 0 {
		s = s[i+1:]
	}
	return s
}

// versionSamples lists the published samples written against one exact
// version, sorted so the APIs they answer for group together.
func (s *site) versionSamples(r *http.Request, eco, name, version string) []SampleListItem {
	all, err := s.d.Store.PackageSamples(r.Context(), eco, name, packageSampleLimit)
	if err != nil {
		return nil
	}
	var out []SampleListItem
	for _, item := range all {
		if item.Version == version {
			out = append(out, item)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := "", ""
		if len(out[i].Symbols) > 0 {
			a = out[i].Symbols[0]
		}
		if len(out[j].Symbols) > 0 {
			b = out[j].Symbols[0]
		}
		if a != b {
			return a < b
		}
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out
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
	Crumbs    []crumb
	// Pivot is the OS × runtime summary of the same snapshot the detail
	// table renders; its cells anchor down to that table.
	Pivot pivotGrid
	// Samples are the contract records written against this exact symbol.
	// Without them this page — the deepest node in the hierarchy — offered no
	// link but an in-page anchor, so a reader who followed the cube all the
	// way down had to climb back up to reach the evidence the descent was for.
	Samples []SampleListItem
}

func (s *site) symbolPage(w http.ResponseWriter, r *http.Request, lang, eco, name, version, symbol string) {
	purl := domain.PURL{Ecosystem: eco, Name: name, Version: version}.String()
	// Published answers are reason enough for a symbol to have a page, the
	// same rule the version page already applies one level up. Requiring a
	// snapshot meant the page existed only under the exact spelling the
	// snapshot was filed as: /v5.10.0/pgx.CollectRows answered while
	// /v5.10.0/CollectRows did not, though both name the same API and the
	// second is what the symbol list now links.
	samples := s.symbolSamples(r, eco, name, version, symbol)
	var doc snapshotDoc
	raw, ok := s.d.Store.SnapshotJSON(r.Context(), purl, symbol)
	switch {
	case ok:
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			s.unavailable(w, r, lang)
			return
		}
	case len(samples) == 0:
		s.notFound(w, r, lang)
		return
	}
	matrix := buildMatrix(lang, doc)
	clusters := buildClusters(doc.Failures)
	pivot := buildPivot(doc.Rows, osRowKey, contextColKey, func(row, col string) string {
		return "#env-detail"
	}, time.Now())
	// A 1×1 pivot repeats the single detail row below it; skip the summary.
	if len(pivot.Rows) == 1 && len(pivot.Cols) == 1 {
		pivot = pivotGrid{}
	}

	// Descriptive title leads with the strongest context row:
	// "axios.post axios 1.12.0 node 22 compatibility — CodeSampleX".
	titleParts := []string{symbol, name, version}
	if len(matrix) > 0 && matrix[0].Context != "unknown" {
		titleParts = append(titleParts, matrix[0].Context)
	}
	title := i18n.T(lang, "title.compatibility_one", strings.Join(titleParts, " ")) + " — CodeSampleX"

	base := s.base(r)
	b := s.page(r, lang, title, i18n.T(lang, "meta.explorer", symbol+" — "+name+"@"+version))
	verPath := versionHref(eco, name, version)
	verHref := base + verPath
	symbolPath := symbolHref(eco, name, version, symbol)
	if parsed, err := url.Parse(symbolPath); err == nil {
		// page() intentionally drops arbitrary query parameters from SEO
		// canonicals. Here ?symbol= is identity, not a filter, so retain it
		// in canonical, hreflang and language-switch links.
		b.path = parsed.Path
		b.query = parsed.Query()
		b.Canonical = base + b.WithLang(symbolPath)
		b.Alternates = queryAlternatesWithQuery(base, parsed.Path, parsed.Query())
	}
	b.JSONLD = []template.JS{breadcrumbJSONLD([][2]string{
		{"CodeSampleX", base + "/"},
		{name, base + pkgHref(eco, name)},
		{version, verHref},
		{symbol, base + symbolPath},
	})}
	s.render(w, "symbol", http.StatusOK, symbolPage{
		basePage: b, Ecosystem: eco, Name: name, Ver: version, Symbol: symbol,
		PURL: purl, Matrix: matrix, Clusters: clusters,
		Generated: datePart(doc.GeneratedAt),
		Crumbs:    leaf(recordCrumbs(b, eco, name, version, symbol)),
		Pivot:     pivot,
		Samples:   samples,
	})
}

// ---------------------------------------------------------------------------
// Records: everything the network has evidence for, searchable and paged.

// recordsPerPage keeps the page readable. A dependency tree runs to
// hundreds of packages, and a single unbounded list is unusable.
const recordsPerPage = 40

// maxRecordsPage is the deepest page ?page= may ask for. Past this is
// beyond any real record set, and the multiplication below must not
// overflow.
const maxRecordsPage = 1 << 20

type recordsPage struct {
	basePage
	Filter           RecordFilter
	Hits             []PackageHit
	Total            int
	EcosystemOptions []filterOption
	OSOptions        []filterOption
	RuntimeOptions   []filterOption
	BasisOptions     []filterOption
	HasFilters       bool
	ClearHref        string
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
	filter := cleanRecordFilter(RecordFilter{
		Query:     r.URL.Query().Get("q"),
		Ecosystem: r.URL.Query().Get("eco"),
		OS:        r.URL.Query().Get("os"),
		Runtime:   r.URL.Query().Get("runtime"),
		Basis:     r.URL.Query().Get("basis"),
	})
	// maxRecordsPage bounds ?page= before it is multiplied. Atoi happily
	// returns 9223372036854775807, (page-1)*recordsPerPage overflowed to a
	// negative offset, and the store sliced with it — so any browser could
	// panic the page with a URL. Deeper than this is past the end of any
	// real record set anyway, and renders as an empty page.
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 1 {
		page = min(p, maxRecordsPage)
	}

	hits, total, err := s.d.Store.RecordPackages(r.Context(), filter, (page-1)*recordsPerPage, recordsPerPage)
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
		http.Redirect(w, r, recordsHref(filter, pages, lang), http.StatusFound)
		return
	}

	from := (page-1)*recordsPerPage + 1
	to := (page-1)*recordsPerPage + len(hits)
	if total == 0 {
		from = 0
	}
	n := func(v int) string { return i18n.FormatInt(lang, int64(v)) }
	view := recordsPage{
		Filter: filter, Hits: hits, Total: total,
		EcosystemOptions: ecosystemOptions(filter.Ecosystem),
		OSOptions:        osOptions(filter.OS),
		RuntimeOptions:   runtimeOptions(filter.Runtime),
		BasisOptions:     basisOptions(lang, filter.Basis),
		HasFilters:       filter.Query != "" || filter.Ecosystem != "" || filter.OS != "" || filter.Runtime != "" || filter.Basis != "",
		ClearHref:        recordsHref(RecordFilter{}, 1, lang),
		Page:             page, Pages: pages,
		RangeText: i18n.T(lang, "records.range", n(from), n(to), n(total)),
		PageText:  i18n.T(lang, "records.page", n(page), n(pages)),
	}
	if page > 1 {
		view.PrevHref = recordsHref(filter, page-1, lang)
	}
	if page < pages {
		view.NextHref = recordsHref(filter, page+1, lang)
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
func recordsHref(filter RecordFilter, page int, lang string) string {
	v := url.Values{}
	if filter.Query != "" {
		v.Set("q", filter.Query)
	}
	if filter.Ecosystem != "" {
		v.Set("eco", filter.Ecosystem)
	}
	if filter.OS != "" {
		v.Set("os", filter.OS)
	}
	if filter.Runtime != "" {
		v.Set("runtime", filter.Runtime)
	}
	if filter.Basis != "" {
		v.Set("basis", filter.Basis)
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
	Context     string
	Environment environmentView
	Capability  string
	// Contract is the contract stage's own result, kept apart from the
	// rendered Stages string so the page can ask whether one actually
	// passed rather than parsing its own display text.
	Contract  string
	Stages    string
	Verifier  string
	CreatedAt string
	PeerID    string
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
	Meta                SampleMeta
	Manifest            *domain.SampleManifest
	Level               string
	Context             string
	Goal                string
	Packages            []pkgRef
	Receipts            []receiptView
	DeclaredEnvironment environmentView
	EvidenceBasisKey    string
	Crumbs              []crumb
}

// levelBadge maps a sample status to the honest verification level
// (goal.md §6.2).
//
// PUBLISHED was mapped straight to L3_CONTRACT_PASS — "the sample's
// intended behaviour was verified" — on the assumption that publication
// implies a local contract pass. Nothing enforces that: `csx sample
// publish` does not require `csx sample verify`, and a POST to /v1/samples
// needs no receipt at all, so a sample the network had never run carried a
// badge saying its contract had passed. That is the wrong claim in the
// direction that flatters, on the page a reader goes to in order to decide
// whether to trust it.
//
// contractPassed is whether any receipt actually records one. Without it a
// published sample is source that arrived, which is exactly L0.
func levelBadge(status string, contractPassed bool) string {
	switch status {
	case "LOCAL":
		return string(domain.L0SourceOnly)
	case "LOCAL_PASS", "PUBLISHED":
		if !contractPassed {
			return string(domain.L0SourceOnly)
		}
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
				Context:     rec.Environment.ContextLabel(),
				Environment: makeEnvironmentView(lang, rec.Environment),
				Capability:  string(rec.SandboxCapability),
				Contract:    rec.Stages["contract"],
				Stages:      strings.Join(parts, " · "),
				Verifier:    rec.VerifierAdapter,
				CreatedAt:   datePart(rec.CreatedAt),
				PeerID:      rec.PeerID,
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
	var declaredEnvironment environmentView
	if manifest != nil {
		goal = strings.TrimSpace(manifest.Case.Goal)
		env = manifest.Environment
		ctx = env.ContextLabel()
		purls = manifest.Packages
		syms = manifest.Symbols
		refs = packageRefs(purls)
		declaredEnvironment = makeEnvironmentView(lang, env)
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

	level := levelBadge(meta.Status, anyContractPass(receipts))
	basisKey := "sample.basis_source"
	switch level {
	case string(domain.L3ContractPass):
		basisKey = "sample.basis_contract"
	case string(domain.L4CrossPass):
		basisKey = "sample.basis_cross"
	case string(domain.L5MatrixPass):
		basisKey = "sample.basis_matrix"
	}
	// The sample is a tracked object like any other: give it the same path
	// back up through the package it is about.
	sampleCrumbs := []crumb{{Label: i18n.T(lang, "nav.records"), Href: b.WithLang("/records")}}
	if len(refs) > 0 {
		if parsed, err := domain.ParsePURL(refs[0].PURL); err == nil && knownEcosystems[parsed.Ecosystem] {
			sampleCrumbs = recordCrumbs(b, parsed.Ecosystem, parsed.Name, "", "")
		}
	}
	sampleCrumbs = append(sampleCrumbs, crumb{Label: i18n.T(lang, "sample.title") + " " + shortHash(meta.SampleID)})

	s.render(w, "sample", http.StatusOK, samplePageData{
		basePage: b, Meta: meta, Manifest: manifest,
		Level: level, Context: ctx, Goal: goal,
		Packages: refs, Receipts: receipts,
		DeclaredEnvironment: declaredEnvironment, EvidenceBasisKey: basisKey,
		Crumbs: sampleCrumbs,
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

// anyContractPass reports whether any receipt on this sample records a
// contract that actually passed.
func anyContractPass(receipts []receiptView) bool {
	for _, r := range receipts {
		if strings.EqualFold(r.Contract, "PASS") {
			return true
		}
	}
	return false
}
