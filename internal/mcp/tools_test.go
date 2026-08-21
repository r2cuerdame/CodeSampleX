package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// callTool runs one tools/call round trip and returns the tool result.
func callTool(t *testing.T, c *pipeClient, name string, args map[string]any) map[string]any {
	t.Helper()
	return result(t, c.call(99, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	}))
}

// toolText extracts the first text content item.
func toolText(t *testing.T, res map[string]any) string {
	t.Helper()
	content, ok := res["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("tool result has no content: %v", res)
	}
	first, ok := content[0].(map[string]any)
	if !ok || first["type"] != "text" {
		t.Fatalf("first content item is not text: %v", content[0])
	}
	text, _ := first["text"].(string)
	return text
}

func TestSearchKnownSolutionSanitizesErrorText(t *testing.T) {
	var got domain.SearchRequest
	deps := emptyDeps()
	deps.Search = func(_ context.Context, req domain.SearchRequest) (domain.SearchResponse, string) {
		got = req
		return domain.SearchResponse{
			SchemaVersion: 1,
			Results: []domain.SearchResult{{
				Grade:      domain.GradeCompatible,
				Confidence: "HIGH",
				Score:      0.9,
				SampleID:   "sha256:abababababababababababababababababababababababababababababababab",
				Exact:      []string{"axios 1.12", "node 22"},
				Different:  []string{"Sample uses ESM", "Current project uses CJS"},
				Adaptation: []string{"Import syntax only"},
				Evidence: domain.EvidenceSummary{
					ProjectCompileObservations: 18291,
					CleanBuilds:                821,
					ContractPasses:             318,
					IndependentCrossPeers:      74,
					PassRate:                   0.94,
					Confidence:                 "HIGH",
					ElevatedFailures:           []string{"node 18 + esm"},
				},
			}},
		}, "0123456789abcdef0123456789abcdef"
	}
	c := startServer(t, deps)

	rawErr := `C:\Users\bob\proj\src\index.ts(12,5): error TS2345: Argument of type 'string' is not assignable.` +
		"\n    at C:\\Users\\bob\\proj\\node_modules\\axios\\lib\\core.js:88"
	res := callTool(t, c, "search_known_solution", map[string]any{
		"query":    "axios post type error",
		"packages": []string{"pkg:npm/axios@1.12.0"},
		"environment": map[string]any{
			"ecosystem":        "npm",
			"os":               "windows",
			"arch":             "amd64",
			"runtime":          "node",
			"runtimeVersion":   "22.18",
			"moduleSystem":     "cjs",
			"executionContext": "node",
		},
		"errorText": rawErr,
	})
	structured := res["structuredContent"].(map[string]any)
	if structured["offerId"] != "0123456789abcdef0123456789abcdef" {
		t.Errorf("search structured offerId = %v", structured["offerId"])
	}
	if strings.Contains(toolText(t, res), "0123456789abcdef0123456789abcdef") {
		t.Error("local offerId leaked into human-readable/public-style search text")
	}

	// The fake must have received a sanitized request: fingerprint + code
	// only, no path fragments anywhere in the serialized request.
	if got.ErrorCode != "TS2345" {
		t.Errorf("SearchRequest.ErrorCode = %q, want TS2345", got.ErrorCode)
	}
	if !strings.HasPrefix(got.ErrorFingerprint, "sha256:") {
		t.Errorf("SearchRequest.ErrorFingerprint = %q, want sha256:...", got.ErrorFingerprint)
	}
	reqJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	for _, leak := range []string{`C:\`, `C:\\`, "Users", "bob", "index.ts", "proj"} {
		if strings.Contains(string(reqJSON), leak) {
			t.Errorf("SearchRequest leaks %q: %s", leak, reqJSON)
		}
	}
	if got.Environment.ExecutionContext != "node" {
		t.Errorf("environment.executionContext = %q, want node", got.Environment.ExecutionContext)
	}
	if got.SchemaVersion != 2 || got.SymbolProvenance != domain.SearchProvenanceExplicit ||
		got.EnvironmentProvenance != domain.SearchProvenanceExplicit || got.EnvironmentIsContext() {
		t.Errorf("MCP input must negotiate explicit/fail-closed v2 provenance: %+v", got)
	}

	// §11.5 layout: MATCH/CONFIDENCE header + the four sections.
	text := toolText(t, res)
	if !strings.HasPrefix(text, "DECISION: REVERIFY — ") {
		t.Errorf("compact decision must be the first line for an adaptation:\n%s", text)
	}
	for _, want := range []string{
		"MATCH: COMPATIBLE",
		"CONFIDENCE: HIGH",
		"Exact\n",
		"Different\n",
		"Adaptation needed\n",
		"Evidence\n",
		"- Project compile observations: 18291",
		"- Clean builds: 821",
		"- Contract passes: 318",
		"- Distinct verifying peer keys: 74",
		"not verified as separate parties",
		"- Elevated failures: node 18 + esm",
		"Import syntax only",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("search text missing %q:\n%s", want, text)
		}
	}
	// Observation counts and verification counts stay separated: the
	// observation line must not claim execution and the contract line must
	// carry the verification class.
	if !strings.Contains(text, "USAGE_OBSERVATION") || !strings.Contains(text, "SAMPLE_VERIFICATION") {
		t.Errorf("evidence lines must label their evidence classes:\n%s", text)
	}
	obsLine := lineContaining(text, "Project compile observations")
	if strings.Contains(obsLine, "SAMPLE_VERIFICATION") {
		t.Errorf("observation line claims verification: %s", obsLine)
	}
	verLine := lineContaining(text, "Contract passes")
	if strings.Contains(verLine, "USAGE_OBSERVATION") {
		t.Errorf("verification line claims observation: %s", verLine)
	}

	if _, ok := res["structuredContent"].(map[string]any); !ok {
		t.Errorf("search result has no structuredContent: %v", res)
	}
}

func lineContaining(text, want string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, want) {
			return l
		}
	}
	return ""
}

func TestSearchMissRendersNoSafeMatch(t *testing.T) {
	c := startServer(t, emptyDeps()) // emptyDeps Search always misses
	res := callTool(t, c, "search_known_solution", map[string]any{"query": "anything"})
	text := toolText(t, res)
	if !strings.HasPrefix(text, "DECISION: UNKNOWN — ") {
		t.Errorf("miss decision must be first:\n%s", text)
	}
	if !strings.Contains(text, "MATCH: NO_SAFE_MATCH") {
		t.Errorf("miss text missing NO_SAFE_MATCH:\n%s", text)
	}
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("miss result has no structuredContent")
	}
	if miss, _ := sc["miss"].(bool); !miss {
		t.Errorf("structuredContent.miss = %v, want true", sc["miss"])
	}
}

func TestSearchDecisionIsFirstAndEarned(t *testing.T) {
	base := domain.SearchResult{
		Grade: domain.GradeExact,
		Evidence: domain.EvidenceSummary{
			ContractPasses: 1,
		},
	}
	tests := []struct {
		name string
		res  domain.SearchResult
		want string
	}{
		{"verified detour", func() domain.SearchResult { r := base; r.ExactFailureMatched = true; return r }(), "VERIFIED_DETOUR"},
		{"verified reuse", base, "REUSE_VERIFIED"},
		{"adaptation", func() domain.SearchResult {
			r := base
			r.Grade = domain.GradeAdaptationRequired
			r.Adaptation = []string{"change imports"}
			return r
		}(), "REVERIFY"},
		{"different", func() domain.SearchResult {
			r := base
			r.Grade = domain.GradeCompatible
			r.Different = []string{"Sample uses linux"}
			return r
		}(), "REVERIFY"},
		{"no contract", func() domain.SearchResult { r := base; r.Evidence.ContractPasses = 0; return r }(), "REVERIFY"},
		{"reference only", func() domain.SearchResult {
			r := base
			r.Grade = domain.GradeReferenceOnly
			return r
		}(), "REFERENCE_ONLY"},
		{"reference with difference", func() domain.SearchResult {
			r := base
			r.Grade = domain.GradeReferenceOnly
			r.Different = []string{"different runtime"}
			return r
		}(), "REVERIFY"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			text := renderSearchResponse(domain.SearchResponse{Results: []domain.SearchResult{tc.res}})
			wantPrefix := "DECISION: " + tc.want + " — "
			if !strings.HasPrefix(text, wantPrefix) {
				t.Fatalf("text prefix = %q, want %q\n%s", strings.SplitN(text, "\n", 2)[0], wantPrefix, text)
			}
			if !strings.Contains(text, "\n\nMATCH: ") {
				t.Errorf("existing detail must remain below the compact decision:\n%s", text)
			}
		})
	}
}

// TestSearchMissCarriesPackageEvidence: on a young network almost every
// search misses, so a miss must still be worth the round trip — it hands
// back what the cache knows about the packages asked for, labeled as
// observation evidence and never as a solution.
func TestSearchMissCarriesPackageEvidence(t *testing.T) {
	deps := emptyDeps()
	var gotPurls []string
	deps.Overview = func(_ context.Context, purls []string, _ domain.EnvironmentFingerprint) ([]PackageOverview, error) {
		gotPurls = purls
		return []PackageOverview{
			{PURL: "pkg:npm/axios@1.12.0", Cached: true, Observations: 412,
				PeerBuckets: 37, PassRate: 0.99, Samples: 2, TopFailure: "ERR_MODULE_NOT_FOUND"},
			{PURL: "pkg:npm/zod@3.23.8"}, // nothing cached
		}, nil
	}
	c := startServer(t, deps)
	res := callTool(t, c, "search_known_solution", map[string]any{
		"query":    "post json",
		"packages": []string{"pkg:npm/axios@1.12.0", "pkg:npm/zod@3.23.8"},
	})
	text := toolText(t, res)

	if len(gotPurls) != 2 {
		t.Fatalf("Overview got %v, want both request packages", gotPurls)
	}
	if !strings.Contains(text, "MATCH: NO_SAFE_MATCH") {
		t.Errorf("miss must still say NO_SAFE_MATCH:\n%s", text)
	}
	axios := lineContaining(text, "axios")
	for _, want := range []string{"412 observations", "37 independent peer buckets", "0.99", "2 sample(s) that built"} {
		if !strings.Contains(axios, want) {
			t.Errorf("axios line %q missing %q", axios, want)
		}
	}
	// An uncached package is UNKNOWN — saying otherwise would invent a fact.
	zod := lineContaining(text, "zod")
	if !strings.Contains(zod, "UNKNOWN, not incompatible") {
		t.Errorf("uncached package line %q must read as UNKNOWN", zod)
	}
	// Observation counts must never be dressed up as execution proof.
	if !strings.Contains(text, "NOT execution proof") {
		t.Errorf("evidence class label missing:\n%s", text)
	}
	if sc, ok := res["structuredContent"].(map[string]any); !ok {
		t.Error("miss result has no structuredContent")
	} else if _, ok := sc["packageOverview"]; !ok {
		t.Errorf("structuredContent missing packageOverview: %v", sc)
	}
}

func TestGetSampleRoundTrip(t *testing.T) {
	deps := emptyDeps()
	deps.GetSample = func(_ context.Context, id string) (domain.SampleManifest, map[string]string, error) {
		if id != "sha256:deadbeef" {
			t.Errorf("GetSample id = %q", id)
		}
		m := domain.SampleManifest{
			SchemaVersion:   1,
			Case:            domain.Case{SchemaVersion: 1, Kind: "HOW", Goal: "axios post basics", Packages: []string{"pkg:npm/axios@1.12.0"}, Contract: []string{"posts JSON"}},
			Packages:        []string{"pkg:npm/axios@1.12.0"},
			License:         "MIT-0",
			ContractCommand: []string{"node", "test/contract.mjs"},
		}
		files := map[string]string{
			"csx.json":  `{"schemaVersion":1}`,
			"index.mjs": "import axios from 'axios'\n",
		}
		return m, files, nil
	}
	c := startServer(t, deps)
	res := callTool(t, c, "get_sample", map[string]any{"sampleId": "sha256:deadbeef"})
	text := toolText(t, res)
	for _, want := range []string{"index.mjs", "csx.json", "MIT-0", "axios post basics"} {
		if !strings.Contains(text, want) {
			t.Errorf("get_sample text missing %q:\n%s", want, text)
		}
	}
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("get_sample has no structuredContent")
	}
	files, ok := sc["files"].(map[string]any)
	if !ok || len(files) != 2 {
		t.Errorf("structuredContent.files = %v, want 2 files", sc["files"])
	}
}

func TestExplainCompatibilityRoundTrip(t *testing.T) {
	deps := emptyDeps()
	deps.Explain = func(_ context.Context, purl, symbol string, env domain.EnvironmentFingerprint) (string, json.RawMessage, error) {
		if purl != "pkg:npm/axios@1.12.0" || symbol != "axios.post" {
			t.Errorf("Explain(%q, %q)", purl, symbol)
		}
		if env.BrowserFamily != "safari" || env.BrowserMajor != "19" {
			t.Errorf("Explain env = %+v, want safari 19", env)
		}
		return "observation evidence separate from verification evidence", json.RawMessage(`{"key":"npm/axios/1"}`), nil
	}
	c := startServer(t, deps)
	res := callTool(t, c, "explain_compatibility", map[string]any{
		"package": "pkg:npm/axios@1.12.0",
		"symbol":  "axios.post",
		"environment": map[string]any{
			"executionContext": "browser",
			"browserFamily":    "safari",
			"browserMajor":     "19",
		},
	})
	sc, ok := res["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("explain has no structuredContent")
	}
	snap, ok := sc["snapshot"].(map[string]any)
	if !ok || snap["key"] != "npm/axios/1" {
		t.Errorf("structuredContent.snapshot = %v", sc["snapshot"])
	}
}

func TestRunObservedCommandRoundTrip(t *testing.T) {
	deps := emptyDeps()
	deps.RunObserved = func(_ context.Context, argv []string, cwd string) (int, string, string, []string, error) {
		if len(argv) != 3 || argv[0] != "npm" {
			t.Errorf("RunObserved argv = %v", argv)
		}
		if cwd != "." {
			t.Errorf("RunObserved cwd = %q", cwd)
		}
		return 1, "PROJECT_COMPILE", "FAIL", []string{"errorCode: TS2345", "error TS2345: <path> is not assignable"}, nil
	}
	c := startServer(t, deps)
	res := callTool(t, c, "run_observed_command", map[string]any{
		"command": []string{"npm", "run", "build"},
		"cwd":     ".",
	})
	text := toolText(t, res)
	for _, want := range []string{"Exit code: 1", "PROJECT_COMPILE", "FAIL", "USAGE_OBSERVATION", "TS2345"} {
		if !strings.Contains(text, want) {
			t.Errorf("run_observed text missing %q:\n%s", want, text)
		}
	}
	sc := res["structuredContent"].(map[string]any)
	if sc["exitCode"].(float64) != 1 || sc["stage"] != "PROJECT_COMPILE" || sc["result"] != "FAIL" {
		t.Errorf("structuredContent = %v", sc)
	}
	if sc["evidenceClass"] != "USAGE_OBSERVATION" {
		t.Errorf("evidenceClass = %v, want USAGE_OBSERVATION", sc["evidenceClass"])
	}
}

func TestReportSampleAdoptionRoundTrip(t *testing.T) {
	var gotOffer string
	var gotID string
	var gotApplied bool
	var gotBuild *bool
	deps := emptyDeps()
	deps.ReportAdoption = func(_ context.Context, offerID, id string, applied bool, buildPass *bool) (localdb.InterventionOutcome, error) {
		gotOffer, gotID, gotApplied, gotBuild = offerID, id, applied, buildPass
		return localdb.InterventionOutcome{}, nil
	}
	c := startServer(t, deps)
	res := callTool(t, c, "report_sample_adoption", map[string]any{
		"offerId":   "0123456789abcdef0123456789abcdef",
		"sampleId":  "sha256:abababababababababababababababababababababababababababababababab",
		"applied":   true,
		"buildPass": true,
	})
	if gotOffer != "0123456789abcdef0123456789abcdef" || gotID != "sha256:abababababababababababababababababababababababababababababababab" || !gotApplied {
		t.Errorf("ReportAdoption got (%q, %q, %v)", gotOffer, gotID, gotApplied)
	}
	if gotBuild == nil || !*gotBuild {
		t.Errorf("ReportAdoption buildPass = %v, want *true", gotBuild)
	}
	if !strings.Contains(toolText(t, res), "ADOPTION_EVIDENCE") {
		t.Errorf("adoption text missing evidence class")
	}
	if strings.Contains(strings.ToLower(toolText(t, res)), "failure avoided") {
		t.Errorf("neutral adoption was labeled as a failure avoided: %s", toolText(t, res))
	}

	// buildPass omitted → nil pointer.
	callTool(t, c, "report_sample_adoption", map[string]any{
		"offerId":  "fedcba9876543210fedcba9876543210",
		"sampleId": "sha256:abababababababababababababababababababababababababababababababab",
		"applied":  false,
	})
	if gotBuild != nil {
		t.Errorf("omitted buildPass arrived as %v, want nil", gotBuild)
	}
	if gotApplied {
		t.Errorf("applied=false arrived as true")
	}

	deps.ReportAdoption = func(_ context.Context, _, _ string, _ bool, buildPass *bool) (localdb.InterventionOutcome, error) {
		return localdb.InterventionOutcome{
			ExactFailureMatched: true,
			VerifiedOffer:       true,
			Applied:             true,
			BuildPass:           sql.NullBool{Bool: true, Valid: buildPass != nil},
		}, nil
	}
	res = callTool(t, c, "report_sample_adoption", map[string]any{
		"offerId":   "0123456789abcdef0123456789abcdef",
		"sampleId":  "sha256:abababababababababababababababababababababababababababababababab",
		"applied":   true,
		"buildPass": true,
	})
	if !strings.Contains(strings.ToLower(toolText(t, res)), "reported failure avoided") {
		t.Errorf("four-stage outcome lacks the earned label: %s", toolText(t, res))
	}
	sc := res["structuredContent"].(map[string]any)
	if sc["reportedFailureAvoided"] != true {
		t.Errorf("reportedFailureAvoided = %v, want true", sc["reportedFailureAvoided"])
	}

	res = callTool(t, c, "report_sample_adoption", map[string]any{
		"sampleId": "sha256:abababababababababababababababababababababababababababababababab",
		"applied":  true,
	})
	if !res["isError"].(bool) || !strings.Contains(toolText(t, res), "re-run search_known_solution") {
		t.Fatalf("legacy no-token report was not neutral/re-search: %v", res)
	}
}

func TestProposePublicSampleRequiresApprovalWording(t *testing.T) {
	deps := emptyDeps()
	deps.Propose = func(_ context.Context, goal string, pkgs, symbols []string) (samples.SanitizedSpec, string, string, error) {
		if goal != "axios upload with progress" {
			t.Errorf("Propose goal = %q", goal)
		}
		if len(pkgs) != 1 || pkgs[0] != "pkg:npm/axios@1.12.0" {
			t.Errorf("Propose pkgs = %v", pkgs)
		}
		spec := samples.BuildSpec(samples.ScanInputs{Goal: goal, Kind: "HOW", Packages: pkgs, Symbols: symbols})
		return spec, spec.PromptText(), `C:\fake\work\sample-1`, nil
	}
	c := startServer(t, deps)
	res := callTool(t, c, "propose_public_sample", map[string]any{
		"goal":     "axios upload with progress",
		"packages": []string{"pkg:npm/axios@1.12.0"},
		"symbols":  []string{"axios.post"},
	})
	text := toolText(t, res)
	if !strings.Contains(text, "explicit approval") || !strings.Contains(text, "csx sample") {
		t.Errorf("propose text must state publish requires user approval via CLI:\n%s", text)
	}
	// The agent is the only party that knows a proposal now exists. If it
	// does not surface that, the workspace is written and then forgotten —
	// which is how a prepared sample silently fails to reach the network.
	if !strings.Contains(text, "TELL THE USER") {
		t.Errorf("propose must instruct the agent to surface the pending sample:\n%s", text)
	}
	if !strings.Contains(text, `csx sample create C:\fake\work\sample-1`) {
		t.Errorf("propose must give the exact create command for this workdir:\n%s", text)
	}
	if !strings.Contains(text, "csx sample pending") {
		t.Errorf("propose must mention how to find unreviewed proposals later:\n%s", text)
	}

	sc := res["structuredContent"].(map[string]any)
	if v, _ := sc["publishRequiresUserApproval"].(bool); !v {
		t.Errorf("structuredContent.publishRequiresUserApproval = %v, want true", sc["publishRequiresUserApproval"])
	}
	if sc["workdir"] != `C:\fake\work\sample-1` {
		t.Errorf("structuredContent.workdir = %v", sc["workdir"])
	}
}

func TestListLocalHitsAndStats(t *testing.T) {
	deps := emptyDeps()
	deps.LocalHits = func(context.Context) ([]localdb.HitRow, error) {
		return []localdb.HitRow{{
			TS:       time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC),
			Query:    "axios post",
			Grade:    domain.GradeCompatible,
			SampleID: "sha256:abababababababababababababababababababababababababababababababab",
			Adopted:  true,
		}}, nil
	}
	deps.LocalStats = func(context.Context) (map[string]any, error) {
		return map[string]any{"mode": "community", "hits": 7}, nil
	}
	c := startServer(t, deps)

	res := callTool(t, c, "list_local_hits", map[string]any{})
	if !strings.Contains(toolText(t, res), "axios post") {
		t.Errorf("hits text missing query")
	}
	sc := res["structuredContent"].(map[string]any)
	hits, ok := sc["hits"].([]any)
	if !ok || len(hits) != 1 {
		t.Fatalf("structuredContent.hits = %v", sc["hits"])
	}
	hit := hits[0].(map[string]any)
	if hit["grade"] != "COMPATIBLE" || hit["adopted"] != true {
		t.Errorf("hit = %v", hit)
	}
	if _, present := hit["postBuildPass"]; present {
		t.Errorf("unknown postBuildPass must be omitted, got %v", hit)
	}

	res = callTool(t, c, "get_local_stats", map[string]any{})
	if !strings.Contains(toolText(t, res), "community") {
		t.Errorf("stats text missing mode")
	}
	sc = res["structuredContent"].(map[string]any)
	if sc["mode"] != "community" {
		t.Errorf("stats structuredContent = %v", sc)
	}
}

func TestToolErrorsBecomeIsErrorResults(t *testing.T) {
	c := startServer(t, emptyDeps()) // GetSample not wired → error

	res := callTool(t, c, "get_sample", map[string]any{"sampleId": "sha256:missing"})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("tool failure must set isError, got %v", res)
	}
	if !strings.Contains(toolText(t, res), "not wired") {
		t.Errorf("tool error text missing cause")
	}

	// Missing required argument is also a readable tool error.
	res = callTool(t, c, "search_known_solution", map[string]any{})
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("missing query must set isError, got %v", res)
	}
}

// TestSearchMissExplainsAnEmptyCache: an agent that installed the binary
// without running `csx init` would otherwise see NO_SAFE_MATCH forever and
// conclude the network is empty. An empty local cache is a different fact
// from an empty network and must be reported as such, with the way out.
func TestSearchMissExplainsAnEmptyCache(t *testing.T) {
	for _, tc := range []struct {
		mode, want string
	}{
		{"", "csx init"},          // never initialized
		{"community", "csx sync"}, // joined, but nothing warmed yet
	} {
		t.Run("mode="+tc.mode, func(t *testing.T) {
			deps := emptyDeps()
			deps.LocalReadiness = func(context.Context) (string, int, error) {
				return tc.mode, 0, nil
			}
			c := startServer(t, deps)
			res := callTool(t, c, "search_known_solution", map[string]any{"query": "anything"})
			text := toolText(t, res)
			if !strings.Contains(text, "MATCH: NO_SAFE_MATCH") {
				t.Errorf("miss header missing:\n%s", text)
			}
			if !strings.Contains(text, tc.want) {
				t.Errorf("miss does not tell the agent to run %q:\n%s", tc.want, text)
			}
			sc, ok := res["structuredContent"].(map[string]any)
			if !ok {
				t.Fatal("no structuredContent")
			}
			if ready, _ := sc["localReady"].(bool); ready {
				t.Error("localReady must be false when no shards are cached")
			}
		})
	}

	// A warm cache says none of this — the miss is then about the network.
	deps := emptyDeps()
	deps.LocalReadiness = func(context.Context) (string, int, error) { return "community", 42, nil }
	c := startServer(t, deps)
	text := toolText(t, callTool(t, c, "search_known_solution", map[string]any{"query": "anything"}))
	if strings.Contains(text, "csx init") || strings.Contains(text, "csx sync") {
		t.Errorf("warm cache must not nag about setup:\n%s", text)
	}
}
