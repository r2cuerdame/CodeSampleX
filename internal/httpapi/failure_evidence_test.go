package httpapi

import (
	"strconv"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
)

func TestReceiptFailureEvidenceEnforcesExitCodeWireBoundary(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("legacy uint32 exit status does not fit in int on this architecture")
	}
	makeReceipt := func(code int) domain.VerificationReceipt {
		term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}
		summary := "process exited without a conventional status"
		failure := domain.FailureEvidence{
			TerminationKind: domain.TerminationExit,
			ExitCode:        &code,
			ErrorSummary:    summary,
			EvidenceQuality: domain.EvidenceComplete,
		}
		failure.Fingerprint = domain.FailureFingerprint(domain.StageContract, term, "", summary)
		return domain.VerificationReceipt{
			SchemaVersion: 2,
			Stages:        map[string]string{"contract": "FAIL"},
			StageFailures: map[string]domain.FailureEvidence{"contract": failure},
		}
	}

	legacyWindows := int(uint64(1<<32 - 1))
	if err := receiptFailureEvidenceIsSafe(makeReceipt(legacyWindows)); err != nil {
		t.Fatalf("matching legacy Windows receipt rejected: %v", err)
	}
	for _, code := range []int{int(int64(-1<<31) - 1), int(uint64(1 << 32))} {
		if err := receiptFailureEvidenceIsSafe(makeReceipt(code)); err == nil || !strings.Contains(err.Error(), "exitCode") {
			t.Errorf("out-of-bound exitCode %d accepted: %v", code, err)
		}
	}
}

func TestReceiptFailureEvidenceAcceptsCanonicalAndRejectsRawOrMismatchedData(t *testing.T) {
	code := 1
	failure := sanitizer.SanitizeFailure("at render (/tmp/app/node_modules/react/index.js:42:7): connection refused 127.0.0.1:5432", domain.StageContract,
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &code}, []string{"react"})
	if strings.Contains(failure.ErrorSummary, "node_modules/react") {
		t.Fatalf("receipt failure retained client-only package path token: %q", failure.ErrorSummary)
	}
	receipt := domain.VerificationReceipt{SchemaVersion: 2,
		Stages:        map[string]string{"contract": "FAIL"},
		StageFailures: map[string]domain.FailureEvidence{"contract": failure}}
	if err := receiptFailureEvidenceIsSafe(receipt); err != nil {
		t.Fatalf("canonical failure rejected: %v", err)
	}

	raw := receipt
	raw.StageFailures = map[string]domain.FailureEvidence{"contract": failure}
	leaked := raw.StageFailures["contract"]
	leaked.ErrorSummary = `C:\Users\alice\secret\contract.go:14 connection refused`
	raw.StageFailures["contract"] = leaked
	if err := receiptFailureEvidenceIsSafe(raw); err == nil || !strings.Contains(err.Error(), "secret-safe") {
		t.Fatalf("raw path was accepted: %v", err)
	}

	mismatched := receipt
	mismatched.Stages = map[string]string{"contract": "PASS"}
	if err := receiptFailureEvidenceIsSafe(mismatched); err == nil {
		t.Fatal("failure evidence was accepted on a PASS stage")
	}
}

func TestReceiptFailureEvidenceDoesNotApplyVerifierUsername(t *testing.T) {
	exitCode := 1
	summary := "FAIL example.com/csx-production-modern-failure-canary: connection refused"
	term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode}
	failure := domain.FailureEvidence{
		TerminationKind: domain.TerminationExit, ExitCode: &exitCode,
		ErrorSummary: summary, EvidenceQuality: domain.EvidenceComplete,
	}
	failure.Fingerprint = domain.FailureFingerprint(domain.StageContract, term, "", summary)
	receipt := domain.VerificationReceipt{SchemaVersion: 2,
		Stages: map[string]string{"contract": "FAIL"}, StageFailures: map[string]domain.FailureEvidence{"contract": failure}}
	if err := receiptFailureEvidenceIsSafe(receipt); err != nil {
		t.Fatalf("producer-canonical summary containing the verifier account name was rejected: %v", err)
	}
}

func TestReceiptFailureEvidenceValidatesEveryLineageCoordinate(t *testing.T) {
	base := domain.VerificationReceipt{
		SchemaVersion: 2,
		Stages:        map[string]string{"contract": "FAIL"},
		StageFailures: map[string]domain.FailureEvidence{
			"contract": {EvidenceQuality: domain.EvidenceMissing},
		},
	}
	if err := receiptFailureEvidenceIsSafe(base); err != nil {
		t.Fatalf("empty safe lineage rejected: %v", err)
	}

	for _, tc := range []struct {
		name string
		mut  func(*domain.FailureEvidence)
		want string
	}{
		{"outer command allowlist", func(f *domain.FailureEvidence) { f.OuterCommand = "secret-project test" }, "outerCommand"},
		{"outer command canonical spacing", func(f *domain.FailureEvidence) { f.OuterCommand = "go  test" }, "outerCommand"},
		{"outer command cap", func(f *domain.FailureEvidence) { f.OuterCommand = strings.Repeat("g", 33) }, "outerCommand"},
		{"outer stage vocabulary", func(f *domain.FailureEvidence) { f.OuterStage = domain.StageContract }, "outerStage"},
		{"toolchain coordinate", func(f *domain.FailureEvidence) { f.ActualToolchain = `C:\Users\alice\private` }, "actualToolchain"},
		{"toolchain cap", func(f *domain.FailureEvidence) { f.ActualToolchain = strings.Repeat("a", 65) }, "actualToolchain"},
		{"stage evidence vocabulary", func(f *domain.FailureEvidence) { f.StageEvidence = "model-guessed" }, "stageEvidence"},
		{"evidence gap vocabulary", func(f *domain.FailureEvidence) { f.EvidenceGap = "private-log-missing" }, "evidenceGap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt := base
			failure := receipt.StageFailures["contract"]
			tc.mut(&failure)
			receipt.StageFailures = map[string]domain.FailureEvidence{"contract": failure}
			err := receiptFailureEvidenceIsSafe(receipt)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid lineage error = %v, want %q", err, tc.want)
			}
		})
	}
}
