# CodeSampleX

> **Tested. Not guessed.** — 추측이 아니라, 테스트한 결과입니다.

<p align="center">
  <img src="../../internal/web/static/inspector-hero-v1.webp" alt="CodeSampleX 호환성 검사관" width="560">
</p>

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX는 개발자 라이브러리·런타임·툴체인을 위한 **열린 호환성 테스트 네트워크**입니다. 문서를 요약하지도 않고, 일화를 수집하지도 않습니다. 실제로 기록된 환경에서 진짜 빌드와 계약(contract) 테스트를 실행한 뒤 — 무엇이 실제로 동작했고, 어디서 깨졌으며, 그 각각을 얼마나 확신하는지를 보여줍니다.

- 호환성 지도: **https://codesamplex.dev**
- 이 네트워크가 답하는 질문: *거기서 돌아가나요?* — 이 API가, 이 버전에서, 이 OS 위, 이 런타임에서.
- 이 네트워크가 주는 답: *테스트해 봤습니다. 결과는 이렇습니다.*

## 거기서 돌아가나요?

모든 결과는 환경 정보가 붙어 있는 실제 실행 기록이므로, 데이터는 호환성 매트릭스로 피벗됩니다 — OS × 런타임, 버전 × 아키텍처, 심볼 × OS. 라이브 네트워크에서 가져온 실제 단면(`axios.post`, 2026년 8월 측정):

```text
axios.post · axios 1.12.2                node 22          node 24
linux                                    ✓ PASS verified     —
windows                                  ○ 3/9 observed ! ?  (process runs: 3 pass · 6 fail)
```

저 행은 예시로 그린 그림이 아니라 [실제 라이브 페이지](https://codesamplex.dev/npm/axios/1.12.2/axios.post)입니다. linux의 `PASS`는 고정된(pinned) 컨테이너에서 실행된 계약이고, windows 셀은 증거가 더 약하다는 사실(`?` 불확실성 표시가 붙은 `○` 관측 — 계약 실행이 아닌 빌드 관측)과 측정된 실패(`!`)를 있는 그대로 드러냅니다. 알 수 없는 것은 `—`로 남습니다. 패키지가 속한 생태계나 문서로부터 아무것도 추론하지 않습니다.

웹 탐색기는 모든 2차원 그리드를 N차원 큐브의 단면으로 다룹니다. 임의의 두 차원(OS, 런타임, 패키지 버전, 심볼, 아키텍처, 패키지 매니저, 실행 컨텍스트, libc)을 축으로 고르고, 나머지는 필터로 고정한 뒤, 셀을 클릭하면 한 단계씩 더 깊이 — 정확히 측정된 조합들까지 — 파고들 수 있으며, 서명된 영수증(receipt)은 그 조합들의 심볼 페이지에 담겨 있습니다.

## 왜 테스트가 중요한가

```text
Documentation   → what should work
Code search     → how somebody used it
Community       → what somebody says worked
CodeSampleX     → what was actually tested
```

지도를 정직하게 유지하는 규칙:

- 프로젝트가 컴파일된다는 사실이 심볼이 동작한다는 뜻으로 제시되는 일은 **결코** 없습니다. 관측(observation)과 검증(verification)은 따로 집계되며 절대 합산되지 않습니다.
- 원인을 알 수 없는 것은 `UNKNOWN`으로 남습니다. 잘못된 HIT은 MISS보다 나쁩니다 — `NO_SAFE_MATCH`는 그 자체로 유효한 답입니다.
- 증거는 시간이 지나며 감쇠합니다. 결과의 가중치는 90일마다 절반으로 줄고, 오래된 셀에는 그 사실이 표시됩니다.
- 실패 원인은 확률 분포로 보고되며, 확실성을 지어내는 일은 없습니다.

## CLI 설치

CLI는 로컬 테스터입니다. 여러분의 실제 빌드를 감싸고, 그 결과를 익명 증거로 바꾸며, 네트워크의 데이터로 답합니다.

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

이 한 줄에는 `curl`과 CA 인증서가 필요한데, 최소 이미지(debian-slim, alpine, 대부분의 에이전트 컨테이너)에는 둘 다 없습니다 — 게다가 `curl … | sh`는 **curl이 없어도 종료 코드 0으로 끝납니다**. 파이프라인은 마지막 명령의 상태만 보고하기 때문입니다. 먼저 사전 요구 사항을 설치하거나 wget을 사용하세요:

```bash
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

바이너리는 `~/.local/bin`에 설치되는데, 이 경로는 기본적으로 어느 시스템의 `PATH`에도 들어 있지 않습니다:

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # the install check — `csx --version` is not a spelling csx accepts
```

바이너리 하나, 질문 하나. `csx init`은 아래의 계약을 보여주고 단 하나의 선택만 묻습니다 — **JOIN COMMUNITY** 또는 **LOCAL ONLY**. `sh`로 파이프해 설치하면 stdin이 다운로드 파이프에 소비되므로, `init`은 안내된 기본값인 JOIN COMMUNITY를 택합니다. 언제든 `csx init --local-only`로 빠져나올 수 있으며, 두 모드 플래그 모두 재실행 가능하고 비대화형입니다. 스크립트나 CI 설정에서는: `csx init --community --yes --no-agents`.

## 테스트하고 확인하기

```bash
csx run -- pnpm build              # wrap any build/test — its result becomes evidence
csx search "axios multipart upload"  # a verified answer, graded for YOUR environment
csx scan                           # record which public packages a project uses, no build
csx stats                          # local dashboard: hits, adoptions, queue
csx ui                             # browser dashboard + privacy preview
csx sync                           # warm the shard cache — once, right after install
```

`csx sync`는 있어도 그만인 장식이 아닙니다. 갓 설치한 상태에는 캐시된 shard가 하나도 없어서, 동기화 전까지 모든 검색이 `NO_SAFE_MATCH`를 반환합니다. 그 이후에는 데몬이 백그라운드에서 캐시를 다시 데워 둡니다.

`csx search`는 모든 결과를 기록된 여러분의 환경에 대해 등급 매깁니다 — `EXACT`, `COMPATIBLE`, `ADAPTATION_REQUIRED`, `REFERENCE_ONLY`, `NO_SAFE_MATCH` — 그리고 답이 증명된 환경과 지금 여러분이 있는 환경 사이의 정확한 델타(`different`, `adaptationNeeded`)를 나열합니다.

## 검증된 샘플

샘플은 스니펫이 아닙니다. 정규 아티팩트의 `sha256:<hex>`로 콘텐츠 주소가 부여된 최소 프로젝트이며, **계약** — 고정된 컨테이너에서 오프라인으로 실행되어 통과한 단언(assertion)들 — 을 갖습니다. 클린룸 작성 루프는 CLI 전용입니다:

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief, empty workspace
csx sample create <dir>      # ingest the clean-room project
csx sample verify <id>       # resolve → compile → contract, sandboxed
csx sample publish <id>      # requires typing exactly "yes"; leakage findings hard-refuse
```

게시 시에는 시크릿, 경로, 프로젝트 이름, 비공개 URL을 스캔하며 — 발견 사항이 있으면 게시가 **차단**되고, 이를 무시할 플래그는 없습니다. 샘플 소스 업로드는 의도적으로 MCP 기능에 넣지 않았습니다. 게시는 CLI 앞에 앉은 사람만 할 수 있습니다.

## 발견 사항

어디서 깨질까요? [Findings](https://codesamplex.dev/findings)는 측정으로 확인된 모순의 목록입니다. 문서(또는 통념)가 말하는 것과 계약이 측정한 것을 나란히 놓습니다 — 문서 불일치, 환경별 실패, 버전 경계. 모든 항목은 그것을 증명하는 계약이 담긴 게시 샘플로 연결되므로, 직접 측정을 재실행해 반박할 수도 있습니다.

기계가 도출한 발견 사항은, 작성자가 바로잡으려는 통념을 함께 기록해 둔 게시 샘플에서 자라납니다. 누군가 페이지를 편집해서 추가하는 것이 아닙니다.

## 증거와 등급

셀을 왜 신뢰할 수 있을까요? 모든 결과는 자신의 증거 등급을 지니고 있습니다. 약함 → 강함 순으로:

| 등급 | 실제로 일어난 일 |
|-------|------------------------|
| `USAGE_OBSERVATION` | 실제 프로젝트가 이 패키지로 빌드/타입체크/테스트되었음 — 관측, 약한 증거 |
| `ADOPTION_EVIDENCE` | 누군가 샘플을 적용한 뒤 빌드가 통과했는지 여부를 보고했음 |
| `SAMPLE_VERIFICATION` | 샘플의 계약이 고정된 컨테이너에서 실행되어 통과했음 |
| `CROSS_PASS` | 독립적인 피어가 다시 실행해 재차 통과했음 |
| `MATRIX_PASS` | 통과한 실행이 2개 이상의 OS/런타임/브라우저 경계에 걸쳐 있음 |
| `STABLE` | 독립 피어 3명 이상이 통과시켰고, 30일 동안 실패 기록이 없음 |

샘플 페이지에는 검증 사다리 `L0_SOURCE_ONLY` → `L5_MATRIX_PASS` 배지도 붙고, 매트릭스 셀에는 신뢰도(`HIGH`/`MEDIUM`/`LOW`), 실패율 상승 플래그, 마지막 확인 날짜가 함께 표시됩니다. 서명된 **v2 영수증**만 `resolvedPackages`를 주장할 수 있습니다 — 작성자가 적어 넣은 버전이 아니라 검증기가 실제로 설치한 버전이며, 스냅샷은 각 영수증을 실제로 실행된 버전 아래에 분류합니다.

공개 카운터는 집계 값(rollup)이며, 계정 없이 JSON으로 받을 수 있습니다:

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

| 필드 | 세는 것 |
|-------|----------------|
| `packages` / `symbols` | 커버리지: 호환성 데이터에 있는 공개 패키지 이름과 관측된 심볼 |
| `evidence` | 수락된 관측 레코드 — 사용자, 프로젝트, 검증된 샘플 수가 아님 |
| `verifiedSamples` | 샌드박스 계약 PASS 영수증이 있는 고유 샘플 수 |
| `peers` / `projectsMonth` | 고유한 익명 일간/월간 기여자 버킷 |
| `postHitBuildsReported` | 측정된 PASS 또는 FAIL이 포함된 채택 보고 |

CodeSampleX는 **신뢰할 수 있는 고유/활성 사용자 수, 살아 있는 MCP 프로세스 수, 설치 성공 수를 아직 측정하지 않습니다**. stats 응답의 `estimated*` 필드는 명시적으로 공식 기반 추정치이며, 측정된 수치로 읽어서는 안 됩니다.

## 기여자 워커

이 네트워크의 환경은 다른 사람들의 머신입니다. 남는 머신이 한 대 있다면 MCP나 에이전트 설정을 건드리지 않고도 Docker로 격리된 검증을 기여할 수 있습니다:

```bash
csx init --community --yes --no-agents --no-daemon
csx worker start                         # idle-aware, 2 Docker lanes
csx worker start --parallel 4 --budget 15m
```

워커는 서버가 할당한 VERIFY 작업(`cross` / `matrix`)만 받습니다 — 큐가 임의의 셸 명령을 보내는 일은 없습니다. 아티팩트는 콘텐츠 주소 기반이며 해시가 검사되고, resolve는 컨테이너 안에서 수행되며, 컴파일과 계약 단계는 고정된 `512m` 메모리 / `256` PID 제한이 걸린 일회용 Docker 작업 공간에서 네트워크 차단 상태로 실행됩니다. Docker 데몬이 없으면 단호히 거부하며, 호스트로 폴백하는 일은 결코 없습니다. 결과는 ed25519로 서명된 v2 영수증이고, 원시 단계 로그는 로컬에 남습니다. [Contribute](https://codesamplex.dev/contribute)를 참고하세요.

## API

웹사이트가 렌더링하는 것과 같은 데이터를, 계정 없이 JSON으로:

| 엔드포인트 | 제공하는 것 |
|----------|----------------|
| `GET /v1/stats` | 일일 네트워크 집계 |
| `POST /v1/search`, `POST /v2/search` | 쿼리 + 환경 지문에 대한 등급 매겨진 답 |
| `GET /v1/registry/packages/{purl}` | 패키지 상세 + 패키지 수준 스냅샷 |
| `GET /v1/registry/symbols/{eco}/{package}/{family}` | 한 심볼의 버전별 스냅샷 |
| `GET /v1/shards/{eco}/{package}/{major}` | 사전 생성된 호환성 shard (ETag 캐시) |
| `GET /v1/samples/{id}`, `…/artifact` | 샘플 메타데이터, 영수증, tar.gz 소스 |
| `GET /v1/wanted` | 수요 큐: 요청되었지만 답을 얻지 못한 것 |
| `GET /v1/adapters` | 생태계별 기능 매트릭스 |

## 에이전트 어댑터 (MCP)

코딩 에이전트는 어댑터를 통해 같은 네트워크를 사용합니다 — MCP는 CLI와 API 위에 얹힌 커넥터이지, 제품 그 자체가 아닙니다:

```text
CodeSampleX
├─ CLI   ← primary local tester
├─ API   ← automation / integration
├─ Web   ← compatibility map / reports
└─ MCP   ← agent adapter
```

`csx init`은 Claude Code, Codex, Gemini CLI, OpenCode를 자동으로 설정합니다. 그 외의 stdio MCP 클라이언트(Cursor, Windsurf, Cline, Zed, VS Code)는 `csx mcp-config`가 출력하는 내용(Codex는 `--toml`)으로 동작합니다 — 이 명령은 바이너리의 절대 경로를 내보내는데, 편집기가 시작한 클라이언트에는 바로 그것이 필요합니다. 서버 자체는 `csx mcp`입니다. 도구는 여덟 개: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats` — 그리고 게시 도구는 의도적으로 없습니다.

에이전트 대상 설치 절차(MCPB 번들과 `SHA256SUMS.txt`가 딸린 바이너리 직접 다운로드 포함): [llms-install.md](../../llms-install.md). 독립 실행형 커뮤니티 설치는 Ed25519로 서명된 manifest를 통해 자동 업데이트되며 `csx update rollback`을 쓸 수 있습니다. `local-only` 설치는 업데이트 요청을 보내지 않습니다.

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

이것은 숨겨진 텔레메트리가 아니라 프로토콜 그 자체입니다. 커뮤니티 피어는 소비자인 **동시에** 생산자입니다. 로컬 전용 모드에서는 아무것도 전송되지 않습니다. 오류는 어떤 용도로 쓰이기 전에 로컬에서 지문(fingerprint)으로 정제되고, 비공개·미확인 패키지는 결코 머신을 떠나지 않으며, `csx ui`의 프라이버시 미리보기에서 데이터가 떠나기 전의 정확한 페이로드를 볼 수 있습니다. `NO_SAFE_MATCH`는 프라이버시에 안전한 Wanted 튜플을 기여합니다 — 공개 패키지, 그 정확한 버전, 그리고 요청이 명확한 패키지 하나를 지목한 경우에는 요청된 공개 심볼까지 — 사용자의 프롬프트는 절대 포함되지 않습니다. 공개 배포판에서 샘플 소스는 **시더 전용**입니다. 검색, 증거, 영수증, wanted 보드는 계정 없이 열려 있습니다.

## 생태계 (Public v1)

**스캔 + 검증** — 프로젝트 감지, lockfile 기반 패키지 해석, 샘플 종단 간 검증: Node/TypeScript(npm, pnpm, yarn — 레퍼런스), Python(pip, uv), Go, Rust/Cargo. Node 샘플은 스스로 선언한 런타임에서 실행되므로, Bun과 Deno의 결과도 가정이 아니라 실측입니다.

**검증만** — 아직 프로젝트 스캐너는 없지만, 게시된 샘플은 고정된 컨테이너에서 빌드되고 계약 테스트를 거칩니다: PHP/Composer, Ruby/Bundler, Dart/pub, Elixir/Hex. Java(Maven/Gradle) 계약 검증은 정확한 JDK 8/11/17/21/25 레인을 고정합니다.

정직한 기능 매트릭스: [docs/adapters.md](../adapters.md) — 심볼 해석 신뢰도에는 항상 라벨이 붙습니다 (`EXACT`/`PROBABLE`/`UNKNOWN`).

## 아키텍처

단일 Go 바이너리(`csx`: 데몬 + CLI + MCP + 피어 노드 + 검증기)와 작은 서버(`csx-server`: PostgreSQL + Caddy 뒤의 서버 렌더링 호환성 탐색기)로 구성됩니다. 샘플은 콘텐츠 주소 기반이며 로컬 캐시 우선 → 피어 → 메인 시더 순으로 배포됩니다. 다운로드된 샘플은 결코 호스트에서 직접 실행되지 않습니다. resolve는 생태계가 지원하는 경우 설치 스크립트를 비활성화한 고정 샌드박스에서 실행되고, resolve 후에는 아티팩트를 다시 해시하며, 컴파일/계약 단계는 네트워크 차단 상태로 실행됩니다. [goal.md](../../goal.md), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md)를 참고하세요.

## 소스에서 빌드하기

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## 라이선스

코드: Apache-2.0. 게시된 샘플의 기본 라이선스는 **MIT-0**입니다.
