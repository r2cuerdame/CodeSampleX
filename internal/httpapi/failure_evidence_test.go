package httpapi

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
)

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
