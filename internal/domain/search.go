package domain

// SearchRequest is the wire query shape shared by the local daemon, MCP
// tools, and the server /v1/search API (goal.md §11.1).
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
	ProjectPackages  []string               `json:"projectPackages,omitempty"`
	Symbols          []string               `json:"symbols,omitempty"`
	Environment      EnvironmentFingerprint `json:"environment"`
	ErrorFingerprint string                 `json:"errorFingerprint,omitempty"`
	// ErrorFingerprints is the same error hashed for every stage it could
	// have been recorded under. A caller pasting a build log knows the
	// error, not the stage it was observed at.
	ErrorFingerprints []string `json:"errorFingerprints,omitempty"`
	ErrorCode         string   `json:"errorCode,omitempty"`
	Limit             int      `json:"limit,omitempty"` // default 3
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
	Grade         MatchGrade      `json:"match"`
	Confidence    string          `json:"confidence"`
	Score         float64         `json:"score"`
	Case          *Case           `json:"case,omitempty"`
	SampleID      string          `json:"sampleId,omitempty"`
	SampleStatus  string          `json:"sampleStatus,omitempty"`
	Exact         []string        `json:"exact"`
	Different     []string        `json:"different"`
	Adaptation    []string        `json:"adaptationNeeded"`
	Evidence      EvidenceSummary `json:"evidence"`
	KnownFailures []KnownFailure  `json:"knownFailures,omitempty"`
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
}
