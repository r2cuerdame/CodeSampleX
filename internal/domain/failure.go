package domain

import (
	"fmt"
	"strings"
)

// TerminationKind is the structured way an executable stage ended. An empty
// value means the producer did not establish termination; it is never another
// spelling of "exit code unavailable".
type TerminationKind string

const (
	TerminationExit               TerminationKind = "exit"
	TerminationSignal             TerminationKind = "signal"
	TerminationTimeout            TerminationKind = "timeout"
	TerminationProcessStartFailed TerminationKind = "process-start-failed"
)

// EvidenceQuality states how much of a failed execution was preserved.
// LegacyIncomplete is reserved for records written before this contract; it
// prevents a historical hash from masquerading as complete modern evidence.
type EvidenceQuality string

const (
	EvidenceComplete         EvidenceQuality = "complete"
	EvidencePartial          EvidenceQuality = "partial"
	EvidenceMissing          EvidenceQuality = "missing"
	EvidenceLegacyIncomplete EvidenceQuality = "legacy-evidence-incomplete"
)

// FailureStageEvidence is the bounded reason an actual failure stage was
// selected. It distinguishes structured proof, parser markers, and aggregate
// evidence without retaining raw logs.
type FailureStageEvidence string

const (
	FailureStageStructuredTermination  FailureStageEvidence = "structured-termination"
	FailureStageResolveDiagnostic      FailureStageEvidence = "resolve-diagnostic"
	FailureStageCompilerDiagnostic     FailureStageEvidence = "compiler-diagnostic"
	FailureStageTestRunnerDiagnostic   FailureStageEvidence = "test-runner-diagnostic"
	FailureStageBuildAggregate         FailureStageEvidence = "build-aggregate"
	FailureStageUnclassifiedDiagnostic FailureStageEvidence = "unclassified-diagnostic"
)

// FailureEvidenceGap states what the captured output could not prove.
type FailureEvidenceGap string

const (
	FailureDiagnosticMissing FailureEvidenceGap = "diagnostic-missing"
	FailureStageUnknown      FailureEvidenceGap = "stage-unknown"
)

// FailureTermination contains only structured process state. ExitCode is a
// pointer because exit 0 is a real value while nil means not recorded.
type FailureTermination struct {
	Kind          TerminationKind `json:"kind,omitempty"`
	ExitCode      *int            `json:"exitCode,omitempty"`
	Signal        string          `json:"signal,omitempty"`
	TimeoutMillis int64           `json:"timeoutMillis,omitempty"`
}

// FailureEvidence is safe public failure material. ErrorSummary is already
// normalized/redacted and capped; raw stdout/stderr never belongs here.
type FailureEvidence struct {
	TerminationKind TerminationKind      `json:"terminationKind,omitempty"`
	ExitCode        *int                 `json:"exitCode,omitempty"`
	Signal          string               `json:"signal,omitempty"`
	TimeoutMillis   int64                `json:"timeoutMillis,omitempty"`
	ErrorSummary    string               `json:"errorSummary,omitempty"`
	ErrorCode       string               `json:"errorCode,omitempty"`
	Fingerprint     string               `json:"fingerprint,omitempty"`
	EvidenceQuality EvidenceQuality      `json:"evidenceQuality"`
	OuterCommand    string               `json:"outerCommand,omitempty"`
	OuterStage      Stage                `json:"outerStage,omitempty"`
	ActualToolchain string               `json:"actualToolchain,omitempty"`
	StageEvidence   FailureStageEvidence `json:"stageEvidence,omitempty"`
	EvidenceGap     FailureEvidenceGap   `json:"evidenceGap,omitempty"`
}

// Termination returns the structured subset of f.
func (f FailureEvidence) Termination() FailureTermination {
	return FailureTermination{Kind: f.TerminationKind, ExitCode: f.ExitCode, Signal: f.Signal, TimeoutMillis: f.TimeoutMillis}
}

// FailureFingerprint returns the canonical v2 cluster identity for modern
// failure evidence. Package/version and exact environment are intentionally
// outside this hash and remain structured dimensions beside the cluster.
func FailureFingerprint(stage Stage, term FailureTermination, errorCode, errorSummary string) string {
	return SHA256Hex([]byte("v2|" + string(stage) + "|" + term.FingerprintCoordinate() + "|" + errorCode + "|" + errorSummary))
}

// ClassifiedFailureFingerprint is the v3 identity for actual-stage evidence.
// Outer command intent is deliberately excluded; the actual failing toolchain
// is included so unrelated compilers do not collapse behind one wrapper.
func ClassifiedFailureFingerprint(stage Stage, actualToolchain string, term FailureTermination, errorCode, errorSummary string) string {
	return SHA256Hex([]byte("v3|" + string(stage) + "|" + strings.ToLower(strings.TrimSpace(actualToolchain)) + "|" + term.FingerprintCoordinate() + "|" + errorCode + "|" + errorSummary))
}

// FailureEnvironmentVariant is one exact recorded environment bucket inside
// a cluster fingerprint. Keeping variants outside the cluster identity avoids
// cardinality explosions while preserving where the failure reproduced.
type FailureEnvironmentVariant struct {
	Environment EnvironmentFingerprint `json:"environment,omitempty"`
	Summary     map[string]string      `json:"summary,omitempty"`
	Count       int64                  `json:"count"`
	FirstSeen   string                 `json:"firstSeen,omitempty"`
	LastSeen    string                 `json:"lastSeen,omitempty"`
}

func (t FailureTermination) Structured() bool {
	switch t.Kind {
	case TerminationExit:
		return t.ExitCode != nil
	case TerminationSignal:
		return strings.TrimSpace(t.Signal) != ""
	case TerminationTimeout:
		return true
	case TerminationProcessStartFailed:
		return true
	default:
		return false
	}
}

// FingerprintCoordinate is a stable, non-secret representation of the
// structured termination state used by the v2 cluster fingerprint.
func (t FailureTermination) FingerprintCoordinate() string {
	switch t.Kind {
	case TerminationExit:
		if t.ExitCode == nil {
			return "exit:unknown"
		}
		return fmt.Sprintf("exit:%d", *t.ExitCode)
	case TerminationSignal:
		return "signal:" + strings.ToUpper(strings.TrimSpace(t.Signal))
	case TerminationTimeout:
		if t.TimeoutMillis > 0 {
			return fmt.Sprintf("timeout:%dms", t.TimeoutMillis)
		}
		return "timeout"
	case TerminationProcessStartFailed:
		return string(TerminationProcessStartFailed)
	default:
		return "unknown"
	}
}
