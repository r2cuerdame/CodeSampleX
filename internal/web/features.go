package web

import (
	"net/http"

	"github.com/r2cuerdame/codesamplex/internal/web/i18n"
)

// featureField and featureTool describe the public MCP surface as it is
// registered in internal/mcp/tools.go. Identifiers and JSON stay in English:
// clients send these exact names regardless of the website locale.
type featureField struct {
	Name        string
	Type        string
	Description string
}

type featureTool struct {
	Name          string
	Summary       string
	What          string
	When          string
	Required      []featureField
	Optional      []featureField
	InputExample  string
	OutputShape   string
	OutputExample string
	Privacy       string
}

type featureGroup struct {
	ID      string
	Title   string
	Summary string
	Tools   []featureTool
}

type featuresPage struct {
	basePage
	Groups []featureGroup
}

func (s *site) features(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	b := s.page(r, lang, i18n.T(lang, "features.title")+" — CodeSampleX",
		i18n.T(lang, "meta.features"))
	s.render(w, "features", http.StatusOK, featuresPage{
		basePage: b,
		Groups:   publicMCPFeatureGroups(),
	})
}

func publicMCPFeatureGroups() []featureGroup {
	return []featureGroup{
		{
			ID:      "find",
			Title:   "Find and inspect evidence",
			Summary: "Search for a verified answer, fetch its files, or inspect compatibility evidence for one public software target and symbol.",
			Tools: []featureTool{
				{
					Name:     "search_known_solution",
					Summary:  "Find a verified solution graded against the environment you describe.",
					What:     "Searches the local CodeSampleX network cache. A hit reports its match grade, environment differences, adaptations, evidence counts, and the contract assertions that actually passed. A miss is returned as NO_SAFE_MATCH, never guessed into a hit.",
					When:     "Call before using a public library, SDK, runtime, OS command or standalone CLI, or when its error may already have a verified detour.",
					Required: []featureField{{"query", "string", "What you are trying to do or fix, in plain words."}},
					Optional: []featureField{
						{"packages", "string[]", "Public target coordinates: pkg:npm/axios@1.12.0 for a registry package or pkg:generic/cli/npm@11.5.2 for the npm CLI itself."},
						{"symbols", "string[]", "Public symbol families, for example axios.post."},
						{"environment", "object", "Sparse environment fingerprint; known values may include ecosystem, OS, architecture, runtime, language, compiler, package manager, module system, frameworks, execution context, browser/engine, libc, virtualization, and container runtime."},
						{"errorText", "string", "Raw error text to sanitize locally into an error code and fingerprint."},
					},
					InputExample: `{
  "query": "send JSON and retry a reset connection",
  "packages": ["pkg:npm/axios@1.12.0"],
  "symbols": ["axios.post"],
  "environment": {"os":"linux","runtime":"node","runtimeVersion":"22.18","executionContext":"node"}
}`,
					OutputShape: "MCP content text plus structuredContent containing the search response and a local offerId on an eligible hit. A miss can also include packageOverview and localReady.",
					OutputExample: `{
  "structuredContent": {
    "schemaVersion": 2,
    "results": [{"grade":"EXACT","sampleId":"sha256:..."}],
    "offerId": "local-offer-id"
  }
}`,
					Privacy: "Searches cached public evidence. Raw error text is sanitized locally and discarded; only its derived fingerprint, error code, and public-symbol mentions are used. Arbitrary generic target names are rejected; only the fixed public CLI/SDK/OS vocabulary can become demand data. The local hit record stays on this machine.",
				},
				{
					Name:         "get_sample",
					Summary:      "Fetch the manifest and readable files for one cached public sample.",
					What:         "Returns the sample manifest and cached text files. Each file is capped at 64 KB and binary files are skipped. Public samples are MIT-0 community artifacts.",
					When:         "Use after search_known_solution returns a sampleId and you need the complete runnable example.",
					Required:     []featureField{{"sampleId", "string", "Content-addressed sample id in sha256:... form."}},
					InputExample: `{"sampleId":"sha256:0123456789abcdef..."}`,
					OutputShape:  "MCP content text plus structuredContent with sampleId, manifest, and a path-to-content files object.",
					OutputExample: `{
  "structuredContent": {
    "sampleId": "sha256:...",
    "manifest": {"license":"MIT-0","packages":["pkg:npm/axios@1.12.0"]},
    "files": {"src/index.mjs":"..."}
  }
}`,
					Privacy: "Reads an already cached public artifact. It does not inspect the current project or upload anything.",
				},
				{
					Name:     "explain_compatibility",
					Summary:  "Explain cached compatibility evidence for a package and optional symbol.",
					What:     "Reads local compatibility shards and keeps project observations separate from contract verification evidence instead of adding unlike evidence together.",
					When:     "Use when you need the per-symbol evidence behind a result or want to compare one package with a target environment.",
					Required: []featureField{{"package", "string", "Package purl, for example pkg:npm/axios@1.12.0."}},
					Optional: []featureField{
						{"symbol", "string", "Public symbol family, for example axios.post."},
						{"environment", "object", "The same sparse environment fingerprint accepted by search_known_solution."},
					},
					InputExample: `{
  "package": "pkg:npm/axios@1.12.0",
  "symbol": "axios.post",
  "environment": {"runtime":"node","runtimeVersion":"22.18"}
}`,
					OutputShape: "MCP content text plus structuredContent with package, symbol, and the underlying compatibility snapshot (or null).",
					OutputExample: `{
  "structuredContent": {
    "package": "pkg:npm/axios@1.12.0",
    "symbol": "axios.post",
    "snapshot": {"schemaVersion":1,"rows":[]}
  }
}`,
					Privacy: "Reads locally cached compatibility shards. It does not send project files, source, or raw logs.",
				},
			},
		},
		{
			ID:      "observe",
			Title:   "Run with observation",
			Summary: "Execute a normal build or test while recording a sanitized public-package outcome.",
			Tools: []featureTool{
				{
					Name:         "run_observed_command",
					Summary:      "Run an argv command through the evidence loop and return its real exit code.",
					What:         "Scans public dependencies, runs the command locally, classifies its stage and result, and returns only sanitized error templates alongside the original exit code.",
					When:         "Use for builds and tests after adopting or creating package-dependent code, instead of running the command outside CodeSampleX.",
					Required:     []featureField{{"command", "string[]", "Command argv, for example [\"npm\",\"test\"]."}},
					Optional:     []featureField{{"cwd", "string", "Working directory; defaults to the current directory."}},
					InputExample: `{"command":["npm","test"],"cwd":"project"}`,
					OutputShape:  "MCP content text plus structuredContent with exitCode, stage, result, sanitizedErrors, and evidenceClass.",
					OutputExample: `{
  "structuredContent": {
    "exitCode": 0,
    "stage": "PROJECT_TEST",
    "result": "PASS",
    "sanitizedErrors": [],
    "evidenceClass": "USAGE_OBSERVATION"
  }
}`,
					Privacy: "The command runs locally. In community mode, only sanitized facts about public packages, versions, symbols, environment, and result can be queued as evidence—never source, project names, paths, secrets, private packages, or raw logs.",
				},
			},
		},
		{
			ID:      "improve",
			Title:   "Close the evidence loop",
			Summary: "Report whether an offered answer worked, or prepare a clean-room sample proposal after a miss.",
			Tools: []featureTool{
				{
					Name:    "report_sample_adoption",
					Summary: "Record whether a search result was applied and whether the next build passed.",
					What:    "Correlates the report with the local offer returned by search_known_solution and records ADOPTION_EVIDENCE. A failure is counted as avoided only when the full local correlation proves it.",
					When:    "Call after deciding whether to use an offered sample, once the post-adoption build result is known or explicitly unknown.",
					Required: []featureField{
						{"offerId", "string", "Opaque local offer id returned by search_known_solution."},
						{"sampleId", "string", "The offered sha256 content address."},
						{"applied", "boolean", "Whether the sample approach was applied."},
					},
					Optional:     []featureField{{"buildPass", "boolean", "Whether the project built or passed after adoption; omit when unknown."}},
					InputExample: `{"offerId":"local-offer-id","sampleId":"sha256:...","applied":true,"buildPass":true}`,
					OutputShape:  "MCP content text plus structuredContent with recorded, uploadQueued, sampleId, applied, reportedFailureAvoided, evidenceClass, and buildPass when supplied.",
					OutputExample: `{
  "structuredContent": {
    "recorded": true,
    "uploadQueued": true,
    "sampleId": "sha256:...",
    "applied": true,
    "buildPass": true,
    "reportedFailureAvoided": false,
    "evidenceClass": "ADOPTION_EVIDENCE"
  }
}`,
					Privacy: "The correlation and hit stay local. Community mode may queue the anonymous adoption outcome; local-only mode records it locally and uploads nothing.",
				},
				{
					Name:    "propose_public_sample",
					Summary: "Create a sanitized clean-room brief and an empty local workspace.",
					What:    "Builds a proposal from a goal, public package purls, and public symbols, then returns generation instructions and the exact empty workspace path. This tool cannot publish.",
					When:    "Use after NO_SAFE_MATCH when you solved the boundary and the observed build or contract passed.",
					Required: []featureField{
						{"goal", "string", "The behavior the sample should prove."},
						{"packages", "string[]", "Public package purls the sample must use."},
					},
					Optional:     []featureField{{"symbols", "string[]", "Public symbol families the sample should demonstrate."}},
					InputExample: `{"goal":"axios upload progress","packages":["pkg:npm/axios@1.12.0"],"symbols":["axios.post"]}`,
					OutputShape:  "MCP content text plus structuredContent with spec, prompt, workdir, and publishRequiresUserApproval.",
					OutputExample: `{
  "structuredContent": {
    "spec": {"goal":"axios upload progress","packages":["pkg:npm/axios@1.12.0"]},
    "prompt": "Generate the sample...",
    "workdir": "<local clean-room path>",
    "publishRequiresUserApproval": true
  }
}`,
					Privacy: "Only the goal and public package/symbol coordinates enter the clean-room spec; project source and paths do not. The workspace is local, and publishing is deliberately unavailable through MCP.",
				},
			},
		},
		{
			ID:      "inspect",
			Title:   "Inspect this installation",
			Summary: "Read recent local search outcomes or the local dashboard counters without uploading anything.",
			Tools: []featureTool{
				{
					Name:         "list_local_hits",
					Summary:      "List recent search hits, grades, and adoption outcomes stored locally.",
					What:         "Returns recent hit rows with timestamp, query, grade, sample id, adopted state, and post-build result when it was reported.",
					When:         "Use to audit which answers this installation received and whether they were later adopted.",
					InputExample: `{}`,
					OutputShape:  "MCP content text plus structuredContent with a hits array. postBuildPass is omitted when unknown.",
					OutputExample: `{
  "structuredContent": {
    "hits": [{"ts":"2026-08-13T10:00:00Z","query":"axios post","grade":"COMPATIBLE","sampleId":"sha256:...","adopted":true,"postBuildPass":true}]
  }
}`,
					Privacy: "This is local dashboard data. Reading it does not upload the query or hit history.",
				},
				{
					Name:         "get_local_stats",
					Summary:      "Read mode, cache, queue, hit, and adoption counters for this installation.",
					What:         "Returns the current local stats object. Common keys include mode, hits, cachedSamples, queuedUploads, pendingObservations, and the verified-detour outcome counters; the available set can grow.",
					When:         "Use to check initialization, cache readiness, pending community work, or whether the evidence loop is being used repeatedly.",
					InputExample: `{}`,
					OutputShape:  "MCP content text plus the local stats map directly as structuredContent.",
					OutputExample: `{
  "structuredContent": {
    "mode": "community",
    "hits": 7,
    "cachedSamples": 31,
    "queuedUploads": 0,
    "pendingObservations": 0
  }
}`,
					Privacy: "All counters are read from the local database and configuration. Calling this tool uploads nothing.",
				},
			},
		},
	}
}
