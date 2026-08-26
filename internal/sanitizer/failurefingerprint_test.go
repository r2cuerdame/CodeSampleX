package sanitizer

import (
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestFailureFingerprintNormalizesVolatileAndSecretMaterial(t *testing.T) {
	a := SanitizeFailure(
		`C:\Users\alice\AppData\Local\Temp\csx-123\main.go:41: pid 8123 token abcdef0123456789abcdef0123456789: connection refused 127.0.0.1:5432`,
		domain.StageProjectTest,
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: intPointer(1)},
		nil,
	)
	b := SanitizeFailure(
		`D:\Temp\csx-999\main.go:98: pid 9921 token deadbeefdeadbeefdeadbeefdeadbeef: connection refused 127.0.0.1:6432`,
		domain.StageProjectTest,
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: intPointer(1)},
		nil,
	)
	if a.Fingerprint != b.Fingerprint {
		t.Fatalf("volatile path/PID/port/token split one failure: %q != %q\n%s\n%s", a.Fingerprint, b.Fingerprint, a.ErrorSummary, b.ErrorSummary)
	}
	for _, secret := range []string{"alice", "8123", "abcdef0123456789", `C:\\Users`} {
		if strings.Contains(a.ErrorSummary, secret) {
			t.Errorf("secret or volatile material %q survived in %q", secret, a.ErrorSummary)
		}
	}
	if !strings.Contains(a.ErrorSummary, "connection refused") {
		t.Fatalf("normalized summary lost the useful error: %q", a.ErrorSummary)
	}
}

func TestFailureFingerprintSeparatesTerminationAndRootError(t *testing.T) {
	exit := SanitizeFailure("connection refused", domain.StageProjectTest,
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: intPointer(1)}, nil)
	timeout := SanitizeFailure("connection refused", domain.StageProjectTest,
		domain.FailureTermination{Kind: domain.TerminationTimeout, TimeoutMillis: 600000}, nil)
	different := SanitizeFailure("permission denied", domain.StageProjectTest,
		domain.FailureTermination{Kind: domain.TerminationExit, ExitCode: intPointer(1)}, nil)
	if exit.Fingerprint == timeout.Fingerprint {
		t.Error("timeout collapsed into exit failure")
	}
	if exit.Fingerprint == different.Fingerprint {
		t.Error("different normalized errors collapsed into one fingerprint")
	}
}

func TestSilentFailureIsEvidenceMissingNotAFingerprint(t *testing.T) {
	got := SanitizeFailure("", domain.StageProjectTest, domain.FailureTermination{}, nil)
	if got.Fingerprint != "" || got.EvidenceQuality != domain.EvidenceMissing {
		t.Fatalf("silent unstructured failure = %+v, want evidence-missing without fingerprint", got)
	}
}

func TestNonExitTerminationKindsStayStructuredAndDistinct(t *testing.T) {
	cases := []domain.FailureTermination{
		{Kind: domain.TerminationTimeout, TimeoutMillis: 300000},
		{Kind: domain.TerminationSignal, Signal: "sigkill"},
		{Kind: domain.TerminationProcessStartFailed},
	}
	seen := map[string]bool{}
	for _, term := range cases {
		got := SanitizeFailure("command failed", domain.StageProjectTest, term, nil)
		if got.TerminationKind != term.Kind || got.EvidenceQuality != domain.EvidenceComplete || got.Fingerprint == "" {
			t.Errorf("termination %s = %+v", term.Kind, got)
		}
		if seen[got.Fingerprint] {
			t.Errorf("termination %s collapsed into another kind", term.Kind)
		}
		seen[got.Fingerprint] = true
	}
}

func TestCanonicalPublicErrorSummaryDoesNotApplyValidatorUsername(t *testing.T) {
	summary := "FAIL example.com/csx-production-modern-failure-canary: connection refused"
	if got := CanonicalPublicErrorSummary(summary, domain.StageProjectTest); got != summary {
		t.Fatalf("portable canonical summary = %q, want %q", got, summary)
	}
}

func intPointer(v int) *int { return &v }
