# v0.1.43 verifier digest — production 실행 증거 (2026-08-24 실측)

R2C-81, 3차 실행 세대. [2026-08-23 문서](verifier-digest-production-evidence-2026-08-23.md)는
같은 질문을 **production이 아직 아무것도 실행하지 않은 시점**에 측정한 기록이다.
그 뒤 production verifier가 실제로 돌기 시작했고, 이 문서는 그 결과를 다시 측정한다.
08-23 문서의 §4·5("verifierImage 0건")와 §6의 좁은 화면 결론은 이 문서가 대체한다.
나머지 절은 그 시점의 기록으로 유효하다.

측정 시각은 2026-08-24 02:45Z ~ 03:05Z이고, 측정 지점은 세 곳이다.

| 지점 | 정체 | 이번 run에서 확인한 값 |
|---|---|---|
| production server | `csx-prod-1` (54.116.158.230, codesamplex.dev) | `CSX_VERSION=3ca13b91c900cb721572f35bdae81dcc3c61e433`, 컨테이너 기동 2026-08-24T02:30:23Z |
| production verifier | `csx-farm-linux-1` (43.200.78.1), systemd `csx-verify` | `csx v0.1.44` (`81fb647a`), active |
| 이 워크스테이션 | Windows 11 Pro 26200, Docker Desktop 29.2.1 (linux 엔진) | peer `ed25519:d91480838ac982c9` |

**배포본과 이 worktree의 관계.** 배포된 서버의 `CSX_VERSION`은 이 worktree의 HEAD와
같은 커밋이다(`3ca13b9`, worktree clean). 그리고 receipt에 이미지를 기록하는 코드 경로는
v0.1.43 이후 한 글자도 바뀌지 않았다 —

```
git diff --quiet v0.1.43 HEAD -- internal/sandbox            → IDENTICAL
git diff --quiet v0.1.43 HEAD -- internal/verifier/engine.go → IDENTICAL
git diff --quiet v0.1.43 HEAD -- internal/domain/types.go    → IDENTICAL
git diff --quiet v0.1.43 HEAD -- internal/identity           → IDENTICAL
```

즉 아래 테스트가 실행하는 바이트는 farm verifier(v0.1.44)와 배포 서버가 실행한 바이트와
같다. 08-23 문서는 "배포본과 두 파일만 다르다"까지만 말할 수 있었고, 지금은 서버가
**정확히 같은 커밋**이다.

---

## 1. Linux base/libc 실측 — 완료, 이번에는 SKIP 0건

R2C-64 당시 Docker Hub anonymous rate limit으로 SKIP됐던 4개 항목을 포함해,
registry의 Linux 항목 **24개 전부**를 이번 run에서 실제로 실행해 측정했다.

```
go test ./internal/sandbox/ -run TestImageBaseMatchesTheRealImage -v -count=1
  → 24 subtests, PASS 24, SKIP 0, ok 17.459s
```

SKIP 0건이 핵심이다. SKIP은 통과가 아니라 "이 머신에서 측정되지 않음"이고,
08-23 문서는 그 구분을 남겼지만 전 항목 측정을 보이지는 못했다.

4개 항목은 별도로 컨테이너를 직접 띄워 다시 읽었다 (`--pull=never --network=none`,
registry 왕복 없음 — rate limit은 이번 측정에 관여하지 않는다).

| pinned reference | 실측 distro | 실측 libc | 실측 runtime | 표(registry) 기대값 | 판정 |
|---|---|---|---|---|---|
| `python:3.12-alpine@sha256:d09d15e6…dc31` | alpine 3.24.1 | musl | Python 3.12.14 | alpine / musl | 일치 |
| `oven/bun:1-alpine@sha256:07235578…37eb` | alpine 3.22.5 | musl | bun 1.4.0 | alpine / musl | 일치 |
| `golang:1.26-alpine@sha256:28d89ee9…b468` | alpine 3.24.1 | musl | go1.26.7 | alpine / musl | 일치 |
| `rust:1-alpine@sha256:a10e64dd…4dce` | alpine 3.24.1 | musl | rustc 1.98.0 | alpine / musl | 일치 |

측정 아키텍처는 linux/amd64 하나다. arm64에서의 base/libc는 이번에도 측정하지 않았다.

## 2. Windows verifier lane 실제 실행 — 미확보 (blocker 유지)

이번 run에서 다시 확인했고, 결론은 08-23과 같다.

* 이 머신에 Windows 컨테이너 엔진이 없다: `com.docker.service` = `Stopped`,
  `\\.\pipe\docker_engine` 부재, `docker context ls`는 `desktop-linux`만 활성.
* production에 Windows worker가 없다: receipt **5,356건 전부** `environment.os = linux`이고
  `os <> 'linux'`인 receipt는 0건이다.

Windows lane은 registry digest만 있고 실행 증거가 없다는 서술이 지금도 정확하다.
production에 Windows worker가 생기기 전에는 어느 머신에서 돌려도 production 증거가
되지 않으므로, 별도 운영 gate로 남긴다.

## 3. production verifier 버전 — 확인

| 대상 | 값 | 근거 |
|---|---|---|
| farm `csx-verify` | `csx v0.1.44` = `81fb647a` | `csx version`, `systemctl is-active` = active |
| production server | `3ca13b91c900…` (v0.1.44 이후 main) | `docker inspect codesamplex-server-1` |

`v0.1.43`(`c7bc0f51`)과 `v0.1.44`(`81fb647a`)는 모두 HEAD의 조상이다
(`git merge-base --is-ancestor`). 요구 조건인 "v0.1.43 이상"은 충족된다.

## 4·5. production receipt의 digest-pinned identity와 서명 — 확보

**production receipt 5,356건 중 `verifierImage`를 가진 것이 431건이다.**
(08-23 1차 측정: `4915 | 0`, 2차 측정: `5087 | 172`)

```
select count(*) total, count(receipt->'verifierImage') with_img, max(created_at) from receipts;
 total | with_img |             max
  5356 |      431 | 2026-08-24 01:23:50.802736+00
```

431건을 read-only로 덤프해 **이 repo의 코드로 오프라인 재검증**했다
(`domain.VerificationReceipt.SigningBytes` + `identity.Verify` + peerID 유도 +
`ReceiptID()` + 서버와 같은 `pinnedImageReference` 정규식).

```
receipts=431 signatureValid=431 peerIdBinding=431 receiptIdMatchesStored=431
digestPinned=431 referenceDigestAgree=431 canonicalRoundTrip=431
window=2026-08-23T04:23:24Z .. 2026-08-24T01:23:50Z
```

여섯 항목이 각각 다른 것을 말한다.

* `signatureValid` — 저장된 문서가 peer의 서명이 덮는 바이트 그대로다. 서명 이후
  누구도 바꾸지 않았다. 이미지 필드도 그 서명 안에 있다.
* `peerIdBinding` — `peerId == "ed25519:" + hex(sha256(peerPubkey))[:16]`. 서명한 키와
  주장하는 정체가 같다.
* `receiptIdMatchesStored` — DB의 `receipt_id`가 저장된 문서의 content hash와 같다.
  **실행 digest와 저장 digest가 같은 문서 안에 있다**는 뜻이고, 두 개의 표를 대조한
  것이 아니다.
* `digestPinned` / `referenceDigestAgree` — `reference`가 `<alias>@sha256:<64hex>`이고
  `digest` 필드가 그 접미사와 같다.
* `canonicalRoundTrip` — 저장 문서를 이 코드로 다시 정규화해도 값이 동일하다.

실행된 이미지는 세 종류다.

| reference | receipt 수 |
|---|---|
| `node:22-alpine@sha256:c610fcdf…aa32` | 348 |
| `golang:1.26-alpine@sha256:28d89ee9…b468` | 79 |
| `python:3.12-alpine@sha256:d09d15e6…dc31` | 4 |

peer는 둘이다: farm `ed25519:c1973797be207ac4` 428건, 이 워크스테이션
`ed25519:d91480838ac982c9` 3건.

이미지 이름은 receipt가 스테이지와 **같은 선택 함수**에서 얻는다
(`DockerRunner.VerifierImage` → `imageForManifestOn`), 그래서 receipt의 이미지와 실제
실행 이미지가 어긋날 수 있는 병렬 경로가 없다.

### 서버/화면에서 같은 identity 조회

세 lane 모두 공개 페이지에서 같은 값으로 조회된다 (HTTP 200, 이번 run 실측).

| sample | 페이지가 보여주는 값 |
|---|---|
| `sha256:f03d79ba…05e4` | `node:22-alpine@sha256:c610fcdfb1d5…` (title에 전체 reference) |
| `sha256:40e31ec2…e4b5` | `golang:1.26-alpine@sha256:28d89ee9cc0f…` |
| `sha256:7934af40…57ab` | `python:3.12-alpine@sha256:d09d15e60962…` |

## 6. 화면 표시와 좁은 화면 — 확인 (08-23 결론 정정)

08-23 문서는 headless Chrome이 요청보다 좁게 렌더링하지 않아 **실제로는 497px에서만**
측정했고, "가로 넘침은 모든 폭에서 0"이라고 적었다. 이번에는 실제 브라우저에서
same-origin iframe으로 **320 / 360 / 480px 뷰포트를 진짜로** 만들어 측정했다 —
iframe 안에서는 media query가 프레임 폭으로 평가되므로 emulation이 아니라 실제 레이아웃이다.

| 뷰포트 | `scrollWidth` | `clientWidth` | 가로 넘침 | digest span 제거 시 넘침 | **digest 기여분** |
|---|---|---|---|---|---|
| 320 | 355 | 305 | 50 | 50 | **0** |
| 360 | 425 | 345 | 80 | 80 | **0** |

**digest 표시가 좁은 화면 가로 폭에 기여하는 값은 0이다** — 08-23의 결론과 같지만,
이번에는 실제로 좁은 뷰포트에서 측정한 값이다. 표시는
`golang:1.26-alpine@sha256:28d89ee9cc0f…`로 alias는 온전하고 digest만 12자로 줄며,
전체 reference는 `title`에 보존된다. runs 표는 `overflow-x: auto` 컨테이너 안에 있고
표 자체는 넘치지 않는다(`scrollWidth == clientWidth == 341`).

정정할 것은 **"모든 폭에서 넘침 0"이라는 문장 쪽이다.** 실제 320/360px에서는 페이지가
50px / 80px 가로로 넘친다. 원인은 digest가 아니라 배지 툴팁이다 — 아래 참고 항목.

live 페이지 렌더에서 같이 확인된 것이 하나 더 있다. 같은 sample 페이지에 receipt 3건이
있는데, 컨테이너가 실제로 돈 1건에만 이미지 줄이 붙고 나머지 2건에는 붙지 않는다.
`nil`은 "기본 이미지"가 아니라 **미확립**이라는 계약이 화면에서 그대로 보인다.

## 7. mutable tag lane 잔존 감사 — 남아 있지 않음

**production 데이터로 확인.** 필드를 쓸 수 있게 된 이후(2026-08-23 04:23Z~) 생성된
receipt는 441건이고, 그중 431건이 digest 고정 이미지를 기록했다. 나머지 10건은
이미지가 없는데, **mutable tag로 돈 것이 아니라 컨테이너가 아예 시작되지 않은 실행**이다.

10건 전부 stage 모양이 하나로 동일하다.

```
{"load":"SKIPPED","compile":"SKIPPED","resolve":"FAIL","contract":"SKIPPED"}
```

그리고 production 자신의 stage log가 이유를 적어 놓았다
(`/home/csxver/.csx/verify-logs/20260823T164536.289-40e31ec292ca9704.log`) —

```
===== resolve (FAIL) =====
sandbox: verifier runtime version "1.26" cannot satisfy "1.27.0"
```

즉 이미지 선택 자체가 실패해 `VerifierImage`가 `nil`이 된 경우다. R2C-94가 고친
Go 1.27 cross job 문제의 production 흔적이고, 이미지 없음이 정확히 옳은 기록이다.
**mutable tag를 기록한 receipt는 production에 0건이다.**

코드 쪽 감사는 배포된 커밋에서 그대로 통과한다.

```
go test ./internal/sandbox/ -run TestEveryVerifierLaneRunsADigestPinnedImage|…
  → ok 17.459s (SKIP 0)

CSX_TEST_DSN=… go test ./internal/httpapi/ ./internal/verifier/ ./internal/web/ \
                       ./internal/domain/ ./internal/serverstore/
  → ok httpapi 1.678s · verifier 1.661s · web 2.231s · domain 0.380s · serverstore 27.206s
```

receipt 이미지 계약을 직접 실행하는 7개 테스트도 개별로 통과했다:
`TestReceiptRecordsTheImageTheStagesRanIn`, `TestTheSignatureCoversTheImage`,
`TestAHostRunNamesNoImage`, `TestServerRefusesAnImageClaimThatIsNotAPin`,
`TestAPinnedImageSurvivesIntoTheStoredReceipt`,
`TestAReceiptWithoutAnImageIsStillAccepted`, `TestAV1ReceiptMayNotCarryAnImage`.

배포된 서버가 이 커밋 그 자체이므로, 서버의 mutable tag 거절(`receiptVerifierImageIsPinned`)은
같은 바이트가 하는 동작이다. production에 일부러 잘못된 receipt를 보내 400을 받아보는
방법도 있으나, 그 경로에 버그가 있다면 오히려 production에 오염을 남기게 되므로 하지 않았다.

---

## 완료 조건 대비 현재 상태

| 완료 조건 | 상태 |
|---|---|
| Linux 미측정 4개 실측 완료 또는 명시적 환경 blocker 기록 | **완료** — 24/24 측정, SKIP 0 (amd64) |
| Windows verifier 실제 실행 증거 확보 | **미확보** — 환경 blocker (§2). production에 Windows worker 부재 |
| production receipt 최소 1건에서 digest-pinned identity와 signature 보존 확인 | **완료** — 431건 전수 재검증 |
| 서버/화면에서 같은 identity 조회 가능 | **완료** — 3개 lane 모두 200, 화면 값 일치 |
| 검증 결과를 한국어로 기록 | 이 문서 |

## 이 문서가 주장하지 않는 것

* Windows verifier lane이 동작한다는 것. registry digest만 있고 실행 증거는 없다.
* amd64 외 아키텍처에서 base/libc가 표와 일치한다는 것. 측정하지 않았다.
* 이번 run 동안 production이 새 receipt를 만들었다는 것. 만들지 않았다
  (마지막 receipt 01:23:50Z, claim 02:45Z 이후 0건). 위 증거는 **이미 있는**
  production 실행 결과를 이번 run에 다시 측정한 것이다.
* production에 쓰기를 했다는 것. 이번 run의 production 접근은 전부 SELECT와 GET이다.

## 부수적으로 발견한 것 (R2C-81 범위 밖)

**좁은 화면에서 sample 페이지가 가로로 넘친다 — 원인은 배지 툴팁이다.**
`span.badge-tip`은 `position:absolute; visibility:hidden; opacity:0`인 숨은 툴팁인데
`width: 352px`로 고정돼 있다. 숨어 있어도 레이아웃 박스는 남으므로 뷰포트가 352px보다
좁으면 문서 `scrollWidth`를 밀어낸다 — 360px에서 오른쪽 끝이 425px, 즉 80px 초과다.
이번 측정에서 조상 중 `overflow-x: auto`가 없는 유일한 초과 요소였다.

digest 표시와 무관하고 컴포넌트도 다르므로 이 문서는 관측만 남긴다.
`max-width`를 뷰포트에 묶거나 툴팁을 `display:none`으로 감추는 쪽 판단이 필요하다.
