package domain

import (
	"crypto/sha256"
	"encoding/hex"
)

// Stage names an observed or verified pipeline step.
// Observation stages (weak, project-level — goal.md §6.1.A) are PROJECT_*;
// verification stages (strong, per-sample — §6.3) are the bare names.
type Stage string

const (
	StageUsed             Stage = "USED"
	StageProjectTypecheck Stage = "PROJECT_TYPECHECK"
	StageProjectCompile   Stage = "PROJECT_COMPILE"
	StageProjectTest      Stage = "PROJECT_TEST"
	StageProjectProcess   Stage = "PROJECT_PROCESS"
	// PROJECT_LOAD observes module/library load in the executing context
	// (import succeeded in node/bun/deno/browser) without asserting more.
	StageProjectLoad Stage = "PROJECT_LOAD"

	// Runtime-instrumentation stages (evidence class
	// RUNTIME_INSTRUMENTATION, goal.md §6.1.D). Emitted ONLY by adapters
	// with A3 capability that directly observed the call — never inferred.
	// No Public v1 adapter claims A3; the stages exist so browser/worker
	// instrumentation can contribute without a schema change.
	// SYMBOL_EXECUTED result is always PASS (execution observed, outcome
	// not asserted). SYMBOL_CALL result PASS = returned, FAIL = threw.
	StageSymbolExecuted Stage = "SYMBOL_EXECUTED"
	StageSymbolCall     Stage = "SYMBOL_CALL"

	StageResolve   Stage = "RESOLVE"
	StageSymbol    Stage = "SYMBOL"
	StageTypecheck Stage = "TYPECHECK"
	StageCompile   Stage = "COMPILE"
	StageLoad      Stage = "LOAD"
	StageExecute   Stage = "EXECUTE"
	StageTest      Stage = "TEST"
	StageContract  Stage = "CONTRACT"
)

// Result of a stage.
type Result string

const (
	ResultPass Result = "PASS"
	ResultFail Result = "FAIL"
)

// SymbolConfidence expresses how sure static analysis is about a symbol
// resolution (goal.md §7.2).
type SymbolConfidence string

const (
	SymbolExact    SymbolConfidence = "EXACT"
	SymbolProbable SymbolConfidence = "PROBABLE"
	SymbolUnknown  SymbolConfidence = "UNKNOWN"
)

// EvidenceClass separates weak co-occurrence evidence from strong
// verification evidence (goal.md §6.1).
type EvidenceClass string

const (
	ClassUsageObservation       EvidenceClass = "USAGE_OBSERVATION"
	ClassAdoptionEvidence       EvidenceClass = "ADOPTION_EVIDENCE"
	ClassSampleVerification     EvidenceClass = "SAMPLE_VERIFICATION"
	ClassRuntimeInstrumentation EvidenceClass = "RUNTIME_INSTRUMENTATION"
)

// VerificationLevel grades how far a sample has been verified (goal.md §6.2).
type VerificationLevel string

const (
	L0SourceOnly   VerificationLevel = "L0_SOURCE_ONLY"
	L1Resolved     VerificationLevel = "L1_RESOLVED"
	L2Compiled     VerificationLevel = "L2_COMPILED"
	L3ContractPass VerificationLevel = "L3_CONTRACT_PASS"
	L4CrossPass    VerificationLevel = "L4_CROSS_PASS"
	L5MatrixPass   VerificationLevel = "L5_MATRIX_PASS"
)

// FailureDomain classifies an inferred failure cause (goal.md §6.4).
// Uncertain causes MUST stay UNKNOWN or be expressed as hypotheses.
type FailureDomain string

const (
	FailCode                 FailureDomain = "CODE"
	FailAPIRemovedOrChanged  FailureDomain = "API_REMOVED_OR_CHANGED"
	FailLibraryRegression    FailureDomain = "LIBRARY_REGRESSION"
	FailTransitiveDependency FailureDomain = "TRANSITIVE_DEPENDENCY"
	FailRuntime              FailureDomain = "RUNTIME"
	FailBrowser              FailureDomain = "BROWSER"
	FailEngine               FailureDomain = "ENGINE"
	FailOS                   FailureDomain = "OS"
	FailArch                 FailureDomain = "ARCH"
	FailToolchain            FailureDomain = "TOOLCHAIN"
	FailConfiguration        FailureDomain = "CONFIGURATION"
	FailExternalService      FailureDomain = "EXTERNAL_SERVICE"
	FailResource             FailureDomain = "RESOURCE"
	FailSecurityPolicy       FailureDomain = "SECURITY_POLICY"
	FailUnknown              FailureDomain = "UNKNOWN"
)

// FailureHypothesis is a probabilistic cause attribution.
type FailureHypothesis struct {
	Domain     FailureDomain `json:"domain"`
	Confidence float64       `json:"confidence"`
}

// MatchGrade classifies how safely a search result fits the requesting
// environment (goal.md §11.4).
type MatchGrade string

const (
	GradeExact              MatchGrade = "EXACT"
	GradeCompatible         MatchGrade = "COMPATIBLE"
	GradeAdaptationRequired MatchGrade = "ADAPTATION_REQUIRED"
	GradeReferenceOnly      MatchGrade = "REFERENCE_ONLY"
	GradeNoSafeMatch        MatchGrade = "NO_SAFE_MATCH"
)

// SandboxCapability declares what isolation a verifying peer can provide
// (goal.md §16.3).
type SandboxCapability string

const (
	CapCompileOnly        SandboxCapability = "COMPILE_ONLY"
	CapContainerRun       SandboxCapability = "CONTAINER_RUN"
	CapStrongIsolationRun SandboxCapability = "STRONG_ISOLATION_RUN"
	CapLiveIntegration    SandboxCapability = "LIVE_INTEGRATION"
)

// WorkerRequirements is the closed, declarative description attached to a
// verification job. A worker must satisfy every non-empty field before it
// claims the job. This stays separate from EnvironmentFingerprint: it says
// what must be available, not where a result already ran.
type WorkerRequirements struct {
	SandboxCapability SandboxCapability `json:"sandboxCapability,omitempty"`
	VerifierAdapter   string            `json:"verifierAdapter,omitempty"`
	Ecosystem         string            `json:"ecosystem,omitempty"`
	Runtime           string            `json:"runtime,omitempty"`
	RuntimeVersion    string            `json:"runtimeVersion,omitempty"`
	ExecutionContext  string            `json:"executionContext,omitempty"`
	BrowserFamily     string            `json:"browserFamily,omitempty"`
	BrowserMajor      string            `json:"browserMajor,omitempty"`
	Engine            string            `json:"engine,omitempty"`
	EngineVersion     string            `json:"engineVersion,omitempty"`
	Frameworks        []string          `json:"frameworks,omitempty"`
}

// ObservationBatch is the anonymous automatic-evidence wire unit
// (goal.md §7.6). It must never carry source, paths, project names,
// or raw logs — only the fields below.
type ObservationBatch struct {
	SchemaVersion    int                    `json:"schemaVersion"`
	Epoch            string                 `json:"epoch"` // daily bucket "2026-08-13"
	AnonID           string                 `json:"anonId"`
	ProjectBucket    string                 `json:"projectBucket"`
	Package          string                 `json:"package"`
	Symbol           string                 `json:"symbol,omitempty"`
	SymbolConfidence SymbolConfidence       `json:"symbolConfidence,omitempty"`
	Environment      EnvironmentFingerprint `json:"environment"`
	Stage            Stage                  `json:"stage"`
	Result           Result                 `json:"result"`
	ObservationCount int                    `json:"observationCount"`
	ErrorFingerprint string                 `json:"errorFingerprint,omitempty"`
	ErrorCode        string                 `json:"errorCode,omitempty"`
}

// Case describes a problem being solved — never code (goal.md §7.4).
type Case struct {
	SchemaVersion int               `json:"schemaVersion"`
	CaseID        string            `json:"caseId,omitempty"`
	Kind          string            `json:"kind"` // HOW | FIX | MIGRATION | CONFIG
	Goal          string            `json:"goal"`
	Packages      []string          `json:"packages"`
	Symbols       []string          `json:"symbols,omitempty"`
	Constraints   map[string]string `json:"constraints,omitempty"`
	Contract      []string          `json:"contract"`
	// Believed is the thing a competent developer or model expects, which
	// the contract then measures and contradicts.
	//
	// It is what turns a sample into a FINDING. The findings page is the
	// most persuasive thing on the site — "the documentation says X, the
	// contract measured Y" does more than any paragraph of explanation —
	// and it was a hand-written list of twenty-nine entries in Go source
	// while the storehouse grew past three hundred samples. The page that
	// convinces people was the one that could not grow.
	//
	// Optional, and omitted when empty, so every case id computed before
	// this field existed is unchanged.
	Believed string `json:"believed,omitempty"`
}

// ComputeID derives the content-addressed case ID over the canonical JSON
// of the case with CaseID cleared.
func (c Case) ComputeID() string {
	c.CaseID = ""
	sum := sha256.Sum256(MustCanonicalJSON(c))
	return "case:sha256:" + hex.EncodeToString(sum[:])
}

// SampleManifest is the csx.json descriptor inside a sample artifact
// (goal.md §7.5).
type SampleManifest struct {
	SchemaVersion   int                    `json:"schemaVersion"`
	Case            Case                   `json:"case"`
	Packages        []string               `json:"packages"`
	Symbols         []string               `json:"symbols,omitempty"`
	Environment     EnvironmentFingerprint `json:"environment"`
	License         string                 `json:"license"` // default MIT-0
	BuildCommand    []string               `json:"buildCommand,omitempty"`
	ContractCommand []string               `json:"contractCommand"`
	VerifierAdapter string                 `json:"verifierAdapter"`
}

// VerificationReceipt is the immutable, signed record of one sample
// verification in one environment (goal.md §7.7).
type VerificationReceipt struct {
	SchemaVersion   int                    `json:"schemaVersion"`
	SampleID        string                 `json:"sampleId"`
	CaseID          string                 `json:"caseId"`
	EnvironmentHash string                 `json:"environmentHash"`
	Environment     EnvironmentFingerprint `json:"environment"`
	Stages          map[string]string      `json:"stages"` // stage → PASS|FAIL|SKIPPED
	// ResolvedPackages is what the resolve stage ACTUALLY installed, read
	// out of the lockfile it generated, as purls.
	//
	// Without it a receipt takes the version from the manifest, which the
	// sample's author wrote and the verification never checked — a claim
	// where this project promises evidence. It is also the axis a
	// regression farm needs: the same contract run against three releases
	// produced three receipts that said exactly the same thing, so there
	// was nowhere for "passes at 2.3, fails at 2.2" to live.
	//
	// Empty means NOT ESTABLISHED, never "matches the manifest". A
	// lockfile that cannot be read yields nothing rather than a guess.
	ResolvedPackages  []string          `json:"resolvedPackages,omitempty"`
	VerifierAdapter   string            `json:"verifierAdapter"`
	SandboxCapability SandboxCapability `json:"sandboxCapability"`
	LogsDigest        string            `json:"logsDigest"`
	CreatedAt         string            `json:"createdAt"` // RFC3339
	PeerID            string            `json:"peerId"`
	PeerPubkey        string            `json:"peerPubkey"`    // base64 ed25519 public key
	PeerSignature     string            `json:"peerSignature"` // base64 over SigningBytes
}

// SigningBytes returns the canonical JSON the peer signature covers
// (the receipt with PeerSignature cleared).
func (r VerificationReceipt) SigningBytes() []byte {
	r.PeerSignature = ""
	return MustCanonicalJSON(r)
}

// ReceiptID is the content hash identifying an immutable receipt.
func (r VerificationReceipt) ReceiptID() string {
	sum := sha256.Sum256(MustCanonicalJSON(r))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// SHA256Hex returns "sha256:<hex>" over b — the project-wide content id form.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
