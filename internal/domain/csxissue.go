package domain

import (
	"errors"
	"strings"
)

// A CSX issue report is the OTHER half of the feedback channel, and it is
// deliberately a different thing from an anomaly report.
//
// An anomaly says "this package behaves differently here than you said" — a
// fact about the world, settled by running the world again. This says "this
// product behaved wrongly" — a fact about US: an answer that hid the failure
// the user was actually looking at, a recommendation from an ecosystem the
// question never mentioned, a tool contract that made a model do the wrong
// thing. Nothing in a container can settle that.
//
// So the two share ingest, redaction and dedupe, and share nothing after it:
// separate verdicts, separate queue, separate table. Mixing them would put
// product defects into the compatibility graph, which is the one place they
// must never reach.
//
// It is conservative on purpose. There is no agent instruction to call this
// on every failure, no automatic ticket, and no target for how many reports
// a healthy week has. Zero is a fine number.

// CSXIssueReportSchemaVersion is the only accepted wire version.
const CSXIssueReportSchemaVersion = 1

// Surfaces a report can be about.
const (
	CSXSurfaceMCP      = "mcp"
	CSXSurfaceServer   = "server"
	CSXSurfaceWeb      = "web"
	CSXSurfaceVerifier = "verifier"
	CSXSurfaceFarm     = "farm"
)

// CSXSurfaces lists every accepted affectedSurface.
func CSXSurfaces() []string {
	return []string{CSXSurfaceMCP, CSXSurfaceServer, CSXSurfaceWeb, CSXSurfaceVerifier, CSXSurfaceFarm}
}

// Issue kinds. A closed vocabulary, because it is what makes two reports of
// one defect one report — and because "it seemed off" has no entry here,
// which is the point.
const (
	// CSXIssueAnswerMasksFailure: a CSX answer displaced or hid the failure
	// the caller was actually looking at. This is the R2C-51 shape.
	CSXIssueAnswerMasksFailure = "answer-masks-original-failure"
	// CSXIssueIrrelevantRecommendation: a returned result is unrelated to
	// what was asked — a different ecosystem, a different problem.
	CSXIssueIrrelevantRecommendation = "irrelevant-recommendation"
	// CSXIssueToolContractMisleads: an MCP tool's schema, description or
	// response shape makes a model behave wrongly.
	CSXIssueToolContractMisleads = "tool-contract-misleads"
	// CSXIssueNondeterministicResponse: the same input produces responses
	// that break the response contract inconsistently.
	CSXIssueNondeterministicResponse = "nondeterministic-response"
	// CSXIssueBrokenInternalReference: sample/evidence/dependency/finding
	// reference each other and one side does not exist.
	CSXIssueBrokenInternalReference = "broken-internal-reference"
	// CSXIssueRuntimeBehavior: timeouts, retries, loops, duplicate calls —
	// how the product behaves rather than what it answers.
	CSXIssueRuntimeBehavior = "runtime-behavior"
)

// CSXIssueKinds lists every accepted issueKind.
func CSXIssueKinds() []string {
	return []string{
		CSXIssueAnswerMasksFailure,
		CSXIssueIrrelevantRecommendation,
		CSXIssueToolContractMisleads,
		CSXIssueNondeterministicResponse,
		CSXIssueBrokenInternalReference,
		CSXIssueRuntimeBehavior,
	}
}

// Report states.
const (
	CSXIssueStatusTriage = "triage"
	// CSXIssueStatusReplayQueued: the report named an input this server can
	// legitimately re-run, and a replay is pending.
	CSXIssueStatusReplayQueued = "replay-queued"
	// CSXIssueStatusNoReplayLane: nothing here can re-run it automatically,
	// and the reason is returned to the caller and shown to an operator. It
	// is NOT hidden as permanently pending.
	CSXIssueStatusNoReplayLane = "no-replay-lane"
	CSXIssueStatusResolved     = "resolved"
)

// Verdicts. Deliberately NOT the anomaly vocabulary: a product defect is not
// a compatibility boundary and must never be promotable to one.
const (
	CSXIssueVerdictDefect               = "confirmed-csx-defect"
	CSXIssueVerdictExpectedBehavior     = "expected-behavior"
	CSXIssueVerdictClientDifference     = "client-difference"
	CSXIssueVerdictNotReproducible      = "not-reproducible"
	CSXIssueVerdictInsufficientEvidence = "insufficient-evidence"
	CSXIssueVerdictDuplicate            = "duplicate"
)

// CSXIssueVerdictConfirmed reports whether a verdict may reach the repair
// queue. It is the ONLY gate: everything else is a closed report.
func CSXIssueVerdictConfirmed(verdict string) bool {
	return verdict == CSXIssueVerdictDefect
}

// CSXIssuePublicInput is the part of a request that was ALREADY PUBLIC and
// can therefore be sent and re-run.
//
// Note what has no field here: the caller's question. There is no `query`,
// no prompt, no free-text input shape — not as a matter of policy layered on
// top, but because the struct has nowhere to put one. That is the whole
// reason the replay lane is narrow, and it is worth stating plainly: for the
// defect class this channel most wants to catch — a search that answered the
// wrong question — the input that would have to be replayed IS the user's
// prompt, and this network never receives it. A stable request fingerprint
// can travel, and a fingerprint cannot be re-run.
type CSXIssuePublicInput struct {
	// Endpoint is the public read route the request went to.
	Endpoint string `json:"endpoint"`
	// Packages are public purls the request named.
	Packages []string `json:"packages,omitempty"`
	Symbols  []string `json:"symbols,omitempty"`
	// Environment is the fingerprint the request was graded against.
	Environment EnvironmentFingerprint `json:"environment,omitempty"`
}

// CSXIssueReport is the wire document.
type CSXIssueReport struct {
	SchemaVersion   int    `json:"schemaVersion"`
	AffectedSurface string `json:"affectedSurface"`
	IssueKind       string `json:"issueKind"`
	// Component is the tool or endpoint the report is about:
	// "search_known_solution", "/v2/search", "csx worker".
	Component string `json:"component"`

	// RequestFingerprint is a stable, non-identifying id for the request.
	// It is what makes two occurrences of one defect one row.
	RequestFingerprint string `json:"requestFingerprint,omitempty"`
	// PublicInput is present only when the request can honestly be restated
	// from public coordinates alone.
	PublicInput *CSXIssuePublicInput `json:"publicInput,omitempty"`

	// ActualBehavior and ExpectedBehavior are short sanitized statements.
	// Both are required and they must differ: a report where they agree is
	// describing something working.
	ActualBehavior   string `json:"actualBehavior"`
	ExpectedBehavior string `json:"expectedBehavior"`

	Reproducible string `json:"reproducible"` // yes | no | unknown
	Confidence   string `json:"confidence"`   // low | medium | high

	RelatedIDs []string `json:"relatedIds,omitempty"`

	// LLMHypothesis is the reporter's guess. Stored, shown, and excluded
	// from the fingerprint and from every verdict — same rule as an anomaly
	// report, for the same reason.
	LLMHypothesis string `json:"llmHypothesis,omitempty"`
}

var (
	ErrCSXIssueSchema       = errors.New("csx issue report schemaVersion must be 1")
	ErrCSXIssueSurface      = errors.New("affectedSurface must be one of: " + strings.Join(CSXSurfaces(), ", "))
	ErrCSXIssueKind         = errors.New("issueKind must be one of: " + strings.Join(CSXIssueKinds(), ", "))
	ErrCSXIssueComponent    = errors.New("component is required: name the tool or endpoint this is about")
	ErrCSXIssueBehavior     = errors.New("actualBehavior and expectedBehavior are both required and must differ")
	ErrCSXIssueNoEvidence   = errors.New("a report needs something re-checkable: give a requestFingerprint, or publicInput naming the public endpoint and coordinates. A description on its own is not a report")
	ErrCSXIssueReproducible = errors.New("reproducible must be yes, no or unknown")
	ErrCSXIssueConfidence   = errors.New("confidence must be low, medium or high")
)

// Normalize canonicalizes the fields the fingerprint and the verdict use.
func (r CSXIssueReport) Normalize() CSXIssueReport {
	r.AffectedSurface = normalizeLower(r.AffectedSurface)
	r.IssueKind = normalizeLower(r.IssueKind)
	r.Component = strings.TrimSpace(r.Component)
	r.RequestFingerprint = strings.ToLower(strings.TrimSpace(r.RequestFingerprint))
	r.ActualBehavior = strings.TrimSpace(r.ActualBehavior)
	r.ExpectedBehavior = strings.TrimSpace(r.ExpectedBehavior)
	r.Reproducible = normalizeLower(r.Reproducible)
	if r.Reproducible == "" {
		r.Reproducible = "unknown"
	}
	r.Confidence = normalizeLower(r.Confidence)
	if r.Confidence == "" {
		r.Confidence = "medium"
	}
	if r.PublicInput != nil {
		r.PublicInput.Endpoint = strings.TrimSpace(r.PublicInput.Endpoint)
		r.PublicInput.Environment = r.PublicInput.Environment.Normalize()
	}
	return r
}

// Validate applies the admission test. It is narrower than it looks: what it
// refuses is a report nobody can act on, which is the same set as the reports
// a model can generate for free.
func (r CSXIssueReport) Validate() error {
	if r.SchemaVersion != CSXIssueReportSchemaVersion {
		return ErrCSXIssueSchema
	}
	if !containsString(CSXSurfaces(), r.AffectedSurface) {
		return ErrCSXIssueSurface
	}
	if !containsString(CSXIssueKinds(), r.IssueKind) {
		return ErrCSXIssueKind
	}
	if r.Component == "" {
		return ErrCSXIssueComponent
	}
	if r.ActualBehavior == "" || r.ExpectedBehavior == "" ||
		strings.EqualFold(r.ActualBehavior, r.ExpectedBehavior) {
		return ErrCSXIssueBehavior
	}
	if r.RequestFingerprint == "" && (r.PublicInput == nil || r.PublicInput.Endpoint == "") {
		return ErrCSXIssueNoEvidence
	}
	switch r.Reproducible {
	case "yes", "no", "unknown":
	default:
		return ErrCSXIssueReproducible
	}
	switch r.Confidence {
	case "low", "medium", "high":
	default:
		return ErrCSXIssueConfidence
	}
	return nil
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// Fingerprint is the dedupe key: which surface, which component, which shape
// of defect, and which request.
//
// The prose is NOT in it, deliberately. Two models describing one defect in
// two sentences must produce one row and one occurrence count — otherwise a
// popular defect becomes a pile of tickets, which is the exact outcome the
// conservative policy exists to prevent.
func (r CSXIssueReport) Fingerprint() string {
	n := r.Normalize()
	request := n.RequestFingerprint
	if request == "" && n.PublicInput != nil {
		parts := append([]string{n.PublicInput.Endpoint}, n.PublicInput.Packages...)
		parts = append(parts, n.PublicInput.Symbols...)
		parts = append(parts, anomalyEnvKey(n.PublicInput.Environment))
		request = strings.Join(parts, ",")
	}
	return SHA256Hex([]byte(strings.Join([]string{
		"csx-issue", "v1", n.AffectedSurface, n.IssueKind, n.Component, request,
	}, "|")))
}

// publicReplayEndpoints are the routes a replay may legitimately re-run: read
// routes whose entire input is public coordinates. A write route is not
// replayable at all — replaying it would mean performing it again.
var publicReplayEndpoints = map[string]bool{
	"/v1/registry/packages": true,
	"/v1/registry/symbols":  true,
	"/v1/shards":            true,
	"/v1/samples":           true,
	"/v1/wanted":            true,
	"/v1/adapters":          true,
	"/v1/stats":             true,
}

// CSXIssueReplayLane reports whether this server can re-run the report's own
// request, and says why not when it cannot.
//
// The "why not" is the useful half. For the defect class this channel most
// wants to catch — a search that answered the wrong question — the input that
// would have to be replayed is the caller's prompt, and the privacy contract
// means this network never has it. A stable fingerprint can travel; a
// fingerprint cannot be re-run. Saying that plainly is the difference between
// a queue an operator can trust and a queue full of work that will never
// move.
func CSXIssueReplayLane(r CSXIssueReport) (endpoint string, replayable bool, reason string) {
	n := r.Normalize()
	if n.AffectedSurface != CSXSurfaceServer {
		return "", false, "no replay lane: only server-surface requests can be re-run here. " +
			"A " + n.AffectedSurface + " report is triaged by a person, which is why it is not left pending"
	}
	if n.PublicInput == nil || n.PublicInput.Endpoint == "" {
		return "", false, "no replay lane: the report carries a request fingerprint but no public input, " +
			"and a fingerprint cannot be re-run. For a search, the input that would have to be replayed is the " +
			"caller's question, which this network deliberately never receives — so this one is triaged by a person"
	}
	route := "/" + strings.Trim(n.PublicInput.Endpoint, "/")
	for prefix := range publicReplayEndpoints {
		if route == prefix || strings.HasPrefix(route, prefix+"/") {
			return route, true, ""
		}
	}
	return "", false, "no replay lane: " + route + " is not a public read route this server may re-run. " +
		"Replaying a write would mean performing it a second time"
}
