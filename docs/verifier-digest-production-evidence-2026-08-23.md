# v0.1.43 verifier digest — production 실행 증거 재검증 (2026-08-23)

R2C-81. [R2C-64](https://linear.app/r2cuerdame/issue/R2C-64)에서 verifier 이미지를 전부
immutable digest로 고정하고 receipt에 실행 이미지 식별자를 기록하도록 바꿨고,
[R2C-80](https://linear.app/r2cuerdame/issue/R2C-80)의 release gate가 **v0.1.43**
(`c7bc0f5179ba534c18f57e84df59932dc67ddf13`) 공개 성공으로 닫혔다. 이 문서는 그 다음
질문 — *그래서 production이 실제로 그 digest를 실행했다는 증거가 있는가* — 에 대해
이 시점에 실제로 측정된 것과, 측정하지 못한 것과 그 이유를 남긴다.

측정 기준 시각은 2026-08-23이고, 측정 지점은 세 곳이다.

| 지점 | 정체 |
|---|---|
| production server | `csx-prod-1` (54.116.158.230, codesamplex.dev), `codesamplex/csx-server:latest` |
| production verifier | `csx-farm-linux-1` (43.200.78.1), systemd `csx-verify` |
| 이 워크스테이션 | Windows 11 Pro 26200, Docker Desktop (linux 컨테이너), csx peer `ed25519:d91480838ac982c9` |

---

## 1. Linux 미측정 4개 base/libc 실측 — 완료

R2C-64 당시 Docker Hub anonymous rate limit 때문에 `TestImageBaseMatchesTheRealImage`가
SKIP했던 4개 항목이다. 이번에는 **registry 왕복이 없었다**: 네 pinned digest 모두 이
머신의 로컬 image store에 이미 존재해서, `docker run <alias>@<digest> --pull=never`가
캐시된 바이트를 그대로 실행했다. 즉 rate limit은 이번 측정에 관여하지 않았고,
SKIP은 "측정 불가"가 아니라 "그때 측정되지 않았음"이었다는 것도 같이 확인된다.

| pinned reference | 실측 libc | 실측 distro | 실측 runtime | 표(registry) 기대값 | 판정 |
|---|---|---|---|---|---|
| `python:3.12-alpine@sha256:d09d15e6…dc31` | musl | alpine 3.24.1 | Python 3.12.14 | alpine / musl | 일치 |
| `oven/bun:1-alpine@sha256:07235578…37eb` | musl | alpine 3.22.5 | bun 1.4.0 | alpine / musl | 일치 |
| `golang:1.26-alpine@sha256:28d89ee9…b468` | musl | alpine 3.24.1 | go1.26.7 | alpine / musl | 일치 |
| `rust:1-alpine@sha256:a10e64dd…4dce` | musl | alpine 3.24.1 | rustc 1.98.0 | alpine / musl | 일치 |

측정 아키텍처는 linux/amd64 하나뿐이다. 등록된 digest는 multi-platform index digest이므로
arm64 worker도 같은 항목을 실행할 수 있지만, **arm64에서의 base/libc는 이번에 측정되지
않았다.** 이것은 표가 틀렸다는 뜻이 아니라 측정 범위가 amd64라는 뜻이다.

`go test ./internal/sandbox/ -run TestImageBaseMatchesTheRealImage` 전체를 이 머신에서
직렬로 돌리면 로컬 캐시에 없는 항목에서 여전히 SKIP이 난다. SKIP은 통과가 아니라
"이 머신에서 측정되지 않음"으로 읽어야 한다.

## 2. Windows verifier lane 실제 실행 — 미확보 (환경 blocker)

확보하지 못했다. 우회하지 않고 이유를 그대로 적는다.

* 이 머신에는 Windows 컨테이너 엔진이 없다. `com.docker.service`는 `Stopped`이고
  `\\.\pipe\docker_engine`은 존재하지 않는다. Docker Desktop은 linux 엔진
  (`desktop-linux` context) 하나만 서빙 중이다.
* Windows optional feature(`Containers`, `Microsoft-Hyper-V-All`) 조회 자체가 관리자
  권한을 요구해 상태조차 읽지 못했다.
* 호스트는 Windows 11 build 26200이고 등록된 Windows verifier 이미지는 전부
  `ltsc2022`(Server 2022, build 20348)다. 커널 버전이 달라 process isolation으로는
  뜨지 않고 Hyper-V isolation이 필요하다.
* 전환 비용이 이 작업의 범위를 넘는다: 관리자 권한, feature 활성화(재부팅 가능성),
  수 GB 규모의 servercore 이미지 pull, 그리고 전환 동안 **linux 엔진 중단** — 이
  머신에서 돌고 있는 다른 작업이 전부 같이 멈춘다.

더 중요한 사실이 하나 있다. **production에는 Windows worker가 아예 존재한 적이 없다.**
receipt 4,915건 전부 `environment.os = linux`이고, Windows receipt는 0건이다.
즉 Windows verifier lane은 현재 코드에만 있고 production에서 한 번도 실행된 적이 없다.
R2C-64에서 확보한 것은 registry digest 뿐이라는 서술이 지금도 정확하다.

## 3. production verifier 버전 — 확인

| 대상 | 값 | 시각 |
|---|---|---|
| production server `CSX_VERSION` | `c7bc0f5179ba534c18f57e84df59932dc67ddf13` (= v0.1.43) | 컨테이너 생성 2026-08-22T16:40:54Z |
| farm `csx-verify` | `farm-verify: csx v0.1.43 parallel=4 budget=unlimited` | 2026-08-22T16:46:56Z 기동 |

farm의 버전 이력상 직전 버전은 v0.1.41이다. verifier가 v0.1.43으로 올라온 것은
2026-08-22 16:46이 처음이다.

## 4·5. production receipt의 digest-pinned identity와 서명 — 미확보

**production receipt 중 `verifierImage`를 가진 것은 0건이다.**

```
select count(*) total, count(receipt->'verifierImage') with_img, max(created_at) from receipts;
 total | with_img |            max
  4915 |        0 | 2026-08-21 20:39:01.157598+00
```

이것은 회귀가 아니라 시간 순서의 결과다. 가장 최근 receipt가 2026-08-21 20:39이고,
verifier가 v0.1.43이 된 것은 2026-08-22 16:46이다. 즉 **v0.1.43 verifier는 기동 이후
단 한 건의 receipt도 만들지 않았다.** 필드를 만들 수 있는 코드가 올라온 뒤로 아무것도
실행되지 않은 것이다.

production이 놀고 있는 이유는 두 갈래이고, 둘 다 측정했다.

* **authoring queue**: `csx-author@1/2`가 5분마다
  `NO_WORK: no uncovered Wanted or evidence-driven expansion work is available for this worker.`
  를 반복한다. 새 sample이 생기지 않으니 originating receipt도 생기지 않는다.
* **cross queue**: open 12건 중 두 peer 모두에게 제안되는 것은 3건
  (job 6035 / 6038 / 6042)뿐이고, 이 3건은 전부
  `wantEnv.runtimeVersion = "1.27"`인 golang job이다. registry의 golang lane은
  `golang:1.26-alpine` 하나뿐이라 `runtimeVersionMatches("1.26","1.27")`가 false가 되고,
  worker는 claim 전에 `canPrepare`에서 fail-closed로 건너뛴다. **영구히 실행 불가능한
  job이다.** 나머지 open job(npm 8건 + golang 1건)은 두 peer가 이미 receipt를 낸
  sample이라 cross 규칙상 두 peer 모두에게서 제외된다.
* 결과: 현재 두 peer(`ed25519:c1973797be207ac4` = farm, `ed25519:d91480838ac982c9` =
  이 워크스테이션) 어느 쪽도 실행 가능한 job이 없다. `csx worker start --once`를
  실제로 돌려 확인했다 — `completed=0 failed=0`.

즉 **기다려서는 이 증거가 생기지 않는다.** 별도 조치가 필요하며, 그 선택은 R2C-81
Linear 댓글의 decision packet으로 올린다.

### 대신 확인한 것: 배포된 코드와 동일한 코드 경로의 E2E

production에 쓰지 않고 확인할 수 있는 범위는 전부 확인했다. 이 worktree HEAD와 배포
커밋 `c7bc0f5`의 diff는 `internal/httpapi/api.go` + `internal/httpapi/authoring_work.go`
(R2C-23 authoring 기능)뿐이고, `internal/sandbox` · `internal/verifier` · `internal/web`
· `internal/domain` · `schemas`는 **배포본과 바이트 단위로 동일하다.** 검증 경로에
관한 한 아래 결과는 배포된 코드의 결과와 같다.

production과 같은 PostgreSQL 스토어(`postgres:17-alpine`)를 붙여 실행:

```
CSX_TEST_DSN="postgres://csx:csx@localhost:55433/csx" \
  go test ./internal/httpapi/ ./internal/verifier/ ./internal/web/ \
          ./internal/sandbox/ ./internal/serverstore/ ./internal/domain/
ok  internal/httpapi  3.907s
ok  internal/verifier  3.192s
ok  internal/web       4.697s
ok  internal/sandbox  21.033s
ok  internal/serverstore  22.167s
ok  internal/domain    0.358s
```

이 안에서 4·5번 항목에 해당하는 주장은 다음 테스트가 실행한다.

* `verifier.TestReceiptRecordsTheImageTheStagesRanIn` — 컨테이너 검증이 만든 receipt의
  `reference`가 `<alias>@<digest>` 형태이고 `digest` 필드와 일치한다.
* `verifier.TestTheSignatureCoversTheImage` — 서명이 이미지 필드를 덮는다. digest를
  바꾸면 서명 검증이 깨지고 receipt id도 달라진다.
* `verifier.TestAHostRunNamesNoImage` — 컨테이너에 들어가지 않은 실행은 필드를
  비운다. 없음은 "기본 이미지"가 아니라 **미확립**이다.
* `httpapi.TestServerRefusesAnImageClaimThatIsNotAPin` — mutable tag, 잘린 digest,
  reference와 어긋나는 digest, 빈 객체를 전부 400으로 거절하고 저장하지 않는다.
* `httpapi.TestAPinnedImageSurvivesIntoTheStoredReceipt` — 서명 검증을 통과한 receipt의
  digest가 저장된 receipt JSON에 그대로 남는다.
* `httpapi.TestAReceiptWithoutAnImageIsStillAccepted` — 이전 peer의 receipt는 여전히
  받되, 없는 필드를 만들어 넣지 않는다.
* `httpapi.TestAV1ReceiptMayNotCarryAnImage` — v1을 자칭하면서 v2 필드를 담을 수 없다.

**이것은 production 실행 증거가 아니다.** 배포된 것과 같은 코드가 그렇게 동작한다는
증거이고, "production worker가 실제로 그 digest를 실행했다"는 주장과는 다른 문장이다.
그 문장은 아직 증거가 없다.

## 6. sample/detail 화면의 image identity 표시 — 확인

`TestDumpSampleReview`로 digest-pinned receipt가 붙은 sample 페이지를 실제 템플릿·실제
`site.css`로 렌더링하고 headless Chrome으로 촬영했다.

* 표시 형태: `node:22-alpine@sha256:c610fcdfb1d5…` — alias는 온전히 남고 digest만
  12자로 줄인다. 전체 reference는 `title` 속성에 그대로 있어, 잘린 것은 라벨이고
  재실행에 쓸 수 있는 원본은 보존된다.
* 좁은 화면: `table.runs .runimage { display: block; overflow-wrap: anywhere; }` 때문에
  긴 digest가 셀 안에서 줄바꿈된다. 실제 측정값 —

  | viewport | `scrollWidth` | `clientWidth` | 가로 넘침 |
  |---|---|---|---|
  | 360 요청(실제 497) | 497 | 497 | 없음 |
  | 480 요청(실제 497) | 497 | 497 | 없음 |
  | 640 요청(실제 603) | 603 | 603 | 없음 |

  digest span을 제거한 사본을 같은 폭으로 렌더링했을 때 값이 **완전히 동일**했다.
  즉 좁은 화면 레이아웃에 digest 표시가 기여하는 가로 폭은 0이다.

  (headless Chrome은 요청한 창 폭보다 작게 렌더링하지 않아 360 요청도 실제로는
  497로 그려진다. 360 스크린샷에서 오른쪽이 잘려 보이는 것은 캔버스가 360이기
  때문이며 페이지의 가로 넘침이 아니다 — 위 측정이 그것을 구분한다.)

> **정정 (2026-08-24, [R2C-148](https://linear.app/r2cuerdame/issue/R2C-148)).** 위 표의
> "360 요청(실제 497)"은 360px에서 넘침이 없다는 증거가 아니다. headless Chrome이 창을
> 497px 아래로 줄이지 않아 **360px는 측정된 적이 없고**, 이 표가 읽은 것은 전부 497px
> 렌더다. 실제로 360px에서 렌더하면 `.badge-tip`가 hidden 상태로 레이아웃 폭을 밀어
> `scrollWidth 426 / clientWidth 360`이 나온다. digest 표시의 기여분이 0이라는 §6의
> 결론 자체는 R2C-148에서 다시 확인됐다 — 넘침의 원인은 digest가 아니라 tooltip이었다.
> 창 대신 고정 폭 iframe 안에서 렌더하면 `100vw`·스크롤바·`documentElement.scrollWidth`가
> 그 폭의 창과 똑같이 동작하므로 497px 바닥이 사라진다. 그 방식으로 320/360/480px를
> 실제로 측정하는 회귀 테스트가 `internal/web/narrowviewport_test.go`다.

## 7. mutable tag로만 실행되는 lane 감사 — 남아 있지 않음

코드 쪽은 다음이 실행으로 확인한다 (`internal/sandbox`, 전부 통과).

* `TestEveryVerifierLaneRunsADigestPinnedImage` — 선택 가능한 모든 lane(Linux 25개 +
  Windows 5개)이 `<alias>@sha256:<64hex>`를 반환한다.
* `TestEveryRegistryEntryIsDigestPinned` — registry 27개 항목 전부 digest 고정이고,
  두 alias가 같은 digest를 주장하지 않으며, libc가 빈 항목이 없다(Windows 제외).
* `TestEveryLaneImageComesFromTheRegistry` — 어떤 lane도 registry 밖 문자열을 고르지 못한다.
* `TestDockerRunReceivesTheDigestNotTheTag` — `docker run` 인자에 alias 단독이 들어가는
  경로가 없다.
* 서버 쪽 이중 방어: `receiptVerifierImageIsPinned`가 mutable tag를 담은 receipt를
  400으로 거절한다. worker가 어떻게 만들어졌든 production DB에는 들어가지 않는다.

그리고 이 감사가 이론이 아니라는 것을 보여주는 실측이 하나 있다. **같은 tag가 세 머신에서
서로 다른 바이트를 가리키고 있다.**

| `golang:1.26-alpine`가 가리키는 digest | 어디 |
|---|---|
| `sha256:28d89ee9…b468` | registry 고정값 (실행 권위) |
| `sha256:3889b425…2d83` | farm 로컬 tag |
| `sha256:70b46548…5816` | 이 워크스테이션 로컬 tag |

tag로 실행했다면 세 머신이 같은 환경을 이름 붙인 receipt로 서로 다른 소프트웨어의
실행을 서명했을 것이다. digest 고정이 막는 것이 정확히 이 상황이다.

farm의 로컬 이미지 목록에는 부수적으로 읽을 것이 하나 더 있다. maven/gradle 항목은
`<none>` 태그의 digest pull(= 고정 경로로 이미 실행됨)인 반면 golang/node/python은
tag pull만 있다. Java lane이 먼저 고정됐고 나머지가 R2C-64에서 따라온 이력과 일치한다.
그리고 farm에는 `golang:1.26-alpine`의 **고정 digest가 아직 pull되어 있지 않다** —
v0.1.43 기동 이후 golang 검증을 한 번도 하지 않았다는 4번 항목의 결론과 같은 말이다.

---

## 완료 조건 대비 현재 상태

| 완료 조건 | 상태 |
|---|---|
| Linux 미측정 4개 실측 완료 또는 명시적 환경 blocker 기록 | **완료** (실측, amd64) |
| Windows verifier 실제 실행 증거 확보 | **미확보** — 환경 blocker (§2). production에 Windows worker 부재 |
| production receipt 최소 1건에서 digest-pinned identity와 signature 보존 확인 | **미확보** — production 유휴 (§4·5) |
| 서버/화면에서 같은 identity 조회 가능 | **부분** — 화면·서버 코드 경로는 확인(§6), production 데이터가 없어 조회 대상 없음 |
| 검증 결과를 한국어로 기록 | 이 문서 |

## 이 문서가 주장하지 않는 것

* production worker가 immutable digest를 실행했다는 것. 아직 증거가 없다.
* Windows verifier lane이 동작한다는 것. registry digest만 있고 실행 증거는 없다.
* amd64 외 아키텍처에서 base/libc가 표와 일치한다는 것. 측정하지 않았다.
* 위 테스트 통과가 production 실행 증거라는 것. 같은 코드의 동작 증거일 뿐이다.

## 부수적으로 발견한 것 (R2C-81 범위 밖)

golang cross job 3건(6035 / 6038 / 6042)이 `runtimeVersion 1.27`을 요구해 영구히
실행 불가능한 상태로 queue에 남아 있다. 어떤 worker도 claim하지 않으므로 통계상
"open"으로 계속 집계된다. registry에 Go 1.27 lane을 추가하든 job을 취소하든 별도
판단이 필요하다. 이 문서는 관측만 남긴다.
