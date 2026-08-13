package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Deps injects tool behavior into the transport so tests fake it and the
// real wiring (NewDeps) stays daemon-free. Every function takes the request
// context; none may retain it.
type Deps struct {
	// Search runs the C7 pipeline over the local store.
	Search func(ctx context.Context, req domain.SearchRequest) domain.SearchResponse
	// GetSample returns a cached sample's manifest and its files
	// (path → content, ≤64KB per file, binaries skipped).
	GetSample func(ctx context.Context, id string) (domain.SampleManifest, map[string]string, error)
	// Explain renders a compatibility explanation for one package/symbol
	// from locally cached shards, with observation and verification
	// evidence kept separate. snapshot is the underlying JSON (may be null).
	Explain func(ctx context.Context, purl, symbol string, env domain.EnvironmentFingerprint) (text string, snapshot json.RawMessage, err error)
	// RunObserved wraps one command in the evidence loop (scan → run →
	// record). sanitized carries only sanitizer output — never raw stderr.
	RunObserved func(ctx context.Context, argv []string, cwd string) (exitCode int, stage, result string, sanitized []string, err error)
	// ReportAdoption records a hit-adoption outcome (ADOPTION_EVIDENCE).
	ReportAdoption func(ctx context.Context, sampleID string, applied bool, buildPass *bool) error
	// Propose builds a sanitized clean-room spec + prompt and creates an
	// empty workspace. It NEVER publishes (goal.md §12.4).
	Propose func(ctx context.Context, goal string, pkgs, symbols []string) (spec samples.SanitizedSpec, prompt string, workdir string, err error)
	// LocalHits lists recent local search hits.
	LocalHits func(ctx context.Context) ([]localdb.HitRow, error)
	// LocalStats returns the local dashboard stats.
	LocalStats func(ctx context.Context) (map[string]any, error)
}

// toolDef is one tools/list entry.
type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// environmentSchema describes the sparse EnvironmentFingerprint accepted by
// search_known_solution and explain_compatibility. The execution-context
// axis is explicit so agents can say "browser=safari 19"
// (docs/execution-context.md §5).
func environmentSchema() map[string]any {
	strProp := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	return map[string]any{
		"type":        "object",
		"description": "Sparse environment fingerprint; set only the dimensions you know. Include executionContext (and browserFamily/browserMajor when browser-like) so results are graded for where the code actually runs.",
		"properties": map[string]any{
			"ecosystem":             strProp("npm | pypi | cargo | golang"),
			"os":                    strProp("windows | linux | darwin"),
			"osVersionBucket":       strProp("e.g. \"11\""),
			"arch":                  strProp("amd64 | arm64"),
			"runtime":               strProp("e.g. node, python, bun, deno"),
			"runtimeVersion":        strProp("e.g. \"22.18\""),
			"language":              strProp("e.g. typescript"),
			"languageVersion":       strProp("e.g. \"5.9\""),
			"compiler":              strProp("e.g. rustc, go"),
			"compilerVersion":       strProp("compiler version"),
			"packageManager":        strProp("e.g. pnpm, npm, uv, cargo"),
			"packageManagerVersion": strProp("package manager version"),
			"moduleSystem":          strProp("esm | cjs"),
			"frameworks":            map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"executionContext":      strProp("where the code runs: node | browser | webview | electron | webworker | serviceworker | bun | deno | ... (open vocabulary)"),
			"browserFamily":         strProp("chrome | edge | firefox | safari | chromium | android-webview | ios-wkwebview | electron"),
			"browserMajor":          strProp("browser major version bucket, e.g. \"140\" or \"19\" for safari 19"),
			"engine":                strProp("chromium | gecko | webkit"),
			"engineVersion":         strProp("engine major version"),
		},
	}
}

// toolDefs lists the C8 tools. There is deliberately NO publish tool:
// publishing a sample requires the user's explicit approval in the CLI
// (goal.md §12.4).
func toolDefs() []toolDef {
	obj := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if required == nil {
			required = []string{}
		}
		s["required"] = required
		return s
	}
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	strArr := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	return []toolDef{
		{
			Name:        "search_known_solution",
			Description: "Search the CodeSampleX network cache for a community-verified solution before solving from scratch. Results are graded for YOUR environment (EXACT/COMPATIBLE/ADAPTATION_REQUIRED/REFERENCE_ONLY); NO_SAFE_MATCH means solve it fresh. Evidence keeps observation counts and verification counts separate — compile observations are never execution proof.",
			InputSchema: obj(map[string]any{
				"query":       str("what you are trying to do or fix, in plain words"),
				"packages":    strArr("package purls involved, e.g. pkg:npm/axios@1.12.0"),
				"symbols":     strArr("symbol families involved, e.g. axios.post"),
				"environment": environmentSchema(),
				"errorText":   str("raw error output, if fixing an error. It is sanitized locally (paths/tokens/usernames stripped) before any use and NEVER forwarded raw; only the derived error code and fingerprint enter the search."),
			}, "query"),
		},
		{
			Name:        "get_sample",
			Description: "Fetch a cached public sample by id: manifest plus file contents (each file capped at 64KB, binaries skipped). Samples are MIT-0 community artifacts; adapt them to the project instead of pasting blindly.",
			InputSchema: obj(map[string]any{
				"sampleId": str("content-addressed sample id, e.g. sha256:..."),
			}, "sampleId"),
		},
		{
			Name:        "explain_compatibility",
			Description: "Explain what the network knows about a package (optionally one symbol) in a given environment, from locally cached compatibility shards. Observation evidence (USAGE_OBSERVATION) and verification evidence (SAMPLE_VERIFICATION) are reported separately and never summed.",
			InputSchema: obj(map[string]any{
				"package":     str("package purl, e.g. pkg:npm/axios@1.12.0"),
				"symbol":      str("symbol family, e.g. axios.post"),
				"environment": environmentSchema(),
			}, "package"),
		},
		{
			Name:        "run_observed_command",
			Description: "Run a shell command wrapped in the csx evidence loop (like `csx run`): the project is scanned, the command runs with its exit code passed through, and anonymous USAGE_OBSERVATION evidence is recorded for public packages only. Returned errors are sanitized templates, never raw logs.",
			InputSchema: obj(map[string]any{
				"command": strArr("argv, e.g. [\"npm\",\"run\",\"build\"]"),
				"cwd":     str("working directory (defaults to the current directory)"),
			}, "command"),
		},
		{
			Name:        "report_sample_adoption",
			Description: "Report whether a sample from search_known_solution was actually applied, and whether the build passed afterwards. Records ADOPTION_EVIDENCE — the network optimizes post-hit success rate, so honest reports matter.",
			InputSchema: obj(map[string]any{
				"sampleId":  str("the adopted (or rejected) sample id"),
				"applied":   map[string]any{"type": "boolean", "description": "true if the sample's approach was applied to the project"},
				"buildPass": map[string]any{"type": "boolean", "description": "whether the project built/passed after adoption; omit if not known yet"},
			}, "sampleId", "applied"),
		},
		{
			Name:        "propose_public_sample",
			Description: "Start a clean-room public sample proposal: builds a sanitized spec (public packages, symbols, goal — never project source or paths), returns generation instructions and an empty workspace directory. This tool CANNOT publish: publishing requires the user's explicit approval via the csx CLI.",
			InputSchema: obj(map[string]any{
				"goal":     str("what the sample should prove, e.g. \"axios file upload with progress\""),
				"packages": strArr("public package purls the sample must use"),
				"symbols":  strArr("symbol families the sample should demonstrate"),
			}, "goal", "packages"),
		},
		{
			Name:        "list_local_hits",
			Description: "List recent local search hits with their grades and adoption outcomes (local dashboard data; never uploaded).",
			InputSchema: obj(map[string]any{}),
		},
		{
			Name:        "get_local_stats",
			Description: "Local CodeSampleX stats: mode, cached samples, hits, pending uploads. Estimated values are labeled estimated.",
			InputSchema: obj(map[string]any{}),
		},
	}
}

// content/result shapes per MCP tools/call.
type contentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content           []contentItem `json:"content"`
	StructuredContent any           `json:"structuredContent,omitempty"`
	IsError           bool          `json:"isError,omitempty"`
}

func textResult(text string, structured any) *toolResult {
	return &toolResult{
		Content:           []contentItem{{Type: "text", Text: text}},
		StructuredContent: structured,
	}
}

func errResult(msg string) *toolResult {
	return &toolResult{
		Content: []contentItem{{Type: "text", Text: msg}},
		IsError: true,
	}
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// toolsCall dispatches one tools/call request. Unknown tools are JSON-RPC
// errors; tool-level failures come back as results with isError:true so the
// agent can read them.
func (s *Server) toolsCall(ctx context.Context, params json.RawMessage) (any, *rpcError) {
	var p callParams
	if len(params) == 0 || json.Unmarshal(params, &p) != nil || p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call requires params.name"}
	}
	if s.Deps == nil {
		return nil, &rpcError{Code: codeInternalError, Message: "server has no tool dependencies wired"}
	}
	args := p.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	var handler func(context.Context, json.RawMessage) *toolResult
	switch p.Name {
	case "search_known_solution":
		handler = s.toolSearch
	case "get_sample":
		handler = s.toolGetSample
	case "explain_compatibility":
		handler = s.toolExplain
	case "run_observed_command":
		handler = s.toolRunObserved
	case "report_sample_adoption":
		handler = s.toolReportAdoption
	case "propose_public_sample":
		handler = s.toolPropose
	case "list_local_hits":
		handler = s.toolListHits
	case "get_local_stats":
		handler = s.toolLocalStats
	default:
		return nil, &rpcError{Code: codeInvalidParams, Message: "unknown tool: " + p.Name}
	}
	if !json.Valid(args) {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call arguments must be a JSON object"}
	}
	return handler(ctx, args), nil
}

// --- search_known_solution ---

type searchArgs struct {
	Query       string                        `json:"query"`
	Packages    []string                      `json:"packages"`
	Symbols     []string                      `json:"symbols"`
	Environment domain.EnvironmentFingerprint `json:"environment"`
	ErrorText   string                        `json:"errorText"`
}

func (s *Server) toolSearch(ctx context.Context, raw json.RawMessage) *toolResult {
	var a searchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return errResult("search_known_solution: bad arguments: " + err.Error())
	}
	if strings.TrimSpace(a.Query) == "" {
		return errResult("search_known_solution: query is required")
	}

	req := domain.SearchRequest{
		SchemaVersion: 1,
		Query:         a.Query,
		Packages:      a.Packages,
		Symbols:       a.Symbols,
		Environment:   a.Environment,
	}
	if req.Environment.SchemaVersion == 0 {
		req.Environment.SchemaVersion = 1
	}

	// errorText is sanitized HERE, before anything reaches the search
	// request: only the derived fingerprint, error code, and public-symbol
	// mentions survive. The raw text (paths, tokens, usernames) is dropped
	// on the floor by construction (goal.md §8.5, contract C11).
	if strings.TrimSpace(a.ErrorText) != "" {
		var pkgNames []string
		for _, ps := range a.Packages {
			if p, err := domain.ParsePURL(ps); err == nil {
				pkgNames = append(pkgNames, p.Name)
			}
		}
		san := sanitizer.Sanitize(a.ErrorText, "", pkgNames)
		req.ErrorFingerprint = san.Fingerprint
		req.ErrorCode = san.Code
		for _, sym := range san.PublicSymbols {
			if !containsFold(req.Symbols, sym) {
				req.Symbols = append(req.Symbols, sym)
			}
		}
	}

	resp := s.Deps.Search(ctx, req)
	return textResult(renderSearchResponse(resp), resp)
}

func containsFold(ss []string, want string) bool {
	for _, s := range ss {
		if strings.EqualFold(s, want) {
			return true
		}
	}
	return false
}

// renderSearchResponse renders the §11.5 layout: MATCH/CONFIDENCE header,
// then Exact / Different / Adaptation needed / Evidence sections. Evidence
// lines keep observation and verification counts separated and label their
// evidence class — compile observations are never presented as execution
// proof (goal.md §3.5).
func renderSearchResponse(resp domain.SearchResponse) string {
	if resp.Miss || len(resp.Results) == 0 {
		return "MATCH: NO_SAFE_MATCH\n\n" +
			"No safe match in the local network cache for this environment. " +
			"NO_SAFE_MATCH is deliberately better than a wrong HIT (goal.md §3.8): solve the problem fresh, " +
			"and consider propose_public_sample afterwards so the network learns."
	}
	var b strings.Builder
	for i, r := range resp.Results {
		if i > 0 {
			b.WriteString("\n--- alternative " + strconv.Itoa(i+1) + " ---\n\n")
		}
		b.WriteString("MATCH: " + string(r.Grade) + "\n")
		b.WriteString("CONFIDENCE: " + r.Confidence + "\n\n")
		writeSection(&b, "Exact", r.Exact)
		writeSection(&b, "Different", r.Different)
		writeSection(&b, "Adaptation needed", r.Adaptation)

		e := r.Evidence
		b.WriteString("Evidence\n")
		fmt.Fprintf(&b, "- Project compile observations: %d [USAGE_OBSERVATION — co-occurrence, not execution proof]\n", e.ProjectCompileObservations)
		fmt.Fprintf(&b, "- Clean builds: %d [USAGE_OBSERVATION]\n", e.CleanBuilds)
		fmt.Fprintf(&b, "- Contract passes: %d [SAMPLE_VERIFICATION — sandboxed contract runs]\n", e.ContractPasses)
		fmt.Fprintf(&b, "- Independent cross peers: %d\n", e.IndependentCrossPeers)
		if len(e.ElevatedFailures) > 0 {
			fmt.Fprintf(&b, "- Elevated failures: %s\n", strings.Join(e.ElevatedFailures, "; "))
		}

		if len(r.KnownFailures) > 0 {
			b.WriteString("\nKnown failures matching your environment\n")
			for _, k := range r.KnownFailures {
				line := "- "
				if k.ErrorCode != "" {
					line += k.ErrorCode + " "
				}
				line += fmt.Sprintf("(count %d)", k.Count)
				if len(k.Hypotheses) > 0 {
					var hs []string
					for _, h := range k.Hypotheses {
						hs = append(hs, fmt.Sprintf("%s %.2f", h.Domain, h.Confidence))
					}
					line += " — hypotheses: " + strings.Join(hs, ", ") + " (probabilistic, not definitive)"
				}
				b.WriteString(line + "\n")
			}
		}

		if r.SampleID != "" {
			b.WriteString("\nSample: " + r.SampleID)
			if r.SampleStatus != "" {
				b.WriteString(" (status " + r.SampleStatus + ")")
			}
			b.WriteString(" — fetch files with get_sample\n")
		}
		if r.Case != nil && r.Case.Goal != "" {
			b.WriteString("Goal: " + r.Case.Goal + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func writeSection(b *strings.Builder, title string, items []string) {
	b.WriteString(title + "\n")
	if len(items) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, it := range items {
		b.WriteString("- " + it + "\n")
	}
	b.WriteString("\n")
}

// --- get_sample ---

type getSampleArgs struct {
	SampleID string `json:"sampleId"`
}

func (s *Server) toolGetSample(ctx context.Context, raw json.RawMessage) *toolResult {
	var a getSampleArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return errResult("get_sample: bad arguments: " + err.Error())
	}
	if a.SampleID == "" {
		return errResult("get_sample: sampleId is required")
	}
	manifest, files, err := s.Deps.GetSample(ctx, a.SampleID)
	if err != nil {
		return errResult("get_sample: " + err.Error())
	}

	var b strings.Builder
	b.WriteString("Sample " + a.SampleID + "\n")
	if manifest.License != "" {
		b.WriteString("License: " + manifest.License + "\n")
	}
	if manifest.Case.Goal != "" {
		b.WriteString("Goal: " + manifest.Case.Goal + "\n")
	}
	if len(manifest.Packages) > 0 {
		b.WriteString("Packages: " + strings.Join(manifest.Packages, ", ") + "\n")
	}
	if len(manifest.ContractCommand) > 0 {
		b.WriteString("Contract command: " + strings.Join(manifest.ContractCommand, " ") + "\n")
	}
	if ctxLabel := manifest.Environment.ContextLabel(); ctxLabel != "" {
		b.WriteString("Declared execution context: " + ctxLabel + "\n")
	}
	if len(files) == 0 {
		b.WriteString("\n(no artifact cached locally — metadata only)\n")
	} else {
		paths := make([]string, 0, len(files))
		for p := range files {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		fmt.Fprintf(&b, "\nFiles (%d):\n", len(paths))
		for _, p := range paths {
			b.WriteString("\n--- " + p + " ---\n")
			b.WriteString(files[p])
			if !strings.HasSuffix(files[p], "\n") {
				b.WriteString("\n")
			}
		}
	}
	structured := map[string]any{
		"sampleId": a.SampleID,
		"manifest": manifest,
		"files":    files,
	}
	return textResult(b.String(), structured)
}

// --- explain_compatibility ---

type explainArgs struct {
	Package     string                        `json:"package"`
	Symbol      string                        `json:"symbol"`
	Environment domain.EnvironmentFingerprint `json:"environment"`
}

func (s *Server) toolExplain(ctx context.Context, raw json.RawMessage) *toolResult {
	var a explainArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return errResult("explain_compatibility: bad arguments: " + err.Error())
	}
	if a.Package == "" {
		return errResult("explain_compatibility: package is required")
	}
	text, snapshot, err := s.Deps.Explain(ctx, a.Package, a.Symbol, a.Environment)
	if err != nil {
		return errResult("explain_compatibility: " + err.Error())
	}
	if len(snapshot) == 0 {
		snapshot = json.RawMessage("null")
	}
	structured := map[string]any{
		"package":  a.Package,
		"symbol":   a.Symbol,
		"snapshot": snapshot,
	}
	return textResult(text, structured)
}

// --- run_observed_command ---

type runArgs struct {
	Command []string `json:"command"`
	Cwd     string   `json:"cwd"`
}

func (s *Server) toolRunObserved(ctx context.Context, raw json.RawMessage) *toolResult {
	var a runArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return errResult("run_observed_command: bad arguments: " + err.Error())
	}
	if len(a.Command) == 0 {
		return errResult("run_observed_command: command is required")
	}
	exitCode, stage, result, sanitized, err := s.Deps.RunObserved(ctx, a.Command, a.Cwd)
	if err != nil {
		return errResult("run_observed_command: " + err.Error())
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Exit code: %d\n", exitCode)
	fmt.Fprintf(&b, "Observed stage: %s — result: %s\n", stage, result)
	b.WriteString("Evidence class: USAGE_OBSERVATION (project-level observation; never execution proof for individual symbols)\n")
	if len(sanitized) > 0 {
		b.WriteString("Sanitized errors:\n")
		for _, line := range sanitized {
			b.WriteString("- " + line + "\n")
		}
	}
	structured := map[string]any{
		"exitCode":        exitCode,
		"stage":           stage,
		"result":          result,
		"sanitizedErrors": sanitized,
		"evidenceClass":   string(domain.ClassUsageObservation),
	}
	return textResult(b.String(), structured)
}

// --- report_sample_adoption ---

type adoptionArgs struct {
	SampleID  string `json:"sampleId"`
	Applied   *bool  `json:"applied"`
	BuildPass *bool  `json:"buildPass"`
}

func (s *Server) toolReportAdoption(ctx context.Context, raw json.RawMessage) *toolResult {
	var a adoptionArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return errResult("report_sample_adoption: bad arguments: " + err.Error())
	}
	if a.SampleID == "" {
		return errResult("report_sample_adoption: sampleId is required")
	}
	if a.Applied == nil {
		return errResult("report_sample_adoption: applied is required")
	}
	if err := s.Deps.ReportAdoption(ctx, a.SampleID, *a.Applied, a.BuildPass); err != nil {
		return errResult("report_sample_adoption: " + err.Error())
	}
	text := fmt.Sprintf("Recorded adoption report for %s (applied=%v", a.SampleID, *a.Applied)
	if a.BuildPass != nil {
		text += fmt.Sprintf(", buildPass=%v", *a.BuildPass)
	}
	text += "). Evidence class: ADOPTION_EVIDENCE — queued for anonymous upload."
	structured := map[string]any{
		"recorded":      true,
		"sampleId":      a.SampleID,
		"applied":       *a.Applied,
		"evidenceClass": string(domain.ClassAdoptionEvidence),
	}
	if a.BuildPass != nil {
		structured["buildPass"] = *a.BuildPass
	}
	return textResult(text, structured)
}

// --- propose_public_sample ---

type proposeArgs struct {
	Goal     string   `json:"goal"`
	Packages []string `json:"packages"`
	Symbols  []string `json:"symbols"`
}

func (s *Server) toolPropose(ctx context.Context, raw json.RawMessage) *toolResult {
	var a proposeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return errResult("propose_public_sample: bad arguments: " + err.Error())
	}
	if strings.TrimSpace(a.Goal) == "" {
		return errResult("propose_public_sample: goal is required")
	}
	if len(a.Packages) == 0 {
		return errResult("propose_public_sample: packages is required")
	}
	spec, prompt, workdir, err := s.Deps.Propose(ctx, a.Goal, a.Packages, a.Symbols)
	if err != nil {
		return errResult("propose_public_sample: " + err.Error())
	}

	var b strings.Builder
	b.WriteString("Clean-room workspace created: " + workdir + "\n\n")
	b.WriteString("Generate the sample in that EMPTY directory following these instructions:\n\n")
	b.WriteString(prompt)
	b.WriteString("\nIMPORTANT — publishing is NOT possible from this tool. ")
	b.WriteString("Publication requires the user's explicit approval via the CLI ")
	b.WriteString("(csx sample create <workdir>, then csx sample preview and csx sample publish, ")
	b.WriteString("where the user reviews every file and confirms). ")
	b.WriteString("MCP deliberately has no publish capability (goal.md §12.4).")

	structured := map[string]any{
		"spec":                        spec,
		"prompt":                      prompt,
		"workdir":                     workdir,
		"publishRequiresUserApproval": true,
	}
	return textResult(b.String(), structured)
}

// --- list_local_hits ---

type hitJSON struct {
	TS            string `json:"ts"`
	Query         string `json:"query,omitempty"`
	Grade         string `json:"grade,omitempty"`
	SampleID      string `json:"sampleId,omitempty"`
	Adopted       bool   `json:"adopted"`
	PostBuildPass *bool  `json:"postBuildPass,omitempty"`
}

func (s *Server) toolListHits(ctx context.Context, _ json.RawMessage) *toolResult {
	rows, err := s.Deps.LocalHits(ctx)
	if err != nil {
		return errResult("list_local_hits: " + err.Error())
	}
	hits := make([]hitJSON, 0, len(rows))
	var b strings.Builder
	fmt.Fprintf(&b, "Recent local hits (%d):\n", len(rows))
	for _, r := range rows {
		h := hitJSON{
			TS:       r.TS.UTC().Format("2006-01-02T15:04:05Z"),
			Query:    r.Query,
			Grade:    string(r.Grade),
			SampleID: r.SampleID,
			Adopted:  r.Adopted,
		}
		if r.PostBuildPass.Valid {
			v := r.PostBuildPass.Bool
			h.PostBuildPass = &v
		}
		hits = append(hits, h)
		fmt.Fprintf(&b, "- %s %s grade=%s sample=%s adopted=%v", h.TS, h.Query, h.Grade, h.SampleID, h.Adopted)
		if h.PostBuildPass != nil {
			fmt.Fprintf(&b, " postBuildPass=%v", *h.PostBuildPass)
		}
		b.WriteString("\n")
	}
	if len(rows) == 0 {
		b.WriteString("- (none)\n")
	}
	return textResult(b.String(), map[string]any{"hits": hits})
}

// --- get_local_stats ---

func (s *Server) toolLocalStats(ctx context.Context, _ json.RawMessage) *toolResult {
	stats, err := s.Deps.LocalStats(ctx)
	if err != nil {
		return errResult("get_local_stats: " + err.Error())
	}
	keys := make([]string, 0, len(stats))
	for k := range stats {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("Local CodeSampleX stats:\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "- %s: %v\n", k, stats[k])
	}
	return textResult(b.String(), stats)
}
