package domain

import (
	"errors"
	"strings"
)

// An anomaly report is a VERIFICATION REQUEST, never a finding.
//
// The consumption side of this network is an LLM, and an LLM can produce an
// unbounded stream of plausible suspicion at no cost. What it can also do —
// and nothing else in the loop can — is run the answer on a machine the
// container farm will never own, and notice that the answer and the machine
// disagree. That second thing is worth having; the first is worth refusing.
//
// So the contract here is deliberately narrow: a report must carry a
// CONCRETE local observation with a public coordinate attached, and the
// reporter's explanation of WHY travels in a separate field that decides
// nothing. Everything a verdict is computed from is either a public
// coordinate, a PASS/FAIL somebody measured, or a signed receipt this
// network produced afterwards.

// AnomalyReportSchemaVersion is the only accepted wire version.
const AnomalyReportSchemaVersion = 1

// Anomaly types. Each names a shape of mismatch that can be stated as a
// public coordinate plus two disagreeing observations, which is the whole
// admission test — "the model thinks this is odd" has no shape here on
// purpose.
const (
	// AnomalyCSXPassLocalFail: the network served a passing conclusion for
	// this coordinate and the same coordinate failed locally.
	AnomalyCSXPassLocalFail = "csx-pass-local-fail"
	// AnomalyCSXFailLocalPass: the network served a failing or incompatible
	// conclusion and the same coordinate passed locally.
	AnomalyCSXFailLocalPass = "csx-fail-local-pass"
	// AnomalySymbolSignatureMismatch: a symbol/API signature the network
	// returned is not the signature the public package actually exports.
	AnomalySymbolSignatureMismatch = "symbol-signature-mismatch"
	// AnomalyUpgradePathBroken: a recommended upgrade path does not
	// resolve, build or run.
	AnomalyUpgradePathBroken = "upgrade-path-broken"
	// AnomalyDependencyGraphUnknown: a sample exists and runs, but the
	// dependency graph reports unknown or contradicts itself.
	AnomalyDependencyGraphUnknown = "dependency-graph-unknown"
	// AnomalyEvidenceConflict: two pieces of evidence at the SAME exact
	// coordinate contradict each other.
	AnomalyEvidenceConflict = "evidence-conflict"
	// AnomalyBrokenInternalReference: sample/evidence/dependency/finding
	// reference each other and one side does not exist.
	AnomalyBrokenInternalReference = "broken-internal-reference"
	// AnomalyRepeatedNoSafeMatch: the same NO_SAFE_MATCH keeps coming back
	// AND a reproducible public failure is attached. NO_SAFE_MATCH on its
	// own is a real answer, not an anomaly.
	AnomalyRepeatedNoSafeMatch = "repeated-no-safe-match"
)

// AnomalyTypes lists every accepted anomalyType, in schema order.
func AnomalyTypes() []string {
	return []string{
		AnomalyCSXPassLocalFail,
		AnomalyCSXFailLocalPass,
		AnomalySymbolSignatureMismatch,
		AnomalyUpgradePathBroken,
		AnomalyDependencyGraphUnknown,
		AnomalyEvidenceConflict,
		AnomalyBrokenInternalReference,
		AnomalyRepeatedNoSafeMatch,
	}
}

// Report ingest states. A report is never "true"; it is at some point in a
// pipeline that may end in a verdict.
const (
	// AnomalyStatusQueued: accepted, deduped, and a reproduction job exists.
	AnomalyStatusQueued = "queued"
	// AnomalyStatusVerifying: a verifier holds the reproduction job.
	AnomalyStatusVerifying = "verifying"
	// AnomalyStatusVerified: a verdict has been recorded.
	AnomalyStatusVerified = "verified"
	// AnomalyStatusUnsupported: accepted, but no verifier lane in this
	// network can reproduce it. It is NOT hidden as permanently pending —
	// the reason is returned to the caller and shown in admin.
	AnomalyStatusUnsupported = "unsupported-verifier-lane"
)

// Verdicts. Only the three "confirmed-" values may promote anything.
const (
	AnomalyVerdictCSXDefect               = "confirmed-csx-defect"
	AnomalyVerdictCompatibilityBoundary   = "confirmed-compatibility-boundary"
	AnomalyVerdictNewEvidence             = "confirmed-new-evidence"
	AnomalyVerdictEnvironmentDifference   = "environment-difference"
	AnomalyVerdictNotReproducible         = "not-reproducible"
	AnomalyVerdictInsufficientEvidence    = "insufficient-evidence"
	AnomalyVerdictDuplicate               = "duplicate"
	anomalyVerdictUndecidedInternalMarker = ""
)

// AnomalyVerdictConfirmed reports whether a verdict may promote a report to
// public evidence or to the internal defect queue. Everything else is a
// closed report and changes nothing anyone can see.
func AnomalyVerdictConfirmed(verdict string) bool {
	switch verdict {
	case AnomalyVerdictCSXDefect, AnomalyVerdictCompatibilityBoundary, AnomalyVerdictNewEvidence:
		return true
	}
	return false
}

// AnomalyObservation is one measured outcome. Result is the only field a
// verdict is ever computed from; Detail is sanitized prose for a human
// reading the queue.
type AnomalyObservation struct {
	// Result is PASS, FAIL or UNKNOWN. UNKNOWN is legal for what CSX said
	// (it may have said nothing) and illegal for the local side, which is
	// the whole admission test.
	Result string `json:"result"`
	// Stage names where the result happened: resolve | compile | typecheck
	// | test | run. Free-form but short.
	Stage string `json:"stage,omitempty"`
	// Detail is a short sanitized statement. Never raw logs.
	Detail string `json:"detail,omitempty"`
}

// AnomalyReport is the wire document an agent submits and the server stores.
type AnomalyReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	AnomalyType   string `json:"anomalyType"`

	// Package is the exact public coordinate the mismatch is about, as a
	// purl. It is required: a report with no public coordinate cannot be
	// reproduced by anybody, which makes it an opinion.
	Package string `json:"package"`
	Symbol  string `json:"symbol,omitempty"`

	// Environment is where the LOCAL observation happened. It is what turns
	// "it fails" into "it fails there", and it is the difference between a
	// confirmed defect and a recorded environment boundary.
	Environment EnvironmentFingerprint `json:"environment"`

	// SampleID, EvidenceID and SearchFingerprint are references to things
	// CSX itself already published. They are the only ids a reporter can
	// hold, and they are how a report is attached to something reproducible.
	SampleID          string `json:"sampleId,omitempty"`
	EvidenceID        string `json:"evidenceId,omitempty"`
	SearchFingerprint string `json:"searchFingerprint,omitempty"`

	CSXObserved   AnomalyObservation `json:"csxObserved"`
	LocalObserved AnomalyObservation `json:"localObserved"`

	// Reproducible is yes | no | unknown, as the reporter measured it — not
	// as it hopes. It ranks; it never decides.
	Reproducible string `json:"reproducible"`
	// Confidence is low | medium | high. Ranking only, never truth.
	Confidence string `json:"confidence"`

	// ErrorCode and ErrorFingerprint are the sanitizer's output for the
	// local failure. Raw stderr never travels; the fingerprint is what makes
	// two reports of the same failure one report.
	ErrorCode        string `json:"errorCode,omitempty"`
	ErrorFingerprint string `json:"errorFingerprint,omitempty"`
	// ErrorTemplate is the sanitized template — placeholders where paths,
	// tokens and usernames were.
	ErrorTemplate string `json:"errorTemplate,omitempty"`

	RelatedIDs []string `json:"relatedIds,omitempty"`

	// LLMHypothesis is the reporter's guess at the cause. It is stored, it
	// is shown to a human, and it is deliberately absent from Fingerprint
	// and from every verdict computation in this file. A wrong guess must
	// cost the report nothing.
	LLMHypothesis string `json:"llmHypothesis,omitempty"`
}

// Errors the ingest path returns. Each one is a sentence a caller can act
// on, because "invalid report" tells an agent nothing about what to fix.
var (
	ErrAnomalySchema           = errors.New("anomaly report schemaVersion must be 1")
	ErrAnomalyType             = errors.New("anomalyType must be one of: " + strings.Join(AnomalyTypes(), ", "))
	ErrAnomalyPackage          = errors.New("package must be a public purl, e.g. pkg:npm/axios@1.12.0")
	ErrAnomalyNoLocalObs       = errors.New("localObserved.result must be PASS or FAIL: a report with no local observation is a hypothesis, and hypotheses are not accepted")
	ErrAnomalyNoMismatch       = errors.New("csxObserved and localObserved must actually disagree: a concrete mismatch is the admission test")
	ErrAnomalyReproducible     = errors.New("reproducible must be yes, no or unknown")
	ErrAnomalyConfidence       = errors.New("confidence must be low, medium or high")
	ErrAnomalyStage            = errors.New("observation stage must be one of: resolve, compile, typecheck, test, run, contract")
	ErrAnomalyIdentifier       = errors.New("sampleId, evidenceId, searchFingerprint, errorFingerprint and relatedIds must be canonical, non-identifying stable ids")
	ErrAnomalyErrorCode        = errors.New("errorCode must be a short structured identifier")
	ErrAnomalyNoSafeMatchAlone = errors.New("a repeated NO_SAFE_MATCH is not an anomaly on its own: attach a reproducible public failure (localObserved FAIL + errorFingerprint + reproducible=yes)")
)

func anomalyContentID(s string) bool {
	const prefix = "sha256:"
	if len(s) != len(prefix)+64 || !strings.HasPrefix(s, prefix) {
		return false
	}
	for _, c := range s[len(prefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// anomalyReferenceID accepts content ids, public package coordinates and
// namespaced stable ids. It deliberately refuses paths, URLs, whitespace and
// bare opaque strings: those shapes can carry client secrets but do not name
// anything in CSX's public data model.
func anomalyReferenceID(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	if anomalyContentID(s) {
		return true
	}
	if strings.HasPrefix(s, "pkg:") {
		_, err := ParsePURL(s)
		return err == nil
	}
	if !strings.ContainsRune(s, ':') {
		return false
	}
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.ContainsRune("-_.:@+", rune(c)) {
			continue
		}
		return false
	}
	return true
}

func anomalyErrorCode(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 128 {
		return false
	}
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.ContainsRune("-_.:", rune(c)) {
			continue
		}
		return false
	}
	return true
}

func anomalyStage(s string) bool {
	switch s {
	case "", "resolve", "compile", "typecheck", "test", "run", "contract":
		return true
	}
	return false
}

func normalizeResult(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "PASS":
		return "PASS"
	case "FAIL":
		return "FAIL"
	case "", "UNKNOWN":
		return "UNKNOWN"
	}
	return ""
}

func normalizeLower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// Normalize canonicalizes the fields a verdict and a fingerprint are
// computed from, so two reports that say the same thing in different case
// dedupe against each other instead of both queuing work.
func (r AnomalyReport) Normalize() AnomalyReport {
	r.AnomalyType = normalizeLower(r.AnomalyType)
	r.Package = strings.TrimSpace(r.Package)
	r.Symbol = strings.TrimSpace(r.Symbol)
	r.SampleID = strings.TrimSpace(r.SampleID)
	r.EvidenceID = strings.TrimSpace(r.EvidenceID)
	r.SearchFingerprint = strings.TrimSpace(r.SearchFingerprint)
	r.CSXObserved.Result = normalizeResult(r.CSXObserved.Result)
	r.CSXObserved.Stage = normalizeLower(r.CSXObserved.Stage)
	r.LocalObserved.Result = normalizeResult(r.LocalObserved.Result)
	r.LocalObserved.Stage = normalizeLower(r.LocalObserved.Stage)
	r.Reproducible = normalizeLower(r.Reproducible)
	if r.Reproducible == "" {
		r.Reproducible = "unknown"
	}
	r.Confidence = normalizeLower(r.Confidence)
	if r.Confidence == "" {
		r.Confidence = "medium"
	}
	r.ErrorCode = strings.TrimSpace(r.ErrorCode)
	r.ErrorFingerprint = strings.ToLower(strings.TrimSpace(r.ErrorFingerprint))
	r.Environment = r.Environment.Normalize()
	return r
}

// Validate applies the admission test. It runs on the client before an
// upload and again on the server, because the client is a program somebody
// else can replace.
//
// Everything it refuses is refused for the same reason: it cannot be
// re-verified by anyone. That is the only bar — not whether the report
// sounds right, not how confident the reporter is.
func (r AnomalyReport) Validate() error {
	if r.SchemaVersion != AnomalyReportSchemaVersion {
		return ErrAnomalySchema
	}
	known := false
	for _, t := range AnomalyTypes() {
		if r.AnomalyType == t {
			known = true
			break
		}
	}
	if !known {
		return ErrAnomalyType
	}
	if _, err := ParsePURL(r.Package); err != nil {
		return ErrAnomalyPackage
	}
	if r.LocalObserved.Result != "PASS" && r.LocalObserved.Result != "FAIL" {
		return ErrAnomalyNoLocalObs
	}
	if r.CSXObserved.Result == "" || r.Reproducible == "" {
		// Normalize() fills these; an unnormalized report reaching here
		// means an unrecognized literal, which is not a value we may guess.
		return ErrAnomalyNoMismatch
	}
	if !anomalyStage(r.CSXObserved.Stage) || !anomalyStage(r.LocalObserved.Stage) {
		return ErrAnomalyStage
	}
	if (r.SampleID != "" && !anomalyContentID(r.SampleID)) ||
		(r.EvidenceID != "" && !anomalyContentID(r.EvidenceID)) ||
		(r.SearchFingerprint != "" && !anomalyContentID(r.SearchFingerprint)) ||
		(r.ErrorFingerprint != "" && !anomalyContentID(r.ErrorFingerprint)) {
		return ErrAnomalyIdentifier
	}
	for _, id := range r.RelatedIDs {
		if !anomalyReferenceID(id) {
			return ErrAnomalyIdentifier
		}
	}
	if !anomalyErrorCode(r.ErrorCode) {
		return ErrAnomalyErrorCode
	}
	switch r.Reproducible {
	case "yes", "no", "unknown":
	default:
		return ErrAnomalyReproducible
	}
	switch r.Confidence {
	case "low", "medium", "high":
	default:
		return ErrAnomalyConfidence
	}
	// The mismatch itself. Unknown-style reports must say that CSX returned
	// UNKNOWN. Conflict and signature reports must instead carry two concrete
	// disagreeing outcomes. Exempting all four types here used to admit
	// PASS/PASS reports with no mismatch at all.
	switch r.AnomalyType {
	case AnomalyDependencyGraphUnknown, AnomalyBrokenInternalReference:
		if r.CSXObserved.Result != "UNKNOWN" {
			return ErrAnomalyNoMismatch
		}
	case AnomalyEvidenceConflict, AnomalySymbolSignatureMismatch:
		if r.CSXObserved.Result == "UNKNOWN" || r.CSXObserved.Result == r.LocalObserved.Result {
			return ErrAnomalyNoMismatch
		}
	case AnomalyRepeatedNoSafeMatch:
		// A miss is a real answer. It becomes a report only with a
		// reproducible public failure attached to it.
		if r.LocalObserved.Result != "FAIL" || r.ErrorFingerprint == "" || r.Reproducible != "yes" {
			return ErrAnomalyNoSafeMatchAlone
		}
	default:
		if r.CSXObserved.Result == r.LocalObserved.Result || r.CSXObserved.Result == "UNKNOWN" {
			return ErrAnomalyNoMismatch
		}
	}
	return nil
}

// anomalyEnvKey reduces an environment to the dimensions that decide
// whether two machines are the same compatibility population.
//
// The full environment hash would make every reporter's machine its own
// fingerprint — a patch-level runtime difference would defeat dedupe
// entirely and every duplicate would queue its own container. These are the
// dimensions a verifier lane can actually be chosen by.
func anomalyEnvKey(env EnvironmentFingerprint) string {
	// Normalized first, always. A caller that left executionContext blank on
	// a node runtime and a receipt whose signer filled it in describe the
	// same machine, and comparing them raw reports an environment difference
	// that does not exist — which turns a real reproduction into
	// "environment-difference" and closes a confirmed defect as a shrug.
	env = env.Normalize()
	parts := []string{
		normalizeLower(env.Ecosystem),
		normalizeLower(env.OS),
		normalizeLower(env.Arch),
		normalizeLower(env.Runtime),
		normalizeLower(RuntimeLine(env.Runtime, env.RuntimeVersion)),
		normalizeLower(env.Libc),
		normalizeLower(env.ExecutionContext),
		normalizeLower(env.BrowserFamily),
		normalizeLower(env.BrowserMajor),
	}
	return strings.Join(parts, "|")
}

// Fingerprint is the dedupe key: exact public coordinate plus the shape of
// the mismatch. Two agents that hit the same wall produce the same string,
// which is what stops one bad answer from queuing a thousand containers.
//
// LLMHypothesis, confidence and free prose are deliberately NOT in it. Two
// reports of one fact must not become two facts because two models
// explained it differently.
func (r AnomalyReport) Fingerprint() string {
	n := r.Normalize()
	parts := []string{
		"anomaly", "v1",
		n.AnomalyType,
		n.Package,
		n.Symbol,
		anomalyEnvKey(n.Environment),
		n.CSXObserved.Result,
		n.LocalObserved.Result,
		n.LocalObserved.Stage,
		n.ErrorCode,
		n.ErrorFingerprint,
		n.SampleID,
	}
	return SHA256Hex([]byte(strings.Join(parts, "|")))
}

// AnomalyVerdictFromReceipt decides what a reproduction actually showed.
//
// The receipt is the authority and the ONLY authority. The report's own
// confidence, its hypothesis and how sure the model sounded do not appear
// below: what appears is the contract result a signed receipt recorded, the
// PASS/FAIL the reporter measured, and whether the two machines were the
// same kind of machine.
//
// decided=false means the contract never ran — a resolve or compile failure
// measured the verifier, not the sample — so the report stays open and the
// existing cross-verification retry does its work. Reporting a verdict from
// a run that never reached the assertion would be inventing one.
func AnomalyVerdictFromReceipt(report AnomalyReport, receipt VerificationReceipt) (verdict string, decided bool) {
	r := report.Normalize()
	// A normal sample contract measures whether that sample runs. It does not
	// inspect the dependency graph, compare two stored evidence records, or
	// prove that an internal reference exists. Treating its PASS as evidence
	// for any of those assertions confirmed defects the receipt never tested.
	switch r.AnomalyType {
	case AnomalyDependencyGraphUnknown, AnomalyEvidenceConflict, AnomalyBrokenInternalReference:
		return anomalyVerdictUndecidedInternalMarker, false
	}
	contract := normalizeResult(receipt.Stages["contract"])
	if contract != "PASS" && contract != "FAIL" {
		return anomalyVerdictUndecidedInternalMarker, false
	}

	sameEnvironment := anomalyEnvKey(r.Environment) == anomalyEnvKey(receipt.Environment)

	// The reproduction agrees with the reporter and contradicts what the
	// network served: the report is confirmed, and what KIND of confirmation
	// it is comes from the shape of the claim, not from its author's guess.
	if contract == r.LocalObserved.Result && contract != r.CSXObserved.Result {
		switch r.AnomalyType {
		case AnomalyCSXFailLocalPass:
			// The network said it does not work and it does. That is new
			// evidence, not a defect in a served answer.
			return AnomalyVerdictNewEvidence, true
		case AnomalySymbolSignatureMismatch, AnomalyUpgradePathBroken:
			// The mismatch is a real edge of what works where.
			return AnomalyVerdictCompatibilityBoundary, true
		default:
			// The network served a conclusion an independent clean run does
			// not support. That is ours to fix.
			return AnomalyVerdictCSXDefect, true
		}
	}

	// The reproduction agrees with what the network served. Then the local
	// failure was real but not universal — and whether that is a boundary or
	// a phantom is decided by whether the two machines were even comparable.
	if sameEnvironment {
		return AnomalyVerdictNotReproducible, true
	}
	return AnomalyVerdictEnvironmentDifference, true
}
