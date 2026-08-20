package web

import (
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func fpCluster(symbol, fp string, count int64) failureCluster {
	return failureCluster{
		Symbol: symbol, Stage: "PROJECT_TEST", Fingerprint: fp,
		Count: count, Versions: []string{"v5.10.0"},
		EnvSummary: map[string]string{"os": "windows", "runtime": "go@1.26"},
	}
}

// The recorder files one observation against the package AND one against
// every symbol it detected, so one broken build arrives as several clusters
// with the SAME fingerprint. pgx v5.10.0 listed the same 181 failures twice —
// once as the package and once as pgx/v5.Conn — and a reader had to work out
// they were one event.
//
// They are one failure seen at two grains. It is listed once, and the symbols
// it touched are named on it. The count is the largest, never the sum: the
// package-level count already contains the symbol's.
func TestOneFailureSeenAtTwoGrainsIsListedOnce(t *testing.T) {
	got := buildClusters([]failureCluster{
		fpCluster("", "sha256:219c01", 181),
		fpCluster("github.com/jackc/pgx/v5.Conn", "sha256:219c01", 181),
	})
	if len(got) != 1 {
		t.Fatalf("clusters = %d, want the one failure they both describe", len(got))
	}
	if got[0].Count != 181 {
		t.Errorf("count = %d, want 181 and never their sum", got[0].Count)
	}
	if got[0].Symbol != "github.com/jackc/pgx/v5.Conn" {
		t.Errorf("symbol = %q, want the symbol the package-level row could not name", got[0].Symbol)
	}
}

// Different fingerprints are different failures however alike they look.
func TestDifferentFingerprintsStayApart(t *testing.T) {
	got := buildClusters([]failureCluster{
		fpCluster("", "sha256:aaa", 3),
		fpCluster("", "sha256:bbb", 5),
	})
	if len(got) != 2 {
		t.Errorf("clusters = %d, want both failures", len(got))
	}
}

// The same fingerprint in a different environment is a different fact: it
// says the failure reproduces there too, which is the thing worth knowing.
func TestTheSameFailureInAnotherEnvironmentIsItsOwnRow(t *testing.T) {
	a := fpCluster("", "sha256:219c01", 181)
	b := fpCluster("", "sha256:219c01", 4)
	b.EnvSummary = map[string]string{"os": "linux", "runtime": "go@1.26"}
	if got := buildClusters([]failureCluster{a, b}); len(got) != 2 {
		t.Errorf("clusters = %d, want one per environment", len(got))
	}
}

// UNKNOWN at full confidence is the sanitizer saying it could not tell, which
// the missing error code beside it already says. Every Go test failure in
// production carried exactly that chip, under a note explaining that
// hypotheses are inference rather than measurement — noise dressed as
// analysis, on the row where the reader most needs to read carefully.
func TestAnUnknownHypothesisIsNotAnAnalysis(t *testing.T) {
	c := fpCluster("", "sha256:aaa", 3)
	c.Hypotheses = []domain.FailureHypothesis{{Domain: domain.FailUnknown, Confidence: 1}}
	got := buildClusters([]failureCluster{c})
	if len(got[0].Hypotheses) != 0 {
		t.Errorf("hypotheses = %+v, want the row to say nothing rather than \"UNKNOWN 100%%\"",
			got[0].Hypotheses)
	}
}

// A hypothesis that names a domain is worth reading.
func TestANamedHypothesisSurvives(t *testing.T) {
	c := fpCluster("", "sha256:bbb", 3)
	c.Hypotheses = []domain.FailureHypothesis{
		{Domain: domain.FailUnknown, Confidence: 0.3},
		{Domain: "CONFIGURATION", Confidence: 0.7},
	}
	got := buildClusters([]failureCluster{c})
	if len(got[0].Hypotheses) != 1 || got[0].Hypotheses[0].Domain != "CONFIGURATION" {
		t.Errorf("hypotheses = %+v, want the named one kept", got[0].Hypotheses)
	}
}
