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

const (
	minSignedProcessExitCode = int64(-1 << 31)
	maxSignedProcessExitCode = int64(1<<31 - 1)
	maxLegacyWindowsExitCode = int64(1<<32 - 1)
)

// CanonicalExitCode maps the process status carried on the public wire onto
// the signed 32-bit representation used by the evidence schema and PostgreSQL
// INTEGER columns. Windows returns a DWORD: on 64-bit Go, a native status such
// as -1 is consequently exposed as 4294967295. Clients shipped before this
// boundary persisted that unsigned spelling, so the upper uint32 half remains
// accepted and is interpreted as the same two's-complement signed value.
// Values outside signed-int32 or uint32 cannot be an OS process status and are
// rejected by the server rather than reaching storage.
func CanonicalExitCode(code int) (int, bool) {
	v := int64(code)
	switch {
	case v >= minSignedProcessExitCode && v <= maxSignedProcessExitCode:
		return code, true
	case v > maxSignedProcessExitCode && v <= maxLegacyWindowsExitCode:
		return int(int32(uint32(v))), true
	default:
		return 0, false
	}
}

// Canonical returns t with an accepted exit status in signed-int32 form. An
// invalid value is left intact so the validation boundary can reject it with
// a useful field-level error instead of silently inventing a status.
func (t FailureTermination) Canonical() FailureTermination {
	if t.ExitCode == nil {
		return t
	}
	code, ok := CanonicalExitCode(*t.ExitCode)
	if !ok || code == *t.ExitCode {
		return t
	}
	t.ExitCode = &code
	return t
}

// CanonicalizeObservationFailure repairs the signed representation and its
// derived fingerprint together. It is used both for already-durable client
// backlog rows and at server ingest, so a rolling upgrade works in either
// order. Legacy evidence whose fingerprint contract predates structured
// termination is preserved byte-for-byte.
func CanonicalizeObservationFailure(b ObservationBatch) ObservationBatch {
	if b.ExitCode == nil {
		return b
	}
	originalTerm := FailureTermination{
		Kind: b.TerminationKind, ExitCode: b.ExitCode,
		Signal: b.Signal, TimeoutMillis: b.TimeoutMillis,
	}
	code, ok := CanonicalExitCode(*b.ExitCode)
	if !ok || code == *b.ExitCode {
		return b
	}
	// Never repair an unrelated or forged fingerprint into a valid one. The
	// compatibility transform is permitted only when the submitted digest is
	// exactly the digest the legacy unsigned spelling would have produced.
	if b.ErrorFingerprint != "" && (b.EvidenceQuality == EvidenceComplete || b.EvidenceQuality == EvidencePartial) {
		legacyFingerprint := FailureFingerprint(b.Stage, originalTerm, b.ErrorCode, b.ErrorSummary)
		if b.ActualToolchain != "" {
			legacyFingerprint = ClassifiedFailureFingerprint(b.Stage, b.ActualToolchain, originalTerm, b.ErrorCode, b.ErrorSummary)
		}
		if b.ErrorFingerprint != legacyFingerprint {
			return b
		}
	}
	b.ExitCode = &code
	term := FailureTermination{
		Kind: b.TerminationKind, ExitCode: b.ExitCode,
		Signal: b.Signal, TimeoutMillis: b.TimeoutMillis,
	}
	if b.ErrorFingerprint != "" && (b.EvidenceQuality == EvidenceComplete || b.EvidenceQuality == EvidencePartial) {
		b.ErrorFingerprint = FailureFingerprint(b.Stage, term, b.ErrorCode, b.ErrorSummary)
		if b.ActualToolchain != "" {
			b.ErrorFingerprint = ClassifiedFailureFingerprint(b.Stage, b.ActualToolchain, term, b.ErrorCode, b.ErrorSummary)
		}
	}
	return b
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
