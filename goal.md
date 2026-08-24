# CodeSampleX — Product Goal & Public v1 Architecture

> **이 문서는 설계 문서이지 현재 상태 보드가 아니다.**
>
> 아래의 절 번호(§2.3, §6.1, §10.2 …)는 코드 주석이 실제로 참조하는 안정된
> 주소이므로 유지된다. 하지만 **무엇이 실제로 동작하는지의 source of truth는
> 이 문서가 아니라 코드와 그것을 고정하는 테스트다.** 특히 아래 Public v1
> 체크리스트의 `[ ]`는 "미구현"이 아니라 "작성 당시의 계획 항목"이며, 현재
> 구현 상태를 뜻하지 않는다. 지금 무엇이 성립하는지는 `go test ./...`와
> [README.md](README.md)가 답한다.
>
> **Tested. Not guessed.**
>
> CodeSampleX는 개발자 라이브러리·런타임·툴체인을 위한 **열린 호환성 테스트
> 네트워크**다. 코딩 LLM과 사람이 공개 라이브러리의 동일한 사용법과 동일한
> 오류를 매번 처음부터 추론하지 않도록, 실제 환경에서 실행된 증거와 검증된
> 답안지를 제공한다. 클라이언트는 여전히 local-first이고 샘플 배포도
> 로컬 캐시 → 피어 → 시더 순이지만, 제품이 파는 것은 캐시가 아니라
> **"거기서 돌아가나요"에 대한 실행된 답**이다.

---

## 0. 프로젝트 상태

- 프로젝트명: **CodeSampleX**
- 공식 도메인: **CodeSampleX.dev**
- 배포 형태: 오픈소스 로컬 클라이언트 + 공개 네트워크 + 웹 Compatibility Explorer
- 초기 운영 모델: 무료 Public Network + GitHub Sponsors
- 향후 가능 모델: 네트워크에 참여하지 않는 API-only 사용자를 위한 Hosted API
- Public v1 공개 원칙: 기능 일부만 공개하지 않는다. 기업 기능만 제외하고 **사용 → 증거 생성 → 호환성 향상 → 검색 → 선택적 Sample 기여 → 크로스 검증**의 전체 루프가 닫힌 상태로 한 번에 공개한다.

---

# 1. 해결하려는 문제

코딩 LLM은 전 세계에서 같은 일을 반복한다.

```text
공개 라이브러리 API 사용법 질문
→ 문서/README/GitHub 검색
→ 코드 추론
→ 의존성 설치
→ 컴파일 실패
→ 버전과 환경 차이 추론
→ 수정
→ 성공
→ 세션이 끝나면 성공 경험 대부분 소멸
```

다른 사용자의 LLM도 같은 문제를 처음부터 다시 푼다.

공식 문서는 정확한 원재료지만 다음을 보장하지 않는다.

- 현재 프로젝트의 정확한 라이브러리 버전에서 동작하는가
- 현재 런타임·컴파일러·패키지 매니저·OS 조합에서 동작하는가
- 코드가 단지 컴파일되는가, 실제로 실행되는가
- 실행되더라도 기대한 행동까지 성공하는가
- 실패가 라이브러리 회귀인지, 런타임인지, OS인지, 설정인지
- 동일한 문제를 해결한 검증된 최소 코드가 이미 존재하는가

버전 조합은 중앙 CI가 모두 테스트하기 어려울 정도로 폭발한다.

```text
Library Version
× Runtime Version
× Language/Compiler Version
× Package Manager
× OS
× Architecture
× Module System
× Framework
× Transitive Dependencies
```

하지만 이 조합들은 이미 전 세계 개발자의 PC와 CI에서 매일 자연스럽게 컴파일되고 실행된다.

CodeSampleX는 이 활동에서 **코드를 수집하지 않고, 공개 패키지에 대한 익명 Evidence만 수집**한다. 그리고 사용자가 명시적으로 선택한 경우에만 공개용 최소 Sample을 별도로 만들어 공유한다.

---

# 2. 핵심 명제

## 2.1 사용자 계약

> **나는 무료 소비자인 동시에 공유 생산자다.**

CodeSampleX Community Network는 무료다. 대신 Community Peer는 공개 패키지에 대한 익명 호환성 Evidence를 자동으로 제공한다.

이는 숨겨진 telemetry가 아니라 Public Network 참여 조건이자 프로토콜의 핵심이다.

## 2.2 코드와 Evidence는 별개다

- 자동 기여: 공개 패키지·버전·심볼·환경·컴파일/테스트 결과의 익명 Evidence
- 자동 기여 금지: 프로젝트 소스, 파일명, 경로, 저장소명, 비밀정보, 원본 로그
- 선택 기여: 사용자가 직접 승인한 clean-room Sample

## 2.3 Sample과 Compatibility는 별개다

- **Sample**: “어떻게 구현하는가?”에 대한 답안지
- **Evidence**: “어떤 환경에서 어떤 단계까지 관측됐는가?”에 대한 사실
- **Compatibility Graph**: Evidence를 집계해 계산한 확률적 지도

Compatibility는 Sample의 속성 하나가 아니다. 여러 Evidence에서 파생되는 View다.

## 2.4 성공보다 정직한 실패가 중요하다

PASS/FAIL 숫자만 모으지 않는다.

- 어느 단계에서 실패했는지
- 어떤 오류 fingerprint였는지
- 무엇이 관측됐고 무엇이 추론됐는지
- 원인이 확정인지 상관관계인지

를 분리한다.

## 2.5 전체 문제를 다시 풀지 않는다

CodeSampleX는 LLM에게 무조건 정답 하나를 강요하지 않는다.

```text
가장 가까운 검증 답안지
+ 현재 프로젝트 환경과의 차이
+ 증거 강도
+ 알려진 실패
```

를 반환한다.

LLM은 전체 문제 대신 **차이(delta)만 추론**한다.

---

# 3. 절대 원칙

다음 원칙을 깨는 변경은 프로젝트 소유자의 명시적 승인 없이는 허용하지 않는다.

1. **실제 프로젝트 소스는 자동으로 외부 전송하지 않는다.**
2. 자동 Evidence 수집 대상은 **공개 registry에 존재하는 공개 패키지**로 제한한다.
3. Public Network를 사용하는 Community Peer는 소비자인 동시에 Evidence 생산자다.
4. Evidence Network와 Sample Pool을 독립된 시스템으로 유지한다.
5. 프로젝트 컴파일 성공을 개별 API의 실행 성공으로 과장하지 않는다.
6. 원인이 불확실한 실패는 반드시 `UNKNOWN` 또는 확률적 가설로 표현한다.
7. 검색은 환경 적합성과 검증 강도를 자연어 유사도보다 우선한다.
8. 애매한 Sample을 억지로 반환하지 않는다. `NO SAFE MATCH`가 잘못된 HIT보다 낫다.
9. 로컬 기능은 중앙 서버 장애 중에도 가능한 범위에서 계속 동작한다.
10. 서버의 LLM 추론이나 중앙 빌드 팜에 제품의 기본 동작이 의존하지 않게 한다.
11. Sample source 공개와 유휴 CPU 검증은 각각 별도의 명시적 동의를 요구한다.
12. Public v1은 전체 플라이휠이 닫히기 전에는 공개 출시하지 않는다.
13. 블록체인·토큰 경제·전체 원장은 도입하지 않는다.
14. Core client와 Public protocol은 오픈소스로 유지한다.

---

# 4. 제품의 네 개 레이어

## Layer A — Evidence Network

Community Peer가 공개 라이브러리를 실제로 사용하는 과정에서 익명 Evidence를 생성한다.

예:

```text
pkg:npm/axios@1.12.0
symbol: axios.post
runtime: node@22
language: typescript@5.9
package manager: pnpm@10
os: windows@11
stage: PROJECT_COMPILE
result: PASS
```

이 데이터에는 사용자 코드가 없다.

## Layer B — Compatibility Graph

서버는 Evidence를 집계하여 다음과 같은 지도를 만든다.

```text
axios.post / axios@1.12.x

Node 20 + TS 5.8 + npm + Linux      HIGH
Node 22 + TS 5.9 + pnpm + Windows   HIGH
Node 24 + TS 5.9 + pnpm + Windows   MEDIUM
Node 18 + TS 5.9 + ESM              ELEVATED FAILURE
```

Compatibility는 Boolean이 아니라 다음을 포함한다.

- 관측 단계
- 성공률과 실패율
- 독립 환경/peer 수
- 최신성
- 증거 품질
- confidence
- 알려진 오류 cluster

## Layer C — Answer/Sample Pool

사용자가 명시적으로 기여한 공개용 최소 프로젝트다.

```text
Case: axios로 multipart 파일 업로드
Sample: 독립 실행 가능한 최소 프로젝트
Contract: 로컬 echo server가 실제 multipart body를 받았는지 검증
```

Sample은 실제 프로젝트에서 코드를 잘라 올리지 않는다. Sanitized Spec을 기반으로 로컬 LLM이 clean-room에서 새로 생성한다.

## Layer D — Agent Delivery

Codex, Claude Code, Gemini CLI, OpenCode 등 코딩 agent는 MCP/CLI를 통해 다음을 받는다.

```text
- 가장 가까운 Case/Sample
- 현재 환경과 Sample 환경의 차이
- Compatibility Evidence
- Known Failures
- 필요한 최소 수정
```

---

# 5. 사용 모드와 네트워크 계약

## 5.1 Community Peer — 무료 Public Network

얻는 것:

- Public Compatibility Graph 검색
- Public Sample 검색/다운로드
- 로컬 package shard 동기화
- P2P/서버 fallback Sample 캐시
- MCP/CLI agent integration
- 개인 Reasoning Cache

필수 기여:

- 공개 패키지/version 사용 Evidence
- 공개 API/symbol 사용 Evidence(탐지 가능한 범위)
- CSX가 관찰한 compile/typecheck/test/process PASS·FAIL
- 로컬에서 정제된 오류 fingerprint

선택 기여:

- 공개 Sample source
- 유휴 CPU 기반 cross verification
- GitHub identity를 이용한 Origin Seeder 표시

## 5.2 Local-only — 무료

- 개인 로컬 캐시만 사용
- 외부 Evidence 전송 없음
- Public Network API/MCP pool 사용 없음
- 로컬에서 직접 생성한 private answer는 유지 가능

웹사이트의 공개 Compatibility Explorer 자체는 누구나 열람할 수 있다.

## 5.3 API-only — 향후 유료

- 로컬 peer/daemon 없이 Hosted API만 소비
- storage/verification/evidence를 기여하지 않음
- Public v1 범위에서 제외

## 5.4 설치 화면에 반드시 표시할 계약

```text
CodeSampleX Community

You get
✓ Public compatibility knowledge
✓ Verified code answers
✓ Local agent integration
✓ Public sample cache

You contribute
✓ Public package/version usage
✓ Public API/symbol usage when detectable
✓ Build/typecheck/test result
✓ Sanitized failure fingerprints

Never shared automatically
✕ Source code
✕ Repository/project name
✕ File names or paths
✕ Source snippets
✕ Secrets or environment variables
✕ Private packages
✕ Raw compiler/runtime logs

[ JOIN COMMUNITY ]    [ LOCAL ONLY ]
```

---

# 6. 진실 모델: 무엇을 실제로 안다고 말할 수 있는가

CodeSampleX의 신뢰는 PASS 개수가 아니라 **증거의 의미를 과장하지 않는 것**에서 나온다.

## 6.1 Evidence Class

### A. `USAGE_OBSERVATION` — 자동, 약한 증거

로컬 프로젝트에서 공개 package/symbol 사용이 정적으로 관측됐다.

```text
USED
PROJECT_TYPECHECK_PASS / FAIL
PROJECT_COMPILE_PASS / FAIL
PROJECT_TEST_PASS / FAIL
PROJECT_PROCESS_PASS / FAIL
```

주의:

- `PROJECT_COMPILE_PASS`는 해당 symbol을 포함한 프로젝트가 컴파일됐다는 뜻이다.
- 해당 함수가 실제 실행됐거나 올바른 결과를 냈다는 뜻이 아니다.
- 프로젝트 전체 실패를 특정 symbol의 실패로 단정하지 않는다.

### B. `ADOPTION_EVIDENCE` — 자동 또는 agent report, 중간 증거

특정 Sample/패턴을 프로젝트에 적용한 뒤 프로젝트 빌드/테스트가 성공했다.

Sample이 실사용에 도움이 됐다는 신호지만, 독립 contract 검증보다 약하다.

### C. `SAMPLE_VERIFICATION` — 공개 Sample, 강한 증거

동일한 content-addressed Sample을 clean environment에서 검증했다.

```text
RESOLVE_PASS / FAIL
TYPECHECK_PASS / FAIL
COMPILE_PASS / FAIL
LOAD_PASS / FAIL
PROCESS_PASS / FAIL
TEST_PASS / FAIL
CONTRACT_PASS / FAIL
```

### D. `RUNTIME_INSTRUMENTATION` — 명시적 지원 환경, 강한 증거

특정 public symbol의 실제 호출/반환/throw가 관측됐다.

```text
SYMBOL_EXECUTED
SYMBOL_RETURNED
SYMBOL_THROWN
```

Public v1의 모든 언어에서 이를 보장하지 않는다. 지원 가능한 adapter에서만 명시적으로 표시한다.

## 6.2 검증 등급

```text
L0  SOURCE_ONLY
    공식 문서/README/GitHub에 존재하지만 CSX 검증 없음

L1  RESOLVED
    정확한 dependency와 lockfile 설치 성공

L2  COMPILED
    typecheck/compile 성공

L3  CONTRACT_PASS
    Sample이 의도한 행동 검증까지 성공

L4  CROSS_PASS
    독립 peer가 동일 Sample/Contract를 재현

L5  MATRIX_PASS
    서로 다른 OS/runtime/version 경계에서 재현
```

## 6.3 실패 단계

한 번의 검증은 단일 PASS/FAIL이 아니라 단계별 결과를 가진다.

```text
RESOLVE   → dependency 설치/해결
SYMBOL    → 필요한 public symbol 존재
TYPECHECK → 타입 검사
COMPILE   → 컴파일/빌드
LOAD      → runtime import/load
EXECUTE   → 프로세스 또는 함수 실행
TEST      → 테스트 suite
CONTRACT  → 기대 동작 검증
```

예:

```text
COMPILE ✓ / LOAD ✕
→ ESM/CJS, runtime, native dependency 가능성

EXECUTE ✓ / CONTRACT ✕
→ 프로그램은 돌았지만 의도한 동작 실패
```

## 6.4 실패 원인 분류

관측된 오류와 추론한 원인을 분리한다.

```text
CODE
API_REMOVED_OR_CHANGED
LIBRARY_REGRESSION
TRANSITIVE_DEPENDENCY
RUNTIME
OS
ARCH
TOOLCHAIN
CONFIGURATION
EXTERNAL_SERVICE
RESOURCE
SECURITY_POLICY
UNKNOWN
```

원인을 확정할 수 없으면 확률적 가설만 제공한다.

```json
{
  "observed": "ERR_REQUIRE_ESM",
  "hypotheses": [
    { "domain": "CONFIGURATION", "confidence": 0.72 },
    { "domain": "RUNTIME", "confidence": 0.21 },
    { "domain": "LIBRARY_REGRESSION", "confidence": 0.07 }
  ]
}
```

---

# 7. 핵심 데이터 모델

## 7.1 `PackageCoordinate`

생태계 독립 식별자로 PURL 형태를 사용한다.

```text
pkg:npm/axios@1.12.0
pkg:pypi/fastapi@0.116.0
pkg:cargo/serde@1.0.0
pkg:golang/github.com/example/module@v1.2.0
```

버전은 manifest의 range가 아니라 가능한 경우 lockfile에서 실제 resolved version을 읽는다.

## 7.2 `SymbolFamily`와 `SymbolVersion`

라이브러리 단위만으로는 부족하다. 함수·클래스·메서드·프로퍼티·옵션까지 본다.

```text
Package
└─ PackageVersion
   └─ SymbolVersion
      ├─ Function
      ├─ Class
      ├─ Method
      ├─ Property
      └─ Feature/Option
```

`SymbolFamily`는 버전 간 동일 개념을 묶고, `SymbolVersion`은 특정 package version의 실제 signature를 표현한다.

```json
{
  "package": "pkg:npm/axios@1.12.0",
  "family": "axios.post",
  "kind": "function",
  "signatureHash": "sha256:...",
  "sourceConfidence": "EXACT"
}
```

동적 언어의 symbol resolution은 다음 confidence를 가진다.

```text
EXACT
PROBABLE
UNKNOWN
```

## 7.3 `EnvironmentFingerprint`

환경은 schema version을 가진 sparse object다. 모든 언어에 의미 없는 차원을 강제하지 않는다.

```json
{
  "schemaVersion": 1,
  "ecosystem": "npm",
  "os": "windows",
  "osVersionBucket": "11",
  "arch": "x64",
  "runtime": "node",
  "runtimeVersion": "22.18",
  "language": "typescript",
  "languageVersion": "5.9",
  "packageManager": "pnpm",
  "packageManagerVersion": "10",
  "moduleSystem": "esm",
  "frameworks": ["next@16"]
}
```

개인 식별 가능성을 낮추기 위해 웹에 표시되는 집계에서는 patch version 등을 필요에 따라 bucket 처리한다.

## 7.4 `Case`

Case는 해결하려는 문제다. 코드가 아니다.

```yaml
kind: HOW
goal: Upload a multipart file using axios
packages:
  - pkg:npm/axios@1.12.0
symbols:
  - axios.post
  - FormData
constraints:
  runtime: node
  moduleSystem: esm
contract:
  - start a local echo server
  - upload one text file
  - assert multipart content-type
  - assert expected body was received
```

Case type:

```text
HOW
FIX
MIGRATION
CONFIG
```

## 7.5 `Sample`

Case를 해결하는 공개 가능한 독립 코드 artifact다.

```text
sample/
├─ csx.json
├─ package.json
├─ lockfile
├─ toolchain config
├─ src/
├─ test/
└─ README.md
```

Sample 규칙:

- 한 Case/목적에 집중한다.
- node_modules, venv, target binary 등 생성물은 포함하지 않는다.
- secret, 실제 URL, 회사명, 프로젝트명, 사용자 식별자를 포함하지 않는다.
- dependency는 최소화하고 exact/lock 가능한 형태로 기록한다.
- Contract가 없는 Sample은 최대 L2까지만 올라갈 수 있다.
- 내용이 바뀌면 새 Sample ID가 된다.
- `sampleId = SHA-256(canonical artifact)`
- 공개 Sample의 기본 라이선스는 permissive license를 사용한다. Public v1 구현 전 `MIT-0`을 기본안으로 확정 검토한다.

## 7.6 `AutomaticObservationBatch`

자동 Evidence는 명령 실행마다 원시 이벤트를 서버에 올리지 않는다.

로컬에서 일정 기간 집계 후 다음 단위로 전송한다.

```json
{
  "epoch": "daily-bucket",
  "package": "pkg:npm/axios@1.12.0",
  "symbol": "axios.post",
  "symbolConfidence": "EXACT",
  "environment": "env-hash",
  "stage": "PROJECT_COMPILE",
  "result": "PASS",
  "observationCount": 3,
  "errorFingerprint": null
}
```

동일 프로젝트에서 반복 빌드한 횟수가 독립 프로젝트 1,000개처럼 집계되지 않도록 로컬/서버에서 중복 억제한다.

## 7.7 `VerificationReceipt`

공개 Sample 검증 결과는 강한 Evidence이며 immutable receipt로 남긴다.

```json
{
  "schemaVersion": 1,
  "sampleId": "sha256:...",
  "caseId": "case:...",
  "environmentHash": "sha256:...",
  "stages": {
    "resolve": "PASS",
    "compile": "PASS",
    "contract": "PASS"
  },
  "verifierAdapter": "node-typescript@1.0.0",
  "sandboxCapability": "CONTAINER_RUN",
  "logsDigest": "sha256:...",
  "createdAt": "...",
  "peerSignature": "..."
}
```

## 7.8 `CompatibilitySnapshot`

웹/API에서 raw Evidence를 매번 집계하지 않는다.

```text
package + version + symbol + environment dimensions
→ counts
→ weighted confidence
→ failure clusters
→ last seen
```

Snapshot은 read-optimized materialized aggregate다.

---

# 8. 자동 Evidence 수집 파이프라인

## 8.1 수집 대상 판별

자동 수집은 공개 registry allowlist를 기준으로 한다.

초기 registry:

- npm registry
- PyPI
- crates.io
- Go module proxy/public module metadata

자동 제외:

```text
private registry
file: dependency
local path dependency
git+ssh private URL
인증 없이는 조회되지 않는 package
사내 namespace로 판별된 package
사용자가 명시적으로 제외한 package
```

공개 scoped package는 registry에서 공개 여부가 확인된 경우만 포함한다.

## 8.2 프로젝트 스캔

로컬 client가 다음을 읽는다.

- manifest
- lockfile
- compiler/runtime config
- 공개 import/use statements
- 지원되는 언어의 AST/type information

읽은 원본은 로컬 분석에만 사용하며 서버에 전송하지 않는다.

## 8.3 명령 관찰

임의의 모든 shell 명령을 마법처럼 관찰한다고 가정하지 않는다.

Public v1에서 Evidence가 보장되는 경로:

1. `csx run -- <command>` CLI wrapper
2. MCP의 CSX 실행 tool을 통한 build/test command
3. 공식 지원 agent의 hook/command integration
4. 사용자가 명시적으로 설정한 shell integration

예:

```bash
csx run -- pnpm build
csx run -- npm test
csx run -- uv run pytest
csx run -- go test ./...
csx run -- cargo test
```

`csx init`은 MCP 등록뿐 아니라 지원되는 agent rule/skill/hook을 설치하여 공개 라이브러리 작업 전에 CSX 검색과 CSX command runner를 우선 사용하게 한다.

## 8.4 로컬 분석

```text
환경 fingerprint 생성
→ 사용 package/version 확인
→ public symbol 추출
→ command stage/result 확인
→ raw error sanitization
→ local aggregate 갱신
```

## 8.5 오류 정제

원본 오류 로그는 외부로 보내지 않는다.

로컬에서 제거/일반화할 항목:

- 절대·상대 경로
- 저장소/프로젝트명
- 사용자명
- private identifier
- 문자열 literal
- URL, token, secret
- source line/snippet

유지 가능한 항목:

- 공개 package 이름/version
- 공개 API/symbol
- compiler/runtime error code
- 정제된 message template
- stack frame의 공개 package 영역
- stage

공유 예:

```text
errorCode: TS2345
fingerprint: sha256:...
publicSymbols: [axios.post]
stage: PROJECT_TYPECHECK
```

## 8.6 익명성과 중복 억제

자동 Evidence identity와 기여자 identity를 분리한다.

- 자동 Evidence: rotating pseudonymous ID
- Sample Origin Seeder/Verifier reputation: 명시적 persistent identity

자동 Evidence는 package stack 전체를 한 번에 보내지 않고 최소 fact 단위로 batch한다. 서버는 Evidence와 IP를 영구 연결하지 않는다.

반복 빌드로 숫자가 부풀지 않도록 독립성 지표를 구분한다.

```text
observation_count
unique_peer_bucket_count
unique_project_bucket_count
recent_independent_count
```

정확한 사용자·프로젝트 추적보다 익명성과 통계적 유효성을 우선한다.

---

# 9. Sample 기여 파이프라인

Sample은 자동 Evidence와 다른 계약이다.

## 9.1 기여 시작

MISS가 해결되거나 재사용 가능한 패턴이 탐지되면 로컬에서 알린다.

```text
Reusable public-library solution detected.
Create a clean public Sample?

[ CREATE SAMPLE ]  [ NOT NOW ]
```

## 9.2 Sanitized Spec 생성

기본적으로 LLM에게 실제 프로젝트 source를 직접 주지 않는다.

로컬 extractor가 다음만 만든다.

```text
- 공개 package/version
- 공개 symbol
- 의도
- 필요한 config/runtime 조건
- 성공한 build/test stage
- 사용자 설명
```

사용자가 특별히 로컬 source context를 허용할 수는 있지만 source는 외부로 전송되지 않으며, 생성 결과는 반드시 leakage scan과 사용자 preview를 거친다.

## 9.3 Clean-room 생성

사용자의 로컬 LLM/CLI가 독립 최소 프로젝트를 새로 작성한다.

```text
Sanitized Spec
→ LLM generation
→ clean temporary directory
→ dependency resolve
→ compile/typecheck
→ contract test
→ leakage scan
→ preview
```

## 9.4 게시 승인

사용자가 확인하는 항목:

- 공개될 파일 전체
- package/version
- Case/intent
- contract
- 라이선스
- Origin Seeder 이름 또는 anonymous

명시적 `[PUBLISH]` 이전에는 네트워크로 전송하지 않는다.

## 9.5 Origin Seeder

최초 공개 기여자는 Sample의 Origin Seeder로 기록된다.

```text
Origin Seeder: @r2cuerdame
Generated with: user-selected local LLM
Clean Build: PASS
Contract: PASS
```

코드가 이후 fork/수정돼도 최초 seed 이력은 유지한다.

## 9.6 Cross Verification

새 Sample은 최초 peer의 PASS만으로 높은 신뢰를 얻지 않는다.

```text
Origin PASS
→ Cross Verification Queue
→ 다른 peer 환경
→ 동일 artifact/contract 실행
→ signed receipt
```

상태:

```text
LOCAL_PASS
CROSS_PASS
MATRIX_PASS
STABLE
```

Idle Verification은 별도 opt-in이며 budget을 사용자가 정한다.

```text
OFF
5 min/day
15 min/day
Idle only
Unlimited
```

---

# 10. 실패 원인 분리와 크로스 검증

## 10.1 관측과 원인 추론 분리

한 환경의 실패만으로 원인을 확정하지 않는다.

```text
Windows / Node22 / axios1.12 → FAIL
```

이 결과 하나는 오류 stage와 fingerprint만 제공한다.

## 10.2 정보량이 높은 다음 검증 선택

Sample에 대해 한 변수씩 바꾼 검증 job을 생성한다.

```text
A: Linux   / Node22 / axios1.12
B: Windows / Node24 / axios1.12
C: Windows / Node22 / axios1.11
```

예:

```text
OS changed      → PASS
Runtime changed → FAIL
Library changed → PASS
```

결과는 `axios1.12 × Windows interaction` 가능성을 높이지만, 충분한 Evidence 전에는 확정하지 않는다.

## 10.3 Regression 탐지

```text
library 1.11 + 동일 환경 → PASS
library 1.12 + 여러 환경 → FAIL
```

이면 library regression cluster 후보로 표시한다.

## 10.4 외부 환경 실패

네트워크, 인증, rate limit, 외부 API 장애는 library failure와 분리한다.

Contract는 가능하면 local fixture/mock server를 사용한다. 실제 외부 서비스가 필요한 `LIVE_INTEGRATION` 검증은 별도 capability와 명시적 opt-in으로 제한한다.

---

# 11. 검색 엔진

검색 품질은 CodeSampleX의 핵심 제품이다.

## 11.1 입력

```json
{
  "query": "axios로 multipart 파일 업로드",
  "packages": ["pkg:npm/axios@1.12.0"],
  "symbols": ["axios.post", "FormData"],
  "environment": {
    "runtime": "node@22",
    "language": "typescript@5.9",
    "moduleSystem": "cjs",
    "os": "windows@11"
  },
  "errorFingerprint": null
}
```

Agent가 이미 알고 있는 프로젝트 환경을 함께 보내며, daemon도 lockfile/config에서 자동 보완한다.

## 11.2 로컬 shard

모든 세계 metadata를 각 PC에 복제하지 않는다.

서버는 ecosystem/package-major 기반 compact shard를 발행한다.

```text
pkg:npm/axios@1
pkg:npm/zod@4
pkg:pypi/fastapi@0
```

Client는 다음을 우선 warm한다.

```text
현재 프로젝트 dependency
최근 사용 package
Global HOT package
사용자가 pin한 package
```

로컬에는 metadata/index를 넓게, Sample source는 수요 기반으로 저장한다.

## 11.3 검색 단계

```text
1. Package/version exact filter
2. Symbol/signature exact match
3. Error fingerprint exact match
4. FTS/BM25 lexical search
5. Intent semantic similarity
6. Environment compatibility gate
7. Verification strength rerank
8. Recency/regression penalty
9. Known failure penalty
```

Embedding은 보조 수단이다. API symbol·package·version의 정확한 토큰을 우선한다.

## 11.4 환경 적합도

결과 등급:

```text
EXACT
COMPATIBLE
ADAPTATION_REQUIRED
REFERENCE_ONLY
NO_SAFE_MATCH
```

Version distance, module system, runtime, package manager, OS sensitivity를 구분한다.

모든 차원의 Cartesian product를 만들지 않는다. Sample/Case별 `sensitiveDimensions`를 추정하고 실제 수요와 실패 경계부터 채운다.

## 11.5 LLM 응답

```text
MATCH: COMPATIBLE
CONFIDENCE: HIGH

Exact
- axios 1.12
- Node 22
- TypeScript 5.9

Different
- Sample uses ESM
- Current project uses CJS

Adaptation needed
- Import syntax only

Evidence
- Project compile observations: 18,291
- Clean builds: 821
- Contract passes: 318
- Independent cross peers: 74
- Elevated failures: Node 18 + ESM
```

Top 1만 강제하지 않고 필요 시 상위 2~3개의 다른 구현을 제공한다.

## 11.6 핵심 품질 원칙

- HIT 수보다 **HIT 후 성공률**을 우선한다.
- 낮은 confidence 결과는 자동 적용하지 않는다.
- Sample을 그대로 붙이지 않고 delta를 명시한다.
- 잘못된 HIT는 MISS보다 나쁘다.
- 실패 자동 조회에서 로컬 명령의 exit code와 stdout/stderr가 1차 증거다. 네트워크 답변은 별도 보조 섹션이며 원문을 가리거나 대체하지 않는다.
- 실패 진단은 **진단을 실제로 담은 스트림**에서 만든다. stderr만 sanitize하면 stdout으로 보고하는 도구(`tsc`, `go test`, `pytest`, 대부분의 npm script)의 실패가 빈 문자열의 fingerprint로 기록·조회된다. 그 해시는 어떤 실패와도 일치하지 않고, 남은 free-text 질의는 hash 뿐이라 엔진은 가장 가까운 무관한 sample을 답으로 돌려준다.
- 아무것도 출력하지 않은 실패는 sanitized error가 **없다**. 빈 문자열의 fingerprint는 모든 침묵한 실패가 공유하는 값이므로 실패의 신원이 될 수 없고, 그것만 반환하면 답이 없는 질문을 네트워크에 던지게 된다.
- LOW confidence, `REFERENCE_ONLY`, 또는 현재 명령의 생태계와 무관한 결과는 `REFERENCE_CANDIDATE`로 표시하며 자동 수정 근거로 제시하지 않는다.
- 생태계가 다른 결과는 **표시만 낮추는 것으로 부족하다.** 라벨은 봉투이고 agent가 읽는 것은 본문이므로, `DECISION: REUSE_VERIFIED`·`MATCH: COMPATIBLE`·contract block을 그대로 렌더링하면 라벨은 참고라고 말하고 본문은 쓰라고 말하게 된다. 상태를 `NO_RELEVANT_MATCH`로 내리고 sample id와 사유 한 줄만 남긴다. 유일한 예외는 이 실패의 sanitized fingerprint가 그 sample에 정확히 일치한 경우(`ExactFailureMatched`)이며, 이는 등급이 아니라 실패와 sample 사이의 명시적 evidence 연결이다.
- 봉투와 본문이 서로 다른 말을 하면 이기는 쪽은 본문이다. `advisoryOnly` 추천의 첫 줄이 `DECISION: REUSE_VERIFIED`이면 라벨은 참고라고, 본문은 쓰라고 말하는 것이므로 실패 자동 조회에서는 판정 줄을 `DECISION: REFERENCE_CANDIDATE`로 바꾼다. 그 아래의 receipt·delta·evidence·contract는 네트워크가 실제로 측정한 것이라 라벨 때문에 잃을 수 없다. 이 재작성은 묻지 않은 조회에만 적용하고, 호출자가 스스로 물은 `search_known_solution`의 판정 줄은 그대로 둔다.
- 이 relevance gate는 `run_observed_command`와 Claude/Codex hook이 **같은 정의**(`domain.SearchResult.UnrelatedToCommand`)를 공유한다. 묻지 않았는데 끼어드는 hook에서는 낮춘 표시 대신 침묵이 같은 계약의 표현이다.

---

# 12. 로컬 클라이언트

## 12.1 배포

한 번 설치하면 모든 구성요소가 함께 설치된다.

```bash
csx init
```

설치 항목:

```text
Local daemon
CLI
MCP server
Local SQLite/index/cache
Verifier adapters
Community/Local-only contract
Agent rules/skills/hooks
Server/bootstrap configuration
Optional local dashboard
```

## 12.2 역할

```text
CodeSampleX Local Daemon
├─ MCP Server
├─ CLI / Local API
├─ Project Scanner
├─ Environment Fingerprinter
├─ Public Package/Symbol Analyzer
├─ Command Runner/Observer
├─ Error Sanitizer
├─ Local Evidence Aggregator
├─ Local Search Engine
├─ Sample Cache
├─ Verification Engine
├─ Contribution Workflow
└─ Peer Node
```

## 12.3 인터페이스 역할

- **MCP**: 코딩 LLM의 주 소비 인터페이스
- **CLI**: 설치, 자동화, 디버깅, 명시적 command observation
- **GUI**: 계약/설정, 공개 전 Sample preview, cache/기여/절감 통계

## 12.4 MCP tool 초안

```text
search_known_solution
get_sample
explain_compatibility
run_observed_command
report_sample_adoption
propose_public_sample
list_local_hits
```

`publish_public_sample`은 MCP가 임의로 실행하지 못하며 GUI/CLI의 사용자 승인 단계가 필요하다.

## 12.5 Local UI

초기에는 별도 Electron 앱이 아니라 다음 형태로 제공한다.

```bash
csx ui
```

localhost dashboard:

```text
Community status
Local cache
Project dependencies
Hits / Misses
Post-hit build pass
Estimated reasoning avoided
Automatic evidence sent
Origin Seeds
Cross verifications
Privacy preview
```

---

# 13. 생태계 Adapter

Public v1에서 다음 네 생태계를 한 번에 지원한다.

- Node / JavaScript / TypeScript
- Python
- Go
- Rust

단, 모든 adapter가 동일한 symbol/runtime 관찰 수준을 가장하지 않는다.

## 13.1 Capability Level

```text
A0  Package/version detection
A1  Build/typecheck/test observation
A2  Static symbol resolution
A3  Runtime symbol instrumentation
A4  Clean Sample + Contract verification
```

각 adapter는 지원 capability를 공개한다.

## 13.2 Node/JS/TS — 기준 구현

Package manager:

```text
npm
pnpm
yarn
```

기능:

- package.json + lockfile resolved version
- TS/JS AST import/call 분석
- TypeScript type information 활용 가능한 경우 exact symbol resolution
- ESM/CJS 차원
- npm lifecycle script 정책
- typecheck/build/test command profile

Node/TS는 Public v1에서 가장 깊고 많은 초기 Seed를 가진 reference ecosystem으로 삼는다.

## 13.3 Python

Package manager/environment:

```text
pip
uv
poetry(가능한 범위)
venv
```

기능:

- installed distribution/resolved version
- AST import/call 분석
- 동적 attribute/reflection은 confidence 하향
- compile/import/pytest 관찰

## 13.4 Go

- go.mod/go.sum
- `go list`, `go/packages`, type information
- package + exported symbol resolution
- `go build`, `go test`

## 13.5 Rust

- Cargo.toml/Cargo.lock
- cargo metadata
- compile/test evidence
- macro/dynamic expansion 때문에 symbol resolution은 보수적으로 시작
- rustdoc/doctest는 고신뢰 초기 Seed 후보

---

# 14. 서버와 CodeSampleX.dev

## 14.1 초기 서버 원칙

초기에는 한 대의 Lightsail에서 완전히 동작하게 한다.

```text
CodeSampleX Host
├─ Public Website
├─ Registry API
├─ Search API
├─ Evidence Ingest
├─ Compatibility Aggregator
├─ Package Shard Publisher
├─ Sample Registry
├─ Main Seeder / HTTP Blob Fallback
├─ Verification Queue
├─ Peer Tracker / Rendezvous
├─ GitHub Identity
└─ Network Statistics
```

2GB RAM은 최소 시작점이며, 안정적 Public v1 운영에는 4GB 구성을 우선 검토한다. 서버는 snapshot/backup으로 쉽게 상향할 수 있어야 한다.

## 14.2 초기 배포

```text
Docker Compose
├─ Caddy
├─ csx-server
└─ PostgreSQL
```

초기에는 Redis, Kafka, Kubernetes를 사용하지 않는다.

- Verification Queue: PostgreSQL 기반
- Blob: local content-addressed filesystem + nightly S3-compatible backup
- 이후 BlobStore interface로 object storage/OCI/P2P를 추가
- 사이트/API/Tracker는 가능한 한 단일 Go server로 통합

## 14.3 PostgreSQL 역할

- package/version/symbol registry
- Case/Sample metadata
- automatic evidence aggregates
- strong verification receipts
- compatibility snapshots
- failure clusters
- HOT score
- identities/contributor stats
- verification jobs
- peer announcements

## 14.4 저장 정책

- 자동 Evidence: 로컬 집계 후 batch 업로드
- 서버는 raw command/log/source를 받지 않음
- linkability가 있는 임시 anti-duplication bucket은 제한된 기간 후 제거
- Compatibility aggregate는 장기 보존
- Sample verification receipt는 장기 보존
- Sample source는 main seeder에 최소 한 copy를 둘 수 있으나, 프로토콜상 중앙만 보유할 수 있게 만들지 않음

## 14.5 웹사이트

메인 페이지는 설치와 네트워크 가치만 크게 보여준다.

```text
CodeSampleX
Stop solving the same code twice.

[ Install ]

Peers
Packages
Symbols
Evidence
Verified Samples
Post-hit success rate
Estimated reasoning avoided

More users
→ More real environments
→ Better compatibility evidence
→ Better answers
```

상세 페이지는 Compatibility Explorer다.

```text
/npm/axios/1.12/axios.post
```

표시:

- package/version/symbol/signature
- runtime/OS/toolchain/package manager matrix
- Evidence class별 수치
- confidence와 coverage
- 최근 failure cluster
- regression 후보
- 연결된 Case/Sample
- Origin Seeder/Cross Verification

웹 요청 시 raw Evidence를 집계하지 않고 `CompatibilitySnapshot`만 읽는다.

## 14.6 공개 탐색과 SEO

사람과 LLM이 검색엔진에서 다음 질문으로 유입될 수 있어야 한다.

```text
axios 1.12 node 24 compatibility
zod 4 typescript 5.9 error
pnpm package API example
```

사이트는 단순 SaaS dashboard가 아니라 **공개 호환성 지도와 설치 랜딩**이다.

---

# 15. P2P와 Main Seeder

P2P는 서버비 절감보다 다음 가치가 더 중요하다.

- 각자의 실제 환경이 verifier가 됨
- 인기 Sample이 자연스럽게 복제됨
- 서버 장애 중에도 로컬 캐시 사용 가능
- 공개 집단지성의 배포망

## 15.1 Public v1 전송 순서

```text
Local Cache
→ Reachable Peer
→ Main Seeder / HTTP Fallback
→ MISS
```

Sample은 작으므로 순수 P2P를 종교적으로 강제하지 않는다.

## 15.2 초기 discovery

- 중앙 Tracker/Rendezvous
- bootstrap node
- direct peer transfer when reachable
- relay 또는 HTTP fallback

DHT와 완전 분산 discovery는 Public v1 이후에 검토한다.

## 15.3 Cache 정책

```text
Project-relevant samples
Recently used samples
Global HOT samples
Verification-demand samples
Pinned samples
```

사용자가 검색하지 않은 HOT Sample도 제한된 cache budget 내에서 seed할 수 있다.

Sample이 사라져도 치명적이지 않다. 필요하면 LLM이 다시 생성할 수 있다. 다만 초기에는 Main Seeder가 public Sample의 안정적인 fallback을 제공한다.

## 15.4 저장 포맷

```text
SampleManifest
+ canonical source archive
+ lockfiles
+ contract
+ metadata
→ SHA-256 content ID
```

Storage provider abstraction:

```text
LocalCAS
PeerBlob
HTTPBlob
ObjectStorage
OCIRegistry(optional)
```

---

# 16. 보안

## 16.1 외부 Sample은 host에서 직접 실행하지 않는다

```text
Downloaded Sample
→ archive/path/symlink 검사
→ static inspection
→ dependency policy 검사
→ sandbox resolve
→ network-off compile
→ stronger sandbox run/contract
```

## 16.2 npm lifecycle scripts

기본 resolve는 install scripts를 차단한다.

```text
npm ci --ignore-scripts
pnpm install --ignore-scripts
```

install script가 필요한 package는 별도 capability와 강한 sandbox를 요구하고 명확히 표시한다.

## 16.3 Sandbox Capability

```text
COMPILE_ONLY
CONTAINER_RUN
STRONG_ISOLATION_RUN
LIVE_INTEGRATION
```

Peer는 자신이 제공 가능한 capability만 선언한다.

## 16.4 Sample 제한

- compressed source artifact size 제한
- binary 포함 금지 또는 엄격한 quarantine
- host filesystem mount 금지
- credential/environment variable 제공 금지
- CPU/RAM/time/process 제한
- Contract 실행 시 기본 network 차단

## 16.5 가짜 Evidence와 Sybil

서명된 peer receipt도 절대적인 진실이 아니다. 한 사람이 여러 identity를 만들 수 있다.

신뢰 계산 요소:

- Evidence class
- independent environment diversity
- peer activity age
- historical consistency
- recent success/failure honesty
- GitHub-linked verifier 여부
- official CI/verifier 여부
- cross-peer agreement

단순 `peer count`를 신뢰 점수로 사용하지 않는다.

---

# 17. 경쟁 제품과의 경계

CodeSampleX는 다음 제품을 그대로 복제하지 않는다.

## 문서/Context RAG

- 기존 문서와 snippet을 LLM에게 주는 제품이 아님
- 문서를 원재료로 사용할 수 있으나 핵심은 실제 환경 Evidence다

## 코드 검색

- GitHub 코드 조각을 검색하는 제품이 아님
- 최소 독립 Sample과 검증 이력을 제공한다

## Agent Memory

- 프로젝트 대화·아키텍처·개인 업무 전체를 기억하지 않는다
- 공개 라이브러리의 재사용 가능한 사용법과 오류 해결에 제한한다

## 문서 예제 검증 CI

- 특정 회사 repo 문서의 example만 유지하는 제품이 아님
- 공개 생태계 전체에서 실제 사용자 환경 Evidence를 모은다

## 중앙 Compatibility Farm

- 모든 조합을 서버에서 직접 돌리지 않는다
- 실제 사용자 개발환경과 선택적 peer verification으로 sparse matrix를 채운다

## 패키지 관리자

- 설치/배포를 대체하지 않는다
- package manager가 만든 실제 resolved environment를 Evidence로 사용한다

CodeSampleX의 차별점:

```text
Public Package
→ Version
→ Symbol/API
→ Real Environment
→ Observed Stage/Result
→ Optional Known-Good Sample
→ Cross Verification
→ LLM Reuse
```

---

# 18. Public v1 필수 범위

기업 기능을 제외한 다음 항목은 첫 공개 버전에 함께 들어가야 한다.

## Client

- [ ] Go 단일 바이너리 배포
- [ ] `csx init`
- [ ] Community/Local-only 계약 선택
- [ ] Local daemon
- [ ] CLI
- [ ] MCP server
- [ ] SQLite local DB + FTS index
- [ ] local Sample cache
- [ ] agent rules/skills/hooks 설치
- [ ] `csx run -- <command>`
- [ ] `csx ui`

## Evidence

- [ ] 공개 package 판별
- [ ] lockfile resolved version
- [ ] environment fingerprint
- [ ] package/symbol usage 분석
- [ ] symbol confidence
- [ ] compile/typecheck/test/process stage 관찰
- [ ] PASS/FAIL 수집
- [ ] raw error sanitization
- [ ] local aggregation/batching
- [ ] 익명 Community upload
- [ ] private package 완전 제외

## Ecosystem

- [ ] Node/JS/TS: npm, pnpm, yarn
- [ ] Python: pip, uv baseline
- [ ] Go modules
- [ ] Rust/Cargo
- [ ] adapter capability matrix 공개

## Search

- [ ] package/version/symbol exact search
- [ ] error fingerprint search
- [ ] local FTS/BM25
- [ ] package shard sync/warm
- [ ] environment compatibility rerank
- [ ] Evidence strength/confidence
- [ ] `EXACT / COMPATIBLE / ADAPTATION_REQUIRED / NO_SAFE_MATCH`
- [ ] delta response

## Sample

- [ ] Case/Sample schema
- [ ] content-addressed artifact
- [ ] clean-room generation workflow
- [ ] leakage scan
- [ ] user preview/approval
- [ ] Origin Seeder
- [ ] clean resolve/compile/contract
- [ ] cross verification queue
- [ ] optional idle verification budget

## Server

- [ ] Registry API
- [ ] Evidence ingest
- [ ] Compatibility aggregation
- [ ] materialized snapshots
- [ ] Search API
- [ ] package shard publisher
- [ ] Sample registry/blob fallback
- [ ] Main Seeder
- [ ] peer tracker/rendezvous
- [ ] GitHub identity
- [ ] network statistics
- [ ] backup/restore

## Website

- [ ] CodeSampleX.dev landing
- [ ] 설치 명령
- [ ] “무엇이 좋아지는가” 설명
- [ ] network dashboard
- [ ] package/version/symbol explorer
- [ ] compatibility matrix
- [ ] failure/regression view
- [ ] Sample detail
- [ ] contributor/Origin Seeder profile
- [ ] estimated reasoning avoided

## Distribution

- [ ] local cache
- [ ] peer lookup
- [ ] peer Sample transfer 가능한 경로
- [ ] Main Seeder/HTTP fallback
- [ ] HOT cache policy

---

# 19. Public v1에서 제외

- Enterprise/private package network
- SSO/SLA/on-premise
- API-only 과금
- 모든 IDE의 전용 extension
- 모든 agent의 완전한 shell interception
- 모든 언어의 runtime symbol instrumentation
- Android/iOS/Unreal/Unity/C++ verifier
- 중앙 대규모 build farm
- 블록체인/토큰/ratio 경제
- 범용 프로젝트 memory
- 아키텍처/비즈니스 로직 공유
- 자동 source publication
- 실패 원인의 확정적 판정
- 완전 DHT-only network

---

# 20. 기술 스택

## 20.1 Core/Server

**Go**를 기본 구현 언어로 한다.

이유:

- Windows/macOS/Linux 단일 바이너리
- daemon/CLI/server 공유 코드
- 로컬 process orchestration
- embedded web assets
- SQLite/PostgreSQL 연동
- MCP/libp2p 확장
- 낮은 서버 자원 사용

## 20.2 Local

- SQLite
- FTS5
- content-addressed filesystem cache
- Unix socket / Windows named pipe 기반 local daemon 통신 우선
- MCP stdio

## 20.3 Server

- Go HTTP server
- PostgreSQL
- Caddy
- server-rendered web 우선
- Docker Compose

Web은 첫 버전에서 별도 거대한 frontend stack을 만들지 않는다. Compatibility Explorer가 요구하는 UX를 만족하는 범위에서 Go template/HTMX 또는 경량 정적 JS를 우선한다.

## 20.4 Repository 구조 초안

```text
CodeSampleX/
├─ cmd/
│  ├─ csx/
│  └─ csx-server/
├─ internal/
│  ├─ daemon/
│  ├─ mcp/
│  ├─ cli/
│  ├─ scanner/
│  ├─ environment/
│  ├─ evidence/
│  ├─ sanitizer/
│  ├─ search/
│  ├─ samples/
│  ├─ verifier/
│  ├─ sandbox/
│  ├─ registry/
│  ├─ compatibility/
│  ├─ peer/
│  ├─ storage/
│  ├─ identity/
│  └─ web/
├─ adapters/
│  ├─ node/
│  ├─ python/
│  ├─ go/
│  └─ rust/
├─ schemas/
│  └─ v1/
├─ deploy/
│  ├─ docker-compose.yml
│  └─ caddy/
├─ docs/
└─ goal.md
```

Adapter는 core에 언어별 예외를 직접 누적하지 않도록 interface로 분리한다.

---

# 21. API 초안

## Local MCP/Daemon

```text
search_known_solution
get_sample
explain_compatibility
run_observed_command
report_sample_adoption
propose_public_sample
get_local_stats
```

## Server

```text
POST /v1/evidence/batches
GET  /v1/registry/packages/{purl}
GET  /v1/registry/symbols/{id}
POST /v1/search
GET  /v1/shards/{ecosystem}/{package}/{major}
POST /v1/samples
GET  /v1/samples/{sampleId}
POST /v1/verifications
GET  /v1/verification/jobs
POST /v1/peers/announce
GET  /v1/stats
```

모든 schema와 protocol은 처음부터 version을 가진다.

---

# 22. 핵심 KPI

Sample 개수나 가입자 수만으로 성공을 판단하지 않는다.

```text
Agent Search Invocation Rate
Public library 문제에서 agent가 실제로 CSX를 먼저 호출한 비율

Precision@1
첫 결과가 실제로 적합한 비율

Accepted HIT Rate
Agent가 결과를 채택한 비율

Post-HIT Build Pass
채택 후 빌드/테스트가 성공한 비율

Adaptation Distance
Sample에서 실제 적용까지 필요한 수정량

Evidence Coverage
실제 수요 환경이 얼마나 채워졌는가

Cross Verification Rate
Origin PASS가 독립 환경에서 재현된 비율

Failure Attribution Confidence
UNKNOWN에서 의미 있는 cluster로 좁혀진 비율

Reasoning Avoided
중복 LLM 호출·빌드 재시도·추정 토큰·시간 절감량
```

`Reasoning Avoided`는 정확한 사실처럼 표시하지 않는다.

- HIT가 채택됐는지
- 적용 후 성공했는지
- 유사 MISS가 성공할 때 평균 LLM 호출/빌드 재시도가 얼마였는지

를 이용한 **estimated metric**으로 명시한다.

---

# 23. 주요 위험과 대응

## Agent가 MCP를 사용하지 않음

대응:

- `csx init`이 rule/skill/hook까지 설치
- public library 작업 전에 CSX 검색을 명시
- `run_observed_command`를 기본 workflow에 삽입
- HIT 이득을 dashboard로 보여줌

## 자동 Evidence가 개인정보처럼 느껴짐

대응:

- Community 계약을 설치 시 명시
- 코드 미전송을 기술적으로 보장
- 전송 예정 데이터 local preview
- public package only
- rotating anonymous identity
- raw error/log 금지
- open-source client

## 개별 API 성공을 잘못 추론함

대응:

- Usage Observation과 Sample Verification 분리
- 프로젝트 PASS는 co-occurrence로만 사용
- runtime/contract Evidence만 강한 성공으로 취급
- UI에 Evidence class를 항상 표시

## Symbol 분석이 언어별로 부정확함

대응:

- `EXACT / PROBABLE / UNKNOWN`
- adapter capability 공개
- package-level Evidence로 안전하게 fallback

## 가짜 PASS/FAIL

대응:

- 단순 count 대신 Evidence class/독립성/환경 다양성/역사적 일관성
- strong receipt 서명
- suspicious burst 제한
- 숫자와 confidence 분리

## 악성 Sample

대응:

- host 직접 실행 금지
- install scripts 기본 차단
- sandbox capability
- content hash
- source-only artifact
- network/resource 제한

## 콜드스타트

대응:

- 개발자인 소유자가 실제 작업 중 첫 Seeder 역할
- 공식 examples/doctest/rustdoc/Go examples를 SOURCE_ONLY 또는 verified seed로 활용
- npm/Node/TS에서 초기 밀도를 집중 확보
- MISS 해결이 자동 local cache가 되고, 선택적으로 public Sample이 되는 구조
- Sample이 없어도 Evidence Graph 자체가 먼저 가치 생성

## 서버 비용

대응:

- 서버 LLM 추론 없음
- 중앙 build farm 없음
- 자동 Evidence local aggregation
- website는 materialized snapshot 조회
- Sample은 작고 P2P/cache 가능
- 단일 Lightsail + PostgreSQL로 시작

---

# 24. 내부 개발 순서

Public v1은 한 번에 공개하지만 내부 구현은 다음 순서로 진행한다.

## Milestone 1 — Schema와 단일 로컬 루프

- Package/Symbol/Environment/Evidence/Case/Sample schema 확정
- Go CLI/daemon
- SQLite
- Node/TS project scan
- `csx run`
- local Evidence aggregation
- local search

## Milestone 2 — 중앙 점화 장치

- Lightsail deployment
- PostgreSQL
- Evidence ingest
- Compatibility snapshots
- package shard
- CodeSampleX.dev 최소 landing/explorer

## Milestone 3 — Agent 통합

- MCP
- Codex/Claude/OpenCode 등 지원 가능한 rule/skill/hook
- search → result delta → observed command workflow
- HIT/Adoption 측정

## Milestone 4 — 네 생태계

- Node/TS reference adapter 완성
- Python baseline
- Go baseline
- Rust baseline
- capability matrix

## Milestone 5 — Public Sample

- clean-room contribution
- Sample artifact
- sandbox verifier
- user preview/publish
- Origin Seeder/GitHub identity

## Milestone 6 — Cross Verification와 분산 배포

- verification queue
- idle budget
- signed receipt
- tracker/rendezvous
- peer transfer
- Main Seeder fallback
- HOT cache

## Milestone 7 — Public v1 Release Gate

전체 플라이휠이 실제로 동작하는지 end-to-end 검증한 뒤 공개한다.

---

# 25. Public v1 완료 조건

다음 시나리오가 모두 실제로 동작해야 한다.

## 시나리오 A — 자동 Evidence

1. 사용자가 Community mode로 설치한다.
2. 공개 npm package를 사용하는 프로젝트를 연다.
3. `csx run -- pnpm build` 또는 agent integration으로 빌드한다.
4. CSX가 package/version/environment/symbol을 로컬 분석한다.
5. source/path/project name/raw error가 외부로 나가지 않는다.
6. sanitized Evidence batch가 서버에 들어간다.
7. CodeSampleX.dev의 Compatibility Snapshot에 반영된다.

## 시나리오 B — 실패 Evidence

1. 공개 package 조합에서 compile/test가 실패한다.
2. 로컬에서 raw error가 정제된다.
3. stage/error fingerprint만 공유된다.
4. 서버가 동일 cluster를 집계한다.
5. 웹과 MCP가 “관측된 실패”와 “추정 원인”을 분리해 보여준다.

## 시나리오 C — 검색과 재사용

1. Agent가 공개 API 사용법을 요청받는다.
2. CSX가 package/version/symbol/environment를 추출한다.
3. local shard에서 후보를 찾는다.
4. 가장 가까운 Sample/Evidence와 delta를 반환한다.
5. Agent가 적용한다.
6. build/test 결과가 다시 Evidence가 된다.

## 시나리오 D — Sample 기여

1. MISS를 LLM이 해결한다.
2. 사용자가 Sample 생성을 선택한다.
3. Sanitized Spec만으로 clean-room Sample을 만든다.
4. sandbox에서 resolve/compile/contract가 통과한다.
5. 사용자가 공개 파일 전체를 검토한다.
6. Origin Seeder 이름으로 게시한다.
7. 다른 peer가 동일 artifact를 cross verify한다.
8. Sample status가 `CROSS_PASS`로 상승한다.

## 시나리오 E — Private 보호

1. private registry/local path/private Git package를 사용하는 프로젝트를 연다.
2. 해당 package/symbol은 Evidence 수집에서 완전히 제외된다.
3. public package Evidence에도 프로젝트 단위 상관관계를 복원할 정보가 포함되지 않는다.

## 시나리오 F — 서버 장애

1. 서버가 일시적으로 응답하지 않는다.
2. Local cache/search는 계속 동작한다.
3. Evidence는 로컬 queue에 보관된다.
4. 서버 복구 후 batch upload한다.

---

# 26. 최종 제품 정의

> **CodeSampleX는 코드 샘플 사이트가 아니다.**
>
> 전 세계 개발자가 공개 라이브러리를 실제로 사용하는 과정에서 생성되는 익명 Evidence를 모아, package/version/symbol/environment별 Compatibility Graph를 만들고, 검증된 최소 Sample과 함께 코딩 LLM에 제공하는 공개 분산형 Reasoning Cache다.

공식 문서는 “무엇이 가능해야 하는가”를 말한다.

CodeSampleX는 다음을 말한다.

```text
이 API가 실제로 어디서 관측됐는가
어느 단계까지 성공했는가
어떤 환경에서 실패가 증가하는가
그 실패가 무엇과 상관관계가 있는가
이미 검증된 답안지가 있는가
현재 프로젝트와 답안지의 차이는 무엇인가
```

궁극적인 목표는 하나다.

> **전 세계의 코딩 LLM이 이미 끝난 추론을 다시 하지 않게 한다.**

---

# 부록 A — Execution Context 축 (2026-08-13 확장)

Environment 모델은 OS/Runtime/Language에 한정되지 않는다. **Execution Context**는
독립적인 핵심 호환성 축이다.

```text
Environment
├ OS / OS Version / Arch
├ Language / Language Version / Compiler
├ Package Manager / PM Version
└ Execution Context (extensible)
   ├ Node / Bun / Deno
   ├ Browser (chrome | edge | firefox | safari | chromium)
   ├ Android WebView / iOS WKWebView
   ├ Electron
   └ Web Worker / Service Worker
```

원칙:

1. 브라우저 Evidence는 `browserFamily / browserMajor / engine / engineVersion`만 기록한다.
   **전체 User-Agent는 서버로 보내지 않는다** — 로컬 정규화 후 폐기한다.
2. 같은 package/symbol이라도 Node 22, Chrome 140, Safari 19, Android WebView는
   서로 다른 환경으로 집계·검색·표시된다.
3. 단계 구분에 `PROJECT_LOAD`, `SYMBOL_EXECUTED`, `SYMBOL_CALL`이 추가된다.
   SYMBOL 단계는 실행이 **직접 관측된 경우에만** 기록한다(A3 adapter 전용, 추측 금지).
4. 실패 원인 도메인에 `BROWSER`, `ENGINE`이 추가된다. 증거가 없으면 `UNKNOWN` 또는
   confidence가 있는 가설로 유지하고, 한 변수씩 다른 cross verification으로 좁힌다.
5. 검색은 현재 프로젝트의 Execution Context를 sensitive dimension으로 반영한다.
   Safari 프로젝트에는 Node에서만 검증된 Sample보다 Safari 증거를 우선한다.
6. 빌드/테스트 관측은 toolchain context(node)에 기록한다. 브라우저 context Evidence는
   실제로 그 환경에서 실행된 단계만 만든다.

상세 설계: `docs/execution-context.md`.
