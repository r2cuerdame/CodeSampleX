package domain

// SearchProvenance records whether search input was declared by the caller or
// inferred from ambient project/error context. The empty value is deliberate:
// it is the third state needed to distinguish legacy JSON from an explicit
// declaration at a protocol boundary.
type SearchProvenance string

const (
	SearchProvenanceExplicit SearchProvenance = "explicit"
	SearchProvenanceContext  SearchProvenance = "context"
)

// SearchRequest is the in-process superset shared by the local daemon, MCP,
// and public search. Public v1 uses only its frozen original fields; v2 owns
// provenance/context additions (goal.md §11.1).
type SearchRequest struct {
	SchemaVersion int      `json:"schemaVersion"`
	Query         string   `json:"query"`
	Packages      []string `json:"packages,omitempty"`
	// ProjectPackages is the caller's dependency tree, filled in
	// automatically rather than named by the caller. It ranks: a sample
	// about something already in the tree is more likely to be the one
	// wanted. It must never GRADE, because the caller did not ask about it.
	//
	// Splitting it out fixed the worst hit-rate defect the project has had.
	// `csx search` fills packages from the lockfile, so asking "freeze the
	// clock in a python test" inside a Go checkout arrived carrying two
	// unrelated Go purls; the grader read that as "you asked about these
	// packages and this sample is about none of them", returned
	// REFERENCE_ONLY, and the multiplier put the correct freezegun sample
	// at 0.105 against a 0.25 miss threshold. From an empty directory the
	// same query scored 0.27 and answered correctly.
	//
	// An agent is always inside SOME project, and the library it asks about
	// is usually one it is about to add — so the dominant real case was the
	// broken one.
	ProjectPackages []string `json:"projectPackages,omitempty"`
	// ContextSymbols are scanner/error-derived context. They may improve ranking,
	// but unlike Symbols they were not declared by the caller and therefore
	// must never exclude a candidate.
	ContextSymbols []string `json:"contextSymbols,omitempty"`
	Symbols        []string `json:"symbols,omitempty"`
	// SymbolProvenance is explicit on public/MCP requests and context for
	// scanner-derived local requests. A new CLI also populates Symbols for an
	// old daemon; the new daemon uses this marker to avoid treating that legacy
	// compatibility copy as an exclusion.
	SymbolProvenance SearchProvenance       `json:"symbolProvenance,omitempty"`
	Environment      EnvironmentFingerprint `json:"environment"`
	// EnvironmentProvenance records whether ecosystem-scoped dimensions came
	// from the caller or the current project. Only context dimensions may be
	// softened when the candidate belongs to another ecosystem.
	EnvironmentProvenance SearchProvenance `json:"environmentProvenance,omitempty"`
	ErrorFingerprint      string           `json:"errorFingerprint,omitempty"`
	// ErrorFingerprints is the same error hashed for every stage it could
	// have been recorded under. A caller pasting a build log knows the
	// error, not the stage it was observed at.
	ErrorFingerprints []string `json:"errorFingerprints,omitempty"`
	ErrorCode         string   `json:"errorCode,omitempty"`
	Limit             int      `json:"limit,omitempty"` // default 3
}

// EnvironmentIsContext is fail-closed: absent/unknown provenance is explicit.
// The local daemon is the only boundary allowed to translate a proven legacy
// CLI request into context provenance.
func (r SearchRequest) EnvironmentIsContext() bool {
	return r.EnvironmentProvenance == SearchProvenanceContext
}

// EvidenceSummary carries the honest numbers behind a result (goal.md §11.5).
// Compile observations are co-occurrence evidence, never execution proof.
type EvidenceSummary struct {
	ProjectCompileObservations int64    `json:"projectCompileObservations"`
	CleanBuilds                int64    `json:"cleanBuilds"`
	ContractPasses             int64    `json:"contractPasses"`
	IndependentCrossPeers      int64    `json:"independentCrossPeers"`
	UniquePeerBuckets          int64    `json:"uniquePeerBuckets"`
	PassRate                   float64  `json:"passRate"`
	Confidence                 string   `json:"confidence"` // HIGH | MEDIUM | LOW
	ElevatedFailures           []string `json:"elevatedFailures,omitempty"`
	LastSeen                   string   `json:"lastSeen,omitempty"`
}

// KnownFailure names a failure cluster relevant to the requesting env.
type KnownFailure struct {
	ErrorCode   string              `json:"errorCode,omitempty"`
	Fingerprint string              `json:"fingerprint,omitempty"`
	Count       int64               `json:"count"`
	EnvSummary  map[string]string   `json:"envSummary,omitempty"`
	Hypotheses  []FailureHypothesis `json:"hypotheses,omitempty"`
}

// SearchResult is one ranked answer with its environment delta
// (goal.md §11.5): the LLM reasons over Different/Adaptation, not the
// whole problem.
type SearchResult struct {
	Grade        MatchGrade `json:"match"`
	Confidence   string     `json:"confidence"`
	Score        float64    `json:"score"`
	Case         *Case      `json:"case,omitempty"`
	SampleID     string     `json:"sampleId,omitempty"`
	SampleStatus string     `json:"sampleStatus,omitempty"`
	// ExactFailureMatched is true only when one of the caller's sanitized
	// error fingerprints equalled a recorded failure for a nonempty symbol
	// declared by this candidate, and the selected nonempty contract passed.
	// An error code, package match, different-symbol cluster, or semantic hit
	// must never set it.
	ExactFailureMatched bool            `json:"exactFailureMatched"`
	Exact               []string        `json:"exact"`
	Different           []string        `json:"different"`
	Adaptation          []string        `json:"adaptationNeeded"`
	Evidence            EvidenceSummary `json:"evidence"`
	KnownFailures       []KnownFailure  `json:"knownFailures,omitempty"`
}

// ObservedEnvironment is the closed set of environment dimensions a relayed
// observation may carry. It is a fixed projection rather than the whole
// fingerprint: the full record has ~25 fields including distro, libc version,
// container runtime, virtualization, frameworks and CI, and shipping those
// against a stable anonymous id is a fingerprint, not a measurement.
type ObservedEnvironment struct {
	OS             string `json:"os,omitempty"`
	Arch           string `json:"arch,omitempty"`
	Runtime        string `json:"runtime,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
	PackageManager string `json:"packageManager,omitempty"`
	Libc           string `json:"libc,omitempty"`
	Context        string `json:"context,omitempty"`
}

// ObservedCell is one (environment, stage) tally exactly as recorded. It is
// counts and coordinates — no grade, no confidence, no score, no sample.
type ObservedCell struct {
	Environment ObservedEnvironment `json:"environment"`
	Stage       string              `json:"stage"`
	Pass        int64               `json:"pass"`
	Fail        int64               `json:"fail"`
	// Reporters is the peak number of distinct reporting machines in a single
	// epoch, never a sum across days: one machine building all afternoon is
	// one data point, and summing would let a single caller manufacture
	// volume.
	Reporters int    `json:"reporters"`
	LastSeen  string `json:"lastSeen,omitempty"`
}

// ObservedError is one recorded failure signature. ErrorCode is a public
// identifier such as ERR_REQUIRE_ESM; no error text ever travels.
type ObservedError struct {
	Stage       string            `json:"stage"`
	ErrorCode   string            `json:"errorCode,omitempty"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Count       int64             `json:"count"`
	Environment map[string]string `json:"environment,omitempty"`
	// Reporters and Projects say how widespread the failure is. Count alone
	// is misleading: one machine building all afternoon reports thousands of
	// occurrences and proves nothing about anyone else.
	Reporters int `json:"reporters,omitempty"`
	Projects  int `json:"projects,omitempty"`
}

// ObservedReports is what the network already recorded about a coordinate
// when it has no verified sample to offer. It is RELAYED, never asserted:
// the project stands behind the samples it ran and the findings it detected,
// and these are neither. The grade stays NO_SAFE_MATCH — nothing here has
// been proven for the caller's case — and this payload is the difference
// between saying so with an empty hand and saying so while handing over
// everything already known.
type ObservedReports struct {
	PURL   string          `json:"purl"`
	Symbol string          `json:"symbol,omitempty"`
	Cells  []ObservedCell  `json:"cells,omitempty"`
	Errors []ObservedError `json:"errors,omitempty"`
	// Basis is always ObservedBasis. It is stated on the wire so a reader
	// cannot mistake the payload for verification by omission.
	Basis string `json:"basis"`
	// Note restates the limit in words for a model that skims structure.
	Note string `json:"note"`
}

const (
	// ObservedBasis is the only basis an ObservedReports may claim.
	ObservedBasis = "observed"
	// ObservedNote travels with every relay.
	ObservedNote = "Reported by machines that ran this, not verified by this project. " +
		"No sample was proven for your case; these are the recorded runs, as recorded."
)

// VerifiedOffer reports whether this result is safe to offer as already
// verified in the caller's environment. A contract PASS is necessary but
// not sufficient: adaptation, any disclosed difference, and reference-only
// grades all require the caller to verify again.
func (r SearchResult) VerifiedOffer() bool {
	if r.Evidence.ContractPasses <= 0 || len(r.Different) > 0 || len(r.Adaptation) > 0 {
		return false
	}
	return r.Grade == GradeExact || r.Grade == GradeCompatible
}

// ConfidenceReason explains, in one clause, what this result's confidence
// level is actually about.
//
// Confidence is a statement about INDEPENDENCE: MEDIUM needs two separate
// machines to have run the thing, HIGH needs three plus volume. On a young
// network almost nothing has that, so almost every answer reads
// "CONFIDENCE: LOW" -- next to a sample whose contract passed in a pinned
// container with the network off. A reader takes that as "this answer is
// probably wrong", which is not what it says and not what the evidence
// shows. Naming the reason costs one clause and stops the label from
// arguing against the evidence printed beside it.
func (r SearchResult) ConfidenceReason() string {
	e := r.Evidence
	independence := e.IndependentCrossPeers
	if e.UniquePeerBuckets > independence {
		independence = e.UniquePeerBuckets
	}
	switch {
	case r.Confidence == "HIGH":
		return ""
	case independence >= 2:
		return "" // the label is already carrying its own weight
	case e.ContractPasses > 0 && independence <= 1:
		return "its contract passed here, but only one machine has run it; " +
			"independent runs are what raises this"
	case e.ProjectCompileObservations == 0 && e.ContractPasses == 0:
		return "nothing has been observed or verified for this yet"
	default:
		return "only one machine has reported on this so far"
	}
}

// SearchResponse: Miss=true means NO_SAFE_MATCH — deliberately better than
// a wrong HIT (goal.md §3.8).
type SearchResponse struct {
	SchemaVersion int            `json:"schemaVersion"`
	Results       []SearchResult `json:"results"`
	Miss          bool           `json:"miss"`
	// Observed rides on a MISS and only on a miss. It hangs off the response
	// rather than off a result so it can never be read as a property of a
	// sample, and so the grade path never has it in scope.
	Observed *ObservedReports `json:"observed,omitempty"`
}
