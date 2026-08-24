from pathlib import Path
import re


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    s = p.read_text(encoding="utf-8")
    if old not in s:
        raise SystemExit(f"pattern not found in {path}: {old[:120]!r}")
    p.write_text(s.replace(old, new, 1), encoding="utf-8")


# P1: retrieval and outcome recording are two phases. The relevance gate must
# decide the visible result before an offer/hit is recorded.
replace_once(
    "internal/mcp/tools.go",
    "\tSearch func(ctx context.Context, req domain.SearchRequest) (resp domain.SearchResponse, offerID string)\n",
    "\tSearch func(ctx context.Context, req domain.SearchRequest) (resp domain.SearchResponse, offerID string)\n"
    "\t// SearchRaw retrieves candidates without recording a hit/offer.\n"
    "\tSearchRaw func(ctx context.Context, req domain.SearchRequest) domain.SearchResponse\n"
    "\t// RecordSearchOutcome records only the response that normal output actually exposed.\n"
    "\tRecordSearchOutcome func(ctx context.Context, req domain.SearchRequest, resp domain.SearchResponse) string\n",
)

p = Path("internal/mcp/deps.go")
s = p.read_text(encoding="utf-8")
marker = "\n\td := &Deps{\n"
if marker not in s:
    raise SystemExit("deps marker not found")
closures = r'''
	searchRaw := func(ctx context.Context, req domain.SearchRequest) domain.SearchResponse {
		resp := engine.Search(ctx, req)
		if resp.Miss {
			live := currentConfig(home)
			if live.Mode == config.ModeCommunity {
				syncer := &search.Syncer{DB: db, ServerURL: live.ServerURL, HTTP: syncHTTP}
				if search.FetchMissing(ctx, engine, syncer, live.Mode, req) {
					resp = engine.Search(ctx, req)
				}
			}
		}
		return resp
	}
	recordSearch := func(ctx context.Context, req domain.SearchRequest, resp domain.SearchResponse) string {
		return recordSearchOutcomeReloaded(ctx, db, ident,
			func() *config.Config { return currentConfig(home) }, req, resp)
	}
'''
s = s.replace(marker, closures + marker, 1)
pattern = re.compile(
    r"\t\tSearch: func\(ctx context\.Context, req domain\.SearchRequest\) \(domain\.SearchResponse, string\) \{.*?\n\t\t\},\n\t\tGetSample:",
    re.S,
)
replacement = r'''		Search: func(ctx context.Context, req domain.SearchRequest) (domain.SearchResponse, string) {
			resp := searchRaw(ctx, req)
			return resp, recordSearch(ctx, req, resp)
		},
		SearchRaw:            searchRaw,
		RecordSearchOutcome: recordSearch,
		GetSample:'''
s, count = pattern.subn(replacement, s, count=1)
if count != 1:
    raise SystemExit(f"deps Search closure replacement count={count}")
p.write_text(s, encoding="utf-8")

replace_once(
    "internal/mcp/tools.go",
    "\tresp, offerID := s.Deps.Search(ctx, req)\n",
    r'''	var (
		resp    domain.SearchResponse
		offerID string
	)
	twoPhase := s.Deps.SearchRaw != nil && s.Deps.RecordSearchOutcome != nil
	if twoPhase {
		resp = s.Deps.SearchRaw(ctx, req)
	} else {
		resp, offerID = s.Deps.Search(ctx, req)
	}
''',
)
replace_once(
    "internal/mcp/tools.go",
    "\tresp, suppressed := domain.GateNormalOutput(req, resp, nil)\n",
    r'''	resp, suppressed := domain.GateNormalOutput(req, resp, nil)
	if twoPhase {
		offerID = s.Deps.RecordSearchOutcome(ctx, req, resp)
	} else if len(suppressed) > 0 {
		// Compatibility fakes may still provide only Search. They cannot be
		// re-recorded safely, so never expose an offer for a hidden result.
		offerID = ""
	}
''',
)

# P2: each rendered survivor gets its own mechanically generated relevance line.
replace_once(
    "internal/mcp/tools.go",
    "\ttext := renderSearchResponse(resp)\n\tif len(resp.Results) > 0 {\n\t\ttext = withRelevance(text, resp.Results[0].RelevanceLine(req, nil))\n\t}\n",
    r'''	relevance := make([]string, len(resp.Results))
	for i := range resp.Results {
		relevance[i] = resp.Results[i].RelevanceLine(req, nil)
	}
	text := renderSearchResponseWithRelevance(resp, relevance)
''',
)
replace_once(
    "internal/mcp/tools.go",
    "func renderSearchResponse(resp domain.SearchResponse) string {\n",
    r'''func renderSearchResponse(resp domain.SearchResponse) string {
	return renderSearchResponseWithRelevance(resp, nil)
}

func renderSearchResponseWithRelevance(resp domain.SearchResponse, relevance []string) string {
''',
)
replace_once(
    "internal/mcp/tools.go",
    "\t\tif why := r.ConfidenceReason(); why != \"\" {\n\t\t\tb.WriteString(\" — \" + why)\n\t\t}\n\t\tb.WriteString(\"\\n\\n\")\n",
    r'''		if why := r.ConfidenceReason(); why != "" {
			b.WriteString(" — " + why)
		}
		b.WriteString("\n")
		if i < len(relevance) && relevance[i] != "" {
			b.WriteString(relevance[i] + "\n")
		}
		b.WriteString("\n")
''',
)
replace_once(
    "internal/mcp/tools.go",
    "\t\tText: withRelevance(\n\t\t\tadvisoryDecision(renderSearchResponse(resp), advisory, reason),\n\t\t\ttop.RelevanceLine(req, argv)),\n",
    r'''		Text: advisoryDecision(
			renderSearchResponseWithRelevance(resp, []string{top.RelevanceLine(req, argv)}),
			advisory, reason),
''',
)

# P2: hook relevance filtering happens before one-result truncation.
p = Path("internal/cli/hook.go")
s = p.read_text(encoding="utf-8")
start = s.index("\t// One answer. This arrives unasked")
end = s.index("\tclassification, advisoryOnly, reason := resp.Results[0].RecommendationClassification()", start)
new = r'''	// Filter the full retrieval set before selecting the one hook answer.
	// Similarity ranking and fact relevance are different questions: an
	// unrelated #1 must not hide a relevant #2.
	retrieved := append([]domain.SearchResult(nil), resp.Results...)
	resp, _ = domain.GateNormalOutput(req, resp, proj.Argv)
	if resp.Miss || len(resp.Results) == 0 {
		if len(retrieved) > 0 {
			if reason := retrieved[0].SuppressionReason(req, proj.Argv); reason != "" {
				return quiet(reason, hookSuppressionDetail(retrieved[0], reason))
			}
		}
		asked, _ := json.Marshal(req)
		return quiet(hookTraceNoMatch, "nothing here has been proven for this failure; asked: "+string(asked))
	}

	// One answer only after irrelevant candidates have been removed.
	if len(resp.Results) > 1 {
		resp.Results = resp.Results[:1]
	}
'''
p.write_text(s[:start] + new + s[end:], encoding="utf-8")

# P2: letter-only POSIX errno identifiers are structured diagnostics, but
# arbitrary ALL-CAPS prose is not.
replace_once(
    "internal/domain/outputrelevance.go",
    "var diagnosticToken = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:[0-9_][A-Z0-9_]*)+$`)\n",
    r'''var diagnosticToken = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:[0-9_][A-Z0-9_]*)+$`)

var standardErrnoToken = map[string]struct{}{
	"EACCES": {}, "EAGAIN": {}, "EBADF": {}, "EBUSY": {}, "ECANCELED": {},
	"ECHILD": {}, "ECONNABORTED": {}, "ECONNREFUSED": {}, "ECONNRESET": {},
	"EEXIST": {}, "EFAULT": {}, "EINPROGRESS": {}, "EINTR": {}, "EINVAL": {},
	"EIO": {}, "EISDIR": {}, "ELOOP": {}, "EMFILE": {}, "ENAMETOOLONG": {},
	"ENETDOWN": {}, "ENETRESET": {}, "ENETUNREACH": {}, "ENFILE": {},
	"ENOBUFS": {}, "ENODEV": {}, "ENOENT": {}, "ENOEXEC": {}, "ENOMEM": {},
	"ENOSPC": {}, "ENOSYS": {}, "ENOTCONN": {}, "ENOTDIR": {}, "ENOTEMPTY": {},
	"ENOTSOCK": {}, "ENOTSUP": {}, "ENOTTY": {}, "ENXIO": {}, "EOPNOTSUPP": {},
	"EOVERFLOW": {}, "EPERM": {}, "EPIPE": {}, "EPROTO": {},
	"EPROTONOSUPPORT": {}, "EPROTOTYPE": {}, "ERANGE": {}, "EROFS": {},
	"ESRCH": {}, "ETIMEDOUT": {}, "EWOULDBLOCK": {}, "EXDEV": {},
}

func isDiagnosticToken(token string) bool {
	if diagnosticToken.MatchString(token) {
		return true
	}
	_, ok := standardErrnoToken[token]
	return ok
}
''',
)
replace_once(
    "internal/domain/outputrelevance.go",
    "\t\tif diagnosticToken.MatchString(field) {\n",
    "\t\tif isDiagnosticToken(field) {\n",
)

# Focused review regressions.
Path("internal/domain/outputrelevance_review_test.go").write_text(r'''package domain

import "testing"

func TestRequestDiagnosticsRecognizesLetterOnlyErrnos(t *testing.T) {
	for _, code := range []string{"ENOENT", "EACCES", "EPERM"} {
		got := requestDiagnostics(SearchRequest{Query: "operation failed with " + code})
		found := false
		for _, v := range got {
			if v == code {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s not recognized: %#v", code, got)
		}
	}
}

func TestRequestDiagnosticsRejectsGenericAllCapsWords(t *testing.T) {
	for _, word := range []string{"ERROR", "EXACT", "SHA"} {
		got := requestDiagnostics(SearchRequest{Query: "ordinary prose " + word})
		for _, v := range got {
			if v == word {
				t.Fatalf("generic word %s promoted: %#v", word, got)
			}
		}
	}
}
''', encoding="utf-8")

Path("internal/mcp/reviewfix_test.go").write_text(r'''package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestToolSearchRecordsFilteredVisibleTop(t *testing.T) {
	var recorded domain.SearchResponse
	s := &Server{Deps: &Deps{
		SearchRaw: func(context.Context, domain.SearchRequest) domain.SearchResponse {
			return domain.SearchResponse{Results: []domain.SearchResult{
				{SampleID: "hidden", Grade: domain.GradeCompatible, Confidence: "LOW", Case: &domain.Case{Goal: "format integers", Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"}}},
				{SampleID: "visible", Grade: domain.GradeCompatible, Confidence: "LOW", Case: &domain.Case{Goal: "deploy immutable commit", Packages: []string{"pkg:golang/example.com/deploy@v1.0.0"}}},
			}}
		},
		RecordSearchOutcome: func(_ context.Context, _ domain.SearchRequest, resp domain.SearchResponse) string {
			recorded = resp
			return "offer-visible"
		},
		MachineEnv: func(context.Context) domain.EnvironmentFingerprint { return domain.EnvironmentFingerprint{SchemaVersion: 1} },
	}}
	raw, _ := json.Marshal(searchArgs{Query: "deploy immutable commit", Packages: []string{"pkg:golang/example.com/deploy@v1.0.0"}})
	got := s.toolSearch(context.Background(), raw)
	if len(recorded.Results) != 1 || recorded.Results[0].SampleID != "visible" {
		t.Fatalf("recorded wrong response: %#v", recorded.Results)
	}
	structured, ok := got.StructuredContent.(localSearchStructured)
	if !ok || structured.OfferID != "offer-visible" {
		t.Fatalf("wrong offer: %#v", got.StructuredContent)
	}
}

func TestToolSearchRecordsMissWhenEverythingSuppressed(t *testing.T) {
	var recorded domain.SearchResponse
	s := &Server{Deps: &Deps{
		SearchRaw: func(context.Context, domain.SearchRequest) domain.SearchResponse {
			return domain.SearchResponse{Results: []domain.SearchResult{{SampleID: "hidden", Grade: domain.GradeCompatible, Confidence: "LOW", Case: &domain.Case{Goal: "format integers", Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"}}}}}
		},
		RecordSearchOutcome: func(_ context.Context, _ domain.SearchRequest, resp domain.SearchResponse) string {
			recorded = resp
			return ""
		},
		MachineEnv: func(context.Context) domain.EnvironmentFingerprint { return domain.EnvironmentFingerprint{SchemaVersion: 1} },
	}}
	raw, _ := json.Marshal(searchArgs{Query: "deploy immutable commit"})
	_ = s.toolSearch(context.Background(), raw)
	if !recorded.Miss || len(recorded.Results) != 0 {
		t.Fatalf("suppressed result recorded as hit: %#v", recorded)
	}
}

func TestRenderSearchResponseExplainsEachSurvivor(t *testing.T) {
	resp := domain.SearchResponse{Results: []domain.SearchResult{{Grade: domain.GradeCompatible, Confidence: "LOW"}, {Grade: domain.GradeReferenceOnly, Confidence: "LOW"}}}
	text := renderSearchResponseWithRelevance(resp, []string{"Relevance: first.", "Relevance: second."})
	alt := strings.Index(text, "--- alternative 2 ---")
	if alt < 0 || !strings.Contains(text[:alt], "Relevance: first.") || !strings.Contains(text[alt:], "Relevance: second.") {
		t.Fatalf("per-result relevance missing:\n%s", text)
	}
}
''', encoding="utf-8")

Path("internal/cli/hook_reviewfix_test.go").write_text(r'''package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/r2cuerdame/codesamplex/internal/domain"
)

func TestHookFiltersBeforeSelectingOneAnswer(t *testing.T) {
	env, out := hookHarness(t, hookInput(t, "Bash", "go test ./...", "ENOENT"), func(e *hookEnv) {
		e.inspect = func(context.Context, string, [][]string) hookProject {
			return hookProject{Known: true, Stage: domain.StageProjectTest, Argv: []string{"go", "test", "./..."}}
		}
		e.search = func(context.Context, domain.SearchRequest) (domain.SearchResponse, error) {
			return domain.SearchResponse{Results: []domain.SearchResult{
				{SampleID: "sha256:hidden", Grade: domain.GradeCompatible, Confidence: "LOW", Case: &domain.Case{Goal: "format integers", Packages: []string{"pkg:golang/github.com/dustin/go-humanize@v1.0.1"}}},
				{SampleID: "sha256:visible", Grade: domain.GradeReferenceOnly, Confidence: "LOW", Case: &domain.Case{Goal: "handle missing files", Contract: []string{"os.Open reports ENOENT when the path is absent"}}},
			}}, nil
		}
	})
	if code := hookAgentMain(context.Background(), env); code != 0 {
		t.Fatalf("hook exit=%d", code)
	}
	if !strings.Contains(out.String(), "sha256:visible") || strings.Contains(out.String(), "sha256:hidden") {
		t.Fatalf("hook selected wrong candidate: %s", out.String())
	}
}
''', encoding="utf-8")
