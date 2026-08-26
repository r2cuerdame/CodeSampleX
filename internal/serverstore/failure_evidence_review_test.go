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
// Keep this file in the normal PR CI path so final review repairs are rechecked.
func reviewBatch(f domain.FailureEvidence) domain.ObservationBatch {
	return domain.ObservationBatch{
		SchemaVersion:      2,
		Epoch:              "2026-08-24",
		AnonID:             "anon-review",
		ProjectBucket:      "project-review",
		Package:            "pkg:npm/react@19.1.1",
		Environment:        domain.EnvironmentFingerprint{SchemaVersion: 1, Ecosystem: "npm", OS: "linux", Arch: "amd64"},
		Stage:              domain.StageProjectTest,
		Result:             domain.ResultFail,
		ObservationCount:   1,
		ErrorFingerprint:   f.Fingerprint,
		ErrorCode:          f.ErrorCode,
		TerminationKind:    f.TerminationKind,
		ExitCode:           f.ExitCode,
		Signal:             f.Signal,
		TimeoutMillis:      f.TimeoutMillis,
		ErrorSummary:       f.ErrorSummary,
		EvidenceQuality:    f.EvidenceQuality,
		OuterCommand:       f.OuterCommand,
		OuterStage:         f.OuterStage,
		ActualToolchain:    f.ActualToolchain,
		StageEvidence:      f.StageEvidence,
		FailureEvidenceGap: f.EvidenceGap,
	}
}

func TestValidateBatchKeepsV1AndV2FingerprintContractsSeparate(t *testing.T) {
	exitCode := 1
	term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode}
	legacy := sanitizer.SanitizeFailure("ERR_RENDER_FAILED while rendering component",
		domain.StageProjectTest, term, nil)
	v1 := reviewBatch(legacy)
	v1.SchemaVersion = 1
	if err := ValidateBatch(v1); err != nil {
		t.Fatalf("legacy v1 failure rejected by new server: %v", err)
	}

	classified := sanitizer.SanitizeClassifiedFailure("src/index.ts(12,5): error TS2352: bad conversion",
		domain.StageProjectCompile, term, nil, "go test", domain.StageProjectTest,
		"typescript/tsc", domain.FailureStageCompilerDiagnostic, "")
	v2 := reviewBatch(classified)
	v2.Stage = domain.StageProjectCompile
	if err := ValidateBatch(v2); err != nil {
		t.Fatalf("classified v2 failure rejected by new server: %v", err)
	}
	v2.SchemaVersion = 1
	if err := ValidateBatch(v2); err == nil || !strings.Contains(err.Error(), "must not contain failure lineage") {
		t.Fatalf("classified failure masqueraded as v1: %v", err)
	}
}

func TestValidateBatchVersionsActualStageVocabulary(t *testing.T) {
	b := obsBatch("anonaaaa", "projaaaa", 1)
	b.Stage = domain.StageProjectResolve
	if err := ValidateBatch(b); err == nil {
		t.Fatal("v1 batch used the v2 PROJECT_RESOLVE vocabulary")
	}
	b.SchemaVersion = 2
	if err := ValidateBatch(b); err != nil {
		t.Fatalf("v2 PROJECT_RESOLVE batch rejected: %v", err)
	}
}

func TestValidateBatchAcceptsClassifiedV3LineageAndRejectsPrivateOuterCommand(t *testing.T) {
	exitCode := 1
	term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode}
	f := sanitizer.SanitizeClassifiedFailure("src/index.ts(12,5): error TS2352: bad conversion",
		domain.StageProjectCompile, term, nil, "go test", domain.StageProjectTest,
		"typescript/tsc", domain.FailureStageCompilerDiagnostic, "")
	b := reviewBatch(f)
	b.Stage = domain.StageProjectCompile
	if err := ValidateBatch(b); err != nil {
		t.Fatalf("canonical v3 lineage rejected: %v", err)
	}
	b.OuterCommand = "secret-project test"
	if err := ValidateBatch(b); err == nil || !strings.Contains(err.Error(), "known public tool") {
		t.Fatalf("private outer command accepted: %v", err)
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

func TestValidateBatchDoesNotTreatValidatorUsernameAsProducerPII(t *testing.T) {
	exitCode := 1
	summary := "FAIL example.com/csx-production-modern-failure-canary: connection refused"
	term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode}
	f := domain.FailureEvidence{
		TerminationKind: domain.TerminationExit, ExitCode: &exitCode,
		ErrorSummary: summary, EvidenceQuality: domain.EvidenceComplete,
	}
	f.Fingerprint = domain.FailureFingerprint(domain.StageProjectTest, term, "", summary)
	if err := ValidateBatch(reviewBatch(f)); err != nil {
		t.Fatalf("producer-canonical summary containing the production account name was rejected: %v", err)
	}
}

func TestFakeEvidenceAggregateRetainsEveryOuterCommand(t *testing.T) {
	exitCode := 1
	term := domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: &exitCode}
	classified := func(outer string) domain.ObservationBatch {
		f := sanitizer.SanitizeClassifiedFailure("src/index.ts(12,5): error TS2352: bad conversion",
			domain.StageProjectCompile, term, nil, outer, domain.StageProjectTest,
			"typescript/tsc", domain.FailureStageCompilerDiagnostic, "")
		b := reviewBatch(f)
		b.Stage = domain.StageProjectCompile
		return b
	}
	first := classified("go test")
	second := classified("npm test")
	second.AnonID = "anon-review-two"
	second.ProjectBucket = "project-review-two"

	store := NewFake()
	if accepted, rejected, err := store.IngestBatches(t.Context(), []domain.ObservationBatch{first, second}); err != nil || accepted != 2 || len(rejected) != 0 {
		t.Fatalf("ingest = accepted %d, rejected %+v, err %v", accepted, rejected, err)
	}
	rows, err := store.EvidenceForTarget(t.Context(), first.Package, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || strings.Join(rows[0].OuterCommands, ",") != "go test,npm test" {
		t.Fatalf("aggregate outer commands = %+v", rows)
	}
}
