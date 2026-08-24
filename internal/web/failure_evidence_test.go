package web

import (
	"os"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestFailureClusterRendersTerminationSummaryQualityAndDiagnosticCandidate(t *testing.T) {
	views := buildClusters([]failureCluster{{
		Stage: "PROJECT_TEST", TerminationKind: string(domain.TerminationTimeout), TimeoutMillis: 600000,
		ErrorSummary: "go test timeout after <duration>", Fingerprint: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Count: 147, EvidenceQuality: string(domain.EvidenceComplete), DiagnosticCandidate: false,
		EnvVariants: []domain.FailureEnvironmentVariant{{Summary: map[string]string{"os": "windows", "runtime": "go@1.26"}, Count: 147}},
	}})
	if len(views) != 1 {
		t.Fatalf("views = %d", len(views))
	}
	got := views[0]
	if got.Termination != "timeout 10m" || got.ErrorSummary != "go test timeout after <duration>" || got.EvidenceQuality != "complete" {
		t.Fatalf("view = %+v", got)
	}
}

func TestEvidenceGapNeverRendersRawNoErrorCodeCopy(t *testing.T) {
	views := buildClusters([]failureCluster{{
		Stage: "PROJECT_TEST", Count: 227, EvidenceQuality: string(domain.EvidenceLegacyIncomplete), DiagnosticCandidate: true,
	}})
	if len(views) != 1 || !views[0].EvidenceGap || !views[0].DiagnosticCandidate {
		t.Fatalf("gap view = %+v", views)
	}
	b, err := os.ReadFile("templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	for _, forbidden := range []string{"no error code recorded", "에러 코드 기록 없음"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("raw no-error-code copy survived: %q", forbidden)
		}
	}
	for _, want := range []string{"symbol.evidence_gap", "symbol.evidence_quality", "symbol.diagnostic_candidate"} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q: %s", want, page)
		}
	}
}
