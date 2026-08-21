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
	searchrelevance "github.com/r2cuerdame/codesamplex/internal/search/relevance"
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
	SampleID string `json:"sampleId"`
	Goal     string `json:"goal,omitempty"`
	Status   string `json:"status,omitempty"`
	License  string `json:"license,omitempty"`
	// Packages is what the sample's own manifest declares. It remains useful
	// for relevance and disclosure, but never establishes a verified version.
	Packages []string `json:"packages,omitempty"`
	// Symbols is the bounded manifest-declared list carried by current
	// shards. Older shards omit it; omission means exact failure attribution
	// is unavailable, never that every package-level failure belongs to the
	// sample.
	Symbols []string `json:"symbols,omitempty"`
	// Verifications keeps each resolved package set with the stages and
	// environment of the receipt that established it. The legacy fields stay
	// readable for old shards, but never establish a version.
	Verifications  []shardVerificationEntry      `json:"verifications,omitempty"`
	Environment    domain.EnvironmentFingerprint `json:"environment"`
	ContractStages map[string]string             `json:"contractStages,omitempty"`
	// Contract is what the sample's contract command asserted and proved in
	// a pinned container. Absent from shards generated before this field
	// existed, which simply means the claims are unknown here rather than
	// that there were none.
	Contract []string `json:"contract,omitempty"`
	// Believed is the belief the sample corrects, when its author stated
	// one. Absent from older shards and from samples that correct nothing.
	Believed string `json:"believed,omitempty"`
}

type shardVerificationEntry struct {
	ResolvedPackages  []string                      `json:"resolvedPackages"`
	Environment       domain.EnvironmentFingerprint `json:"environment"`
	Stages            map[string]string             `json:"stages"`
	VerificationLevel int                           `json:"verificationLevel,omitempty"`
	CreatedAt         string                        `json:"createdAt,omitempty"`
}

// verificationVariant is one coherent execution claim. It is never merged
// with another variant: package versions, environment and PASS/FAIL belong
// to the same receipt or they do not belong in one search grade.
type verificationVariant struct {
	// key identifies the variant for de-duplication. It is derived once, on
	// first use, because deriving it normalises an environment, hashes it and
	// renders canonical JSON — work that used to be repeated for every
	// element of the list on every append.
	key      string
	packages []domain.PURL
	env      domain.EnvironmentFingerprint
	stages   map[string]string
	level    int
	created  time.Time
	receipt  *domain.VerificationReceipt
}

// candidate is one sample under consideration, from the local samples table
// or a cached shard. It carries only public sample/case data — nothing
// project-identifying enters a result by construction.
type candidate struct {
	sampleID string
	status   string
	caseObj  *domain.Case
	env      domain.EnvironmentFingerprint
	// packages is the union of every package this candidate can be REACHED
	// by: one sample is listed in a shard under several package versions, and
	// keeping only the first made name relevance depend on iteration order.
	// It is a relevance set, never an authority on what the sample declares.
	packages []domain.PURL
	// declared is what the SAMPLE ITSELF asks for, from its manifest. It is
	// retained for relevance and honest display, but does not prove what the
	// resolver selected.
	//
	// Conflating reachability, declaration and verification is how MCP search
	// came to answer "MATCH: EXACT,
	// Exact: axios 1.12" for a sample whose csx.json pins axios@1.19.0 —
	// packageRelation keeps whichever pair scores best, and with every shard
	// key unioned on, one of them always matched the request exactly. The
	// server graded the same input ADAPTATION_REQUIRED because it reads the
	// manifest. A wrong HIT is worse than a MISS (goal.md §3.8), and this was
	// the wrong HIT arriving with the highest possible confidence.
	declared []domain.PURL
	// verifications are receipt-scoped exact resolver outputs. Flattening
	// them would let one version's PASS verify another version's package set.
	verifications  []verificationVariant
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

// sampleIDFromDocID maps an FTS document id back to the sample it indexes.
//
// Two writers disagreed about the key. Shard sync indexes a sample as
// "sample:"+sampleID; SeedSampleDoc — the path the unit tests use — indexes
// the bare sampleID. Scoring looked the id up bare, so on any real install,
// where every candidate arrives through shard sync, NO FTS hit ever matched
// a candidate: bm25 was computed, ranked, and thrown away.
//
// weightFTS is 0.30, the single largest relevance term, and it was
// contributing nothing. What remained was intentOverlap, a shared-token
// ratio divided by the length of the question — so asking a longer, more
// specific question scored LOWER. Measured on the live network: "clap
// derive parse" answered, "parse command line flags in rust with clap"
// returned NO_SAFE_MATCH, for the same sample.
//
// Every test passed throughout, because the fixtures used the other writer.
func sampleIDFromDocID(docID string) string {
	return strings.TrimPrefix(docID, "sample:")
}

// Search implements the C7 pipeline: candidate collection (package /
// symbol / error-fingerprint / FTS sources), score fusion, environment
// gate + execution-context rules, verification-strength rerank, recency
// decay, and the known-failure demotion. Best score below the threshold
// means NO_SAFE_MATCH: Miss=true, no results.
func (e Engine) Search(ctx context.Context, req domain.SearchRequest) domain.SearchResponse {
	version := 1
	if req.SchemaVersion >= 2 {
		version = 2
	}
	resp := domain.SearchResponse{SchemaVersion: version, Results: []domain.SearchResult{}}
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
	// One result per (packages, symbols) coordinate — the same fold the HTTP
	// search applies. Two samples for one coordinate are the same answer
	// twice: side by side they burn the caller's result budget and read as
	// corroboration, and this engine serves search_known_solution, the
	// surface where that budget is three.
	seenCoordinate := map[string]bool{}
	for _, s := range all {
		if len(resp.Results) == limit || s.score < missThreshold {
			break
		}
		if key, ok := domain.ResultCoordinate(s.res); ok {
			if seenCoordinate[key] {
				continue
			}
			seenCoordinate[key] = true
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
		// A local row carries the real manifest, so its packages are declared.
		c.declared = c.packages
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
				if ss.SampleID == "" {
					continue
				}
				if existing := cands[ss.SampleID]; existing != nil {
					// The NETWORK decides a sample's status; a local row is
					// a cache of the artifact, not of the network's
					// judgement. storeFetched writes "PUBLISHED" for
					// anything it downloads, so fetching a STABLE sample
					// downgraded it locally from verification level 5 to 3
					// — a ×3 strength multiplier to ×1 — and using a sample
					// made it markedly harder to find again, sometimes
					// under the miss threshold entirely.
					if statusRank(ss.Status) > statusRank(existing.status) {
						existing.status = ss.Status
					}
					if len(existing.declared) == 0 {
						existing.declared = parsePURLs(ss.Packages)
					}
					if len(existing.symbols) == 0 {
						existing.symbols = append([]string(nil), ss.Symbols...)
					}
					for _, variant := range variantsFromShard(ss.Verifications) {
						existing.verifications = appendVerification(existing.verifications, variant)
						for _, verified := range variant.packages {
							existing.packages = appendPURL(existing.packages, verified)
						}
					}
					// Local metadata stays authoritative, but a sample listed
					// in several shards uses several packages, and keeping
					// only the first one made both version grading and
					// package-name relevance depend on shard iteration order.
					existing.packages = appendPURL(existing.packages, p)
					continue
				}
				c := &candidate{
					sampleID:       ss.SampleID,
					status:         ss.Status,
					env:            ss.Environment.Normalize(),
					packages:       []domain.PURL{p},
					declared:       parsePURLs(ss.Packages),
					symbols:        append([]string(nil), ss.Symbols...),
					verifications:  variantsFromShard(ss.Verifications),
					contractStages: ss.ContractStages,
				}
				for _, variant := range c.verifications {
					for _, verified := range variant.packages {
						c.packages = appendPURL(c.packages, verified)
					}
				}
				if ss.Goal != "" {
					// case.Packages must be what the sample declares. Older
					// shards do not carry it, and filling it from the shard
					// key put a version the sample never used into the field
					// a client parses.
					c.caseObj = &domain.Case{
						SchemaVersion: 1, Kind: "HOW", Goal: ss.Goal,
						Packages: ss.Packages,
						Symbols:  append([]string(nil), ss.Symbols...),
						// What the caller probably believes, which is the
						// half of the answer a goal sentence cannot carry.
						Believed: ss.Believed,
						// What the contract actually proved. Older shards do
						// not carry it and simply have none.
						Contract: ss.Contract,
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
				if c := cands[sampleIDFromDocID(h.DocID)]; c != nil {
					c.ftsScore = h.Score / max
				}
			}
		}
	}
	return cands, evidence, nil
}

// scoreCandidate produces one SearchResult plus its fused score.
func (e Engine) scoreCandidate(ctx context.Context, req domain.SearchRequest, reqEnv domain.EnvironmentFingerprint, reqPkgs []domain.PURL, c *candidate, evidence map[string]*pkgEvidence, now time.Time) (domain.SearchResult, float64) {
	receipts, _ := e.DB.ReceiptsForSample(ctx, c.sampleID)
	askedPkgs := reqPkgs
	fromTree := false
	if len(askedPkgs) == 0 {
		// Nothing was asked about by name, so the caller's dependency tree
		// gets to rank — but a tree that contains none of the sample's
		// packages is silence, not a mismatch, so relNone drops back to
		// unspecified rather than grading the answer REFERENCE_ONLY.
		askedPkgs = parsePURLs(req.ProjectPackages)
	}
	environmentContext := req.EnvironmentIsContext()
	selection := selectGradeVariantWithProvenance(c, receipts, askedPkgs, reqEnv, environmentContext)
	rel, reqP, samP := selection.rel, selection.reqP, selection.samP
	if len(reqPkgs) == 0 {
		if len(askedPkgs) > 0 && rel > relNone {
			fromTree = true
		} else {
			rel = relUnspecified
		}
	}

	// One entry per PACKAGE, however many of its versions the candidate
	// declares. The key drops the version, so a sample declaring
	// axios@1.12.0 and axios@1.13.0 collected the same symbol entries
	// twice — and every observation count built from them came out
	// doubled, in the numbers a caller reads as measurements.
	var syms []shardSymbolEntry
	seenPkg := map[string]bool{}
	relevancePackages := append([]domain.PURL(nil), c.packages...)
	for _, claim := range selection.claims {
		relevancePackages = appendPURL(relevancePackages, claim.purl)
	}
	for _, p := range relevancePackages {
		k := pkgKey(p)
		if seenPkg[k] {
			continue
		}
		seenPkg[k] = true
		if pe := evidence[k]; pe != nil {
			syms = append(syms, pe.symbols...)
		}
	}
	// Steps 1–5: relevance fusion (exact tokens outrank lexical match).
	base := relWeight(rel)
	if fromTree {
		base *= treeRelevanceFactor
	}
	declaredSymbolHit := len(matchingSymbols(req.Symbols, c)) > 0
	contextSymbolHit := len(matchingSymbols(req.ContextSymbols, c)) > 0
	if declaredSymbolHit || contextSymbolHit {
		base += weightSymbol
	}
	_, codeHit := errorHits(req, syms)
	fingerprintPackages := candidateFingerprintPackages(req, selection.claims, evidence, c)
	fpHit := len(fingerprintPackages) > 0
	if fpHit {
		base += weightErrorFingerprint
	}
	if codeHit {
		base += weightErrorCode
	}
	// Relevance to the question actually asked, as opposed to overlap with
	// whatever happens to be in the caller's dependency tree.
	relevance := weightFTS*c.ftsScore + weightIntent*intentOverlap(req.Query, c)
	if named, _ := intentSignal(req.Query, c); named > 0 {
		relevance += weightNamedSubject
	}
	base += relevance

	// Steps 6+9: environment gate, execution-context axis, known failures.
	samEco := ecosystemOf(samP, reqEnv, c)
	askedEnv := envAskedAbout(reqEnv, samEco, environmentContext)
	dims := compareEnv(askedEnv, selection.env, samEco, environmentContext)
	cd := compareContext(askedEnv, selection.env)
	matched := matchingFailures(reqEnv, syms)
	elevated := elevatedInRequestEnv(req, matched)
	grade, adaptations := buildGrade(rel, dims, cd, elevated)
	exact, different := buildDelta(rel, reqP, samP, dims, cd)

	summary := buildEvidence(syms, selection.receipts, selection.stages, now)

	// Steps 7–8: verification-strength rerank + recency decay.
	lvl := selection.level
	score := base * envFit(grade, cd) * recency(c, now) * strengthBoost(lvl)

	// A sample must be about the question asked. An exact package match
	// alone scores 0.45 before any multiplier — past missThreshold on its
	// own — so every verified sample for a package in the caller's
	// dependency tree used to come back as a confident hit no matter what
	// was asked: "how to bake a chocolate cake" returned the google/uuid
	// sample as MATCH: EXACT because uuid was an indirect dependency.
	//
	// The test is topic words in common, not the fused relevance score:
	// c.ftsScore is normalized against the best hit of this query alone,
	// so a single weak lexical match normalizes to a perfect 1.0 and
	// carries the full FTS weight. Sharing no content word with the goal
	// is the unambiguous signal, and this is the wrong HIT the project
	// rates worse than a MISS (goal.md §3.8) — so it scores zero rather
	// than merely ranking low.
	//
	// Matching the caller's actual error is exempt: a fingerprint or code
	// hit is direct evidence of relevance whatever the prose says.
	if len(req.Symbols) > 0 && !declaredSymbolHit {
		score = 0
	}
	if !fpHit && !codeHit && !declaredSymbolHit && !aboutTheSameThing(req.Query, c) {
		score = 0
	}
	// A package-less request searches the global candidate pool. An explicit
	// ecosystem is a caller constraint there, not ambient project context;
	// only a full declared-symbol match is strong enough to cross it.
	if len(reqPkgs) == 0 && !environmentContext && reqEnv.Ecosystem != "" &&
		!declaredSymbolHit && !candidateSupportsEcosystem(c, reqEnv.Ecosystem) {
		score = 0
	}

	// A caller who NAMED packages and got a sample about none of them has
	// not been answered. relNone was graded REFERENCE_ONLY and returned, so
	// "parse a large CSV lazily without loading it into memory" with
	// pkg:pypi/polars named came back with an Elixir nimble_csv sample: a
	// different package, a different language, honestly labelled and still
	// not an answer.
	//
	// Worse than the noise: a returned result is not a miss, so the
	// question was never recorded as wanted and nobody learned that a
	// polars sample was needed. The wrong answer displaced the demand
	// signal that would have fixed it.
	//
	// The server-side search already drops these candidates outright. This
	// makes the two implementations agree.
	if rel == relNone {
		score = 0
	}

	exactFailureMatched := fpHit && selectedContractPassed(selection, fingerprintPackages) && candidateHasContract(c)
	res := domain.SearchResult{
		Grade:               grade,
		Confidence:          summary.Confidence,
		Score:               score,
		Case:                c.caseObj,
		SampleID:            c.sampleID,
		SampleStatus:        c.status,
		ExactFailureMatched: exactFailureMatched,
		Exact:               exact,
		Different:           different,
		Adaptation:          adaptations,
		Evidence:            summary,
		KnownFailures:       knownFailures(matched),
	}
	return res, score
}

// buildEvidence fills the honest numbers behind a result. PROJECT_*
// observation counts and contract evidence live in SEPARATE fields and are
// never summed together (goal.md §3.5, docs/execution-context.md §6);
// the C7 confidence formula weighs the classes, it does not conflate them.
func buildEvidence(syms []shardSymbolEntry, receipts []domain.VerificationReceipt, shardStages map[string]string, now time.Time) domain.EvidenceSummary {
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
		pass := int64(st.PassRate*float64(st.ObservationCount) + 0.5)
		fail := st.ObservationCount - pass
		if pass > 0 {
			evSamples = append(evSamples, compatibility.Sample{
				Class: domain.ClassUsageObservation, Result: domain.ResultPass, Count: pass})
		}
		if fail > 0 {
			evSamples = append(evSamples, compatibility.Sample{
				Class: domain.ClassUsageObservation, Result: domain.ResultFail, Count: fail})
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
			Class: domain.ClassSampleVerification, Result: res, Count: 1})
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
	if len(receipts) == 0 && shardStages["contract"] == "PASS" {
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

// matchesAnyFingerprint reports whether a stored fingerprint is one of the
// forms the caller's error could have been recorded as.
func matchesAnyFingerprint(req domain.SearchRequest, stored string) bool {
	if req.ErrorFingerprint != "" && stored == req.ErrorFingerprint {
		return true
	}
	for _, fp := range req.ErrorFingerprints {
		if fp != "" && stored == fp {
			return true
		}
	}
	return false
}

// errorHits reports exact error-fingerprint / error-code hits in the shard
// failure lists of the candidate's packages (§11.3 step 3).
func errorHits(req domain.SearchRequest, syms []shardSymbolEntry) (fp, code bool) {
	for _, s := range syms {
		for _, f := range s.Failures {
			if f.Fingerprint != "" && matchesAnyFingerprint(req, f.Fingerprint) {
				fp = true
			}
			if req.ErrorCode != "" && f.ErrorCode != "" && strings.EqualFold(f.ErrorCode, req.ErrorCode) {
				code = true
			}
		}
	}
	return fp, code
}

// candidateFingerprintPackages binds every fingerprint match to the package
// whose failure evidence contained it. Flattening all package symbols into
// one slice loses that identity and lets a PASS for another dependency earn
// an exact detour.
func candidateFingerprintPackages(req domain.SearchRequest, claims []packageClaim,
	evidence map[string]*pkgEvidence, c *candidate) []domain.PURL {
	explicit := make(map[string]bool, len(req.Packages))
	for _, raw := range req.Packages {
		if p, err := domain.ParsePURL(raw); err == nil && p.String() == raw {
			explicit[p.String()] = true
		}
	}
	var matched []domain.PURL
	seen := map[string]bool{}
	for _, claim := range claims {
		p := claim.purl
		// Project/package claims widen discovery only when the caller omitted
		// packages. Once req.Packages is explicit, a failure must belong to
		// that exact canonical PURL (including version). Otherwise an axios-
		// only request can become an exact detour through a lodash claim from
		// the same multi-package sample.
		if len(req.Packages) > 0 && !explicit[p.String()] {
			continue
		}
		key := pkgKey(p)
		if seen[key] {
			continue
		}
		seen[key] = true
		pe := evidence[key]
		if pe == nil {
			continue
		}
		found := false
		for _, s := range pe.symbols {
			if !candidateDeclaresSymbol(c, s.Family) {
				continue
			}
			for _, f := range s.Failures {
				if f.Fingerprint != "" && matchesAnyFingerprint(req, f.Fingerprint) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			matched = append(matched, p)
		}
	}
	return matched
}

func candidateDeclaresSymbol(c *candidate, symbol string) bool {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" || c == nil {
		return false
	}
	for _, declared := range c.symbols {
		declared = strings.TrimSpace(declared)
		if declared != "" && strings.EqualFold(declared, symbol) {
			return true
		}
	}
	if c.caseObj != nil {
		for _, declared := range c.caseObj.Symbols {
			declared = strings.TrimSpace(declared)
			if declared != "" && strings.EqualFold(declared, symbol) {
				return true
			}
		}
	}
	return false
}

func candidateHasContract(c *candidate) bool {
	if c == nil || c.caseObj == nil {
		return false
	}
	for _, line := range c.caseObj.Contract {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func selectedContractPassed(selection gradeSelection, failurePackages []domain.PURL) bool {
	if selection.stages["contract"] != string(domain.ResultPass) || len(selection.resolved) == 0 {
		return false
	}
	for _, resolved := range selection.resolved {
		for _, failed := range failurePackages {
			if samePackageIdentity(resolved, failed) {
				return true
			}
		}
	}
	return false
}

func samePackageIdentity(a, b domain.PURL) bool {
	return strings.EqualFold(a.Ecosystem, b.Ecosystem) && strings.EqualFold(a.Name, b.Name)
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
		if f.Fingerprint != "" && matchesAnyFingerprint(req, f.Fingerprint) {
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
			// The release LINE, for the same reason the grader uses it: go
			// 1.9 and go 1.26 are seven years apart and both major "1", so
			// matching by major reported a failure seen only on 1.26 as
			// something that would bite a caller on 1.9 -- and a known
			// failure demotes the sample that would have helped them.
			if ver != "" && releaseLineOf(req.Runtime, ver) !=
				releaseLineOf(req.Runtime, req.RuntimeVersion) {
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
// stopWords are ignored when judging what a question is ABOUT. Counting
// them let "how to bake a chocolate cake" overlap the goal "…validate a
// UUID in Go…" on the word "a", which is not a topic in common.
// contentTokens reduces text to the words that carry its topic: lowercase,
// split on every non-alphanumeric boundary, no stop words, nothing shorter
// than three letters.
//
// Splitting on punctuation is what makes package names usable as topic
// words: "react-dom" has to yield "react", and "@scope/pkg" has to yield
// "scope" and "pkg", or a question about react fails to match the sample
// that uses react-dom.
// genericNameWords are the words that appear inside package names without
// identifying anything: the language or runtime the package is for, and the
// structural nouns half a registry uses.
//
// Splitting a name into all of its words made every one of them a STRONG
// identifier — the class of token that means "the question named this
// library" — and one strong token opens the relevance gate by itself. So
// python-dateutil was named by the word "python", and answered a question
// about protobuf on Alpine with a sample about parsing ambiguous dates,
// graded COMPATIBLE at 0.79. The same hole sits under go-*, node-*, rust-*,
// java-*, php-* and every *-sdk, *-client and *-utils in every registry.
// containsWord reports whether word appears in s delimited by something
// other than a letter or digit. A plain substring test is far too loose for
// the short names this exists to catch: a package named "go" would be named
// by any question mentioning mongodb, django or google.
func containsWord(s, word string) bool {
	return searchrelevance.ContainsIdentifier(s, word)
}

// nameTokens returns the parts of a package name that actually identify it.
//
// The whole name is kept as one token too, so a package whose entire name
// is a generic word ("go", "core") is still nameable by it — there the word
// IS the identifier rather than a prefix.
func nameTokens(name string) []string {
	return searchrelevance.NameTokens(name)
}

func contentTokens(s string) []string {
	return searchrelevance.ContentTokens(s)
}

// sharedIntent reports how many content words the question has in common
// with what the sample is ABOUT: its goal, the symbols it demonstrates and
// the names of the packages it uses.
//
// Matching the goal sentence alone was too strict. "render a react
// component to an html string" shares no content word with the goal
// "Choose between renderToString and renderToStaticMarkup without breaking
// hydration", yet that is exactly the right sample — the word they have in
// common is the package name. Including symbols and package names keeps
// the gate closed on genuinely unrelated questions (a cake recipe shares
// nothing with axios, post, json or the sample's prose) while letting a
// question find a sample that words its goal differently.
func sharedIntent(query string, c *candidate) int {
	if c == nil || c.caseObj == nil {
		return 0
	}
	about := map[string]bool{}
	add := func(text string) {
		for _, t := range contentTokens(text) {
			about[t] = true
		}
	}
	add(c.caseObj.Goal)
	for _, sym := range c.caseObj.Symbols {
		add(sym)
	}
	for _, pkg := range c.packages {
		add(pkg.Name) // "react", "axios" — how people name the thing
	}
	shared := 0
	for _, t := range contentTokens(query) {
		if about[t] {
			shared++
		}
	}
	return shared
}

// aboutTheSameThing reports whether the question and the candidate share
// enough to be about one subject.
//
// What matters is the KIND of word shared, not how many. Sharing the
// package name or a symbol name is a specific identifier and settles it on
// its own: "render a react component to an html string" shares nothing
// with the goal "Choose between renderToString and renderToStaticMarkup
// without breaking hydration" except the word react, and that is exactly
// the right sample.
//
// Sharing only prose from the goal sentence is much weaker. Asked "validate
// a map with Ecto.Changeset without a Repo", a fresh install answered with
// a Go google/uuid sample: uuid sat in the caller's dependency tree, an
// exact package match alone scores past the miss threshold, and the single
// generic word "validate" appeared in both goals. Wrong ecosystem, wrong
// language, wrong question — presented as a match. So prose alone has to
// clear a higher bar, and a longer question, carrying more signal, is
// asked for more of it.
func aboutTheSameThing(query string, c *candidate) bool {
	strong, prose := intentSignal(query, c)
	if strong > 0 {
		return true
	}
	if len(contentTokens(query)) >= 4 {
		return prose >= 2
	}
	return prose >= 1
}

// intentSignal splits the overlap into identifier words (package names and
// symbols) and prose words (the goal sentence).
func intentSignal(query string, c *candidate) (strong, prose int) {
	if c == nil {
		return 0, 0
	}
	goal := ""
	var symbols []string
	if c.caseObj != nil {
		goal = c.caseObj.Goal
		symbols = append(symbols, c.caseObj.Symbols...)
	}
	symbols = append(symbols, c.symbols...)
	var names []string
	for _, pkg := range c.packages {
		names = append(names, pkg.Name)
	}
	return searchrelevance.Signal(query, goal, names, symbols)
}

func intentOverlap(query string, c *candidate) float64 {
	q := contentTokens(query)
	if len(q) == 0 {
		return 0
	}
	return float64(sharedIntent(query, c)) / float64(len(q))
}

// matchingSymbols returns the candidate's actual declared identities matched
// by the request.
func matchingSymbols(reqSyms []string, c *candidate) []string {
	var declared []string
	declared = append(declared, c.symbols...)
	if c.caseObj != nil {
		declared = append(declared, c.caseObj.Symbols...)
	}
	return searchrelevance.MatchedDeclaredSymbols(reqSyms, declared)
}

func candidateSupportsEcosystem(c *candidate, ecosystem string) bool {
	for _, p := range c.packages {
		if strings.EqualFold(p.Ecosystem, ecosystem) {
			return true
		}
	}
	return false
}

// ecosystemOf picks the ecosystem governing dimension sensitivity.
// envAskedAbout drops the request's ecosystem-scoped dimensions when the
// question is about a different ecosystem than the caller's project.
//
// `csx search` fills the request environment from the current directory,
// which is right for os, arch and libc and wrong for everything else. An
// agent is always inside SOME project, and the library it asks about is
// usually one it is about to add — so the request arrived saying runtime
// go, language go, packageManager gomod, and every Python, Rust and Elixir
// sample was then judged against a Go project and graded incompatible.
//
// Measured on a fresh install: from an empty directory three of four
// queries returned the right sample; run from inside a Go checkout the
// same three returned NO_SAFE_MATCH. The caller's project being written in
// Go says nothing about which Python they would run, and comparing those
// axes answers a question nobody asked.
//
// The host axes stay: os, arch, libc, virtualization and CI are true of
// the machine whatever the question is about, and they are exactly the
// dimensions a cross-ecosystem answer still needs to be honest about.
func envAskedAbout(req domain.EnvironmentFingerprint, samEcosystem string, inferred bool) domain.EnvironmentFingerprint {
	if samEcosystem == "" || req.Ecosystem == "" || strings.EqualFold(req.Ecosystem, samEcosystem) {
		return req
	}
	if !inferred {
		return req
	}
	// The ecosystem itself stays. Everything below is a dimension describing
	// how the CALLER's ecosystem runs, and comparing those against another
	// ecosystem's sample answers a question nobody asked — that is what this
	// function is for. But "which ecosystem" is not one of them: it is the
	// fact that makes a cross-ecosystem answer recognisable as one.
	//
	// Blanking it removed the only line that said so. From a pypi project a
	// Python import error returned an npm/jest sample graded COMPATIBLE, and
	// its delta named the OS and nothing else — the answer read as a near
	// fit. compareEnv now caps an inferred mismatch at ADAPTATION_REQUIRED
	// rather than forcing REFERENCE_ONLY, so this does not undo the fix
	// above it.
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

// recency is 1 for everything: samples do not rot.
//
// It applied a 90-day half-life to a sample's local age, which ranked by when
// it happened to be published rather than by how well it answered the
// question. A sample is about one pinned release and stays as true as the day
// it was verified. The function is kept so callers read as they did; there is
// nothing left to weigh.
func recency(*candidate, time.Time) float64 { return 1 }

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

type packageClaim struct {
	purl         domain.PURL
	versionKnown bool
}

type gradeSelection struct {
	claims   []packageClaim
	rel      pkgRel
	reqP     domain.PURL
	samP     domain.PURL
	env      domain.EnvironmentFingerprint
	stages   map[string]string
	receipts []domain.VerificationReceipt
	// resolved is the exact package set of the selected receipt/shard
	// verification variant. It is deliberately empty for aggregate legacy
	// contractStages and v1 receipts, which cannot bind a PASS to a package.
	resolved []domain.PURL
	level    int
	created  time.Time
}

// selectGradeVariant chooses one real receipt execution to grade. It never
// unions package versions from different receipts. Declared identities not
// present in that execution remain package-only claims, so resolving axios
// does not make a declared zod dependency disappear from search.
func selectGradeVariant(c *candidate, receipts []domain.VerificationReceipt, askedPkgs []domain.PURL,
	reqEnv domain.EnvironmentFingerprint) gradeSelection {
	return selectGradeVariantWithProvenance(c, receipts, askedPkgs, reqEnv, false)
}

func selectGradeVariantWithProvenance(c *candidate, receipts []domain.VerificationReceipt, askedPkgs []domain.PURL,
	reqEnv domain.EnvironmentFingerprint, environmentInferred bool) gradeSelection {
	variants := append([]verificationVariant(nil), c.verifications...)
	for i := range receipts {
		packages := verifiedPURLsFromReceipt(receipts[i])
		if len(packages) == 0 {
			continue
		}
		created, _ := time.Parse(time.RFC3339, receipts[i].CreatedAt)
		variants = append(variants, verificationVariant{
			packages: packages,
			env:      receipts[i].Environment.Normalize(),
			stages:   receipts[i].Stages,
			created:  created,
			receipt:  &receipts[i],
		})
	}

	if len(variants) == 0 {
		claims := unknownPackageClaims(c)
		rel, reqP, samP := packageRelationClaims(askedPkgs, claims)
		return gradeSelection{
			claims: claims, rel: rel, reqP: reqP, samP: samP,
			env: c.env, stages: c.contractStages, receipts: receipts,
			level: verificationLevel(c.status, c.contractStages, receipts),
		}
	}

	var best gradeSelection
	haveBest := false
	for _, variant := range variants {
		claims := packageClaimsForVariant(c, variant.packages)
		rel, reqP, samP := packageRelationClaims(askedPkgs, claims)
		matching := receiptsForPackageSet(receipts, variant.packages)
		level := variant.level
		if localLevel := verificationLevel("", variant.stages, matching); localLevel > level {
			level = localLevel
		}
		selection := gradeSelection{
			claims: claims, rel: rel, reqP: reqP, samP: samP,
			env: variant.env.Normalize(), stages: variant.stages,
			receipts: matching, resolved: append([]domain.PURL(nil), variant.packages...),
			level: level, created: variant.created,
		}
		if !haveBest || betterGradeSelection(selection, best, reqEnv, environmentInferred, c) {
			best, haveBest = selection, true
		}
	}
	return best
}

func betterGradeSelection(a, b gradeSelection, reqEnv domain.EnvironmentFingerprint,
	environmentInferred bool, c *candidate) bool {
	if a.rel != b.rel {
		return a.rel > b.rel
	}
	if ar, br := stageVerdictRank(a.stages), stageVerdictRank(b.stages); ar != br {
		return ar > br
	}
	if ar, br := selectionEnvironmentRank(a, reqEnv, environmentInferred, c),
		selectionEnvironmentRank(b, reqEnv, environmentInferred, c); ar != br {
		return ar > br
	}
	if a.level != b.level {
		return a.level > b.level
	}
	if !a.created.Equal(b.created) {
		return a.created.After(b.created)
	}
	return packageClaimKey(a.claims) < packageClaimKey(b.claims)
}

func stageVerdictRank(stages map[string]string) int {
	switch stages["contract"] {
	case string(domain.ResultPass):
		return 2
	case string(domain.ResultFail):
		return 0
	default:
		return 1
	}
}

func selectionEnvironmentRank(s gradeSelection, reqEnv domain.EnvironmentFingerprint,
	environmentInferred bool, c *candidate) int {
	ecosystem := ecosystemOf(s.samP, reqEnv, c)
	asked := envAskedAbout(reqEnv, ecosystem, environmentInferred)
	grade, _ := buildGrade(s.rel, compareEnv(asked, s.env, ecosystem, environmentInferred), compareContext(asked, s.env), false)
	switch grade {
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

func unknownPackageClaims(c *candidate) []packageClaim {
	return packageClaimsForVariant(c, nil)
}

func packageClaimsForVariant(c *candidate, verified []domain.PURL) []packageClaim {
	base := c.declared
	if len(base) == 0 {
		base = c.packages
	}
	var out []packageClaim
	index := map[string]int{}
	for _, p := range base {
		key := packageIdentityKey(p)
		if _, exists := index[key]; exists {
			continue
		}
		index[key] = len(out)
		out = append(out, packageClaim{purl: p})
	}
	for _, p := range verified {
		key := packageIdentityKey(p)
		if i, exists := index[key]; exists {
			out[i] = packageClaim{purl: p, versionKnown: true}
			continue
		}
		index[key] = len(out)
		out = append(out, packageClaim{purl: p, versionKnown: true})
	}
	return out
}

func packageIdentityKey(p domain.PURL) string {
	return strings.ToLower(p.Ecosystem) + "\x00" + strings.ToLower(p.Name)
}

func packageClaimKey(claims []packageClaim) string {
	parts := make([]string, 0, len(claims))
	for _, claim := range claims {
		known := "0"
		if claim.versionKnown {
			known = "1"
		}
		parts = append(parts, claim.purl.String()+"\x00"+known)
	}
	return strings.Join(parts, "\x01")
}

func packageRelationClaims(reqPkgs []domain.PURL, claims []packageClaim) (pkgRel, domain.PURL, domain.PURL) {
	var reqP, samP domain.PURL
	if len(claims) > 0 {
		samP = claims[0].purl
	}
	if len(reqPkgs) == 0 {
		return relUnspecified, reqP, samP
	}
	reqP = reqPkgs[0]
	best, found := relNone, false
	for _, rp := range reqPkgs {
		for _, claim := range claims {
			cp := claim.purl
			if rp.Ecosystem != cp.Ecosystem || !equalFoldName(rp.Name, cp.Name) {
				continue
			}
			r := relPackageOnly
			if claim.versionKnown && rp.Version != "" {
				switch {
				case rp.Version == cp.Version:
					r = relExactVersion
				case rp.MajorMinor() == cp.MajorMinor():
					r = relMajorMinor
				case rp.BreakingBucket() == cp.BreakingBucket():
					r = relMajor
				default:
					r = relMajorDiff
				}
			}
			if !found || r < best {
				best, reqP, samP, found = r, rp, cp, true
			}
		}
	}
	if !found {
		return relNone, reqP, samP
	}
	return best, reqP, samP
}

func variantsFromShard(entries []shardVerificationEntry) []verificationVariant {
	var out []verificationVariant
	for _, entry := range entries {
		if entry.Stages["resolve"] != string(domain.ResultPass) {
			continue
		}
		packages := parseVerifiedPURLs(entry.ResolvedPackages)
		if len(packages) == 0 {
			continue
		}
		level := entry.VerificationLevel
		if entry.Stages["contract"] != string(domain.ResultPass) {
			level = 0
		} else if level < 3 {
			level = 3
		} else if level > 4 {
			level = 4
		}
		created, _ := time.Parse(time.RFC3339, entry.CreatedAt)
		out = appendVerification(out, verificationVariant{
			packages: packages, env: entry.Environment.Normalize(), stages: entry.Stages,
			level: level, created: created,
		})
	}
	return out
}

// appendVerification adds a variant unless the list already holds the same
// one, keeping the higher verification level when it does.
//
// The key is remembered on the variant instead of being recomputed. It was
// derived for every element already in the list on every single append, and
// deriving one means normalising an environment, hashing it, and rendering
// canonical JSON — so a candidate with n variants paid that n^2/2 times, on
// every query, for the 5,347 shard sample entries this walks.
func appendVerification(list []verificationVariant, add verificationVariant) []verificationVariant {
	if add.key == "" {
		add.key = verificationKey(add)
	}
	for i := range list {
		if list[i].key == "" {
			list[i].key = verificationKey(list[i])
		}
		if list[i].key == add.key {
			if add.level > list[i].level {
				list[i].level = add.level
			}
			return list
		}
	}
	return append(list, add)
}

func verificationKey(v verificationVariant) string {
	return receiptPackageSetKey(v.packages) + "\x00" + v.env.Normalize().Hash() + "\x00" +
		string(domain.MustCanonicalJSON(v.stages)) + "\x00" + v.created.UTC().Format(time.RFC3339Nano)
}

func receiptPackageSetKey(packages []domain.PURL) string {
	parts := make([]string, 0, len(packages))
	for _, p := range packages {
		parts = append(parts, p.String())
	}
	return strings.Join(parts, "\x00")
}

func receiptsForPackageSet(receipts []domain.VerificationReceipt, packages []domain.PURL) []domain.VerificationReceipt {
	want := receiptPackageSetKey(packages)
	var out []domain.VerificationReceipt
	for _, receipt := range receipts {
		resolved := verifiedPURLsFromReceipt(receipt)
		if len(resolved) > 0 && receiptPackageSetKey(resolved) == want {
			out = append(out, receipt)
		}
	}
	return out
}

func verifiedPURLsFromReceipt(receipt domain.VerificationReceipt) []domain.PURL {
	if receipt.SchemaVersion != 2 || receipt.Stages["resolve"] != string(domain.ResultPass) ||
		len(receipt.ResolvedPackages) == 0 {
		return nil
	}
	return parseVerifiedPURLs(receipt.ResolvedPackages)
}

// parseVerifiedPURLs fails the entire claim closed. Partially accepting a
// malformed list would let one convenient purl establish a version while
// discarding the evidence that the signed list itself was invalid.
func parseVerifiedPURLs(raw []string) []domain.PURL {
	out := make([]domain.PURL, 0, len(raw))
	for i, value := range raw {
		p, err := domain.ParsePURL(value)
		if err != nil || p.String() != value || !domain.ConcreteResolvedVersion(p.Version) ||
			(i > 0 && value <= raw[i-1]) {
			return nil
		}
		out = append(out, p)
	}
	return out
}

// appendPURL adds p unless an equal purl is already present.
func appendPURL(list []domain.PURL, p domain.PURL) []domain.PURL {
	for _, existing := range list {
		if existing.Ecosystem == p.Ecosystem &&
			strings.EqualFold(existing.Name, p.Name) &&
			existing.Version == p.Version {
			return list
		}
	}
	return append(list, p)
}

// statusRank orders sample statuses by how much verification they assert,
// so the strongest one anybody knows about wins.
func statusRank(status string) int {
	switch status {
	case "STABLE":
		return 5
	case "MATRIX_PASS":
		return 4
	case "CROSS_PASS":
		return 3
	case "PUBLISHED":
		return 2
	case "LOCAL_PASS":
		return 1
	}
	return 0
}
