# CodeSampleX

> **같은 코드 문제를 두 번 풀지 마세요.** (Stop solving the same code twice.)

<p align="center">
  <img src="../../internal/web/static/inspector-hero-v1.webp" alt="CodeSampleX 호환성 검사관" width="560">
</p>

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX는 코딩 LLM을 위한 **로컬 우선(local-first) 분산 추론 캐시**입니다. 지구상의 모든 에이전트가 공개 라이브러리의 동작 방식을 각자 다시 추론하고, 똑같은 버전 비호환 문제에 반복해서 부딪히는 대신 — CodeSampleX는 실제 개발 환경에서 익명 호환성 **Evidence**를 수집하고, 검증된 정답과 여러분 프로젝트 사이의 정확한 델타(delta)와 함께 **검증된 최소 Sample**을 제공합니다.

- 웹사이트 & Compatibility Explorer: **https://codesamplex.dev**
- LLM이 더 이상 반복해서 답할 필요가 없어지는 질문 하나: *`axios.post`가 axios 1.12 + Node 22 + pnpm + Windows 11 환경에서 실제로 동작하는가 — 동작하지 않는다면 어느 단계에서 깨지는가?*
- **Claude Code, Codex, Gemini CLI, OpenCode**에서 바로 동작하고, 다른 MCP 클라이언트(Cursor, Windsurf, Cline, Zed, VS Code)에도 붙습니다.

## 실시간 네트워크

[검증 기록](https://codesamplex.dev/records) · [발견 사항](https://codesamplex.dev/findings) · [필요한 샘플](https://codesamplex.dev/wanted) · [기여 방법](https://codesamplex.dev/contribute)

공개 카운터는 5분마다 집계되며 로그인 없이 JSON으로 확인할 수 있습니다:

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

공개 배포판의 샘플 소스 업로드는 현재 **시더 전용**입니다. 공식 샘플은 출처를 확인할 수 있는 클린룸 프로젝트만 받습니다. 검색, 익명 Evidence, 검증 영수증, wanted 보드, `NO_SAFE_MATCH` 요청은 계정 없이 모두 열려 있습니다.

## 설치

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

바이너리 하나, 질문 하나. `csx init`은 커뮤니티 계약을 보여주고 단 하나의 선택만 묻습니다 — **JOIN COMMUNITY** 또는 **LOCAL ONLY**. 나머지(데몬, Claude Code / Codex / Gemini CLI / OpenCode용 MCP 등록, 에이전트 규칙)는 모두 자동입니다.

## 계약

```text
You get                              You contribute
✓ Public compatibility knowledge     ✓ Public package/version usage
✓ Verified code answers              ✓ Public API/symbol usage when detectable
✓ Local agent integration            ✓ Build/typecheck/test result
✓ Public sample cache                ✓ Sanitized failure fingerprints

Never shared automatically
✕ Source code        ✕ Repository/project name   ✕ File names or paths
✕ Source snippets    ✕ Secrets or env variables  ✕ Private packages
✕ Raw compiler/runtime logs
```

이것은 숨겨진 텔레메트리가 아니라 프로토콜 그 자체입니다. 커뮤니티 피어는 소비자인 **동시에** 생산자입니다. 로컬 전용 모드에서는 아무것도 전송되지 않습니다. `csx ui`의 프라이버시 미리보기에서 데이터가 머신을 떠나기 전에 정확한 페이로드를 확인할 수 있습니다.

## 동작 방식

```text
you build/test through csx (or your agent does)
→ local analysis: public packages, lockfile-resolved versions, symbols, environment
→ raw errors sanitized locally into fingerprints (paths/names/secrets stripped)
→ anonymous evidence batches → Compatibility Graph on codesamplex.dev
→ your LLM asks CSX first: nearest verified Sample + environment delta
→ it reasons about the DELTA, not the whole problem
```

네 개의 레이어를 정직하게 분리해서 유지합니다:

| 레이어 | 무엇인가 | 신뢰 수준 |
|-------|------------|-------|
| Evidence Network | 익명화된 패키지/버전/심볼/환경/단계/결과 사실 | weak→strong, 클래스 라벨링 |
| Compatibility Graph | 환경별로 집계된 확률적 지도 (실행 컨텍스트 포함: Node/Chrome/Safari/Electron/…) | 파생 뷰 |
| Sample Pool | 사용자 승인, 클린룸, 콘텐츠 주소 기반 최소 프로젝트 | 계약 검증, 교차 검증 |
| Agent Delivery | MCP/CLI: 가장 가까운 샘플 + 델타 + 알려진 실패 사례 | EXACT→NO_SAFE_MATCH 등급 |

프로젝트가 컴파일된다는 사실이 심볼이 동작한다는 의미로 포장되는 일은 결코 없습니다. 원인을 알 수 없는 것은 `UNKNOWN`으로 남습니다. 잘못된 HIT은 MISS보다 나쁩니다 — `NO_SAFE_MATCH`는 버그가 아니라 기능입니다.

검증 영수증은 두 개의 wire 버전을 지원합니다. 기존 v1 영수증도 읽을 수 있지만, 서명된 **v2 영수증**만 `resolvedPackages`를 담을 수 있습니다. 이 값은 resolve가 성공한 직후 검증기가 실제로 설치한 패키지에서 읽은 정규 purl입니다. 출처가 모호하면 버전을 추측하지 않고 비워 둡니다. 서버는 정렬되지 않았거나, 비정규·미선언·다른 생태계 패키지이거나, resolve PASS 없이 붙은 버전 주장을 거부합니다. 호환성 스냅샷도 작성자가 manifest에 적은 버전이 아니라 실제로 실행된 버전에 영수증을 귀속합니다.

## 에이전트 통합 (MCP)

**`csx init`이 자동으로 설정:** Claude Code · Codex · Gemini CLI · OpenCode.

**다른 MCP 클라이언트**(Cursor, Windsurf, Cline, Zed, VS Code)도 됩니다. `csx`는 표준 stdio MCP 서버입니다:

```sh
csx mcp-config          # JSON: Cursor, Cline, Windsurf, Zed, VS Code
csx mcp-config --toml   # TOML: Codex
```

설정은 직접 쓰지 말고 이 명령이 출력한 것을 붙여넣으세요. **설치 경로의 절대 경로**를 넣어 주는데, 그게 핵심입니다: MCP 클라이언트는 로그인 셸에서 시작되지 않아 편집기가 물려준 환경을 그대로 씁니다. 그래서 `"command": "csx"`처럼 이름만 적으면 본인 `PATH`를 고쳐 놓았더라도 실행되지 않습니다.

모델을 가리지 않습니다. 같은 호환성 증거를 Claude, GPT·Codex, Gemini, Llama 등 MCP 도구를 호출할 수 있는 모든 모델이 함께 씁니다.

도구: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats`. 샘플 소스 업로드는 의도적으로 MCP 기능에 포함하지 **않았습니다**. `propose_public_sample`은 정제된 클린룸 작업 명세만 만들며, 권한이 있는 시더가 CLI에서 전체 내용을 검토하고 명시적으로 승인해야 게시할 수 있습니다.

```bash
csx sync                   # 설치 직후 공개 shard 캐시 준비
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

새 설치는 shard 캐시가 비어 있으므로 첫 검색 전에 `csx sync`를 실행해야 합니다. 그렇지 않으면 네트워크에 답이 있어도 모든 검색이 `NO_SAFE_MATCH`처럼 보입니다.

## 생태계 (Public v1)

**프로젝트 스캔 + 샘플 검증:** Node/TypeScript(npm, pnpm, yarn 레퍼런스), Python(pip, uv), Go, Rust/Cargo. Node 샘플은 선언한 런타임에서 검증하므로 Bun과 Deno 결과도 추측이 아닙니다.

**샘플 검증:** PHP/Composer, Ruby/Bundler, Dart/pub, Elixir/Hex. 아직 프로젝트 스캐너는 없지만, 공개 샘플은 고정된 컨테이너에서 빌드되고 계약 테스트를 통과해야 합니다.

정직한 기능 매트릭스: [docs/adapters.md](../adapters.md) — v1에서는 어떤 어댑터도 런타임 심볼 계측을 주장하지 않으며, 심볼 해석 신뢰도에는 항상 라벨이 붙습니다 (`EXACT`/`PROBABLE`/`UNKNOWN`).

## 아키텍처

단일 Go 바이너리(`csx`: 데몬 + CLI + MCP + 피어 노드 + 검증기)와 작은 서버(`csx-server`: PostgreSQL + Caddy 뒤의 서버 렌더링 탐색기)로 구성됩니다. 샘플은 콘텐츠 주소 기반(`sha256`)이며 로컬 캐시 우선 → 피어 → 메인 시더 순으로 배포됩니다. case identity는 주장 내용에서 계산되므로 낡았거나 복사해 붙인 `caseId`는 거부됩니다.

다운로드된 샘플은 호스트에서 직접 실행되지 않습니다. 의존성 해석은 고정된 샌드박스에서 수행하고, 생태계가 지원하면 설치 스크립트를 비활성화합니다. resolve 뒤에는 immutable artifact를 다시 해시하며, 컴파일과 계약 단계는 네트워크를 차단합니다. ed25519로 서명된 v2 영수증에는 각 단계 결과, 실행 환경, 로그 digest, 그리고 검증기가 실제로 확인할 수 있었던 패키지 버전만 들어갑니다. 호환성 집계도 영수증별 패키지 집합을 분리해, 실행하지 않은 버전이나 의존성 조합의 증거로 섞이지 않게 합니다. 자세한 내용은 [goal.md](../../goal.md) (제품 스펙), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md)를 참고하세요.

## 소스에서 빌드하기

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## 라이선스

코드: Apache-2.0. 게시된 샘플의 기본 라이선스는 **MIT-0**입니다.
