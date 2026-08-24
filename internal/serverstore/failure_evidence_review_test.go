package serverstore

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
)

// These regressions pin the review boundary end-to-end: public v2 failure
// evidence must be independently canonical at ingest, and its fingerprint
// must be derived from structured evidence rather than trusted from a client.
func reviewBatch(f domain.FailureEvidence) domain.ObservationBatch {
	return domain.ObservationBatch{
		SchemaVersion:    1,
		Epoch:            "2026-08-24",
		AnonID:           "anon-review",
		ProjectBucket:    "project-review",
		Package:          "pkg:npm/react@19.1.1",
		Environment:      domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64"},
		Stage:            domain.StageProjectTest,
		Result:           domain.ResultFail,
		ObservationCount: 1,
		ErrorFingerprint: f.Fingerprint,
		ErrorCode:        f.ErrorCode,
		TerminationKind:  f.TerminationKind,
		ExitCode:         f.ExitCode,
		Signal:           f.Signal,
		TimeoutMillis:    f.TimeoutMillis,
		ErrorSummary:     f.ErrorSummary,
		EvidenceQuality:  f.EvidenceQuality,
	}
}

func TestValidateBatchAcceptsCanonicalFailureWithoutNodeModuleLeak(t *testing.T) {
	exitCode := 1
	term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode}
	f := sanitizer.SanitizeFailure(
		"at render (/tmp/app/node_modules/react/index.js:42:7): ERR_RENDER_FAILED",
		domain.StageProjectTest,
		term,
		[]string{"react"},
	)
	if strings.Contains(f.ErrorSummary, "node_modules/react") {
		t.Fatalf("public failure evidence retained a client-only package path token: %q", f.ErrorSummary)
	}
	if !strings.Contains(f.ErrorSummary, "<path>") {
		t.Fatalf("node_modules path was not normalized: %q", f.ErrorSummary)
	}
	if err := ValidateBatch(reviewBatch(f)); err != nil {
		t.Fatalf("canonical v2 summary was rejected: %v", err)
	}
}

func TestValidateBatchRejectsFingerprintThatDoesNotMatchEvidence(t *testing.T) {
	exitCode := 1
	term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode}
	f := sanitizer.SanitizeFailure(
		"ERR_RENDER_FAILED while rendering component",
		domain.StageProjectTest,
		term,
		[]string{"react"},
	)
	b := reviewBatch(f)
	b.ErrorFingerprint = "sha256:" + strings.Repeat("0", 64)
	if err := ValidateBatch(b); err == nil || !strings.Contains(err.Error(), "does not match structured failure evidence") {
		t.Fatalf("mismatched modern fingerprint accepted: %v", err)
	}
}
