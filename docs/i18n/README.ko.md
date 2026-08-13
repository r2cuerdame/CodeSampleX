# CodeSampleX

> **같은 코드 문제를 두 번 풀지 마세요.** (Stop solving the same code twice.)

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX는 코딩 LLM을 위한 **로컬 우선(local-first) 분산 추론 캐시**입니다. 지구상의 모든 에이전트가 공개 라이브러리의 동작 방식을 각자 다시 추론하고, 똑같은 버전 비호환 문제에 반복해서 부딪히는 대신 — CodeSampleX는 실제 개발 환경에서 익명 호환성 **Evidence**를 수집하고, 검증된 정답과 여러분 프로젝트 사이의 정확한 델타(delta)와 함께 **검증된 최소 Sample**을 제공합니다.

- 웹사이트 & Compatibility Explorer: **https://codesamplex.dev**
- LLM이 더 이상 반복해서 답할 필요가 없어지는 질문 하나: *`axios.post`가 axios 1.12 + Node 22 + pnpm + Windows 11 환경에서 실제로 동작하는가 — 동작하지 않는다면 어느 단계에서 깨지는가?*
- **Claude Code, Codex, Gemini CLI, OpenCode**에서 바로 동작하고, 다른 MCP 클라이언트(Cursor, Windsurf, Cline, Zed, VS Code)에도 붙습니다.

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

## 에이전트 통합 (MCP)

**`csx init`이 자동으로 설정:** Claude Code · Codex · Gemini CLI · OpenCode.

**다른 MCP 클라이언트**(Cursor, Windsurf, Cline, Zed, VS Code)도 됩니다. `csx`는 표준 stdio MCP 서버입니다:

```json
{"mcpServers": {"csx": {"command": "csx", "args": ["mcp"]}}}
```

모델을 가리지 않습니다. 같은 호환성 증거를 Claude, GPT·Codex, Gemini, Llama 등 MCP 도구를 호출할 수 있는 모든 모델이 함께 씁니다.

도구: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats`. 샘플 게시는 의도적으로 MCP 기능에 포함하지 **않았습니다** — 전체 미리보기 후 CLI에서 명시적으로 승인해야만 가능합니다.

```bash
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

## 생태계 (Public v1)

Node/TypeScript (npm, pnpm, yarn — 레퍼런스), Python (pip, uv), Go, Rust/Cargo. 정직한 기능 매트릭스: [docs/adapters.md](../adapters.md) — v1에서는 어떤 어댑터도 런타임 심볼 계측을 주장하지 않으며, 심볼 해석 신뢰도에는 항상 라벨이 붙습니다 (`EXACT`/`PROBABLE`/`UNKNOWN`).

## 아키텍처

단일 Go 바이너리(`csx`: 데몬 + CLI + MCP + 피어 노드 + 검증기)와 작은 서버(`csx-server`: PostgreSQL + Caddy 뒤의 서버 렌더링 탐색기)로 구성됩니다. 샘플은 콘텐츠 주소 기반(`sha256`)이며 로컬 캐시 우선 → 피어 → 메인 시더 순으로 배포됩니다. 다운로드된 샘플은 절대 호스트에서 직접 실행되지 않습니다 — `--ignore-scripts`로 의존성을 해석하고, 네트워크가 차단된 샌드박스에서 컴파일과 계약 검증을 수행하며, 영수증(receipt)은 ed25519로 서명됩니다. 자세한 내용은 [goal.md](../../goal.md) (제품 스펙), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md)를 참고하세요.

## 소스에서 빌드하기

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## 라이선스

코드: Apache-2.0. 게시된 샘플의 기본 라이선스는 **MIT-0**입니다.
