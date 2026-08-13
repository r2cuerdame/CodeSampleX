// Package search implements the local search engine over the SQLite store:
// candidate collection from cached samples, compatibility shards, and the
// FTS index; C7 score fusion; environment grading with the always-sensitive
// execution-context axis (docs/execution-context.md §5); and §11.5 delta
// construction. NO_SAFE_MATCH (Miss) is deliberately better than a wrong
// HIT (goal.md §3.8).
package search

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/compatibility"
	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// DefaultLimit is the result count when the request omits Limit (C7).
const DefaultLimit = 3

// ftsCandidateLimit bounds how many FTS hits feed candidate scoring.
const ftsCandidateLimit = 50

// Engine runs searches over one local store.
type Engine struct {
	DB *localdb.DB
}

// Shard wire structs mirror schemas/v1/shard.json (contract C6).
type shardFile struct {
	SchemaVersion int            `json:"schemaVersion"`
	Key           string         `json:"key"`
	GeneratedAt   string         `json:"generatedAt"`
	Packages      []shardPackage `json:"packages"`
}

type shardPackage struct {
	PURL    string             `json:"purl"`
	Symbols []shardSymbolEntry `json:"symbols,omitempty"`
	Samples []shardSampleEntry `json:"samples,omitempty"`
}

type shardSymbolEntry struct {
	Family   string           `json:"family"`
	Stats    shardSymbolStats `json:"stats"`
	Failures []shardFailure   `json:"failures,omitempty"`
}

type shardStageCount struct {
	Pass int64 `json:"pass"`
	Fail int64 `json:"fail"`
}

type shardSymbolStats struct {
	ObservationCount  int64                      `json:"observationCount"`
	UniquePeerBuckets int64                      `json:"uniquePeerBuckets"`
	PassRate          float64                    `json:"passRate"`
	ByStage           map[string]shardStageCount `json:"byStage,omitempty"`
	Confidence        string                     `json:"confidence,omitempty"`
	LastSeen          string                     `json:"lastSeen,omitempty"`
}

type shardFailure struct {
	ErrorCode   string                     `json:"errorCode,omitempty"`
	Fingerprint string                     `json:"fingerprint,omitempty"`
	Count       int64                      `json:"count"`
	EnvSummary  map[string]string          `json:"envSummary,omitempty"`
	Hypotheses  []domain.FailureHypothesis `json:"hypotheses,omitempty"`
}

type shardSampleEntry struct {
	SampleID       string                        `json:"sampleId"`
	Goal           string                        `json:"goal,omitempty"`
	Status         string                        `json:"status,omitempty"`
	License        string                        `json:"license,omitempty"`
	Environment    domain.EnvironmentFingerprint `json:"environment"`
	ContractStages map[string]string             `json:"contractStages,omitempty"`
}

// candidate is one sample under consideration, from the local samples table
// or a cached shard. It carries only public sample/case data — nothing
// project-identifying enters a result by construction.
type candidate struct {
	sampleID       string
	status         string
	caseObj        *domain.Case
	env            domain.EnvironmentFingerprint
	packages       []domain.PURL
	symbols        []string
	createdAt      time.Time
	contractStages map[string]string // from a shard sample entry, if any
	ftsScore       float64           // normalized 0..1 within this query
}

// pkgEvidence aggregates shard symbol stats + failure lists for one
// (ecosystem, package name).
type pkgEvidence struct {
	symbols []shardSymbolEntry
}

func pkgKey(p domain.PURL) string {
	return p.Ecosystem + "/" + strings.ToLower(p.Name)
}

// Search implements the C7 pipeline: candidate collection (package /
// symbol / error-fingerprint / FTS sources), score fusion, environment
// gate + execution-context rules, verification-strength rerank, recency
// decay, and the known-failure demotion. Best score below the threshold
// means NO_SAFE_MATCH: Miss=true, no results.
func (e Engine) Search(ctx context.Context, req domain.SearchRequest) domain.SearchResponse {
	resp := domain.SearchResponse{SchemaVersion: 1, Results: []domain.SearchResult{}}
	if e.DB == nil {
		resp.Miss = true
		return resp
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	reqEnv := req.Environment.Normalize()
	reqPkgs := parsePURLs(req.Packages)

	cands, evidence, err := e.collect(ctx, req)
	if err != nil || len(cands) == 0 {
		resp.Miss = true
		return resp
	}

	now := time.Now().UTC()
	type scored struct {
		res   domain.SearchResult
		score float64
		id    string
	}
	all := make([]scored, 0, len(cands))
	for _, c := range cands {
		res, sc := e.scoreCandidate(ctx, req, reqEnv, reqPkgs, c, evidence, now)
		all = append(all, scored{res: res, score: sc, id: c.sampleID})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].id < all[j].id
	})
	if all[0].score < missThreshold {
		resp.Miss = true
		return resp
	}
	for _, s := range all {
		if len(resp.Results) == limit || s.score < missThreshold {
			break
		}
		resp.Results = append(resp.Results, s.res)
	}
	return resp
}

// collect gathers candidates from the local samples table, cached shards,
// and the FTS index, plus the per-package shard evidence used for error
// matching and evidence summaries.
func (e Engine) collect(ctx context.Context, req domain.SearchRequest) (map[string]*candidate, map[string]*pkgEvidence, error) {
	cands := map[string]*candidate{}

	rows, err := e.DB.ListSamples(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range rows {
		var m domain.SampleManifest
		if json.Unmarshal([]byte(r.ManifestJSON), &m) != nil {
			continue
		}
		cc := m.Case
		if cc.CaseID == "" {
			cc.CaseID = cc.ComputeID()
		}
		c := &candidate{
			sampleID:  r.SampleID,
			status:    r.Status,
			caseObj:   &cc,
			env:       m.Environment.Normalize(),
			symbols:   m.Symbols,
			createdAt: r.CreatedAt,
		}
		c.packages = parsePURLs(m.Packages)
		if len(c.packages) == 0 {
			c.packages = parsePURLs(cc.Packages)
		}
		cands[r.SampleID] = c
	}

	evidence := map[string]*pkgEvidence{}
	shards, err := e.DB.ListShards(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range shards {
		var sf shardFile
		if json.Unmarshal([]byte(s.JSON), &sf) != nil {
			continue
		}
		for _, sp := range sf.Packages {
			p, perr := domain.ParsePURL(sp.PURL)
			if perr != nil {
				continue
			}
			key := pkgKey(p)
			pe := evidence[key]
			if pe == nil {
				pe = &pkgEvidence{}
				evidence[key] = pe
			}
			pe.symbols = append(pe.symbols, sp.Symbols...)
			for _, ss := range sp.Samples {
				if ss.SampleID == "" || cands[ss.SampleID] != nil {
					continue // local metadata is authoritative
				}
				c := &candidate{
					sampleID:       ss.SampleID,
					status:         ss.Status,
					env:            ss.Environment.Normalize(),
					packages:       []domain.PURL{p},
					contractStages: ss.ContractStages,
				}
				if ss.Goal != "" {
					c.caseObj = &domain.Case{
						SchemaVersion: 1, Kind: "HOW", Goal: ss.Goal,
						Packages: []string{sp.PURL},
					}
				}
				cands[ss.SampleID] = c
			}
		}
	}

	if strings.TrimSpace(req.Query) != "" {
		hits, ferr := e.DB.FTSQuery(ctx, req.Query, ftsCandidateLimit)
		if ferr != nil {
			return nil, nil, ferr
		}
		var max float64
		for _, h := range hits {
			if h.Score > max {
				max = h.Score
			}
		}
		if max > 0 {
			for _, h := range hits {
				if c := cands[h.DocID]; c != nil {
					c.ftsScore = h.Score / max
				}
			}
		}
	}
	return cands, evidence, nil
}

// scoreCandidate produces one SearchResult plus its fused score.
func (e Engine) scoreCandidate(ctx context.Context, req domain.SearchRequest, reqEnv domain.EnvironmentFingerprint, reqPkgs []domain.PURL, c *candidate, evidence map[string]*pkgEvidence, now time.Time) (domain.SearchResult, float64) {
	rel, reqP, samP := packageRelation(reqPkgs, c.packages)

	var syms []shardSymbolEntry
	for _, p := range c.packages {
		if pe := evidence[pkgKey(p)]; pe != nil {
			syms = append(syms, pe.symbols...)
		}
	}
	receipts, _ := e.DB.ReceiptsForSample(ctx, c.sampleID)

	// Steps 1–5: relevance fusion (exact tokens outrank lexical match).
	base := relWeight(rel)
	if matchesSymbols(req.Symbols, c) {
		base += weightSymbol
	}
	fpHit, codeHit := errorHits(req, syms)
	if fpHit {
		base += weightErrorFingerprint
	}
	if codeHit {
		base += weightErrorCode
	}
	base += weightFTS * c.ftsScore
	base += weightIntent * intentOverlap(req.Query, c)

	// Steps 6+9: environment gate, execution-context axis, known failures.
	dims := compareEnv(reqEnv, c.env, ecosystemOf(samP, reqEnv, c))
	cd := compareContext(reqEnv, c.env)
	matched := matchingFailures(reqEnv, syms)
	elevated := elevatedInRequestEnv(req, matched)
	grade, adaptations := buildGrade(rel, dims, cd, elevated)
	exact, different := buildDelta(rel, reqP, samP, dims, cd)

	summary := buildEvidence(c, syms, receipts, now)

	// Steps 7–8: verification-strength rerank + recency decay.
	lvl := verificationLevel(c.status, c.contractStages, receipts)
	score := base * envFit(grade, cd) * recency(c, now) * strengthBoost(lvl)

	res := domain.SearchResult{
		Grade:         grade,
		Confidence:    summary.Confidence,
		Score:         score,
		Case:          c.caseObj,
		SampleID:      c.sampleID,
		SampleStatus:  c.status,
		Exact:         exact,
		Different:     different,
		Adaptation:    adaptations,
		Evidence:      summary,
		KnownFailures: knownFailures(matched),
	}
	return res, score
}

// buildEvidence fills the honest numbers behind a result. PROJECT_*
// observation counts and contract evidence live in SEPARATE fields and are
// never summed together (goal.md §3.5, docs/execution-context.md §6);
// the C7 confidence formula weighs the classes, it does not conflate them.
func buildEvidence(c *candidate, syms []shardSymbolEntry, receipts []domain.VerificationReceipt, now time.Time) domain.EvidenceSummary {
	var s domain.EvidenceSummary
	var evSamples []compatibility.Sample
	var lastSeen string

	for _, sym := range syms {
		st := sym.Stats
		if pc, ok := st.ByStage[string(domain.StageProjectCompile)]; ok {
			s.ProjectCompileObservations += pc.Pass + pc.Fail
			s.CleanBuilds += pc.Pass
		}
		if st.UniquePeerBuckets > s.UniquePeerBuckets {
			s.UniquePeerBuckets = st.UniquePeerBuckets
		}
		if st.LastSeen > lastSeen {
			lastSeen = st.LastSeen
		}
		age := ageFrom(st.LastSeen, now)
		pass := int64(st.PassRate*float64(st.ObservationCount) + 0.5)
		fail := st.ObservationCount - pass
		if pass > 0 {
			evSamples = append(evSamples, compatibility.Sample{
				Class: domain.ClassUsageObservation, Result: domain.ResultPass, Count: pass, Age: age})
		}
		if fail > 0 {
			evSamples = append(evSamples, compatibility.Sample{
				Class: domain.ClassUsageObservation, Result: domain.ResultFail, Count: fail, Age: age})
		}
		for _, f := range sym.Failures {
			if f.Count >= elevatedFailureMinCount {
				s.ElevatedFailures = append(s.ElevatedFailures, humanEnvSummary(f.EnvSummary))
			}
		}
	}

	peers := map[string]bool{}
	for _, r := range receipts {
		res, ok := contractResult(r.Stages)
		if !ok {
			continue
		}
		evSamples = append(evSamples, compatibility.Sample{
			Class: domain.ClassSampleVerification, Result: res, Count: 1,
			Age: ageFrom(r.CreatedAt, now)})
		if res == domain.ResultPass {
			s.ContractPasses++
			if r.PeerID != "" {
				peers[r.PeerID] = true
			}
		}
	}
	// A shard sample entry may declare a contract PASS the local store has
	// no receipt for; count it only when no receipts exist to avoid double
	// counting.
	if len(receipts) == 0 && c.contractStages["contract"] == "PASS" {
		s.ContractPasses++
	}
	s.IndependentCrossPeers = int64(len(peers))

	independence := s.UniquePeerBuckets
	if s.IndependentCrossPeers > independence {
		independence = s.IndependentCrossPeers
	}
	v := compatibility.Compute(evSamples, independence)
	s.PassRate = v.PassRate
	s.Confidence = v.Confidence
	s.LastSeen = lastSeen
	return s
}

// contractResult reads the contract stage of a receipt; SKIPPED and absent
// stages contribute nothing.
func contractResult(stages map[string]string) (domain.Result, bool) {
	switch stages["contract"] {
	case "PASS":
		return domain.ResultPass, true
	case "FAIL":
		return domain.ResultFail, true
	}
	return "", false
}

// errorHits reports exact error-fingerprint / error-code hits in the shard
// failure lists of the candidate's packages (§11.3 step 3).
func errorHits(req domain.SearchRequest, syms []shardSymbolEntry) (fp, code bool) {
	for _, s := range syms {
		for _, f := range s.Failures {
			if req.ErrorFingerprint != "" && f.Fingerprint == req.ErrorFingerprint {
				fp = true
			}
			if req.ErrorCode != "" && f.ErrorCode != "" && strings.EqualFold(f.ErrorCode, req.ErrorCode) {
				code = true
			}
		}
	}
	return fp, code
}

// matchingFailures returns the shard failures whose envSummary matches the
// requester environment — the clusters that would bite THIS user.
func matchingFailures(reqEnv domain.EnvironmentFingerprint, syms []shardSymbolEntry) []shardFailure {
	var out []shardFailure
	for _, s := range syms {
		for _, f := range s.Failures {
			if envSummaryMatches(reqEnv, f.EnvSummary) {
				out = append(out, f)
			}
		}
	}
	return out
}

// elevatedInRequestEnv applies the known-failure demotion rule, exempting
// the failure the user is explicitly searching a fix for.
func elevatedInRequestEnv(req domain.SearchRequest, matched []shardFailure) bool {
	for _, f := range matched {
		if f.Count < elevatedFailureMinCount {
			continue
		}
		if req.ErrorFingerprint != "" && f.Fingerprint == req.ErrorFingerprint {
			continue
		}
		if req.ErrorCode != "" && f.ErrorCode != "" && strings.EqualFold(f.ErrorCode, req.ErrorCode) {
			continue
		}
		return true
	}
	return false
}

// envSummaryMatches is conservative: every summary key must positively
// match the requester env; unknown keys mean the cluster cannot be
// confirmed to apply, so it does not demote.
func envSummaryMatches(req domain.EnvironmentFingerprint, summary map[string]string) bool {
	if len(summary) == 0 {
		return false
	}
	for k, v := range summary {
		switch k {
		case "moduleSystem":
			if !strings.EqualFold(v, req.ModuleSystem) {
				return false
			}
		case "runtime":
			name, ver, _ := strings.Cut(v, "@")
			if !strings.EqualFold(name, req.Runtime) {
				return false
			}
			if ver != "" && majorOf(ver) != majorOf(req.RuntimeVersion) {
				return false
			}
		case "browserFamily":
			if !strings.EqualFold(v, req.BrowserFamily) {
				return false
			}
		case "browserMajor":
			if majorOf(v) != majorOf(req.BrowserMajor) {
				return false
			}
		case "executionContext":
			if !strings.EqualFold(v, contextOf(req)) {
				return false
			}
		case "engine":
			if !strings.EqualFold(v, req.Normalize().Engine) {
				return false
			}
		case "os":
			if !strings.EqualFold(v, req.OS) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// knownFailures passes matched clusters through, hypotheses intact —
// probabilistic attributions are never turned into definitive causes
// (goal.md §3.6).
func knownFailures(matched []shardFailure) []domain.KnownFailure {
	if len(matched) == 0 {
		return nil
	}
	out := make([]domain.KnownFailure, 0, len(matched))
	for _, f := range matched {
		out = append(out, domain.KnownFailure{
			ErrorCode:   f.ErrorCode,
			Fingerprint: f.Fingerprint,
			Count:       f.Count,
			EnvSummary:  f.EnvSummary,
			Hypotheses:  f.Hypotheses,
		})
	}
	return out
}

// intentOverlap is the v1 step-5 similarity: query-token overlap against
// the case goal (no embeddings in Public v1).
func intentOverlap(query string, c *candidate) float64 {
	q := strings.Fields(strings.ToLower(query))
	if len(q) == 0 || c.caseObj == nil {
		return 0
	}
	goal := map[string]bool{}
	for _, t := range strings.Fields(strings.ToLower(c.caseObj.Goal)) {
		goal[t] = true
	}
	hit := 0
	for _, t := range q {
		if goal[t] {
			hit++
		}
	}
	return float64(hit) / float64(len(q))
}

// matchesSymbols reports whether any requested symbol family matches the
// candidate's declared symbols.
func matchesSymbols(reqSyms []string, c *candidate) bool {
	if len(reqSyms) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, s := range c.symbols {
		have[strings.ToLower(s)] = true
	}
	if c.caseObj != nil {
		for _, s := range c.caseObj.Symbols {
			have[strings.ToLower(s)] = true
		}
	}
	for _, s := range reqSyms {
		if have[strings.ToLower(s)] {
			return true
		}
	}
	return false
}

// ecosystemOf picks the ecosystem governing dimension sensitivity.
func ecosystemOf(samP domain.PURL, reqEnv domain.EnvironmentFingerprint, c *candidate) string {
	if samP.Ecosystem != "" {
		return samP.Ecosystem
	}
	if len(c.packages) > 0 {
		return c.packages[0].Ecosystem
	}
	if c.env.Ecosystem != "" {
		return c.env.Ecosystem
	}
	return reqEnv.Ecosystem
}

// recency applies the C7 half-life to the sample's local age; unknown age
// (shard-only candidates) is not penalized.
func recency(c *candidate, now time.Time) float64 {
	if c.createdAt.IsZero() {
		return 1
	}
	return compatibility.RecencyDecay(now.Sub(c.createdAt))
}

// ageFrom parses an RFC3339 stamp into an age; unparseable means age 0.
func ageFrom(stamp string, now time.Time) time.Duration {
	if stamp == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return 0
	}
	age := now.Sub(t)
	if age < 0 {
		return 0
	}
	return age
}

// parsePURLs parses the valid purls and drops malformed entries.
func parsePURLs(ss []string) []domain.PURL {
	var out []domain.PURL
	for _, s := range ss {
		if p, err := domain.ParsePURL(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}
