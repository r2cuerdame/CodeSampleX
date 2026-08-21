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
	// API is the read half of the HTTP surface. The page described the CLI
	// and the MCP tools and never mentioned that the network answers HTTP
	// directly, so a reader who wanted the evidence without either had no
	// way to learn these exist.
	API []apiEndpoint
}

func (s *site) features(w http.ResponseWriter, r *http.Request) {
	lang := s.negotiate(w, r)
	b := s.page(r, lang, i18n.T(lang, "features.title")+" — CodeSampleX",
		i18n.T(lang, "meta.features"))
	s.render(w, "features", http.StatusOK, featuresPage{
		basePage: b,
		Groups:   localizedMCPFeatureGroups(lang),
		API:      publicReadAPI(),
	})
}

func localizedMCPFeatureGroups(lang string) []featureGroup {
	groups := publicMCPFeatureGroups()
	if lang != "ko" {
		return groups
	}
	for gi := range groups {
		groups[gi].Title = koreanFeatureCopy(groups[gi].Title)
		groups[gi].Summary = koreanFeatureCopy(groups[gi].Summary)
		for ti := range groups[gi].Tools {
			tool := &groups[gi].Tools[ti]
			tool.Summary = koreanFeatureCopy(tool.Summary)
			tool.What = koreanFeatureCopy(tool.What)
			tool.When = koreanFeatureCopy(tool.When)
			tool.OutputShape = koreanFeatureCopy(tool.OutputShape)
			tool.Privacy = koreanFeatureCopy(tool.Privacy)
			for fi := range tool.Required {
				tool.Required[fi].Description = koreanFeatureCopy(tool.Required[fi].Description)
			}
			for fi := range tool.Optional {
				tool.Optional[fi].Description = koreanFeatureCopy(tool.Optional[fi].Description)
			}
		}
	}
	return groups
}

func koreanFeatureCopy(source string) string {
	if translated, ok := koreanFeatureTranslations[source]; ok {
		return translated
	}
	return source
}

var koreanFeatureTranslations = map[string]string{
	"Find and inspect evidence": "증거 찾기와 확인",
	"Search for a verified answer, fetch its files, or inspect compatibility evidence for one public software target and symbol.": "공개 소프트웨어와 심벌에 대해 검증된 답을 찾고, 파일을 받거나 호환성 증거를 확인합니다.",
	"Run with observation": "실행하며 관측하기",
	"Execute a normal build or test while recording a sanitized public-package outcome.": "평소처럼 빌드나 테스트를 실행하면서 공개 패키지의 정제된 결과를 기록합니다.",
	"Close the evidence loop": "증거 순환 완성하기",
	"Report whether an offered answer worked, or prepare a clean-room sample proposal after a miss.": "제공된 답이 실제로 통했는지 알리거나, 답을 못 찾은 뒤 클린룸 샘플 제안을 준비합니다.",
	"Inspect this installation": "이 설치 상태 확인하기",
	"Read recent local search outcomes or the local dashboard counters without uploading anything.": "아무것도 업로드하지 않고 최근 로컬 검색 결과와 로컬 통계를 확인합니다.",

	"Find a verified solution graded against the environment you describe.": "설명한 환경을 기준으로 등급이 매겨진 검증 해법을 찾습니다.",
	"Searches the local CodeSampleX network cache. A hit reports its match grade, environment differences, adaptations, evidence counts, and the contract assertions that actually passed. A miss is returned as NO_SAFE_MATCH, never guessed into a hit.": "로컬 CodeSampleX 네트워크 캐시를 검색합니다. 답이 있으면 일치 등급, 환경 차이, 필요한 수정, 증거 수와 실제 통과한 계약을 보여줍니다. 답이 없으면 추측하지 않고 NO_SAFE_MATCH를 반환합니다.",
	"Call before using a public library, SDK, runtime, OS command or standalone CLI, or when its error may already have a verified detour.":                                                                                                                "공개 라이브러리·SDK·런타임·OS 명령·독립 CLI를 쓰기 전이나, 이미 검증된 오류 우회법이 있을 법할 때 호출합니다.",
	"What you are trying to do or fix, in plain words.":                                                                            "하려는 일이나 고치려는 문제를 평문으로 적습니다.",
	"Public target coordinates: pkg:npm/axios@1.12.0 for a registry package or pkg:generic/cli/npm@11.5.2 for the npm CLI itself.": "공개 대상 좌표입니다. 레지스트리 패키지는 pkg:npm/axios@1.12.0, npm CLI 자체는 pkg:generic/cli/npm@11.5.2처럼 적습니다.",
	"Public symbol families, for example axios.post.":                                                                              "axios.post 같은 공개 심벌 계열입니다.",
	"Sparse environment fingerprint; known values may include ecosystem, OS, architecture, runtime, language, compiler, package manager, module system, frameworks, execution context, browser/engine, libc, virtualization, and container runtime.": "희소한 환경 지문입니다. 생태계, OS, 아키텍처, 런타임, 언어, 컴파일러, 패키지 관리자, 모듈 시스템, 프레임워크, 실행 컨텍스트, 브라우저/엔진, libc, 가상화, 컨테이너 런타임 등을 알 때만 넣습니다.",
	"Raw error text to sanitize locally into an error code and fingerprint.":                                                                                                 "로컬에서 오류 코드와 지문으로 정제할 원본 오류 문구입니다.",
	"MCP content text plus structuredContent containing the search response and a local offerId on an eligible hit. A miss can also include packageOverview and localReady.": "MCP 텍스트와 검색 응답이 든 structuredContent를 반환합니다. 사용할 수 있는 답에는 로컬 offerId가 붙고, 미스에는 packageOverview와 localReady가 포함될 수 있습니다.",
	"Searches cached public evidence. Raw error text is sanitized locally and discarded; only its derived fingerprint, error code, and public-symbol mentions are used. Arbitrary generic target names are rejected; only the fixed public CLI/SDK/OS vocabulary can become demand data. The local hit record stays on this machine.": "캐시된 공개 증거만 검색합니다. 원본 오류 문구는 로컬에서 정제한 뒤 버리고, 파생 지문·오류 코드·공개 심벌 언급만 사용합니다. 임의의 일반 대상 이름은 거부하며 정해진 공개 CLI·SDK·OS 어휘만 수요 데이터가 될 수 있습니다. 로컬 적중 기록은 이 컴퓨터에 남습니다.",

	"Fetch the manifest and readable files for one cached public sample.":                                                                                         "캐시된 공개 샘플 하나의 매니페스트와 읽을 수 있는 파일을 가져옵니다.",
	"Returns the sample manifest and cached text files. Each file is capped at 64 KB and binary files are skipped. Public samples are MIT-0 community artifacts.": "샘플 매니페스트와 캐시된 텍스트 파일을 반환합니다. 파일 하나는 64KB로 제한하고 바이너리는 건너뜁니다. 공개 샘플은 MIT-0 커뮤니티 아티팩트입니다.",
	"Use after search_known_solution returns a sampleId and you need the complete runnable example.":                                                              "search_known_solution이 sampleId를 반환했고 실행 가능한 전체 예제가 필요할 때 사용합니다.",
	"Content-addressed sample id in sha256:... form.":                                                                                                             "sha256:... 형식의 콘텐츠 주소 샘플 ID입니다.",
	"MCP content text plus structuredContent with sampleId, manifest, and a path-to-content files object.":                                                        "MCP 텍스트와 sampleId, manifest, 경로별 내용이 든 files 객체를 structuredContent로 반환합니다.",
	"Reads an already cached public artifact. It does not inspect the current project or upload anything.":                                                        "이미 캐시된 공개 아티팩트만 읽습니다. 현재 프로젝트를 살피거나 아무것도 업로드하지 않습니다.",

	"Explain cached compatibility evidence for a package and optional symbol.":                                                                                 "패키지와 선택한 심벌의 캐시된 호환성 증거를 설명합니다.",
	"Reads local compatibility shards and keeps project observations separate from contract verification evidence instead of adding unlike evidence together.": "로컬 호환성 샤드를 읽고, 성격이 다른 프로젝트 관측과 계약 검증 증거를 합산하지 않고 따로 보여줍니다.",
	"Use when you need the per-symbol evidence behind a result or want to compare one package with a target environment.":                                      "결과 뒤의 심벌별 증거가 필요하거나 패키지 하나를 목표 환경과 비교할 때 사용합니다.",
	"Package purl, for example pkg:npm/axios@1.12.0.":                                                                                                          "pkg:npm/axios@1.12.0 같은 패키지 purl입니다.",
	"Public symbol family, for example axios.post.":                                                                                                            "axios.post 같은 공개 심벌 계열입니다.",
	"The same sparse environment fingerprint accepted by search_known_solution.":                                                                               "search_known_solution이 받는 것과 같은 희소 환경 지문입니다.",
	"MCP content text plus structuredContent with package, symbol, and the underlying compatibility snapshot (or null).":                                       "MCP 텍스트와 package, symbol, 기초 호환성 snapshot(없으면 null)이 든 structuredContent를 반환합니다.",
	"Reads locally cached compatibility shards. It does not send project files, source, or raw logs.":                                                          "로컬에 캐시된 호환성 샤드만 읽습니다. 프로젝트 파일, 소스, 원본 로그를 보내지 않습니다.",

	"Run an argv command through the evidence loop and return its real exit code.":                                                                                       "argv 명령을 증거 순환 안에서 실행하고 실제 종료 코드를 반환합니다.",
	"Scans public dependencies, runs the command locally, classifies its stage and result, and returns only sanitized error templates alongside the original exit code.": "공개 의존성을 살피고 명령을 로컬에서 실행한 뒤 단계와 결과를 분류해, 원래 종료 코드와 정제된 오류 템플릿만 반환합니다.",
	"Use for builds and tests after adopting or creating package-dependent code, instead of running the command outside CodeSampleX.":                                    "패키지 의존 코드를 채택하거나 만든 뒤 빌드와 테스트를 CodeSampleX 밖에서 직접 실행하는 대신 사용합니다.",
	"Command argv, for example [\"npm\",\"test\"].":                                                             "[\"npm\",\"test\"] 같은 명령 argv입니다.",
	"Working directory; defaults to the current directory.":                                                     "작업 디렉터리이며 생략하면 현재 디렉터리입니다.",
	"MCP content text plus structuredContent with exitCode, stage, result, sanitizedErrors, and evidenceClass.": "MCP 텍스트와 exitCode, stage, result, sanitizedErrors, evidenceClass가 든 structuredContent를 반환합니다.",
	"The command runs locally. In community mode, only sanitized facts about public packages, versions, symbols, environment, and result can be queued as evidence—never source, project names, paths, secrets, private packages, or raw logs.": "명령은 로컬에서 실행됩니다. 커뮤니티 모드에서도 공개 패키지·버전·심벌·환경·결과의 정제된 사실만 증거로 대기열에 들어갑니다. 소스, 프로젝트명, 경로, 비밀정보, 비공개 패키지, 원본 로그는 절대 들어가지 않습니다.",

	"Record whether a search result was applied and whether the next build passed.":                                                                                                               "검색 결과를 적용했는지와 다음 빌드가 통과했는지 기록합니다.",
	"Correlates the report with the local offer returned by search_known_solution and records ADOPTION_EVIDENCE. A failure is counted as avoided only when the full local correlation proves it.": "보고를 search_known_solution이 반환한 로컬 제안과 연결해 ADOPTION_EVIDENCE로 기록합니다. 전체 로컬 상관관계가 증명될 때만 실패를 피한 것으로 셉니다.",
	"Call after deciding whether to use an offered sample, once the post-adoption build result is known or explicitly unknown.":                                                                   "제공된 샘플을 쓸지 결정하고 적용 뒤 빌드 결과를 알게 됐거나 알 수 없음이 확정됐을 때 호출합니다.",
	"Opaque local offer id returned by search_known_solution.":                                                                                                                                    "search_known_solution이 반환한 불투명한 로컬 제안 ID입니다.",
	"The offered sha256 content address.":                                    "제공된 sha256 콘텐츠 주소입니다.",
	"Whether the sample approach was applied.":                               "샘플의 접근법을 적용했는지 여부입니다.",
	"Whether the project built or passed after adoption; omit when unknown.": "적용 뒤 프로젝트가 빌드 또는 테스트를 통과했는지 여부이며, 모르면 생략합니다.",
	"MCP content text plus structuredContent with recorded, uploadQueued, sampleId, applied, reportedFailureAvoided, evidenceClass, and buildPass when supplied.": "MCP 텍스트와 recorded, uploadQueued, sampleId, applied, reportedFailureAvoided, evidenceClass 및 제공된 경우 buildPass가 든 structuredContent를 반환합니다.",
	"The correlation and hit stay local. Community mode may queue the anonymous adoption outcome; local-only mode records it locally and uploads nothing.":        "상관관계와 적중 기록은 로컬에 남습니다. 커뮤니티 모드는 익명 적용 결과를 대기열에 넣을 수 있고, 로컬 전용 모드는 로컬에만 기록하며 아무것도 업로드하지 않습니다.",

	"Create a sanitized clean-room brief and an empty local workspace.": "정제된 클린룸 작업 지시서와 빈 로컬 작업공간을 만듭니다.",
	"Builds a proposal from a goal, public package purls, and public symbols, then returns generation instructions and the exact empty workspace path. This tool cannot publish.": "목표, 공개 패키지 purl, 공개 심벌로 제안을 만들고 생성 지침과 정확한 빈 작업공간 경로를 반환합니다. 이 도구는 게시할 수 없습니다.",
	"Use after NO_SAFE_MATCH when you solved the boundary and the observed build or contract passed.":                                                                             "NO_SAFE_MATCH 뒤에 문제 경계를 해결했고 관측 빌드나 계약이 통과했을 때 사용합니다.",
	"The behavior the sample should prove.":                                                                "샘플이 증명해야 할 동작입니다.",
	"Public package purls the sample must use.":                                                            "샘플이 반드시 사용할 공개 패키지 purl입니다.",
	"Public symbol families the sample should demonstrate.":                                                "샘플이 보여줘야 할 공개 심벌 계열입니다.",
	"MCP content text plus structuredContent with spec, prompt, workdir, and publishRequiresUserApproval.": "MCP 텍스트와 spec, prompt, workdir, publishRequiresUserApproval이 든 structuredContent를 반환합니다.",
	"Only the goal and public package/symbol coordinates enter the clean-room spec; project source and paths do not. The workspace is local, and publishing is deliberately unavailable through MCP.": "클린룸 명세에는 목표와 공개 패키지·심벌 좌표만 들어가며 프로젝트 소스와 경로는 들어가지 않습니다. 작업공간은 로컬이고 MCP에는 게시 기능이 의도적으로 없습니다.",

	"List recent search hits, grades, and adoption outcomes stored locally.":                                                      "로컬에 저장된 최근 검색 적중, 등급, 적용 결과를 나열합니다.",
	"Returns recent hit rows with timestamp, query, grade, sample id, adopted state, and post-build result when it was reported.": "최근 적중의 시각, 질의, 등급, 샘플 ID, 적용 상태와 보고된 경우 적용 후 빌드 결과를 반환합니다.",
	"Use to audit which answers this installation received and whether they were later adopted.":                                  "이 설치본이 어떤 답을 받았고 나중에 적용했는지 확인할 때 사용합니다.",
	"MCP content text plus structuredContent with a hits array. postBuildPass is omitted when unknown.":                           "MCP 텍스트와 hits 배열이 든 structuredContent를 반환합니다. postBuildPass는 모르면 생략됩니다.",
	"This is local dashboard data. Reading it does not upload the query or hit history.":                                          "로컬 대시보드 데이터입니다. 읽어도 질의나 적중 기록을 업로드하지 않습니다.",

	"Read mode, cache, queue, hit, and adoption counters for this installation.": "이 설치본의 모드, 캐시, 대기열, 적중, 적용 통계를 읽습니다.",
	"Returns the current local stats object. Common keys include mode, hits, cachedSamples, queuedUploads, pendingObservations, and the verified-detour outcome counters; the available set can grow.": "현재 로컬 통계 객체를 반환합니다. 흔한 키로 mode, hits, cachedSamples, queuedUploads, pendingObservations와 검증된 우회 결과 통계가 있으며 항목은 늘어날 수 있습니다.",
	"Use to check initialization, cache readiness, pending community work, or whether the evidence loop is being used repeatedly.":                                                                     "초기화, 캐시 준비 상태, 대기 중인 커뮤니티 작업, 증거 순환이 반복 사용되는지를 확인할 때 사용합니다.",
	"MCP content text plus the local stats map directly as structuredContent.":                                                                                                                         "MCP 텍스트와 로컬 통계 맵을 structuredContent로 바로 반환합니다.",
	"All counters are read from the local database and configuration. Calling this tool uploads nothing.":                                                                                              "모든 통계는 로컬 데이터베이스와 설정에서 읽습니다. 이 도구를 호출해도 아무것도 업로드하지 않습니다.",
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
