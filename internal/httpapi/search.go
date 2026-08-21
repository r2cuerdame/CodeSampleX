package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	searchrelevance "github.com/r2cuerdame/codesamplex/internal/search/relevance"
	"github.com/r2cuerdame/codesamplex/internal/serverstore"
)

// noSafeMatchThreshold: below this best score the honest answer is
// NO_SAFE_MATCH (goal.md §3.8 — a miss beats a wrong hit).
const noSafeMatchThreshold = 0.25

const maxSearchCandidates = 500

// maxTreePatterns bounds how many lockfile packages widen a search. A
// dependency tree runs to hundreds of entries; letting all of them into the
// candidate query turns one search into a scan.
const maxTreePatterns = 40

// handleSearch implements POST /v1/search: the simplified server-side C7
// pipeline — package/symbol exact filter → snapshot environment fit →
// verification strength from receipts → grade + delta with the
// execution-context rules of docs/execution-context.md §5.
func (a *api) handleSearch(w http.ResponseWriter, r *http.Request) {
	a.handleSearchVersion(w, r, 1)
}

func (a *api) handleSearchV2(w http.ResponseWriter, r *http.Request) {
	a.handleSearchVersion(w, r, 2)
}

func (a *api) handleSearchVersion(w http.ResponseWriter, r *http.Request, responseVersion int) {
	var req domain.SearchRequest
	if !readJSON(w, r, 1<<20, &req) {
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 3
	}
	reqEnv := req.Environment.Normalize()

	reqPURLs := make([]domain.PURL, 0, len(req.Packages))
	patterns := make([]string, 0, len(req.Packages))
	for _, ps := range req.Packages {
		if p, perr := domain.ParsePURL(ps); perr == nil {
			reqPURLs = append(reqPURLs, p)
			patterns = append(patterns, p.AnyVersionPattern())
		}
	}

	// Candidates come from the packages asked about, not from a global
	// newest-N window. Scoring the newest 500 samples made findability a
	// function of publication order — past 500 samples the oldest silently
	// stop being reachable however good they are, and anyone able to
	// publish 500 rows displaces everything else. Only a query with no
	// package falls back to the recency window.
	var samples []serverstore.SampleRow
	var err error
	if len(patterns) > 0 {
		samples, err = a.d.Store.SamplesForPackages(r.Context(), patterns, maxSearchCandidates)
	} else {
		samples, err = a.d.Store.ListSamples(r.Context(), maxSearchCandidates)
		// The caller's dependency tree WIDENS the candidate set; it never
		// narrows it. A question with no named package would otherwise see
		// only the newest maxSearchCandidates rows, so a sample about a
		// library the caller already uses could fall off the end purely by
		// age — and that is the sample most likely to be wanted.
		//
		// Restricting to the tree instead would be the old defect in a new
		// place: the library an agent asks about is usually one it is
		// about to add, so it is precisely NOT in the lockfile.
		if err == nil {
			samples = appendUnseenSamples(r, a, samples, req.ProjectPackages)
		}
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "sample listing failed")
		return
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
	// One answer per coordinate. A duplicate is not a second opinion -- it is
	// the same coordinate measured twice by the same fleet -- and returning
	// both spends the caller's budget while reading as corroboration.
	results = domain.DedupeResultsByCoordinate(results)
	if len(results) == 0 || results[0].Score < noSafeMatchThreshold {
		resp := domain.SearchResponse{
			SchemaVersion: responseVersion, Results: []domain.SearchResult{}, Miss: true,
		}
		// The grade is honest and stays; the empty hand does not. Relayed
		// observations never change Miss, never produce a grade, and never
		// enter the outcome record — this is still NO_SAFE_MATCH.
		a.relayOnMiss(r, responseVersion, &resp, reqPURLs, req.Symbols)
		a.recordSearchOutcome(r, now, resp)
		writeSearchResponse(w, responseVersion, resp)
		return
	}
	if len(results) > limit {
		results = results[:limit]
	}
	resp := domain.SearchResponse{
		SchemaVersion: responseVersion, Results: results, Miss: false,
	}
	a.recordSearchOutcome(r, now, resp)
	writeSearchResponse(w, responseVersion, resp)
}

// recordSearchOutcome writes only the UTC day and one aggregate result class.
// It runs after search has produced a successful response and never receives
// the request, query, package list, symbols, path, client, or an identity.
// Operational metrics must not make a valid search unavailable, so stores that
// lack the optional capability and transient write failures both fail open.
func (a *api) recordSearchOutcome(r *http.Request, at time.Time, resp domain.SearchResponse) {
	recorder, ok := a.d.Store.(serverstore.SearchOutcomeRecorder)
	if !ok {
		return
	}
	outcome := serverstore.SearchOutcomeSampleHit
	if resp.Miss {
		outcome = serverstore.SearchOutcomeNoMatch
	}
	_ = recorder.RecordSearchOutcome(r.Context(), at, outcome)
}

// writeSearchResponse keeps /v1 byte-shape compatible with its original
// additionalProperties:false schema. V2 is the negotiated surface for new
// result metadata such as exactFailureMatched.
func writeSearchResponse(w http.ResponseWriter, version int, resp domain.SearchResponse) {
	if version >= 2 {
		resp.SchemaVersion = 2
		writeJSON(w, http.StatusOK, resp)
		return
	}
	raw, err := json.Marshal(resp)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "search response encoding failed")
		return
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		writeErr(w, http.StatusInternalServerError, "search response encoding failed")
		return
	}
	legacy["schemaVersion"] = float64(1)
	if results, ok := legacy["results"].([]any); ok {
		for _, item := range results {
			if result, ok := item.(map[string]any); ok {
				delete(result, "exactFailureMatched")
			}
		}
	}
	writeJSON(w, http.StatusOK, legacy)
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
	//
	// Every shared package is compared and the WIDEST gap is the one graded,
	// not the first one iteration happens to reach. Breaking on the first
	// name match made the answer depend on array order: ask about
	// [axios@1.12, express@5] against a sample on [express@4, axios@1.12] and
	// the exact axios hit was reported while the express major gap went
	// unmentioned, but swap the request array and the gap appeared. The
	// honest summary of fit is the worst mismatch, not the friendliest.
	var matched domain.PURL
	reqVersion := ""
	if len(reqPURLs) > 0 {
		found := false
		worst := -1
		for _, rp := range reqPURLs {
			for _, sp := range samplePURLs {
				if sp.Ecosystem != rp.Ecosystem || sp.Name != rp.Name {
					continue
				}
				if d := versionDistance(rp, sp); d > worst {
					matched, reqVersion, worst, found = sp, rp.Version, d, true
				}
			}
		}
		if !found {
			return domain.SearchResult{}, false
		}
	} else if len(samplePURLs) > 0 {
		matched = samplePURLs[0]
	}

	// 2. Exact symbol filter.
	declaredSymbols := manifestDeclaredSymbols(manifest)
	matchedDeclared := searchrelevance.MatchedDeclaredSymbols(req.Symbols, declaredSymbols)
	matchedContext := searchrelevance.MatchedDeclaredSymbols(req.ContextSymbols, declaredSymbols)
	matchedSymbol := ""
	if len(req.Symbols) > 0 {
		if len(matchedDeclared) == 0 {
			return domain.SearchResult{}, false
		}
		matchedSymbol = matchedDeclared[0]
	}

	// Base relevance.
	score := 0.0
	if len(reqPURLs) > 0 {
		score += 0.35
	}
	if matchedSymbol != "" || len(matchedContext) > 0 {
		score += 0.2
	}
	overlap := tokenOverlap(req.Query+" "+req.ErrorCode, searchText(manifest))
	score += 0.35 * overlap
	if score == 0 {
		return domain.SearchResult{}, false
	}
	if len(reqPURLs) == 0 && len(req.Symbols) == 0 && len(matchedContext) == 0 && overlap < 0.1 {
		return domain.SearchResult{}, false
	}

	// A package in the caller's dependency tree says the sample is about
	// the right library, never that it answers the question. Naming any
	// package scored 0.35 on its own — past noSafeMatchThreshold before the
	// ×3 verification multiplier — so "how to bake a chocolate cake" with
	// google/uuid in go.mod came back as MATCH: EXACT at 0.84. The
	// relevance guard above only ran when NO package was given, which is
	// the case that needed it least. Sharing no content word with the
	// sample is a miss (goal.md §3.8), unless the caller's own error
	// fingerprint or code matched — that is direct evidence of relevance
	// whatever the prose says.
	text := searchText(manifest)
	// Failure clusters are the server's recorded fingerprint authority. A
	// fingerprint merely appearing in prose is not enough to call the
	// caller's failure an exact match; conversely, a recorded cluster match
	// is direct relevance even when the goal uses different words.
	failureCandidates := eligibleFailurePackages(reqPURLs, samplePURLs)
	knownFailures, fingerprintPackages := a.matchingClusters(r, failureCandidates, reqEnv, req, manifestDeclaredSymbols(manifest))
	candidateFailureMatched := len(fingerprintPackages) > 0
	codeMatched := req.ErrorCode != "" &&
		strings.Contains(strings.ToLower(text), strings.ToLower(req.ErrorCode))
	// The comparison text is what the sample is ABOUT — goal, symbols and
	// package names — not the goal sentence alone. A question can name the
	// package where the goal sentence names the API, and both are the same
	// subject; requiring the sentence to match rejected correct samples.
	// The fingerprint exemption has to be EARNED. This read
	// `req.ErrorFingerprint == ""`, which only asked whether the caller sent
	// one — so attaching any string at all switched the topic guard off and
	// an off-topic question got an answer. A fingerprint is evidence of
	// relevance when it MATCHES the sample, exactly like the error code
	// beside it.
	fingerprintMatched := candidateFailureMatched || requestFingerprintInText(req, text)
	packageNames := samplePackageNames(samplePURLs)
	topicSupported := searchrelevance.AboutSameThing(req.Query, manifest.Case.Goal, packageNames, declaredSymbols)
	if req.Query != "" && !fingerprintMatched && !codeMatched && matchedSymbol == "" && !topicSupported {
		return domain.SearchResult{}, false
	}
	// With no requested package the server is scanning its newest global
	// window. An explicitly declared ecosystem gates that broad fallback;
	// only an exact declared symbol can intentionally cross ecosystems.
	environmentContext := req.EnvironmentIsContext()
	if len(reqPURLs) == 0 && !environmentContext && reqEnv.Ecosystem != "" &&
		matchedSymbol == "" && !purlsSupportEcosystem(samplePURLs, reqEnv.Ecosystem) {
		return domain.SearchResult{}, false
	}

	// Verification receipts are execution variants: resolved package set,
	// environment and stage verdict came from one run and must stay together.
	// Grading against the manifest while borrowing a PASS from an arbitrary
	// same-name receipt made axios@1 on Linux look verified by an axios@2 on
	// Windows receipt. Read the variants before computing the delta so the
	// grade, evidence and exact-failure decision all use the same run.
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

	sampleEnv := manifest.Environment.Normalize()
	askedEnv := serverEnvAskedAbout(reqEnv, matched.Ecosystem, environmentContext)
	delta := envDelta(askedEnv, sampleEnv, matched, reqVersion)
	selected, hasSelected := selectServerReceiptVariant(receipts, reqPURLs, reqEnv, environmentContext)
	strengthReceipts := receipts
	allowAggregateStatus := true
	if hasSelected {
		matched = selected.matched
		delta = selected.delta
		strengthReceipts = receiptsForServerVariant(receipts, selected.receipt)
		allowAggregateStatus = false
	} else if hasResolvedServerReceipt(receipts) {
		// Resolved receipts exist, but none describe a package the caller
		// explicitly asked about. The manifest can still be a reference hit;
		// those unrelated executions cannot verify it for this request.
		strengthReceipts = nil
		allowAggregateStatus = false
	}
	score *= delta.fit

	contractPasses, passPeers, _ := receiptStrength(strengthReceipts)
	if hasSelected {
		contractPasses, passPeers, _ = receiptVariantStrength(strengthReceipts)
	}
	exactFailureMatched := candidateFailureMatched && hasNonemptyContract(manifest.Case.Contract) &&
		hasSelected && selectedServerContractPassed(selected, fingerprintPackages, reqPURLs)
	switch {
	case passPeers >= 2 || (allowAggregateStatus && verifiedStatus(row.Status)): // L4+: cross-verified
		score *= 3
	case contractPasses >= 1: // L3: contract passed at origin
		score *= 2
	}

	// No recency decay. A sample is about one pinned release, so it does not
	// rot on the shelf, and decaying it by publication date sank every sample
	// nobody had capacity to re-verify -- which is nearly all of them. That
	// made findability a function of when something was published, the same
	// defect already removed from candidate selection above.

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
	if len(knownFailures) > 0 {
		grade = worseGrade(grade, domain.GradeReferenceOnly)
	}

	result := domain.SearchResult{
		Grade:               grade,
		Confidence:          summary.Confidence,
		Score:               score,
		SampleID:            row.SampleID,
		SampleStatus:        row.Status,
		ExactFailureMatched: exactFailureMatched,
		Exact:               delta.exact,
		Different:           delta.different,
		Adaptation:          delta.adaptation,
		Evidence:            summary,
		KnownFailures:       knownFailures,
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

// receiptVariantStrength is the strict v2 form used after selecting a real
// execution variant. Row-level fallback columns are legacy indexes, not the
// signed stage verdict, so they cannot verify a selected matrix cell.
func receiptVariantStrength(receipts []compatibility.ReceiptInfo) (int, int, time.Time) {
	passes := 0
	peers := map[string]bool{}
	var last time.Time
	for _, rec := range receipts {
		if rec.CreatedAt.After(last) {
			last = rec.CreatedAt
		}
		if rec.Stages["resolve"] == string(domain.ResultPass) &&
			rec.Stages["contract"] == string(domain.ResultPass) {
			passes++
			peers[rec.PeerID] = true
		}
	}
	return passes, len(peers), last
}

func hasNonemptyContract(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

// serverReceiptSelection is one real v2 execution. None of its fields may be
// borrowed from another receipt: resolved versions, environment and stage
// verdict are the signed statement whose fit is being graded.
type serverReceiptSelection struct {
	receipt    compatibility.ReceiptInfo
	matched    domain.PURL
	reqVersion string
	delta      deltaResult
}

// selectServerReceiptVariant chooses the best real execution for this
// request. Package fit outranks verdict, verdict outranks environment fit,
// matching the local search engine's receipt-variant ordering. Receipts with
// no v2 resolved package identity cannot establish a version and stay out.
func selectServerReceiptVariant(receipts []compatibility.ReceiptInfo, requested []domain.PURL,
	reqEnv domain.EnvironmentFingerprint, environmentInferred bool) (serverReceiptSelection, bool) {

	var best serverReceiptSelection
	bestDistance := 0
	haveBest := false
	for _, receipt := range receipts {
		if len(receipt.ResolvedPackages) == 0 || receipt.Stages["resolve"] != string(domain.ResultPass) {
			continue
		}
		matched, reqVersion, distance, ok := serverVariantPackageMatch(requested, receipt.ResolvedPackages)
		if !ok {
			continue
		}
		askedEnv := serverEnvAskedAbout(reqEnv, matched.Ecosystem, environmentInferred)
		selection := serverReceiptSelection{
			receipt: receipt, matched: matched, reqVersion: reqVersion,
			delta: envDelta(askedEnv, receipt.Env.Normalize(), matched, reqVersion),
		}
		if !haveBest || betterServerReceiptSelection(selection, distance, best, bestDistance) {
			best, bestDistance, haveBest = selection, distance, true
		}
	}
	return best, haveBest
}

func serverVariantPackageMatch(requested, resolved []domain.PURL) (domain.PURL, string, int, bool) {
	if len(resolved) == 0 {
		return domain.PURL{}, "", 0, false
	}
	if len(requested) == 0 {
		return resolved[0], "", 0, true
	}
	var matched domain.PURL
	reqVersion := ""
	worst := -1
	for _, rp := range requested {
		for _, sp := range resolved {
			if !samePackageIdentity(rp, sp) {
				continue
			}
			if distance := versionDistance(rp, sp); distance > worst {
				matched, reqVersion, worst = sp, rp.Version, distance
			}
		}
	}
	return matched, reqVersion, worst, worst >= 0
}

func betterServerReceiptSelection(a serverReceiptSelection, aDistance int,
	b serverReceiptSelection, bDistance int) bool {
	if aDistance != bDistance {
		return aDistance < bDistance
	}
	if ar, br := serverStageVerdictRank(a.receipt.Stages), serverStageVerdictRank(b.receipt.Stages); ar != br {
		return ar > br
	}
	if ar, br := serverDeltaRank(a.delta), serverDeltaRank(b.delta); ar != br {
		return ar > br
	}
	if a.delta.fit != b.delta.fit {
		return a.delta.fit > b.delta.fit
	}
	return a.receipt.CreatedAt.After(b.receipt.CreatedAt)
}

func serverStageVerdictRank(stages map[string]string) int {
	switch stages["contract"] {
	case string(domain.ResultPass):
		return 2
	case string(domain.ResultFail):
		return 0
	default:
		return 1
	}
}

func serverDeltaRank(delta deltaResult) int {
	switch delta.grade {
	case domain.GradeExact:
		return 4
	case domain.GradeCompatible:
		return 3
	case domain.GradeAdaptationRequired:
		return 2
	default:
		return 1
	}
}

func hasResolvedServerReceipt(receipts []compatibility.ReceiptInfo) bool {
	for _, receipt := range receipts {
		if len(receipt.ResolvedPackages) > 0 && receipt.Stages["resolve"] == string(domain.ResultPass) {
			return true
		}
	}
	return false
}

// receiptsForServerVariant retains only independent executions of the exact
// resolved package set in the exact normalized environment selected above.
// A PASS from another matrix cell must not raise this cell's evidence.
func receiptsForServerVariant(receipts []compatibility.ReceiptInfo,
	selected compatibility.ReceiptInfo) []compatibility.ReceiptInfo {

	var out []compatibility.ReceiptInfo
	for _, receipt := range receipts {
		if sameResolvedPackageSet(receipt.ResolvedPackages, selected.ResolvedPackages) &&
			receipt.Env.Normalize().Hash() == selected.Env.Normalize().Hash() {
			out = append(out, receipt)
		}
	}
	return out
}

func sameResolvedPackageSet(a, b []domain.PURL) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameExactPackage(a[i], b[i]) {
			return false
		}
	}
	return true
}

// selectedServerContractPassed requires the selected receipt itself to say
// resolve PASS and contract PASS, and to contain the exact canonical
// package/version that produced the fingerprint. When packages were explicit,
// that same exact PURL must be among them; same-name evidence is insufficient.
func selectedServerContractPassed(selected serverReceiptSelection, failurePackages,
	requested []domain.PURL) bool {
	if len(failurePackages) == 0 || selected.receipt.Stages["resolve"] != string(domain.ResultPass) ||
		selected.receipt.Stages["contract"] != string(domain.ResultPass) {
		return false
	}
	for _, resolved := range selected.receipt.ResolvedPackages {
		for _, failed := range failurePackages {
			if !sameExactPackage(resolved, failed) ||
				(len(requested) > 0 && !containsExactPackage(requested, resolved)) {
				continue
			}
			return true
		}
	}
	return false
}

func sameExactPackage(a, b domain.PURL) bool {
	return samePackageIdentity(a, b) && a.Version == b.Version
}

func containsExactPackage(packages []domain.PURL, want domain.PURL) bool {
	for _, p := range packages {
		if sameExactPackage(p, want) {
			return true
		}
	}
	return false
}

func samePackageIdentity(a, b domain.PURL) bool {
	return strings.EqualFold(a.Ecosystem, b.Ecosystem) && strings.EqualFold(a.Name, b.Name)
}

// eligibleFailurePackages keeps grading and failure attribution separate.
// The widest shared version gap still chooses the grading PURL, while an
// exact failure may belong to any package shared by request and sample. With
// no package filter, every manifest-declared package is eligible.
func eligibleFailurePackages(requested, sample []domain.PURL) []domain.PURL {
	if len(requested) == 0 {
		return uniquePackageIdentities(sample)
	}
	var out []domain.PURL
	for _, sp := range sample {
		for _, rp := range requested {
			if samePackageIdentity(sp, rp) {
				out = append(out, sp)
				break
			}
		}
	}
	return uniquePackageIdentities(out)
}

func uniquePackageIdentities(in []domain.PURL) []domain.PURL {
	var out []domain.PURL
	seen := map[string]bool{}
	for _, p := range in {
		if p.Name == "" {
			continue
		}
		key := strings.ToLower(p.Ecosystem) + "\x00" + strings.ToLower(p.Name)
		if !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
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

// matchingClusters inspects every eligible sample package and preserves the
// package identity of each exact fingerprint match. The environment-summary
// filter controls warnings only; the fingerprint describes the observed
// failure itself. The worst version gap used for grading is intentionally not
// consulted here.
func (a *api) matchingClusters(r *http.Request, packages []domain.PURL,
	reqEnv domain.EnvironmentFingerprint, req domain.SearchRequest, declaredSymbols []string) ([]domain.KnownFailure, []domain.PURL) {
	reqDims := envDims(reqEnv)
	var out []domain.KnownFailure
	var fingerprintPackages []domain.PURL
	matchedPackage := map[string]bool{}
	for _, p := range uniquePackageIdentities(packages) {
		clusters, err := a.d.Store.ListFailureClusters(r.Context(), p.Name)
		if err != nil {
			continue
		}
		for _, c := range clusters {
			// The store looks clusters up by BARE PACKAGE NAME, so ecosystem
			// remains a mandatory identity check for colliding names.
			if c.Ecosystem != "" && p.Ecosystem != "" && !strings.EqualFold(c.Ecosystem, p.Ecosystem) {
				continue
			}
			if declaredFailureSymbol(c.Symbol, declaredSymbols) && matchesSearchFingerprint(req, c.ErrorFingerprint) {
				key := strings.ToLower(p.Ecosystem) + "\x00" + strings.ToLower(p.Name)
				if !matchedPackage[key] {
					matchedPackage[key] = true
					fingerprintPackages = append(fingerprintPackages, p)
				}
			}
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
	}
	return out, fingerprintPackages
}

func declaredFailureSymbol(clusterSymbol string, declared []string) bool {
	clusterSymbol = strings.TrimSpace(clusterSymbol)
	if clusterSymbol == "" {
		return false
	}
	for _, symbol := range declared {
		symbol = strings.TrimSpace(symbol)
		if symbol != "" && strings.EqualFold(symbol, clusterSymbol) {
			return true
		}
	}
	return false
}

func manifestDeclaredSymbols(manifest domain.SampleManifest) []string {
	out := append([]string(nil), manifest.Symbols...)
	out = append(out, manifest.Case.Symbols...)
	return out
}

// matchesSearchFingerprint compares stable hashes exactly. Case folding or
// substring matching would turn a near miss into a claimed failure detour.
func matchesSearchFingerprint(req domain.SearchRequest, stored string) bool {
	if stored == "" {
		return false
	}
	if req.ErrorFingerprint != "" && stored == req.ErrorFingerprint {
		return true
	}
	for _, fingerprint := range req.ErrorFingerprints {
		if fingerprint != "" && stored == fingerprint {
			return true
		}
	}
	return false
}

// requestFingerprintInText preserves the older topic-relevance behavior for
// samples that explicitly discuss a fingerprint. It never sets
// ExactFailureMatched; only recorded failure clusters can earn that field.
func requestFingerprintInText(req domain.SearchRequest, text string) bool {
	text = strings.ToLower(text)
	if req.ErrorFingerprint != "" && strings.Contains(text, strings.ToLower(req.ErrorFingerprint)) {
		return true
	}
	for _, fingerprint := range req.ErrorFingerprints {
		if fingerprint != "" && strings.Contains(text, strings.ToLower(fingerprint)) {
			return true
		}
	}
	return false
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

	sampleEcosystem := sample.Ecosystem
	if sampleEcosystem == "" {
		sampleEcosystem = matched.Ecosystem
	}
	if req.Ecosystem != "" && sampleEcosystem != "" {
		if strings.EqualFold(req.Ecosystem, sampleEcosystem) {
			d.exact = append(d.exact, "ecosystem "+strings.ToLower(sampleEcosystem))
		} else {
			d.grade = worseGrade(d.grade, domain.GradeReferenceOnly)
			d.different = append(d.different, "ecosystem "+strings.ToLower(req.Ecosystem)+
				" (sample: "+strings.ToLower(sampleEcosystem)+")")
			d.fit *= 0.4
		}
	}

	// Package version distance.
	if reqVersion != "" && matched.Name != "" && matched.Version != reqVersion {
		reqP := domain.PURL{Ecosystem: matched.Ecosystem, Name: matched.Name, Version: reqVersion}
		switch {
		// BreakingBucket, not Major: semver makes a 0.x minor bump exactly
		// as breaking as a major one, so cargo 0.6 against 0.8 is a
		// different line entirely, not "a minor version difference". The
		// client grader was fixed to use it and this one was not, so the
		// same axum question got REFERENCE_ONLY from one path and
		// ADAPTATION_REQUIRED with "verify against axum@0.8.1" from the
		// other. Pre-1.0 is where most of Rust and much of Dart lives.
		case reqP.BreakingBucket() != matched.BreakingBucket():
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
	//
	// Resolved rather than read raw. A caller that does not set
	// executionContext — which is most of them, since an agent reports what
	// it detected — lost the comparison entirely, and with it the only axis
	// that separates one language's samples from another's. The client's
	// grader already falls back this way; the server did not.
	reqCtx, sampleCtx := resolveContext(req), resolveContext(sample)
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
		rv, sv := runtimeLine(req.Runtime, req.RuntimeVersion), runtimeLine(sample.Runtime, sample.RuntimeVersion)
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

	// libc is not a mild dimension. It decides whether a package with a
	// native module loads at all, and this project publishes findings that
	// exist only because of it — the esbuild and lightningcss samples are
	// about nothing else. Both sides declared it and nobody compared it, so a
	// glibc caller was told EXACT by a sample verified only on musl.
	if req.Libc != "" && sample.Libc != "" && req.Libc != sample.Libc {
		d.different = append(d.different, "libc "+req.Libc+" (sample: "+sample.Libc+")")
		d.adaptation = append(d.adaptation, "verify on "+req.Libc+": a native module resolved for "+sample.Libc+" may not load")
		d.grade = worseGrade(d.grade, domain.GradeAdaptationRequired)
		d.fit *= 0.8
	} else {
		cmp("libc", req.Libc, sample.Libc)
	}

	// arch, same reason at lower weight: a prebuilt binary is per
	// architecture, and it was declared by both sides and never compared.
	if req.Arch != "" && sample.Arch != "" && req.Arch != sample.Arch {
		d.different = append(d.different, "arch "+req.Arch+" (sample: "+sample.Arch+")")
		d.adaptation = append(d.adaptation, "verify on "+req.Arch)
		d.grade = worseGrade(d.grade, domain.GradeAdaptationRequired)
		d.fit *= 0.85
	} else {
		cmp("arch", req.Arch, sample.Arch)
	}

	// Language, which nothing compared at all although both sides declare it.
	// With executionContext absent on the request this was the last thing
	// standing between a Python question and a Go sample, and a cross-language
	// hit came back reporting the package manager as its only difference.
	if req.Language != "" && sample.Language != "" && req.Language != sample.Language {
		d.grade = worseGrade(d.grade, domain.GradeReferenceOnly)
		d.different = append(d.different, "language "+req.Language+" (sample: "+sample.Language+")")
		d.fit *= 0.3
	} else {
		cmp("language", req.Language, sample.Language)
	}

	// Non-sensitive dims: mild penalties, honest delta lines.
	//
	// "Non-sensitive" bounds how far the grade drops, not whether it drops
	// at all. This branch multiplied the fit and wrote the delta line and
	// left the grade alone, so POST /v1/search answered a linux caller with
	// MATCH: EXACT for a sample verified on windows — and printed
	// "os linux (sample: windows)" directly underneath it. EXACT means
	// nothing here differs from yours; a difference the response itself
	// lists is a difference.
	if req.OS != "" && sample.OS != "" && req.OS != sample.OS {
		d.grade = worseGrade(d.grade, domain.GradeCompatible)
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
	// The package NAME, not the raw purl.
	//
	// A purl tokenizes to pkg, the ecosystem, the name and the version
	// digits — so "pkg" and "npm" were content tokens of EVERY sample, and
	// the relevance gate ("shares no content word with the sample" is a
	// miss) opened for any query containing either. The client-side engine
	// had the same shape of bug through package names; this is its twin,
	// through the purl itself.
	for _, raw := range m.Packages {
		if pp, err := domain.ParsePURL(raw); err == nil && pp.Name != "" {
			b.WriteByte(' ')
			b.WriteString(pp.Name)
			continue
		}
		b.WriteByte(' ')
		b.WriteString(raw)
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

// searchStopWords are ignored when judging what a question is ABOUT.
// Counting them let "how to bake a chocolate cake" overlap the goal
// "…validate a UUID in Go…" on the word "a", which is not a topic in
// common. Mirrors internal/search so the client and the API agree on what
// counts as relevant.
var searchStopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"how": true, "why": true, "what": true, "when": true, "does": true,
	"can": true, "you": true, "your": true, "this": true, "that": true,
	"into": true, "out": true, "not": true, "but": true, "get": true,
	"use": true, "using": true, "make": true, "want": true, "need": true,
	"there": true, "here": true, "some": true, "any": true, "all": true,
}

// sharedContentTokens counts the topic words a query and a sample have in
// common, ignoring stop words and anything shorter than three letters.
func sharedContentTokens(query, text string) int {
	have := map[string]bool{}
	for _, t := range tokens(text) {
		if len(t) >= 3 && !searchStopWords[t] {
			have[t] = true
		}
	}
	shared := 0
	for _, t := range tokens(query) {
		if len(t) >= 3 && !searchStopWords[t] && have[t] {
			shared++
		}
	}
	return shared
}

func samplePackageNames(packages []domain.PURL) []string {
	out := make([]string, 0, len(packages))
	for _, p := range packages {
		if p.Name != "" {
			out = append(out, p.Name)
		}
	}
	return out
}

func purlsSupportEcosystem(packages []domain.PURL, ecosystem string) bool {
	for _, p := range packages {
		if strings.EqualFold(p.Ecosystem, ecosystem) {
			return true
		}
	}
	return false
}

func serverEnvAskedAbout(req domain.EnvironmentFingerprint, sampleEcosystem string,
	inferred bool) domain.EnvironmentFingerprint {
	if !inferred || sampleEcosystem == "" || req.Ecosystem == "" ||
		strings.EqualFold(req.Ecosystem, sampleEcosystem) {
		return req
	}
	req.Ecosystem = ""
	req.Runtime, req.RuntimeVersion = "", ""
	req.Language, req.LanguageVersion = "", ""
	req.Compiler, req.CompilerVersion = "", ""
	req.ModuleSystem = ""
	req.PackageManager, req.PackageManagerVersion = "", ""
	req.ExecutionContext = ""
	req.Engine, req.EngineVersion = "", ""
	req.BrowserFamily, req.BrowserMajor = "", ""
	return req
}

// versionDistance ranks how far apart two versions of the same package are,
// so the grader can pick the widest gap rather than the first match. Larger
// is further: 0 identical, 1 same major.minor, 2 same breaking line,
// 3 different breaking line.
func versionDistance(req, sample domain.PURL) int {
	switch {
	case req.Version == sample.Version:
		return 0
	case req.MajorMinor() == sample.MajorMinor():
		return 1
	case req.BreakingBucket() == sample.BreakingBucket():
		return 2
	default:
		return 3
	}
}

// runtimeLine is the version line a runtime's compatibility actually turns
// on. majorSeg alone compared "1" to "1", so Go 1.9 and Go 1.26 graded
// equal, and so did Python 3.6 and 3.12 — for the two runtimes whose whole
// release history lives in the second segment, that is a wrong answer with
// nothing to warn the reader.
//
// Node, Ruby, PHP and Rust do break on the major, so they keep the cheaper
// comparison; adding a minor there would report a difference that is not one.
func runtimeLine(runtime, version string) string {
	if version == "" {
		return ""
	}
	switch strings.ToLower(runtime) {
	case "go", "golang", "python", "elixir", "dart":
		segs := strings.SplitN(version, ".", 3)
		if len(segs) >= 2 {
			return segs[0] + "." + segs[1]
		}
		return segs[0]
	}
	return majorSeg(version)
}

// resolveContext is the execution context an environment actually describes,
// falling back the way the client's grader does: an explicit context, else a
// browser family, else the runtime. Reading ExecutionContext raw meant a
// request that omitted it skipped the comparison altogether.
func resolveContext(e domain.EnvironmentFingerprint) string {
	if e.ExecutionContext != "" {
		return strings.ToLower(e.ExecutionContext)
	}
	if e.BrowserFamily != "" {
		return "browser"
	}
	return strings.ToLower(e.Runtime)
}

// appendUnseenSamples adds samples for the caller's dependency tree that
// the recency window did not already include, capped so a large lockfile
// cannot turn one search into an unbounded scan.
func appendUnseenSamples(r *http.Request, a *api, have []serverstore.SampleRow, tree []string) []serverstore.SampleRow {
	if len(tree) == 0 {
		return have
	}
	patterns := make([]string, 0, len(tree))
	for _, ps := range tree {
		if p, err := domain.ParsePURL(ps); err == nil {
			patterns = append(patterns, p.AnyVersionPattern())
		}
		if len(patterns) >= maxTreePatterns {
			break
		}
	}
	if len(patterns) == 0 {
		return have
	}
	extra, err := a.d.Store.SamplesForPackages(r.Context(), patterns, maxSearchCandidates)
	if err != nil {
		return have // widening is best-effort; never fail a search over it
	}
	seen := make(map[string]bool, len(have))
	for _, s := range have {
		seen[s.SampleID] = true
	}
	for _, s := range extra {
		if !seen[s.SampleID] {
			have = append(have, s)
		}
	}
	return have
}
