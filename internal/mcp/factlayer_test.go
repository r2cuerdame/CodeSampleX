package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// The R2C-159 live fixture, on the surface it actually happened on.
//
// The work in hand was a GitHub Actions production deploy: workflow_dispatch,
// an immutable canonical main SHA, deploy serialization, machine-readable
// evidence upload. The network answered with FormatInteger/FormatFloat from
// github.com/dustin/go-humanize at MATCH: COMPATIBLE, CONFIDENCE: LOW,
// REFERENCE_CANDIDATE.
//
// Nothing about the sample is wrong. Its contract ran in a pinned container
// and passed. What it has never been is a fact about deploying anything: the
// only thing it had in common with the question was Go on linux/amd64, which
// is a statement about where it can RUN. Normal output is a fact layer, and a
// sample that contributes no fact about the caller's case does not belong in
// it — the label around it says reference, and the body an agent reads says
// COMPATIBLE.
func goHumanizeHit() domain.SearchResponse {
	return domain.SearchResponse{Results: []domain.SearchResult{{
		Grade:      domain.GradeCompatible,
		Confidence: "LOW",
		Score:      0.67,
		SampleID:   "sha256:go-humanize-live",
		Case: &domain.Case{
			Goal:     "Format integers and floats with thousand separators and SI suffixes",
			Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"},
			Symbols:  []string{"humanize.FormatInteger", "humanize.FormatFloat"},
			Contract: []string{"humanize.FormatInteger applies a custom digit-grouping format string"},
		},
		Exact:    []string{"ecosystem golang", "go", "linux alpine"},
		Evidence: domain.EvidenceSummary{ContractPasses: 1, IndependentCrossPeers: 1, Confidence: "LOW"},
	}}}
}

const deployQuery = "GitHub Actions workflow_dispatch deploys an immutable canonical main commit, " +
	"serializes production deploys, checks out exact SHA, uploads machine-readable evidence"

func TestTheDeployQuestionIsNoLongerAnsweredWithANumberFormatter(t *testing.T) {
	deps := emptyDeps()
	deps.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
		return goHumanizeHit(), "offer-1"
	}
	c := startServer(t, deps)

	res := callTool(t, c, "search_known_solution", map[string]any{
		"query": deployQuery,
		"environment": map[string]any{
			"ecosystem": "golang", "os": "linux", "arch": "amd64",
			"runtime": "go", "runtimeVersion": "1.26",
		},
	})

	text := toolText(t, res)
	for _, forbidden := range []string{
		"sha256:go-humanize-live", "go-humanize", "FormatInteger",
		"MATCH: COMPATIBLE", "REFERENCE_CANDIDATE",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("normal output still carries %q:\n%s", forbidden, text)
		}
	}
	// NO_SAFE_MATCH is the honest answer and is kept as one. It is not
	// replaced by a forced HIT, and the forced REFERENCE is what it used to
	// be replaced BY.
	if !strings.Contains(text, "NO_SAFE_MATCH") {
		t.Errorf("the answer is neither the sample nor a miss:\n%s", text)
	}
	structured, _ := res["structuredContent"].(map[string]any)
	response, _ := structured["response"].(map[string]any)
	if response["miss"] != true {
		t.Errorf("structured payload does not report a miss: %s", mustJSON(t, structured))
	}
	if raw, err := json.Marshal(structured); err == nil &&
		strings.Contains(string(raw), "go-humanize") {
		t.Errorf("the suppressed candidate came back through the structured channel: %s", raw)
	}
}

// Suppressed is not deleted. The decision has to be inspectable by whoever
// asks for it, and invisible to everybody who did not — a diagnostic that
// ships in every answer is not a diagnostic, it is the candidate again in a
// neighbouring field.
func TestTheSuppressedCandidateIsVisibleUnderDebug(t *testing.T) {
	t.Setenv("CSX_DEBUG", "1")
	deps := emptyDeps()
	deps.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
		return goHumanizeHit(), "offer-1"
	}
	c := startServer(t, deps)

	res := callTool(t, c, "search_known_solution", map[string]any{"query": deployQuery})
	structured, _ := res["structuredContent"].(map[string]any)
	suppressed, _ := structured["suppressed"].([]any)
	if len(suppressed) != 1 {
		t.Fatalf("debug output does not carry the suppressed candidate: %s", mustJSON(t, structured))
	}
	entry, _ := suppressed[0].(map[string]any)
	if entry["suppressedReason"] != domain.SuppressedInsufficientGoalOverlap {
		t.Errorf("suppressedReason = %v, want %q", entry["suppressedReason"],
			domain.SuppressedInsufficientGoalOverlap)
	}
	if entry["sampleId"] != "sha256:go-humanize-live" {
		t.Errorf("the diagnostic does not name the candidate: %v", entry)
	}
	// Coordinates and a reason. Not the contract, not the evidence counts,
	// not a grade header: a suppressed candidate that ships its body has not
	// been suppressed.
	raw := mustJSON(t, entry)
	for _, body := range []string{
		"Format integers and floats",      // the goal sentence
		"applies a custom digit-grouping", // a contract line
		"humanize.FormatInteger",          // a declared symbol
		"contractPasses",                  // the evidence counts
	} {
		if strings.Contains(raw, body) {
			t.Errorf("the diagnostic reproduced the sample body (%q): %s", body, raw)
		}
	}
	// The human-readable half stays a miss either way.
	if !strings.Contains(toolText(t, res), "NO_SAFE_MATCH") {
		t.Errorf("debug mode changed the answer:\n%s", toolText(t, res))
	}
}

// Recall is the other half of the contract. A question that names the library
// still gets the sample, and now says out loud why it got it.
func TestAQuestionThatNamesTheLibraryStillGetsTheSample(t *testing.T) {
	deps := emptyDeps()
	deps.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
		return goHumanizeHit(), "offer-1"
	}
	c := startServer(t, deps)

	res := callTool(t, c, "search_known_solution", map[string]any{
		"query":    "humanize.FormatInteger prints the wrong thousand separator",
		"packages": []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"},
		"symbols":  []string{"humanize.FormatInteger"},
	})
	text := toolText(t, res)
	if !strings.Contains(text, "sha256:go-humanize-live") {
		t.Errorf("a sample about the very package that was asked about was suppressed:\n%s", text)
	}
	if !strings.Contains(text, "Relevance: ") {
		t.Errorf("the answer never says why this sample is in front of the caller:\n%s", text)
	}
	if !strings.Contains(text, "package you named") {
		t.Errorf("the relevance line does not name the link that earned it:\n%s", text)
	}
}

// The same gate, on the automatic lookup after a failed command, in the case
// the ecosystem gate cannot see: a Go build answered with a Go sample about
// something else.
func TestASameEcosystemButUnrelatedSampleIsDemotedAfterAFailure(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_COMPILE", "FAIL", []string{"deploy/workflow.go<path>: undefined: workflowDispatch"},
				commandOutput{Stderr: "deploy/workflow.go:31:2: undefined: workflowDispatch"}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			return goHumanizeHit(), "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "go", "build", "./..."))
	m := structured(t, out)
	recommendation, _ := m["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != domain.RecommendationNoRelevantMatch {
		t.Errorf("status = %v, want %v: %s", got, domain.RecommendationNoRelevantMatch,
			mustJSON(t, recommendation))
	}
	if got := recommendation["suppressedReason"]; got != domain.SuppressedInsufficientGoalOverlap {
		t.Errorf("suppressedReason = %v, want %q: %s", got,
			domain.SuppressedInsufficientGoalOverlap, mustJSON(t, recommendation))
	}
	body, _ := recommendation["text"].(string)
	for _, forbidden := range []string{"REUSE_VERIFIED", "MATCH: COMPATIBLE", "Proven by its contract"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("an unrelated same-ecosystem sample is still presented as %q:\n%s", forbidden, body)
		}
	}
	// The demoted note must not claim the wrong reason. This sample is Go
	// and so is the command; saying otherwise would be a fabricated fact in
	// the one place the product promises not to fabricate.
	if strings.Contains(body, "does not build for") {
		t.Errorf("the note blames the ecosystem for a same-ecosystem sample:\n%s", body)
	}
	if !strings.Contains(body, "sha256:go-humanize-live") {
		t.Errorf("the candidate was deleted rather than demoted:\n%s", body)
	}
}
