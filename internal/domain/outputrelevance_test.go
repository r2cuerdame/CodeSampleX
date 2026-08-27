package domain

import (
	"strings"
	"testing"
)

// goHumanize is the candidate from the live R2C-159 fixture, verbatim in
// shape: a real sample, a passing contract, graded COMPATIBLE, and about
// number formatting.
func goHumanize() SearchResult {
	return SearchResult{
		Grade:      GradeCompatible,
		Confidence: "LOW",
		SampleID:   "sha256:go-humanize",
		Case: &Case{
			Goal:     "Format integers and floats with thousand separators and SI suffixes",
			Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"},
			Symbols:  []string{"humanize.FormatInteger", "humanize.FormatFloat"},
			Contract: []string{"humanize.FormatInteger applies a custom digit-grouping format string"},
		},
		Exact:    []string{"ecosystem golang", "go", "linux alpine"},
		Evidence: EvidenceSummary{ContractPasses: 1, IndependentCrossPeers: 1, Confidence: "LOW"},
	}
}

// deployRequest is the work that was actually in hand: a GitHub Actions
// production deploy. No package, no symbol, no error — a question.
func deployRequest() SearchRequest {
	return SearchRequest{
		SchemaVersion: 2,
		Query: "GitHub Actions workflow_dispatch deploys an immutable canonical main commit, " +
			"serializes production deploys, checks out exact SHA, uploads machine-readable evidence",
		Environment: EnvironmentFingerprint{
			OS: "linux", Arch: "amd64", Ecosystem: "golang",
			Runtime: "go", RuntimeVersion: "1.26",
		},
	}
}

// The issue's live fixture. Go 1.26 and x64 are where a sample can run; they
// are not what it is about, and they are exactly what an unrelated candidate
// shares.
func TestTheLiveFixtureCandidateIsNotPromotedToNormalOutput(t *testing.T) {
	req, r := deployRequest(), goHumanize()
	if signals := r.RelevanceSignals(req, nil); len(signals) != 0 {
		t.Errorf("a number-formatting sample claimed a link to a deploy question: %v", signals)
	}
	if r.RelevantToRequest(req, nil) {
		t.Error("go-humanize was promoted into normal output for a GitHub Actions deploy question")
	}
	if got := r.SuppressionReason(req, nil); got != SuppressedInsufficientGoalOverlap {
		t.Errorf("suppression reason = %q, want %q", got, SuppressedInsufficientGoalOverlap)
	}
	if line := r.RelevanceLine(req, nil); line != "" {
		t.Errorf("a suppressed candidate manufactured a justification: %q", line)
	}
}

// Sharing a language is sharing a toolchain, not a subject.
func TestSameLanguageAloneDoesNotPromote(t *testing.T) {
	req := SearchRequest{
		Query:       "read a YAML workflow file and validate its jobs",
		Environment: EnvironmentFingerprint{Ecosystem: "golang", Runtime: "go", RuntimeVersion: "1.26"},
	}
	if goHumanize().RelevantToRequest(req, nil) {
		t.Error("two Go things were called one subject")
	}
}

// Stage, arch and runtime are coordinates. Every candidate in the corpus
// shares some of them with every caller.
func TestStageArchAndRuntimeAloneDoNotPromote(t *testing.T) {
	for _, req := range []SearchRequest{
		{Query: "the project test stage failed", Environment: EnvironmentFingerprint{Ecosystem: "golang"}},
		{Query: "cross compile for another target", Environment: EnvironmentFingerprint{Arch: "amd64", OS: "linux"}},
		{Query: "upgrade the toolchain", Environment: EnvironmentFingerprint{Runtime: "go", RuntimeVersion: "1.26"}},
	} {
		r := goHumanize()
		if r.RelevantToRequest(req, nil) {
			t.Errorf("environment overlap alone promoted a candidate for %q", req.Query)
		}
		if got := r.SuppressionReason(req, nil); got != SuppressedInsufficientGoalOverlap {
			t.Errorf("suppression reason for %q = %q", req.Query, got)
		}
	}
}

// Confidence measures how independently a sample was run. It says nothing
// about what the sample is about, so it cannot buy a promotion.
func TestHighConfidenceDoesNotSurviveACompleteSubjectMismatch(t *testing.T) {
	r := goHumanize()
	r.Confidence = "HIGH"
	r.Grade = GradeExact
	r.Evidence = EvidenceSummary{ContractPasses: 9, IndependentCrossPeers: 4, Confidence: "HIGH"}
	if r.RelevantToRequest(deployRequest(), nil) {
		t.Error("a HIGH-confidence sample about something else was promoted anyway")
	}
}

// The inverse, and the one that keeps recall honest: LOW confidence with a
// real package overlap is a real answer and still reaches normal output.
func TestLowConfidenceWithAnExactPackageOverlapIsStillOffered(t *testing.T) {
	req := SearchRequest{
		Query:    "format an integer with a custom separator",
		Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"},
	}
	r := goHumanize()
	signals := r.RelevanceSignals(req, nil)
	if !contains(signals, RelevanceSamePackage) {
		t.Fatalf("a package the caller named was not a relevance signal: %v", signals)
	}
	if got := r.SuppressionReason(req, nil); got != "" {
		t.Errorf("a same-package candidate was suppressed: %q", got)
	}
	if line := r.RelevanceLine(req, nil); !strings.Contains(line, "package you named") {
		t.Errorf("the relevance line does not say why it was shown: %q", line)
	}
}

// Same package, nearby symbol, same version: the adaptation path between
// FormatInteger and FormatFloat is explainable, which is what makes this a
// candidate rather than a coincidence.
func TestSamePackageWithANearbySymbolIsACandidate(t *testing.T) {
	req := SearchRequest{
		Query:    "humanize.Comma prints the wrong separator",
		Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"},
		Symbols:  []string{"humanize.Comma"},
	}
	signals := goHumanize().RelevanceSignals(req, nil)
	if !contains(signals, RelevanceAdjacentSymbol) {
		t.Errorf("a neighbouring API on the same owner was not recognised: %v", signals)
	}
}

// Same package, version mismatch. The gap is what the delta is for; it is
// not a reason to withhold the sample.
func TestAVersionMismatchStillLeavesAnAdaptationCandidate(t *testing.T) {
	req := SearchRequest{
		Query:    "format an integer with a custom separator",
		Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.1.0"},
	}
	r := goHumanize()
	r.Grade = GradeAdaptationRequired
	r.Adaptation = []string{"verify against v1.1.0"}
	if !r.RelevantToRequest(req, nil) {
		t.Error("a version gap suppressed a sample about the very package that was asked about")
	}
}

// The exemption that outranks everything: the caller's own sanitized failure
// fingerprint was recorded against this sample. Different package, different
// subject, and a direct evidence link to the failure in hand.
func TestAnExactFailureFingerprintPromotesAnUnrelatedPackage(t *testing.T) {
	req := SearchRequest{Query: "the build died in a native module", ErrorCode: "ERR_DLOPEN_FAILED"}
	r := goHumanize()
	r.ExactFailureMatched = true
	signals := r.RelevanceSignals(req, nil)
	if !contains(signals, RelevanceExactFailure) {
		t.Fatalf("an exact failure match was not a relevance signal: %v", signals)
	}
	if got := r.SuppressionReason(req, nil); got != "" {
		t.Errorf("a sample that matched this failure's own fingerprint was suppressed: %q", got)
	}
}

// A structured error identifier named by both sides is the "same diagnostic"
// signal, and it works when nothing else is in common.
func TestASharedStructuredErrorPromotes(t *testing.T) {
	r := SearchResult{
		Confidence: "LOW", SampleID: "sha256:esm",
		Case: &Case{
			Goal:     "Load a CommonJS-only dependency from an ESM entry point",
			Packages: []string{"pkg:npm/some-lib@2.0.0"},
			Contract: []string{"require() of an ESM module raises ERR_REQUIRE_ESM"},
		},
	}
	req := SearchRequest{Query: "the import failed", ErrorCode: "ERR_REQUIRE_ESM"}
	if !contains(r.RelevanceSignals(req, nil), RelevanceSharedDiagnostic) {
		t.Error("a structured error named by both sides was not a relevance signal")
	}
	req = SearchRequest{Query: "Error [ERR_REQUIRE_ESM]: require() of ES Module not supported"}
	if !contains(r.RelevanceSignals(req, nil), RelevanceSharedDiagnostic) {
		t.Error("an error code sitting in the query text was not recognised")
	}
}

// The false link this gate closes must not re-enter through the error
// channel: an all-caps word is not an error code.
func TestAnAllCapsWordIsNotAStructuredError(t *testing.T) {
	r := SearchResult{
		Confidence: "LOW",
		Case: &Case{
			Goal:     "Prove that the log filter truncates SHA-256 output to the first 4 bytes",
			Packages: []string{"pkg:golang/github.com/caddyserver/caddy/v2@2.11.4"},
			Symbols:  []string{"filter.hash"},
		},
	}
	if signals := r.RelevanceSignals(deployRequest(), nil); len(signals) != 0 {
		t.Errorf("\"checks out exact SHA\" was read as a structured error: %v", signals)
	}
}

// A question that names the library is the one signal a bare free-text query
// can earn, and it has to keep working.
func TestAQuestionThatNamesTheLibraryIsPromoted(t *testing.T) {
	for _, query := range []string{
		"go-humanize prints the wrong thousand separator",
		"humanize.FormatInteger prints the wrong thousand separator",
	} {
		req := SearchRequest{Query: query}
		if !contains(goHumanize().RelevanceSignals(req, nil), RelevanceNamedSubject) {
			t.Errorf("a question that named the library found nothing: %q", query)
		}
	}
}

// shopspringDecimal is the sample the live corpus actually ranked second for
// the deploy question. It is also the sample somebody asking for banker's
// rounding genuinely wants, which is what makes it the right fixture for both
// sides of the goal-semantics bar.
func shopspringDecimal() SearchResult {
	return SearchResult{
		Grade:      GradeCompatible,
		Confidence: "LOW",
		SampleID:   "sha256:shopspring-decimal",
		Case: &Case{
			Goal: "Perform exact half-to-even banker's rounding, cash rounding at standard " +
				"currency intervals, and precision-controlled division in github.com/shopspring/decimal",
			Packages: []string{"pkg:golang/github.com/shopspring/decimal@1.4.0"},
			Symbols:  []string{"decimal.Decimal.RoundBank", "decimal.Decimal.RoundCash"},
		},
		Evidence: EvidenceSummary{ContractPasses: 1, Confidence: "LOW"},
	}
}

// Describing what you want, without knowing which library does it, is the
// question this network is for. Requiring the caller to name the package
// first would mean only people who already had the answer could find it.
func TestAQuestionThatDescribesTheGoalWithoutNamingTheLibraryIsPromoted(t *testing.T) {
	req := SearchRequest{
		Query: "perform exact banker rounding of currency values with half to even " +
			"at standard cash intervals",
	}
	r := shopspringDecimal()
	signals := r.RelevanceSignals(req, nil)
	if !contains(signals, RelevanceGoalSemantics) {
		t.Fatalf("a question that described the sample's own goal found nothing: %v", signals)
	}
	if line := r.RelevanceLine(req, nil); !strings.Contains(line, "describes the operation") {
		t.Errorf("the relevance line does not name the link that earned it: %q", line)
	}
}

// And the other side of the same bar, on the live fixture: the deploy
// question shares one word with each of the three samples the corpus offered
// it — "exact", "checks", "sha" — and one word is a coincidence.
func TestTheDeployQuestionDoesNotReachTheGoalSemanticsBar(t *testing.T) {
	req := deployRequest()
	for _, r := range []SearchResult{shopspringDecimal(), goHumanize(), {
		Confidence: "LOW", SampleID: "sha256:caddy",
		Case: &Case{
			Goal: "Prove that Caddy's filter.hash log filter truncates SHA-256 output to " +
				"the first 4 bytes rather than producing a full hash",
			Packages: []string{"pkg:golang/github.com/caddyserver/caddy/v2@2.11.4"},
			Symbols:  []string{"filter.hash"},
		},
	}} {
		if signals := r.RelevanceSignals(req, nil); len(signals) != 0 {
			t.Errorf("%s claimed a link to a deploy question: %v", r.SampleID, signals)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// The recurrence. The gate above closed the environment-overlap hole and the
// same deploy question came back with pgx.Conn.Begin and
// registry.LOCAL_MACHINE anyway, both graded COMPATIBLE, both in normal
// output. The debug trace named the signal that promoted them:
// query-names-subject.
//
// Nothing named either library. What happened is that a declared symbol was
// shredded into words and every word became an identifier: "commit" out of
// pgx.Tx.Commit met "immutable canonical main commit", and "machine" out of
// registry.LOCAL_MACHINE met "machine-readable evidence". The same shredding
// is what put pytest.main, Express dispatch and certifi.__main__ in front of
// the same question through the words "main" and "dispatch".
//
// A member name is chosen to describe an operation inside a namespace, not to
// identify a subject across a corpus, and the corpus is full of Commit, Main,
// Dispatch and Machine. Naming a subject means using a name that identifies
// it.
func liveCorpusSubtokenCandidates() []SearchResult {
	return []SearchResult{{
		Grade: GradeCompatible, Confidence: "LOW", SampleID: "sha256:pgx-begin",
		Case: &Case{
			Goal: "Use pgx v5.10.0 Conn.Begin without assuming context cancellation manages " +
				"transaction lifetime, and prove explicit/deferred rollback behavior against a " +
				"deterministic PostgreSQL wire stub.",
			Packages: []string{"pkg:golang/github.com/jackc/pgx/v5@v5.10.0"},
			Symbols: []string{"pgx.Conn.Begin", "pgx.Tx.Commit", "pgx.Tx.Rollback",
				"pgx.ErrTxClosed"},
		},
	}, {
		Grade: GradeCompatible, Confidence: "MEDIUM", SampleID: "sha256:xsys-registry",
		Case: &Case{
			Goal:     "verify golang.org/x/sys/windows/registry.LOCAL_MACHINE in pkg:golang/golang.org/x/sys@v0.47.0",
			Packages: []string{"pkg:golang/golang.org/x/sys@v0.47.0"},
			Symbols:  []string{"golang.org/x/sys/windows/registry.LOCAL_MACHINE"},
		},
	}, {
		Grade: GradeCompatible, Confidence: "LOW", SampleID: "sha256:pytest-main",
		Case: &Case{
			Goal:     "Run a test session in-process and read the exit status it returns",
			Packages: []string{"pkg:pypi/pytest@8.3.3"},
			Symbols:  []string{"pytest.main"},
		},
	}, {
		Grade: GradeCompatible, Confidence: "LOW", SampleID: "sha256:express-dispatch",
		Case: &Case{
			Goal:     "Mount a router and observe how a layer forwards a request down the stack",
			Packages: []string{"pkg:npm/express@4.21.1"},
			Symbols:  []string{"express.Router.dispatch"},
		},
	}, {
		Grade: GradeCompatible, Confidence: "LOW", SampleID: "sha256:certifi-main",
		Case: &Case{
			Goal:     "Print the path of the bundled CA store from the command line",
			Packages: []string{"pkg:pypi/certifi@2024.8.30"},
			Symbols:  []string{"certifi.__main__"},
		},
	}}
}

func TestASymbolSubtokenDoesNotNameTheSubject(t *testing.T) {
	req := deployRequest()
	for _, r := range liveCorpusSubtokenCandidates() {
		if signals := r.RelevanceSignals(req, nil); len(signals) != 0 {
			t.Errorf("%s claimed a link to a deploy question: %v", r.SampleID, signals)
		}
		if r.RelevantToRequest(req, nil) {
			t.Errorf("%s was promoted into normal output for a deploy question", r.SampleID)
		}
		if got := r.SuppressionReason(req, nil); got != SuppressedInsufficientGoalOverlap {
			t.Errorf("%s suppression reason = %q, want %q", r.SampleID, got,
				SuppressedInsufficientGoalOverlap)
		}
	}
}

// Everything the gate still has to let through. A coined member name is an
// identifier even on its own — nobody writes "RoundBank" by accident — and a
// qualified one is an identifier however common its last word is.
func TestNamingASymbolStillPromotes(t *testing.T) {
	cases := []struct {
		query string
		r     SearchResult
	}{
		{"why does RoundBank turn 2.5 into 2", shopspringDecimal()},
		{"decimal.Decimal.RoundCash is off by a cent", shopspringDecimal()},
		{"humanize.FormatInteger prints the wrong thousand separator", goHumanize()},
		{"FormatFloat drops my SI suffix", goHumanize()},
		{"pgx.Tx.Commit returns ErrTxClosed after a deferred rollback",
			liveCorpusSubtokenCandidates()[0]},
		{"reading registry.LOCAL_MACHINE fails under wine",
			liveCorpusSubtokenCandidates()[1]},
		{"pytest exits 0 when no tests ran", liveCorpusSubtokenCandidates()[2]},
	}
	for _, c := range cases {
		req := SearchRequest{Query: c.query}
		if !contains(c.r.RelevanceSignals(req, nil), RelevanceNamedSubject) {
			t.Errorf("a question that named the subject found nothing: %q -> %v",
				c.query, c.r.RelevanceSignals(req, nil))
		}
	}
}
