# Execution Context — 독립 호환성 축 설계 (2026-08-13)

같은 package/version/symbol이라도 **어디서 실행됐는가**는 독립적인 호환성 축이다.
Node 22, Chrome 140, Firefox 142, Safari 19, Android WebView, iOS WKWebView,
Electron 38, Web Worker, Service Worker, Bun, Deno는 서로 다른 환경으로 집계·검색·표시된다.

## 1. 모델

`EnvironmentFingerprint`(schemas/v1/environment.json)에 다음 축이 추가되었다.

```text
executionContext   node | browser | webview | electron | webworker | serviceworker | bun | deno | java | <확장 가능>
browserFamily      chrome | edge | firefox | safari | chromium | android-webview | ios-wkwebview | electron
browserMajor       "140"  (이미 bucket)
engine             chromium | gecko | webkit
engineVersion      engine major
compiler / compilerVersion
```

격리 축(2026-08-13 추가):

```text
virtualization     container | vm | wsl | "" (bare metal 또는 미탐지)
containerRuntime   docker | podman | containerd | kubernetes | lxc
libc               musl | glibc | ""
libcVersion        glibc major.minor (best effort)
distro             /etc/os-release ID (ubuntu | debian | rhel | alpine | amzn | ...)
ci                 자동화 러너 여부
```

- **왜 필요한가**: alpine 컨테이너 빌드와 ubuntu 호스트 빌드가 둘 다 `os=linux`로만 기록되면
  증거가 거짓말을 한다. 네이티브 모듈(sharp, bcrypt, canvas 등)은 musl/glibc 경계에서
  갈리고, 다른 모든 차원이 동일해 보이는데 결과만 다르게 나온다.
- **탐지 방법**(전부 coarse, 개인 식별 불가): `/.dockerenv`, `/run/.containerenv`,
  `/proc/1/cgroup`, `/proc/version`(WSL), DMI product name(VM), musl loader 경로,
  `/etc/os-release`, Windows는 BIOS 제조사 레지스트리 값. 호스트명·컨테이너 ID·머신 ID는
  읽지 않는다.
- **검증 영수증**: 영수증은 호스트가 아니라 실제 pinned verifier image의 배포판과
  libc를 기록한다. 대부분의 경량 lane은 Alpine/musl이고, Java exact matrix는 Amazon
  Linux 2023/glibc다. 이를 그냥 "linux"로 기록하면 Ubuntu·Debian·RHEL 계열까지
  검증한 것처럼 보이게 된다.
- **CI**: CI 러너는 서로 복제본이므로 독립 개발 환경으로 세면 다양성이 과장된다. 지금은
  기록만 하고, 집계에서 할인하는 것은 후속 작업이다.

- `executionContext`는 **open vocabulary**다. 새 runtime은 schema 변경 없이 추가된다.
- CLI 자체가 질문의 대상이면 `executionContext/runtime`에 실제 도구 이름과 버전을
  기록한다. `npm`, `maven`, `git`, `curl`, `gh`도 라이브러리의 부속물이 아니라 독립된
  버전 대상이다. 공개 좌표는 `pkg:generic/cli/<name>@<exact-version>`을 사용한다.
- `domain.EnvironmentFingerprint.Normalize()`: runtime이 node/bun/deno면 context를 자동 유도하고,
  browserFamily에서 engine을 무모순으로 유도한다.
- `ContextLabel()`이 집계 row key와 UI 표시명을 만든다: `chrome 140`, `node 22.18`, `safari 19`.

## 2. 프라이버시: User-Agent는 서버로 보내지 않는다

`environment.ParseUserAgent()`가 **로컬에서** UA를 family/major/engine/engineVersion 4개 필드로
정규화하고 원본 UA는 폐기한다. 목적은 개인 fingerprinting이 아니라 API 호환성 evidence이므로
minor/patch, OS build, 기기 모델 등은 수집하지 않는다. 서버 스키마에는 UA 필드 자체가 없다.

## 3. 정직성: 관측 위치와 관측 수준

- **빌드/테스트 관측은 toolchain context에 기록된다.** browser를 타겟하는 프로젝트라도
  `csx run -- pnpm build`는 node에서 실행되므로 그 Evidence의 context는 `node`다.
  브라우저 context Evidence는 실제로 그 환경에서 실행된 단계(LOAD/PROCESS/SYMBOL_*/CONTRACT)만 만든다.
- 단계 구분(observation-batch.json):

```text
USED                 사용 관측
PROJECT_TYPECHECK    타입체크 결과
PROJECT_COMPILE      빌드 결과
PROJECT_TEST         테스트 결과
PROJECT_LOAD         module/library load 결과 (실행 context 기준)
PROJECT_PROCESS      전체 실행 결과
SYMBOL_EXECUTED      특정 API 실행이 직접 관측됨 (result 항상 PASS)
SYMBOL_CALL          호출 성공/실패가 직접 관측됨 (PASS=returned, FAIL=threw)
CONTRACT             공개 Sample의 의도 행동 검증 (VerificationReceipt 경로)
```

- `SYMBOL_EXECUTED`/`SYMBOL_CALL`은 **A3 capability를 가진 adapter만** 방출한다. Public v1의
  어떤 adapter도 A3를 주장하지 않는다(§13.1, §19). 실행 여부가 불확실하면 추측으로
  SYMBOL 단계를 기록하는 것은 금지이며, 이는 ingest 검증에서도 강제된다
  (A3 미신고 클라이언트의 SYMBOL_* batch 거부).

## 4. 실패 원인 도메인

기존 분류(goal.md §6.4)에 `BROWSER`, `ENGINE`이 추가되었다. 요구사항의 축약명은 기존
정밀 명칭에 대응한다: API→API_REMOVED_OR_CHANGED, LIBRARY→LIBRARY_REGRESSION,
DEPENDENCY→TRANSITIVE_DEPENDENCY, CONFIG→CONFIGURATION, EXTERNAL→EXTERNAL_SERVICE.
원인을 확정할 증거가 없으면 반드시 `UNKNOWN` 또는 confidence가 있는 가설로 유지한다.

Cross verification은 한 변수씩 바꾼 환경(§10.2)에 browser/engine 축을 포함한다:

```text
Chrome 140 / Windows   PASS
Chrome 140 / Linux     PASS
Safari 19 / macOS      FAIL
→ BROWSER/ENGINE(webkit) 가설의 confidence를 높인다. library failure로 확정하지 않는다.
```

집계 휴리스틱: 동일 (package,symbol,stage)에서 FAIL이 특정 engine에만 집중되면
`{ENGINE: 0.6, BROWSER: 0.25, LIBRARY_REGRESSION: 0.15}`식의 가설 분포를 생성한다.

## 5. 검색 반영

- `executionContext`(그리고 browser에서는 `browserFamily`)는 **항상 sensitive dimension**이다.
- 요청 env의 context와 결과 Evidence/Sample env의 context가 다르면:
  - LOAD 이상 단계의 증거만 있는 결과는 `ADAPTATION_REQUIRED`로 강등되고
    adaptation에 "verify in <context>"가 명시된다.
  - 요청 context에서 ELEVATED_FAILURE가 관측된 결과는 `REFERENCE_ONLY`로 강등된다.
- browserMajor 차이는 minor version distance처럼 감점, engine 차이는 context 차이처럼 강한 감점.
- Agent는 MCP `search_known_solution`의 `environment`에 감지한 context를 넣는다
  (예: browser=safari 19). daemon은 프로젝트 스캔에서 bun/deno/electron 단서
  (bun.lockb, deno.json, electron 의존성)를 보완한다.

## 6. 표시(Compatibility Explorer)

Symbol 페이지 matrix는 execution context를 1차 row 그룹으로 사용한다:

```text
axios.post / axios@1.12.x
node 22 · TS 5.9 · pnpm · windows      HIGH
node 24 · TS 5.9 · pnpm · windows      MEDIUM
chrome 140 · windows                   HIGH
firefox 142 · linux                    MEDIUM
safari 19 · macos                      ELEVATED FAILURE
android-webview 140                    UNKNOWN (no evidence)
```

Evidence class별 수치는 context별로 분리 표시하며, PROJECT_* 관측과 SYMBOL_*/CONTRACT
증거를 절대 합산하지 않는다.

## 7. Sample과의 관계

Sample manifest의 `environment.executionContext`는 그 Sample의 contract가 실행되는
context를 선언한다(v1 verifier: node). Compatibility는 여전히 Sample 속성이 아니라
Evidence/Receipt 집계 결과다 — context 축이 늘었을 뿐 원칙은 동일하다.
