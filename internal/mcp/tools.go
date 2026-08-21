package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/r2cuerdame/codesamplex/internal/domain"
	"github.com/r2cuerdame/codesamplex/internal/environment"
	"github.com/r2cuerdame/codesamplex/internal/samples"
	"github.com/r2cuerdame/codesamplex/internal/sanitizer"
	"github.com/r2cuerdame/codesamplex/internal/storage/localdb"
)

// Deps injects tool behavior into the transport so tests fake it and the
// real wiring (NewDeps) stays daemon-free. Every function takes the request
// context; none may retain it.
type Deps struct {
	// Search runs the C7 pipeline over the local store. offerID is an opaque
	// local-only capability for the recorded top result; it is empty on a
	// miss or when local recording failed.
	Search func(ctx context.Context, req domain.SearchRequest) (resp domain.SearchResponse, offerID string)
	// GetSample returns a cached sample's manifest and its files
	// (path → content, ≤64KB per file, binaries skipped).
	GetSample func(ctx context.Context, id string) (domain.SampleManifest, map[string]string, error)
	// Explain renders a compatibility explanation for one package/symbol
	// from locally cached shards, with observation and verification
	// evidence kept separate. snapshot is the underlying JSON (may be null).
	Explain func(ctx context.Context, purl, symbol string, env domain.EnvironmentFingerprint) (text string, snapshot json.RawMessage, err error)
	// Overview summarizes cached evidence for several packages at once. It
	// turns a search miss into a useful answer instead of an empty one; nil
	// simply falls back to the bare NO_SAFE_MATCH text.
	Overview func(ctx context.Context, purls []string, env domain.EnvironmentFingerprint) ([]PackageOverview, error)
	// LocalReadiness reports the install mode and how many compatibility
	// shards are cached. An agent launched straight from a registry — with
	// the binary but no `csx init` — would otherwise see every search miss
	// with no way to tell an empty cache from an empty network.
	LocalReadiness func(ctx context.Context) (mode string, shards int, err error)
	// RunObserved wraps one command in the evidence loop (scan → run →
	// record). sanitized carries only sanitizer output — never raw stderr.
	RunObserved func(ctx context.Context, argv []string, cwd string) (exitCode int, stage, result string, sanitized []string, err error)
	// ReportAdoption records what happened to a returned sample. The outcome
	// can call a failure "avoided" only when the local correlation proves all
	// four stages; the upload remains the existing anonymous adoption event.
	ReportAdoption func(ctx context.Context, offerID, sampleID string, applied bool, buildPass *bool) (localdb.InterventionOutcome, error)
	// Propose builds a sanitized clean-room spec + prompt and creates an
	// empty workspace. It NEVER publishes (goal.md §12.4).
	Propose func(ctx context.Context, goal string, pkgs, symbols []string) (spec samples.SanitizedSpec, prompt string, workdir string, err error)
	// LocalHits lists recent local search hits.
	LocalHits func(ctx context.Context) ([]localdb.HitRow, error)
	// LocalStats returns the local dashboard stats.
	LocalStats func(ctx context.Context) (map[string]any, error)
	// MachineEnv reports this host's environment. nil means collect it.
	MachineEnv func(ctx context.Context) domain.EnvironmentFingerprint
	// Mode reports the configured mode ("community", "local-only", or "").
	// Tools consult it before telling a caller that anything will be sent.
	Mode func() string
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
			"ecosystem":             strProp("npm | pypi | cargo | golang | maven | generic (explicit public CLI, SDK, engine or OS target)"),
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
			// libc decides whether a package with a native module loads at
			// all, and the grader treats it as such -- but it compares the
			// dimension only when BOTH sides declare it, and there was no
			// way for a caller to declare it. An agent on Alpine asking
			// about a sample verified on glibc was told MATCH: EXACT with an
			// empty list of differences.
			"libc":             strProp("musl | glibc — musl (Alpine) cannot load glibc-built native modules"),
			"virtualization":   strProp("e.g. container, wsl, vm"),
			"containerRuntime": strProp("e.g. docker, podman"),
		},
	}
}

// machineEnv is what this host actually is, for the dimensions no agent can
// be expected to state.
func (s *Server) machineEnv(ctx context.Context) domain.EnvironmentFingerprint {
	if s.Deps.MachineEnv != nil {
		return s.Deps.MachineEnv(ctx)
	}
	return environment.Collect(ctx, nil)
}

// fillFromMachine completes the dimensions the caller left blank with facts
// about the machine the caller is running on.
//
// An agent knows its project: the runtime, the package manager, the module
// system. It does not know whether this container is musl or glibc, and it
// was never asked -- the tool schema had no libc property at all. The
// grader compares libc only when both sides declare it, so the dimension
// that decides whether a native module loads simply never took part, and a
// caller on Alpine was told a glibc-verified sample was an EXACT match with
// nothing listed as different.
//
// Anything the caller DID state wins: an agent asking about a deployment
// target it is not currently on is answering a different question, and this
// must not overwrite it. Host-shaped dimensions below the OS are filled
// only when the OS agrees, so a Windows machine does not contribute its
// version bucket to a question about Linux.
func fillFromMachine(req, machine domain.EnvironmentFingerprint) domain.EnvironmentFingerprint {
	if req.OS == "" {
		req.OS = machine.OS
	}
	if req.Arch == "" {
		req.Arch = machine.Arch
	}
	if !strings.EqualFold(req.OS, machine.OS) {
		return req
	}
	if req.OSVersionBucket == "" {
		req.OSVersionBucket = machine.OSVersionBucket
	}
	if req.Libc == "" {
		req.Libc = machine.Libc
	}
	if req.Virtualization == "" {
		req.Virtualization = machine.Virtualization
	}
	if req.ContainerRuntime == "" {
		req.ContainerRuntime = machine.ContainerRuntime
	}
	return req
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
			Description: "Search the CodeSampleX network cache for a community-verified solution before solving from scratch. Results are graded for YOUR environment (EXACT/COMPATIBLE/ADAPTATION_REQUIRED/REFERENCE_ONLY); NO_SAFE_MATCH means solve it fresh. A hit lists what that sample's contract PROVED — the assertions it ran offline in a pinned container and passed, which is where the per-overload, per-option behaviour is: which argument shapes are accepted, what is raised instead, which setting decides. Evidence keeps observation counts and verification counts separate — compile observations are never execution proof, and the counts pool every call shape, so a specific call is answered by the contract lines rather than by the numbers.",
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
				"offerId":   str("opaque local offer id returned by search_known_solution"),
				"sampleId":  str("the adopted (or rejected) sample id"),
				"applied":   map[string]any{"type": "boolean", "description": "true if the sample's approach was applied to the project"},
				"buildPass": map[string]any{"type": "boolean", "description": "whether the project built/passed after adoption; omit if not known yet"},
			}, "offerId", "sampleId", "applied"),
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
	if !isJSONObject(args) {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call arguments must be a JSON object"}
	}
	result := handler(ctx, args)
	if n := staleBuildNotice(); n != "" {
		result.Content = append(result.Content, contentItem{Type: "text", Text: "UPDATE NOTICE: " + n})
	}
	return result, nil
}

// isJSONObject reports whether raw is a JSON object, which is what the
// error above has always claimed to check. json.Valid alone accepts a
// string, a number, an array and null — and null is the one that hurt:
// unmarshalling null into a struct is a no-op that returns no error, so
// {"arguments": null} ran the tool with every field zeroed. A caller that
// sent a malformed argument got a confident empty-query search back
// instead of being told what was wrong with the call.
func isJSONObject(raw json.RawMessage) bool {
	if !json.Valid(raw) {
		return false
	}
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			return b == '{'
		}
	}
	return false
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
		SchemaVersion:         2,
		Query:                 a.Query,
		Packages:              a.Packages,
		Symbols:               a.Symbols,
		SymbolProvenance:      domain.SearchProvenanceExplicit,
		Environment:           a.Environment,
		EnvironmentProvenance: domain.SearchProvenanceExplicit,
	}
	if req.Environment.SchemaVersion == 0 {
		req.Environment.SchemaVersion = 1
	}
	req.Environment = fillFromMachine(req.Environment, s.machineEnv(ctx))

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
		// The stage is unknown here, so ask under all of the stages this
		// error could have been recorded at -- see SanitizedError.
		// Fingerprints. Without this the fingerprint search matched nothing
		// on any install.
		req.ErrorFingerprints = san.Fingerprints()
		req.ErrorCode = san.Code
		for _, sym := range san.PublicSymbols {
			if !containsFold(req.ContextSymbols, sym) {
				req.ContextSymbols = append(req.ContextSymbols, sym)
			}
		}
	}

	resp, offerID := s.Deps.Search(ctx, req)

	// A miss is the common case on a young network, and "nothing here" is a
	// wasted round trip: the cache usually holds observation evidence for
	// the very packages being asked about. Hand that over instead — it is
	// not a solution and is never labeled as one, but it tells the agent
	// whether this combination is well-trodden or unexplored.
	var overview []PackageOverview
	if (resp.Miss || len(resp.Results) == 0) && len(a.Packages) > 0 && s.Deps.Overview != nil {
		overview, _ = s.Deps.Overview(ctx, a.Packages, req.Environment)
	}
	if resp.Miss || len(resp.Results) == 0 {
		hint, ready := s.readinessHint(ctx)
		if len(overview) > 0 || hint != "" || resp.Observed != nil {
			return textResult(renderMiss(overview, hint, resp.Observed), map[string]any{
				"response": resp, "packageOverview": overview, "localReady": ready,
				"observed": resp.Observed,
			})
		}
	}
	return textResult(renderSearchResponse(resp), localSearchStructured{
		SearchResponse: resp,
		OfferID:        offerID,
	})
}

// localSearchStructured deliberately lives in the MCP package rather than
// domain.SearchResponse: offerId is a local capability, not part of the
// public /v1/search schema or any upload document.
type localSearchStructured struct {
	domain.SearchResponse
	OfferID string `json:"offerId,omitempty"`
}

// PackageOverview is the compact "what does the network know about this
// package" summary attached to a miss. Counts are observation evidence:
// projects that compiled with the package present, never proof that a
// specific symbol executed (goal.md §3.5).
type PackageOverview struct {
	PURL         string  `json:"purl"`
	Cached       bool    `json:"cached"`
	Observations int64   `json:"observations"`
	PeerBuckets  int64   `json:"peerBuckets"`
	PassRate     float64 `json:"passRate"`
	Samples      int     `json:"samples"`
	TopFailure   string  `json:"topFailure,omitempty"`
}

// readinessHint explains an EMPTY LOCAL CACHE, which is a different fact
// from an empty network and must never be reported as one. It returns the
// text to append (empty when the cache is warm) and whether this install
// can answer at all.
func (s *Server) readinessHint(ctx context.Context) (string, bool) {
	if s.Deps.LocalReadiness == nil {
		return "", true
	}
	mode, shards, err := s.Deps.LocalReadiness(ctx)
	if err != nil || shards > 0 {
		return "", true
	}
	switch mode {
	case "community", "local-only":
		return "This install has no compatibility shards cached yet, so the miss above says " +
			"nothing about what the network knows. Run `csx sync`, or start the daemon with " +
			"`csx daemon`, and retry.", false
	default:
		return "This install has not been initialized: no shards are cached, so every search " +
			"misses regardless of what the network knows. Run `csx init` to pick a mode and " +
			"warm the cache — it asks one question, and sends nothing unless you join.", false
	}
}

// renderMiss writes NO_SAFE_MATCH, any readiness hint, and the per-package
// evidence summary.
func renderMiss(overview []PackageOverview, hint string, observed *domain.ObservedReports) string {
	var b strings.Builder
	b.WriteString("DECISION: UNKNOWN — no safe verified match.\n\n")
	b.WriteString("MATCH: NO_SAFE_MATCH\n\n")
	b.WriteString("No sample this network built matches this goal here. Solve it fresh — " +
		"a wrong HIT is worse than a MISS (goal.md §3.8).\n\n")
	if hint != "" {
		b.WriteString(hint + "\n\n")
	}
	b.WriteString(renderObserved(observed))
	if len(overview) == 0 {
		return b.String()
	}
	b.WriteString("What the network already knows about these packages " +
		"[USAGE_OBSERVATION — project-level co-occurrence, NOT execution proof]:\n")
	for _, o := range overview {
		if !o.Cached {
			b.WriteString("- " + o.PURL + ": no cached data — UNKNOWN, not incompatible " +
				"(run `csx sync` while the server is reachable)\n")
			continue
		}
		if o.Observations == 0 && o.Samples == 0 {
			b.WriteString("- " + o.PURL + ": shard cached, no observations yet for this package\n")
			continue
		}
		fmt.Fprintf(&b, "- %s: %d observations across %d independent peer buckets, pass rate %.2f",
			o.PURL, o.Observations, o.PeerBuckets, o.PassRate)
		if o.Samples > 0 {
			fmt.Fprintf(&b, "; %d sample(s) that built exist for other goals [SAMPLE_VERIFICATION]", o.Samples)
		}
		if o.TopFailure != "" {
			b.WriteString("; most common recorded failure: " + o.TopFailure)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nUse explain_compatibility for the per-symbol breakdown. " +
		"Once your solution works, propose_public_sample turns it into the answer the next agent gets.\n")
	return b.String()
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
		return "DECISION: UNKNOWN — no safe verified match.\n\n" +
			"MATCH: NO_SAFE_MATCH\n\n" +
			"No safe match in the local network cache for this environment. " +
			"NO_SAFE_MATCH is deliberately better than a wrong HIT (goal.md §3.8): solve the problem fresh, " +
			"and consider propose_public_sample afterwards so the network learns."
	}
	var b strings.Builder
	b.WriteString(renderDecision(resp.Results[0]))
	b.WriteString("\n\n")
	// What this network actually did, before any verdict about where the
	// caller is standing. The answer opened with MATCH: REFERENCE_ONLY over
	// a sample carrying two signed contract receipts, and put the fact that
	// the network had verified it at all under "Evidence", below the deltas
	// — which every windows caller reads, because every verifier is a linux
	// container. The one thing the network owns outright was the one thing
	// it buried.
	b.WriteString(renderBuilt(resp.Results[0]))
	for i, r := range resp.Results {
		if i > 0 {
			b.WriteString("\n--- alternative " + strconv.Itoa(i+1) + " ---\n\n")
		}
		b.WriteString("MATCH: " + string(r.Grade) + "\n")
		b.WriteString("CONFIDENCE: " + r.Confidence)
		if why := r.ConfidenceReason(); why != "" {
			b.WriteString(" — " + why)
		}
		b.WriteString("\n\n")
		// The finding leads the hit. Everything under it describes how well
		// this sample matches and how much ran; the finding is the sentence
		// that says the answer the caller was about to write is wrong, and
		// it used to sit at the bottom, inside the contract block, below the
		// deltas and the evidence counts and the failure clusters.
		writeFinding(&b, r)
		writeSection(&b, "Exact", r.Exact)
		writeSection(&b, "Different", r.Different)
		writeSection(&b, "Adaptation needed", r.Adaptation)

		e := r.Evidence
		b.WriteString("Evidence\n")
		fmt.Fprintf(&b, "- Project compile observations: %d [USAGE_OBSERVATION — co-occurrence, not execution proof]\n", e.ProjectCompileObservations)
		fmt.Fprintf(&b, "- Clean builds: %d [USAGE_OBSERVATION]\n", e.CleanBuilds)
		fmt.Fprintf(&b, "- Contract passes: %d [SAMPLE_VERIFICATION — sandboxed contract runs]\n", e.ContractPasses)
		// "Independent" overstated what this counts. A peer id is a hash of
		// a self-generated key with no registration behind it, so the
		// number is distinct KEYS that reported a pass, and one person can
		// hold as many as they like. It still means something — the
		// contract ran to completion in more than one environment — but it
		// is not proof of independent parties, and naming it that way
		// invited exactly the wrong inference.
		fmt.Fprintf(&b, "- Distinct verifying peer keys: %d "+
			"[self-asserted identities, not verified as separate parties]\n",
			e.IndependentCrossPeers)
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
			// No status. The ladder grades, and a grade is the one thing this
			// network does not offer — half of production's CROSS_PASS labels do
			// not hold under the rule that grants them, and the line at the top
			// already states the fact the label was standing in for.
			b.WriteString("\nSample: " + r.SampleID + " — fetch files with get_sample\n")
		}
		if r.Case != nil && r.Case.Goal != "" {
			b.WriteString("Goal: " + r.Case.Goal + "\n")
		}
		b.WriteString(contractBlock(r))
	}
	return strings.TrimRight(b.String(), "\n")
}

// writeFinding prints the belief this hit's contract contradicts.
//
// It is guarded on a contract having actually passed, the same guard the
// contract block uses: a belief with nothing measured against it is an
// opinion, and an opinion is what the network exists not to publish.
func writeFinding(b *strings.Builder, r domain.SearchResult) {
	if r.Case == nil || r.Evidence.ContractPasses == 0 {
		return
	}
	believed := strings.TrimSpace(r.Case.Believed)
	if believed == "" {
		return
	}
	b.WriteString("FINDING — commonly assumed: " + believed + "\n")
	b.WriteString("  The contract below measured otherwise.\n\n")
}

// renderBuilt states the only thing this network offers: a sample that
// builds, and where it built.
//
// It said VERIFIED. The word is an integrity claim — it invites "verified,
// therefore safe, therefore correct here" — and the network warrants none of
// that. What it did was run the contract in a sandbox and keep the signed
// receipt. Whether the same code builds on the caller's platform it never
// measured, which is why the delta is a list rather than a score.
//
// There is nothing beyond this. No grade, no integrity: a sample that built,
// and the record of where.
func renderBuilt(r domain.SearchResult) string {
	if r.Evidence.ContractPasses <= 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "BUILT: this sample built and its contract passed %d time(s)",
		r.Evidence.ContractPasses)
	if r.Evidence.IndependentCrossPeers > 0 {
		fmt.Fprintf(&b, " across %d verifying peer key(s)", r.Evidence.IndependentCrossPeers)
	}
	if r.SampleStatus != "" {
		// The status ladder is not shown. It grades, and a grade is the one
		// thing this network does not offer; half of production's CROSS_PASS
		// labels do not even hold under the rule that grants them.
		_ = r.SampleStatus
	}
	b.WriteString(".\n")
	if len(r.Different) > 0 {
		b.WriteString("It built somewhere that differs from where you are — see Different below. " +
			"Whether it builds there is not something this network measured, and not something it claims.\n")
	}
	b.WriteString("\n")
	return b.String()
}

// renderDecision is deliberately the first, compact line of an MCP search
// answer. It says what the caller should do before the detailed evidence.
// The exact-failure label is earned only by an exact sanitized fingerprint
// match plus an already-passing contract in a reusable environment.
func renderDecision(r domain.SearchResult) string {
	switch {
	case r.Evidence.ContractPasses <= 0 || len(r.Different) > 0 || len(r.Adaptation) > 0 ||
		r.Grade == domain.GradeAdaptationRequired:
		return "DECISION: REVERIFY — adapt or verify this sample in your environment before use."
	case r.Grade == domain.GradeReferenceOnly:
		return "DECISION: REFERENCE_ONLY — use this as reference only; it is not verified for this environment."
	case !r.VerifiedOffer():
		return "DECISION: REVERIFY — adapt or verify this sample in your environment before use."
	case r.ExactFailureMatched:
		return "DECISION: VERIFIED_DETOUR — exact failure fingerprint matched a contract PASS reusable in this environment."
	default:
		return "DECISION: REUSE_VERIFIED — a contract PASS is reusable in this environment."
	}
}

// maxContractLinesShown bounds an untrimmed list that came from a local
// manifest. A shard-sourced list is already bounded by the server.
const maxContractLinesShown = 8

// contractBlock renders what this sample's contract asserted and proved.
//
// It carries what a goal sentence cannot: which argument shapes are
// accepted, what is raised instead of returned, which option or environment
// setting decides the outcome. It sat in every sample and reached nobody —
// the rendered answer stopped at the goal line, and seeing more cost a
// second get_sample call that an agent only makes after it has already
// decided to use the sample, by which point the claims are no longer what
// it needed them for.
//
// Three clauses keep the heading honest, and each one is load-bearing:
//
//   - Printed only when a contract actually PASSED. Lines without a pass
//     are an author's intent, not a result, and "proven" would then be a
//     claim the evidence does not support.
//   - Scoped to the exact package versions the run used. The delta above
//     prints only major.minor, so this is the only place the pinned version
//     appears — and a claim about 4.4.3 is not a claim about all of 4.4.
//   - Never recomputes a count of what it withheld. A shard-sourced list
//     already ends in the server's own "… and N more" sentinel when it was
//     trimmed; re-trimming and counting again here would understate a long
//     list and swallow that sentinel.
//
// Every line is printed verbatim. Sorting them into "real assertions" and
// "setup" would be a judgement no contract made — and in this repo's own
// corpus the lines that read most like cautions are exactly the ones such a
// filter drops.
func contractBlock(r domain.SearchResult) string {
	if r.Case == nil || len(r.Case.Contract) == 0 || r.Evidence.ContractPasses == 0 {
		return ""
	}
	scope := strings.Join(r.Case.Packages, ", ")
	if scope == "" {
		return "" // nothing to scope the claim to, so make no claim
	}

	var b strings.Builder
	// The belief is NOT repeated here. It leads the hit now — writeFinding
	// prints it above the match deltas — and printing it twice in one answer
	// reads as two findings.
	b.WriteString("Proven by its contract for " + scope + "\n")
	b.WriteString("  (it ran in a pinned container with the network off and passed;\n")
	b.WriteString("   these are the author's own lines about that run)\n")

	lines := r.Case.Contract
	if len(lines) > maxContractLinesShown+1 {
		for _, line := range lines[:maxContractLinesShown] {
			b.WriteString("  - " + line + "\n")
		}
		fmt.Fprintf(&b, "  … and %d more — get_sample\n", len(lines)-maxContractLinesShown)
		return b.String()
	}
	for _, line := range lines {
		b.WriteString("  - " + line + "\n")
	}
	return b.String()
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
	OfferID   string `json:"offerId"`
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
	// A content address, not any string. Without this an agent could report
	// adoption of a sample nothing ever returned — the row is queued for
	// anonymous upload and becomes evidence about a sample that may not
	// exist, and adoption is the number the whole "reasoning avoided"
	// figure is derived from.
	if !validContentAddress(a.SampleID) {
		return errResult("report_sample_adoption: sampleId must be \"sha256:\" + 64 lowercase hex")
	}
	if a.OfferID == "" {
		return errResult("report_sample_adoption: " + localdb.ErrOfferIDRequired.Error())
	}
	if a.Applied == nil {
		return errResult("report_sample_adoption: applied is required")
	}
	outcome, err := s.Deps.ReportAdoption(ctx, a.OfferID, a.SampleID, *a.Applied, a.BuildPass)
	if err != nil {
		return errResult("report_sample_adoption: " + err.Error())
	}
	failureAvoided := outcome.ReportedFailureAvoided()
	var text string
	if failureAvoided {
		text = fmt.Sprintf("Reported failure avoided for %s: exact failure fingerprint matched, a verified detour was offered and applied, and the post-hit build passed", a.SampleID)
	} else {
		text = fmt.Sprintf("Recorded adoption report for %s (applied=%v", a.SampleID, *a.Applied)
	}
	if a.BuildPass != nil {
		if failureAvoided {
			text += fmt.Sprintf(" (buildPass=%v", *a.BuildPass)
		} else {
			text += fmt.Sprintf(", buildPass=%v", *a.BuildPass)
		}
	} else if failureAvoided {
		text += " (buildPass not reported"
	}
	// Do not promise an upload this install will never make. In local-only
	// mode nothing drains the queue, and saying "queued for anonymous
	// upload" to a user who chose the mode that sends nothing was simply
	// false.
	queued := outcome.UploadQueued
	if queued {
		text += "). Evidence class: ADOPTION_EVIDENCE — queued for anonymous upload."
	} else {
		text += "). Evidence class: ADOPTION_EVIDENCE — recorded locally; this install uploads nothing."
	}
	structured := map[string]any{
		"recorded":               true,
		"uploadQueued":           queued,
		"sampleId":               a.SampleID,
		"applied":                *a.Applied,
		"reportedFailureAvoided": failureAvoided,
		"evidenceClass":          string(domain.ClassAdoptionEvidence),
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
	// The workspace is on the machine running the DAEMON. An agent reaching
	// this server over MCP from a container, a remote host or another OS
	// cannot write there, and the observed failure is not an error — it is
	// an agent quietly choosing a path of its own (/root/csx_layout_sample)
	// and writing a perfectly good sample somewhere `csx sample create`
	// will never be pointed at. Every one of those is a proposal the
	// network loses without anyone noticing it was lost.
	b.WriteString("\n\nIF YOU CANNOT WRITE TO THAT EXACT PATH — it is on the machine running " +
		"the csx daemon, which may not be the one you are on — STOP and say so in your reply. " +
		"Do not choose a different directory silently. If you have already written the files " +
		"somewhere else, say exactly where: `csx sample create` accepts any directory, so the " +
		"work is recoverable as long as the user is told the path.\n")
	b.WriteString("\nIMPORTANT — publishing is NOT possible from this tool, and never will be. ")
	b.WriteString("It requires the user's explicit approval after they review every file ")
	b.WriteString("(goal.md §12.4).\n\n")
	// The agent is the only party that knows a proposal now exists. Left to
	// "the user may publish it later", the workspace gets written and then
	// forgotten — every unreviewed proposal is a sample the network lost.
	b.WriteString("TELL THE USER, in your reply, that a sample is ready for review, and give them ")
	b.WriteString("exactly this:\n\n")
	b.WriteString("  csx sample create " + workdir + "\n")
	b.WriteString("  csx sample preview <sampleId>   # shows every file that would be published\n\n")
	b.WriteString("`csx sample pending` lists anything prepared and not yet reviewed.\n\n")
	// Upload is seeded-only, so telling every user to run `publish` sends
	// most of them into a 403. What the sample IS worth to them is the part
	// that was never said: it is local evidence, and the local engine
	// answers from it immediately.
	b.WriteString("WHERE THIS SAMPLE GOES: it stays on this machine and answers this machine's " +
		"own searches from now on, which is most of its value. Upload to the public network is " +
		"seeded-only — samples there are generated and verified by the project so their origin " +
		"is established, and an unseeded `csx sample publish` is refused with a 403 that explains " +
		"itself. To get this into the network, contribute the IDEA rather than the code: the " +
		"package, the API, what you expected and what actually happened — every NO_SAFE_MATCH " +
		"search already files that ask, and the board is https://codesamplex.dev/wanted.")

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

// validContentAddress checks the "sha256:<64 lowercase hex>" form every
// sample id in this system has.
func validContentAddress(id string) bool {
	const prefix = "sha256:"
	if len(id) != len(prefix)+64 || !strings.HasPrefix(id, prefix) {
		return false
	}
	for _, r := range id[len(prefix):] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// renderObserved writes the relayed record beneath a miss.
//
// The framing is doing real work here. An agent handed "297 of 312 passed"
// will read it as a green light whatever label sits above it, so the text
// says twice what it is and never once suggests an action: no snippet, no
// recommendation, no version to prefer. The grade above it is still
// NO_SAFE_MATCH and this changes nothing about that — it is the record, not
// an answer derived from the record.
func renderObserved(o *domain.ObservedReports) string {
	if o == nil || len(o.Cells) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("RECORDED RUNS for " + o.PURL)
	if o.Symbol != "" {
		b.WriteString(" · " + o.Symbol)
	}
	b.WriteString("\n[OBSERVED — reported by machines that ran this, NOT verified by this " +
		"project. This is not a match and does not become one.]\n")
	for _, c := range o.Cells {
		env := strings.Join(nonEmptyParts(
			c.Environment.OS, c.Environment.Arch,
			strings.TrimSpace(c.Environment.Runtime+" "+c.Environment.RuntimeVersion),
			c.Environment.PackageManager, c.Environment.Libc, c.Environment.Context), " / ")
		if env == "" {
			env = "environment not recorded"
		}
		// The reporter count leads. "297 of 312 passed" from one machine and
		// from two hundred are different facts, and the count is the only
		// thing that separates them.
		machines := "machines"
		if c.Reporters == 1 {
			machines = "machine"
		}
		// USED is presence: the package was installed, and nothing was run.
		// Printing it as "N of N passed" rebuilt the USED inflation this
		// project scrubbed from every other rate — on the one surface where
		// an agent reads a green number as a verdict, and in the lead line,
		// because USED cells carry the highest reporter counts and sort
		// first.
		if c.Stage == string(domain.StageUsed) {
			fmt.Fprintf(&b, "- %d reporting %s · %s · USED: %d installs recorded (presence — nothing was run)",
				c.Reporters, machines, env, c.Pass+c.Fail)
		} else {
			fmt.Fprintf(&b, "- %d reporting %s · %s · %s: %d of %d passed",
				c.Reporters, machines, env, c.Stage, c.Pass, c.Pass+c.Fail)
		}
		if c.LastSeen != "" {
			b.WriteString(" · last " + c.LastSeen)
		}
		b.WriteString("\n")
	}
	if len(o.Errors) > 0 {
		b.WriteString("Recorded failures (public error codes; no log text ever leaves a machine):\n")
		for _, e := range o.Errors {
			code := e.ErrorCode
			if code == "" {
				code = e.Fingerprint
			}
			fmt.Fprintf(&b, "- %s at %s · %d occurrences\n", code, e.Stage, e.Count)
		}
	}
	b.WriteString("Use these as facts about other people's machines, not as a verdict about " +
		"yours. Nothing here was proven for your case.\n\n")
	return b.String()
}

func nonEmptyParts(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}
