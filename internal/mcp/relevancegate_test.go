package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

// dartCryptoHit is the sample a broken npm typecheck actually came back with.
//
// Nothing about it is wrong on its own: a contract for pkg:pub/crypto ran in
// a container and passed, and the engine graded it COMPATIBLE for this
// machine because a Dart package has no npm delta to disclose. What it has
// never been is evidence about a TypeScript build.
func dartCryptoHit() domain.SearchResponse {
	return domain.SearchResponse{Results: []domain.SearchResult{{
		Grade:      domain.GradeCompatible,
		Confidence: "LOW",
		SampleID:   "sha256:09b939ee857b27e5",
		Case: &domain.Case{
			Goal:     "Enforce package:crypto internal invariant assertions under Dart's --enable-asserts flag",
			Packages: []string{"pkg:pub/crypto@3.0.6"},
		},
		Evidence: domain.EvidenceSummary{ContractPasses: 1, IndependentCrossPeers: 1},
	}}}
}

// The demotion was real and reached nobody.
//
// A failed `npm run typecheck` was answered with a Dart sample about
// package:crypto. The envelope around it said REFERENCE_CANDIDATE and
// advisoryOnly, and the two kilobytes of prose underneath — the part an agent
// reads — opened "DECISION: REUSE_VERIFIED — a contract PASS is reusable in
// this environment" and "MATCH: COMPATIBLE". The label said reference; the
// body said use it.
func TestACrossEcosystemSampleIsNotRenderedAsAReusableAnswer(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TYPECHECK", "FAIL", []string{"errorCode: TS2352", "src<path>(<n>,<n>): error TS2352: Conversion of <str> to <str> may be a mistake."},
				commandOutput{Stdout: "src/index.ts(12,5): error TS2352: Conversion of type 'string' to type 'number' may be a mistake."}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			return dartCryptoHit(), "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	m := structured(t, out)
	recommendation, _ := m["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != "NO_RELEVANT_MATCH" {
		t.Errorf("status = %v, want NO_RELEVANT_MATCH: %s", got, mustJSON(t, recommendation))
	}
	if got := recommendation["advisoryOnly"]; got != true {
		t.Errorf("advisoryOnly = %v, want true: %s", got, mustJSON(t, recommendation))
	}
	body, _ := recommendation["text"].(string)
	for _, forbidden := range []string{"REUSE_VERIFIED", "MATCH: COMPATIBLE"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("an unrelated sample is still presented as %q:\n%s", forbidden, body)
		}
	}
	if answer, _ := m["networkAnswer"].(string); strings.Contains(answer, "REUSE_VERIFIED") {
		t.Errorf("networkAnswer still promotes the unrelated sample:\n%s", answer)
	}
	// Demoted is not deleted. The agent may still want to look.
	if !strings.Contains(body, "sha256:09b939ee857b27e5") {
		t.Errorf("the candidate was suppressed entirely rather than demoted:\n%s", body)
	}
	if !strings.Contains(body, "pkg:pub/crypto@3.0.6") {
		t.Errorf("the body never says which ecosystem the sample belongs to:\n%s", body)
	}
}

// The local failure is the primary evidence and the demoted candidate must
// not be able to bury it. Two kilobytes of contract prose about the wrong
// language is how an agent ends up running the same build again in a shell.
func TestTheDemotedCandidateStaysShorterThanTheFailure(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TYPECHECK", "FAIL", []string{"errorCode: TS2352"},
				commandOutput{Stdout: "src/index.ts(12,5): error TS2352: Conversion of type 'string' to type 'number' may be a mistake."}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			return dartCryptoHit(), "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	m := structured(t, out)
	recommendation, _ := m["recommendation"].(map[string]any)
	body, _ := recommendation["text"].(string)
	for _, forbidden := range []string{"Proven by its contract", "Evidence", "BUILT:", "Adaptation needed"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("an unrelated sample still ships its %q section:\n%s", forbidden, body)
		}
	}
	// The failure is the evidence. A note about a sample in another language
	// is not allowed to out-shout it.
	failure, _ := m["stdout"].(string)
	if len(body) > 4*len(failure) {
		t.Errorf("the demoted note (%d bytes) dwarfs the failure it accompanies (%d bytes):\n%s",
			len(body), len(failure), body)
	}
	text := resultText(out)
	failureAt := strings.Index(text, "TS2352: Conversion")
	candidateAt := strings.Index(text, "sha256:09b939ee857b27e5")
	if failureAt < 0 {
		t.Fatalf("the local failure is missing from the answer:\n%s", text)
	}
	if candidateAt >= 0 && failureAt > candidateAt {
		t.Errorf("the candidate precedes the failure it was looked up for:\n%s", text)
	}
}

// The exemption. A cross-ecosystem sample that matched this exact sanitized
// failure fingerprint has an evidence link to the failure in hand — that is
// the one thing that outranks "different ecosystem", and it must not be
// demoted along with the neighbours.
func TestAnExactFailureMatchSurvivesTheEcosystemGate(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TYPECHECK", "FAIL", []string{"errorCode: TS2352"},
				commandOutput{Stdout: "src/index.ts(12,5): error TS2352"}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			resp := dartCryptoHit()
			resp.Results[0].ExactFailureMatched = true
			return resp, "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	recommendation, _ := structured(t, out)["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != "FOUND" {
		t.Errorf("status = %v, want FOUND — this sample matched the failure itself: %s",
			got, mustJSON(t, recommendation))
	}
}

// The envelope and the body may not say opposite things.
//
// A LOW-confidence sample in the command's own ecosystem is a real answer and
// is still shown — but it arrives labeled REFERENCE_CANDIDATE / advisoryOnly
// while its own first line reads "DECISION: REUSE_VERIFIED — a contract PASS
// is reusable in this environment". The label is the envelope and the body is
// what gets read, so the payload argues with itself and the half that wins is
// the half that says use it. goal.md §11.6 has said since before any of this
// that a LOW-confidence result is not offered as an automatic fix basis.
func TestAnAdvisoryAnswerDoesNotOpenByCallingItselfReusable(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_COMPILE", "FAIL", []string{"errorCode: TS2352"},
				commandOutput{Stdout: "src/index.ts(12,5): error TS2352"}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			resp := dartCryptoHit()
			resp.Results[0].Case.Packages = []string{"pkg:npm/typescript@5.9.2"}
			return resp, "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	m := structured(t, out)
	recommendation, _ := m["recommendation"].(map[string]any)
	if recommendation["advisoryOnly"] != true {
		t.Fatalf("fixture is not advisory-only: %s", mustJSON(t, recommendation))
	}
	body, _ := recommendation["text"].(string)
	if strings.Contains(body, "REUSE_VERIFIED") {
		t.Errorf("an advisory-only answer opens by calling itself reusable:\n%s", body)
	}
	if !strings.Contains(body, domain.RecommendationReferenceCandidate) {
		t.Errorf("the body never states what the envelope says it is:\n%s", body)
	}
	// Demoting the verdict must not cost the evidence underneath it.
	if !strings.Contains(body, "Contract passes: 1") {
		t.Errorf("the demoted body lost the evidence counts:\n%s", body)
	}
	if !strings.Contains(body, "sha256:09b939ee857b27e5") {
		t.Errorf("the demoted body lost the sample:\n%s", body)
	}
}

// A sample in the command's own ecosystem is what the lookup is for. The gate
// must catch the wrong language, not every LOW-confidence answer.
func TestASameEcosystemCandidateIsStillOffered(t *testing.T) {
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TYPECHECK", "FAIL", []string{"errorCode: TS2352"},
				commandOutput{Stdout: "src/index.ts(12,5): error TS2352"}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			resp := dartCryptoHit()
			resp.Results[0].Case.Packages = []string{"pkg:npm/typescript@5.9.2"}
			return resp, "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	recommendation, _ := structured(t, out)["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != "FOUND" {
		t.Errorf("status = %v, want FOUND: %s", got, mustJSON(t, recommendation))
	}
	if got := recommendation["classification"]; got != domain.RecommendationReferenceCandidate {
		t.Errorf("classification = %v, want REFERENCE_CANDIDATE (confidence is LOW): %s",
			got, mustJSON(t, recommendation))
	}
}

// A failure that printed nothing has no question in it. Asking anyway sends
// the fingerprint of a blank string as the query, and the engine answers it
// with whatever is nearest — which is how a Dart sample became the answer to
// a TypeScript build in the first place.
func TestAFailureWithNothingToAskIsNotAsked(t *testing.T) {
	asked := false
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TYPECHECK", "FAIL", nil, commandOutput{}, nil
		}
		d.Search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, string) {
			asked = true
			return dartCryptoHit(), "offer-1"
		}
	})

	out := s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	if asked {
		t.Error("a failure with no diagnosable output was still turned into a query")
	}
	recommendation, _ := structured(t, out)["recommendation"].(map[string]any)
	if got := recommendation["status"]; got != "SKIPPED" {
		t.Errorf("status = %v, want SKIPPED: %s", got, mustJSON(t, recommendation))
	}
}

// The question the network is asked is the error, not the hash of it. The
// fingerprint is a key for the fingerprint index; pasted into a free-text
// query it is 64 hex digits of noise competing with the four words that
// actually describe the failure.
func TestTheLookupAsksTheErrorAndNotItsHash(t *testing.T) {
	var asked domain.SearchRequest
	s := lookupServer(t, func(d *Deps) {
		d.RunObserved = func(context.Context, []string, string) (int, string, string, []string, commandOutput, error) {
			return 1, "PROJECT_TYPECHECK", "FAIL", []string{
				"errorCode: TS2352",
				"fingerprint: sha256:bef9c54a6a8260dd750f43b21f9370c88a29b9c7ccd94f5b754f5b7ef9584cd8",
				"src<path>(<n>,<n>): error TS2352: Conversion of <str> to <str> may be a mistake.",
			}, commandOutput{Stdout: "src/index.ts(12,5): error TS2352"}, nil
		}
		d.Search = func(_ context.Context, req domain.SearchRequest) (domain.SearchResponse, string) {
			asked = req
			return domain.SearchResponse{Miss: true}, ""
		}
	})

	s.toolRunObserved(context.Background(), runArgsJSON(t, "npm", "run", "typecheck"))
	if strings.Contains(asked.Query, "sha256:") || strings.Contains(asked.Query, "fingerprint:") {
		t.Errorf("the fingerprint was pasted into the free-text query: %q", asked.Query)
	}
	if !strings.Contains(asked.Query, "Conversion of") {
		t.Errorf("the error text never became the question: %q", asked.Query)
	}
	if asked.ErrorCode != "TS2352" {
		t.Errorf("ErrorCode = %q, want TS2352", asked.ErrorCode)
	}
	if asked.Environment.Ecosystem != "npm" {
		t.Errorf("the npm toolchain context was lost: %#v", asked.Environment)
	}
}
