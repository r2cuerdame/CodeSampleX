package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// noSafeMatchThreshold: below this best score the honest answer is
// NO_SAFE_MATCH (goal.md §3.8 — a miss beats a wrong hit).
const noSafeMatchThreshold = 0.25

const maxSearchCandidates = 500

// handleSearch implements POST /v1/search: the simplified server-side C7
// pipeline — package/symbol exact filter → snapshot environment fit →
// verification strength from receipts → grade + delta with the
// execution-context rules of docs/execution-context.md §5.
func (a *api) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req domain.SearchRequest
	if !readJSON(w, r, 1<<20, &req) {
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 3
	}
	reqEnv := req.Environment.Normalize()

	samples, err := a.d.Store.ListSamples(r.Context(), maxSearchCandidates)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sample listing failed")
		return
	}

	reqPURLs := make([]domain.PURL, 0, len(req.Packages))
	for _, ps := range req.Packages {
		if p, perr := domain.ParsePURL(ps); perr == nil {
			reqPURLs = append(reqPURLs, p)
		}
	}

	now := a.now()
	var results []domain.SearchResult
	for _, row := range samples {
		res, ok := a.scoreSample(r, row, req, reqEnv, reqPURLs, now)
		if ok {
			results = append(results, res)
		}
	}

	sort.SliceStable(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if len(results) == 0 || results[0].Score < noSafeMatchThreshold {
		writeJSON(w, http.StatusOK, domain.SearchResponse{
			SchemaVersion: 1, Results: []domain.SearchResult{}, Miss: true,
		})
		return
	}
	if len(results) > limit {
		results = results[:limit]
	}
	writeJSON(w, http.StatusOK, domain.SearchResponse{
		SchemaVersion: 1, Results: results, Miss: false,
	})
}

// scoreSample evaluates one candidate sample against the request; ok=false
// means the exact filters excluded it.
func (a *api) scoreSample(r *http.Request, row serverstore.SampleRow,
	req domain.SearchRequest, reqEnv domain.EnvironmentFingerprint,
	reqPURLs []domain.PURL, now time.Time) (domain.SearchResult, bool) {

	var manifest domain.SampleManifest
	if json.Unmarshal([]byte(row.ManifestJSON), &manifest) != nil {
		return domain.SearchResult{}, false
	}
	var samplePURLs []domain.PURL
	for _, ps := range manifest.Packages {
		if p, err := domain.ParsePURL(ps); err == nil {
			samplePURLs = append(samplePURLs, p)
		}
	}

	// 1. Exact package filter.
	var matched domain.PURL
	reqVersion := ""
	if len(reqPURLs) > 0 {
		found := false
		for _, rp := range reqPURLs {
			for _, sp := range samplePURLs {
				if sp.Ecosystem == rp.Ecosystem && sp.Name == rp.Name {
					matched, reqVersion, found = sp, rp.Version, true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return domain.SearchResult{}, false
		}
	} else if len(samplePURLs) > 0 {
		matched = samplePURLs[0]
	}

	// 2. Exact symbol filter.
	matchedSymbol := ""
	if len(req.Symbols) > 0 {
		for _, rs := range req.Symbols {
			for _, ss := range manifest.Symbols {
				if ss == rs {
					matchedSymbol = ss
					break
				}
			}
			if matchedSymbol != "" {
				break
			}
		}
		if matchedSymbol == "" {
			return domain.SearchResult{}, false
		}
	}

	// Base relevance.
	score := 0.0
	if len(reqPURLs) > 0 {
		score += 0.35
	}
	if matchedSymbol != "" {
		score += 0.2
	}
	overlap := tokenOverlap(req.Query+" "+req.ErrorCode, searchText(manifest))
	score += 0.35 * overlap
	if score == 0 {
		return domain.SearchResult{}, false
	}
	if len(reqPURLs) == 0 && len(req.Symbols) == 0 && overlap < 0.1 {
		return domain.SearchResult{}, false
	}

	// Environment fit + delta (execution context is ALWAYS sensitive).
	sampleEnv := manifest.Environment.Normalize()
	delta := envDelta(reqEnv, sampleEnv, matched, reqVersion)
	score *= delta.fit

	// Verification strength from receipts.
	receiptRows, err := a.d.Store.ReceiptsForSample(r.Context(), row.SampleID)
	if err != nil {
		return domain.SearchResult{}, false
	}
	var receipts []compatibility.ReceiptInfo
	for _, rr := range receiptRows {
		if info, ok := compatibility.ParseReceiptRow(rr); ok {
			receipts = append(receipts, info)
		}
	}
	contractPasses, passPeers, lastReceipt := receiptStrength(receipts)
	switch {
	case passPeers >= 2 || verifiedStatus(row.Status): // L4+: cross-verified
		score *= 3
	case contractPasses >= 1: // L3: contract passed at origin
		score *= 2
	}

	// Recency decay.
	last := row.CreatedAt
	if lastReceipt.After(last) {
		last = lastReceipt
	}
	if !last.IsZero() {
		score *= compatibility.RecencyDecay(now.Sub(last))
	}

	grade := delta.grade

	// Snapshot lookup: evidence summary + elevated-failure demotion in the
	// requester's execution context.
	summary, elevatedInContext := a.snapshotEvidence(r, matched, matchedSymbol, reqEnv)
	summary.ContractPasses = int64(contractPasses)
	summary.IndependentCrossPeers = int64(passPeers)
	if elevatedInContext {
		grade = domain.GradeReferenceOnly
	}

	// Known-failure clusters matching the requester environment cap the
	// grade at REFERENCE_ONLY and ride along as warnings.
	knownFailures := a.matchingClusters(r, matched, reqEnv)
	if len(knownFailures) > 0 {
		grade = worseGrade(grade, domain.GradeReferenceOnly)
	}

	result := domain.SearchResult{
		Grade:         grade,
		Confidence:    summary.Confidence,
		Score:         score,
		SampleID:      row.SampleID,
		SampleStatus:  row.Status,
		Exact:         delta.exact,
		Different:     delta.different,
		Adaptation:    delta.adaptation,
		Evidence:      summary,
		KnownFailures: knownFailures,
	}
	c := manifest.Case
	result.Case = &c
	if result.Confidence == "" {
		result.Confidence = "LOW"
	}
	return result, true
}

// receiptStrength returns contract-PASS count, distinct passing peers, and
// the latest receipt time.
func receiptStrength(receipts []compatibility.ReceiptInfo) (int, int, time.Time) {
	passes := 0
	peers := map[string]bool{}
	var last time.Time
	for _, rec := range receipts {
		if rec.CreatedAt.After(last) {
			last = rec.CreatedAt
		}
		if rec.ContractResult == string(domain.ResultPass) {
			passes++
			peers[rec.PeerID] = true
		}
	}
	return passes, len(peers), last
}

func verifiedStatus(status string) bool {
	switch status {
	case "CROSS_PASS", "MATRIX_PASS", "STABLE":
		return true
	}
	return false
}

// snapshotEvidence reads the materialized snapshot for (purl, symbol) and
// summarizes it, reporting whether the requester's execution context shows
// ELEVATED_FAILURE.
func (a *api) snapshotEvidence(r *http.Request, p domain.PURL, symbol string,
	reqEnv domain.EnvironmentFingerprint) (domain.EvidenceSummary, bool) {

	summary := domain.EvidenceSummary{Confidence: "LOW"}
	if p.Name == "" {
		return summary, false
	}
	js, ok, err := a.d.Store.GetSnapshot(r.Context(), p.String(), symbol)
	if (err != nil || !ok) && symbol != "" {
		js, ok, err = a.d.Store.GetSnapshot(r.Context(), p.String(), "")
	}
	if err != nil || !ok {
		return summary, false
	}
	var snap compatibility.Snapshot
	if json.Unmarshal([]byte(js), &snap) != nil {
		return summary, false
	}

	reqLabel := reqEnv.Bucketed().ContextLabel()
	elevated := false
	bestConfidence := ""
	var lastSeen string
	for _, row := range snap.Rows {
		cc := row.ByStage["PROJECT_COMPILE"]
		summary.ProjectCompileObservations += cc.Pass + cc.Fail
		summary.CleanBuilds += cc.Pass
		if int64(row.UniquePeerBuckets) > summary.UniquePeerBuckets {
			summary.UniquePeerBuckets = int64(row.UniquePeerBuckets)
		}
		if row.LastSeen > lastSeen {
			lastSeen = row.LastSeen
		}
		if row.ContextLabel == reqLabel {
			// The requester's context row decides confidence and pass rate.
			summary.Confidence = row.Confidence
			summary.PassRate = row.PassRate
			bestConfidence = row.Confidence
			if row.ElevatedFailure {
				elevated = true
				summary.ElevatedFailures = append(summary.ElevatedFailures, row.ContextLabel)
			}
		}
	}
	if bestConfidence == "" && len(snap.Rows) > 0 {
		// No row in the requester's context: fall back to the best row but
		// never claim more than MEDIUM for foreign-context evidence.
		best := snap.Rows[0]
		for _, row := range snap.Rows[1:] {
			if confidenceRank(row.Confidence) > confidenceRank(best.Confidence) {
				best = row
			}
		}
		summary.PassRate = best.PassRate
		summary.Confidence = best.Confidence
		if summary.Confidence == "HIGH" {
			summary.Confidence = "MEDIUM"
		}
	}
	summary.LastSeen = lastSeen
	return summary, elevated
}

func confidenceRank(c string) int {
	switch c {
	case "HIGH":
		return 3
	case "MEDIUM":
		return 2
	case "LOW":
		return 1
	}
	return 0
}

// matchingClusters returns known-failure clusters whose environment summary
// matches the requester environment.
func (a *api) matchingClusters(r *http.Request, p domain.PURL,
	reqEnv domain.EnvironmentFingerprint) []domain.KnownFailure {

	if p.Name == "" {
		return nil
	}
	clusters, err := a.d.Store.ListFailureClusters(r.Context(), p.Name)
	if err != nil {
		return nil
	}
	reqDims := envDims(reqEnv)
	var out []domain.KnownFailure
	for _, c := range clusters {
		if c.EnvSummaryJSON == "" {
			continue
		}
		var summary map[string]string
		if json.Unmarshal([]byte(c.EnvSummaryJSON), &summary) != nil || len(summary) == 0 {
			continue
		}
		match := true
		for k, v := range summary {
			if rv, ok := reqDims[k]; !ok || rv != v {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		kf := domain.KnownFailure{
			ErrorCode:   c.ErrorCode,
			Fingerprint: c.ErrorFingerprint,
			Count:       c.ObservationCount,
			EnvSummary:  summary,
		}
		_ = json.Unmarshal([]byte(c.HypothesesJSON), &kf.Hypotheses)
		out = append(out, kf)
	}
	return out
}

// envDims renders the request environment in envSummary vocabulary.
func envDims(e domain.EnvironmentFingerprint) map[string]string {
	e = e.Normalize().Bucketed()
	m := map[string]string{}
	if e.OS != "" {
		m["os"] = e.OS
	}
	if e.Runtime != "" {
		v := e.Runtime
		if e.RuntimeVersion != "" {
			v += "@" + e.RuntimeVersion
		}
		m["runtime"] = v
	}
	if e.ModuleSystem != "" {
		m["moduleSystem"] = e.ModuleSystem
	}
	if e.ExecutionContext != "" {
		m["executionContext"] = e.ExecutionContext
	}
	if e.BrowserFamily != "" {
		m["browserFamily"] = e.BrowserFamily
	}
	if e.Engine != "" {
		m["engine"] = e.Engine
	}
	return m
}

// deltaResult is the environment comparison outcome.
type deltaResult struct {
	fit        float64
	grade      domain.MatchGrade
	exact      []string
	different  []string
	adaptation []string
}

// envDelta compares request and sample environments under the C7 +
// execution-context rules: executionContext (and browserFamily/engine when
// browser-like) is ALWAYS sensitive; a context mismatch caps the grade at
// ADAPTATION_REQUIRED with an explicit "verify in <ctx>" adaptation entry.
func envDelta(req, sample domain.EnvironmentFingerprint, matched domain.PURL, reqVersion string) deltaResult {
	d := deltaResult{fit: 1.0, grade: domain.GradeExact,
		exact: []string{}, different: []string{}, adaptation: []string{}}

	// Package version distance.
	if reqVersion != "" && matched.Name != "" && matched.Version != reqVersion {
		reqP := domain.PURL{Ecosystem: matched.Ecosystem, Name: matched.Name, Version: reqVersion}
		switch {
		case reqP.Major() != matched.Major():
			d.grade = worseGrade(d.grade, domain.GradeReferenceOnly)
			d.different = append(d.different, "package major version ("+reqVersion+" vs "+matched.Version+")")
			d.fit *= 0.5
		case reqP.MajorMinor() != matched.MajorMinor():
			d.grade = worseGrade(d.grade, domain.GradeAdaptationRequired)
			d.different = append(d.different, "package minor version ("+reqVersion+" vs "+matched.Version+")")
			d.adaptation = append(d.adaptation, "verify against "+matched.Name+"@"+reqVersion+" (sample uses "+matched.Version+")")
			d.fit *= 0.9
		default:
			d.exact = append(d.exact, "package "+matched.Name+" "+matched.MajorMinor())
		}
	} else if matched.Name != "" {
		d.exact = append(d.exact, "package "+matched.Name)
	}

	cmp := func(label, rv, sv string) bool { // returns true when comparable and equal
		if rv == "" || sv == "" {
			return false
		}
		if rv == sv {
			d.exact = append(d.exact, label+" "+rv)
			return true
		}
		d.different = append(d.different, label+" "+rv+" (sample: "+sv+")")
		return false
	}

	// Execution context: always sensitive.
	reqCtx, sampleCtx := req.ExecutionContext, sample.ExecutionContext
	ctxMismatch := reqCtx != "" && sampleCtx != "" && reqCtx != sampleCtx
	if req.BrowserFamily != "" && sample.BrowserFamily != "" && req.BrowserFamily != sample.BrowserFamily {
		ctxMismatch = true
	}
	if req.Engine != "" && sample.Engine != "" && req.Engine != sample.Engine {
		ctxMismatch = true // engine mismatch scores like a context mismatch
	}
	if ctxMismatch {
		label := req.Bucketed().ContextLabel()
		if label == "" {
			label = reqCtx
		}
		d.grade = worseGrade(d.grade, domain.GradeAdaptationRequired)
		d.different = append(d.different, "executionContext "+sample.ContextLabel()+" (requested: "+label+")")
		d.adaptation = append(d.adaptation, "verify in "+label)
		d.fit *= 0.5
	} else if reqCtx != "" && reqCtx == sampleCtx {
		d.exact = append(d.exact, "executionContext "+reqCtx)
		// browserMajor distance scores like a minor-version distance.
		if req.BrowserFamily != "" && req.BrowserFamily == sample.BrowserFamily &&
			req.BrowserMajor != "" && sample.BrowserMajor != "" && req.BrowserMajor != sample.BrowserMajor {
			d.different = append(d.different, "browserMajor "+req.BrowserMajor+" (sample: "+sample.BrowserMajor+")")
			d.fit *= 0.9
		}
	}

	// Module system: sensitive for npm.
	if req.ModuleSystem != "" && sample.ModuleSystem != "" && req.ModuleSystem != sample.ModuleSystem {
		d.grade = worseGrade(d.grade, domain.GradeAdaptationRequired)
		d.different = append(d.different, "moduleSystem "+req.ModuleSystem+" (sample: "+sample.ModuleSystem+")")
		d.adaptation = append(d.adaptation, "convert import syntax to "+req.ModuleSystem)
		d.fit *= 0.75
	} else {
		cmp("moduleSystem", req.ModuleSystem, sample.ModuleSystem)
	}

	// Runtime: family change is a context change (handled above); version
	// major difference is an enumerable adaptation.
	if req.Runtime != "" && sample.Runtime != "" && req.Runtime == sample.Runtime {
		rv, sv := majorSeg(req.RuntimeVersion), majorSeg(sample.RuntimeVersion)
		switch {
		case rv != "" && sv != "" && rv != sv:
			d.grade = worseGrade(d.grade, domain.GradeAdaptationRequired)
			d.different = append(d.different, "runtime "+req.Runtime+" "+req.RuntimeVersion+" (sample: "+sample.RuntimeVersion+")")
			d.adaptation = append(d.adaptation, "verify on "+req.Runtime+" "+req.RuntimeVersion)
			d.fit *= 0.85
		case req.RuntimeVersion != "" && sample.RuntimeVersion != "" && req.RuntimeVersion != sample.RuntimeVersion:
			d.exact = append(d.exact, "runtime "+req.Runtime+" (major "+rv+")")
			d.fit *= 0.95
		default:
			d.exact = append(d.exact, "runtime "+req.Runtime)
		}
	}

	// Compiler/toolchain: sensitive for cargo/golang.
	if req.Compiler != "" && sample.Compiler != "" && req.Compiler != sample.Compiler {
		d.grade = worseGrade(d.grade, domain.GradeAdaptationRequired)
		d.different = append(d.different, "compiler "+req.Compiler+" (sample: "+sample.Compiler+")")
		d.adaptation = append(d.adaptation, "verify with "+req.Compiler)
		d.fit *= 0.8
	}

	// Non-sensitive dims: mild penalties, honest delta lines.
	if req.OS != "" && sample.OS != "" && req.OS != sample.OS {
		d.different = append(d.different, "os "+req.OS+" (sample: "+sample.OS+")")
		d.fit *= 0.9
	} else {
		cmp("os", req.OS, sample.OS)
	}
	if req.PackageManager != "" && sample.PackageManager != "" && req.PackageManager != sample.PackageManager {
		d.different = append(d.different, "packageManager "+req.PackageManager+" (sample: "+sample.PackageManager+")")
		d.adaptation = append(d.adaptation, "use "+req.PackageManager+" equivalents of lockfile commands")
		d.grade = worseGrade(d.grade, domain.GradeAdaptationRequired)
		d.fit *= 0.95
	} else {
		cmp("packageManager", req.PackageManager, sample.PackageManager)
	}

	return d
}

func majorSeg(v string) string {
	if i := strings.IndexByte(v, '.'); i >= 0 {
		return v[:i]
	}
	return v
}

var gradeRank = map[domain.MatchGrade]int{
	domain.GradeExact:              4,
	domain.GradeCompatible:         3,
	domain.GradeAdaptationRequired: 2,
	domain.GradeReferenceOnly:      1,
	domain.GradeNoSafeMatch:        0,
}

func worseGrade(a, b domain.MatchGrade) domain.MatchGrade {
	if gradeRank[b] < gradeRank[a] {
		return b
	}
	return a
}

// searchText renders the sample's searchable surface (goal, packages,
// symbols, contract lines).
func searchText(m domain.SampleManifest) string {
	var b strings.Builder
	b.WriteString(m.Case.Goal)
	b.WriteByte(' ')
	b.WriteString(m.Case.Kind)
	for _, p := range m.Packages {
		b.WriteByte(' ')
		b.WriteString(p)
	}
	for _, s := range m.Symbols {
		b.WriteByte(' ')
		b.WriteString(s)
	}
	for _, c := range m.Case.Contract {
		b.WriteByte(' ')
		b.WriteString(c)
	}
	return b.String()
}

// tokenOverlap is the v1 intent-similarity stand-in: fraction of query
// tokens found in the candidate text (no embeddings, §11.3 step 5).
func tokenOverlap(query, text string) float64 {
	q := tokens(query)
	if len(q) == 0 {
		return 0
	}
	t := map[string]bool{}
	for _, tok := range tokens(text) {
		t[tok] = true
	}
	hit := 0
	for _, tok := range q {
		if t[tok] {
			hit++
		}
	}
	return float64(hit) / float64(len(q))
}

func tokens(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	var out []string
	for _, f := range fields {
		if len(f) >= 2 {
			out = append(out, f)
		}
	}
	return out
}
