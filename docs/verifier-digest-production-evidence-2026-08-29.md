# v0.1.43 verifier digest — production 실행 증거 (2026-08-29 실측)

R2C-81, 4차 실행 세대. 앞선 두 문서는 같은 질문을 다른 시점에 측정한 기록이다 —
[08-23](verifier-digest-production-evidence-2026-08-23.md)은 production이 아직 아무것도
실행하지 않은 시점(`verifierImage` 0건), [08-24](verifier-digest-production-evidence-2026-08-24.md)는
막 실행이 시작된 시점(431건)이다. 이 문서는 그 뒤 5일치를 다시 측정한다. 두 문서의
수치는 그 시점의 기록으로 유효하고, 이 문서가 대체하는 것은 개수뿐이다.

측정 시각 2026-08-29 19:00Z ~ 19:15Z. 측정 지점은 세 곳이다.

| 지점 | 정체 | 이번 run에서 확인한 값 |
|---|---|---|
| production server | `csx-prod-1` (54.116.158.230, codesamplex.dev) | `CSX_VERSION=3f6ad8dec0eba78dfe566b096822cf4a41ef47f0`, `CSX_BUILD_VERSION=v0.1.62`, 컨테이너 기동 2026-08-27T13:58:47Z, `/healthz` = ok |
| production verifier | `csx-farm-linux-1` (43.200.78.1), systemd `csx-verify` | `csx v0.1.62`, active |
| 이 워크스테이션 | Windows 11 Pro 26200 | peer `ed25519:d91480838ac982c9` |

**측정 코드와 배포본의 관계.** receipt에 이미지를 기록하는 경로는 배포된 커밋
`3f6ad8d`와 이 브랜치의 base(`origin/main` = `3826d27`)가 바이트 단위로 같다 —

```
git diff --quiet 3f6ad8d origin/main -- internal/sandbox            → IDENTICAL
git diff --quiet 3f6ad8d origin/main -- internal/verifier/engine.go → IDENTICAL
git diff --quiet 3f6ad8d origin/main -- internal/domain/types.go    → IDENTICAL
git diff --quiet 3f6ad8d origin/main -- internal/identity           → IDENTICAL
```

그리고 v0.1.43까지 거슬러 올라가도 이미지 선택·기록 파일 자체는 움직이지 않았다
(`internal/sandbox/images.go`, `internal/sandbox/docker.go`가 `v0.1.43..origin/main`
diff 0). 이 커밋이 `images.go`에 더한 것은 조회 함수 두 개이고 삭제한 줄은 0이므로,
아래 오프라인 재검증이 실행하는 판정 바이트는 farm과 서버가 실행한 바이트다.

---

## 1. production receipt 전수 — 7,204건, 그중 2,279건이 digest 고정 이미지를 기록

read-only SELECT로 측정했다.

```
select count(*) total, count(receipt->'verifierImage') with_img, max(created_at) from receipts;
 total | with_img |             max
  7204 |     2279 | 2026-08-29 15:54:46.402866+00
```

세대별 추이가 이 문서의 요점이다.

| 측정일 | receipt 전체 | `verifierImage` 보유 |
|---|---|---|
| 2026-08-23 | 4,915 | 0 |
| 2026-08-24 | 5,356 | 431 |
| **2026-08-29** | **7,204** | **2,279** |

필드를 쓸 수 있게 된 뒤(2026-08-23T04:23:24Z~) 생성된 receipt는 2,289건이고 그중
2,279건이 이미지를 기록했다. 08-24 측정 이후 새로 생긴 receipt 1,848건은 **전부**
digest 고정 이미지를 기록했다. 이미지 없는 10건은 08-24와 같은 그 10건이고, 새로
늘지 않았다.

그 10건은 mutable tag로 돈 것이 아니라 **컨테이너가 아예 시작되지 않은 실행**이다.
stage 모양이 10건 모두 하나로 같다.

```
{"load":"SKIPPED","compile":"SKIPPED","resolve":"FAIL","contract":"SKIPPED"}
```

이미지 선택 자체가 실패해 `VerifierImage`가 `nil`이 된 경우이고, 이미지 없음이 정확히
옳은 기록이다. `nil`은 "기본 이미지"가 아니라 **미확립**이다.

## 2. 서명·바인딩·content hash — 7,204건 전수 오프라인 재검증, 실패 0

receipt 전체를 read-only로 덤프해 이 저장소의 코드로 다시 검증했다
(`internal/verifier/receiptaudit.go`, 아래 §6).

```
CSX_RECEIPT_DUMP=receipts-all.b64 go test ./internal/verifier/ \
  -run TestProductionReceiptsRanPublishedDigests -v -count=1

receipts=7204 withImage=2279 window=2026-08-13T14:19:03Z..2026-08-29T15:54:46Z
signatureValid=7204 peerIdBinding=7204 receiptIdMatches=7204 canonicalRoundTrip=7204 rowBinding=7204
digestPinned=2279 referenceDigestAgree=2279 publishedImage=2279
os linux = 7204
peer ed25519:2175b912ea1c23b1 = 2641   peer ed25519:c1973797be207ac4 = 2996
peer ed25519:d91480838ac982c9 = 1368   peer ed25519:a2ec939a4c60e243 = 161
peer ed25519:2a6aa94bf40f1df0 = 19     peer ed25519:ac1544ece1e22594 = 19
--- PASS
```

다섯 항목이 각각 다른 것을 말한다.

* `signatureValid` — 저장된 문서가 peer의 키가 덮은 바이트 그대로다. 서명 이후 아무도
  바꾸지 않았고, 이미지 필드도 그 서명 안에 있다.
* `peerIdBinding` — `peerId == "ed25519:" + hex(sha256(peerPubkey))[:16]`. 서명한 키와
  주장하는 정체가 같은 peer다.
* `receiptIdMatches` — DB의 `receipt_id`가 저장된 문서의 content hash와 같다. 실행
  digest와 판정이 **하나의 해시된 문서 안에** 있다는 뜻이고, 두 표를 join한 것이 아니다.
* `canonicalRoundTrip` — 저장 문서를 다시 정규화해도 동일하다. 스키마 밖의 필드가
  있었다면 여기서 떨어져 나가고 content hash가 움직인다.
* `rowBinding` — store의 `peer_id`/`sample_id`/`env_hash` 컬럼이 서명된 본문과 일치한다.
  컬럼은 색인이지 두 번째 진실이 아니다.

peer 6곳, 2026-08-13부터 16일치 전체가 실패 0으로 통과한다.

## 3. 실행된 이미지 = 이 빌드가 published한 digest — 2,279 / 2,279

이번에 새로 측정할 수 있게 된 항목이다. 실행된 reference는 세 개뿐이고, 세 개 모두
`internal/sandbox/images.go` registry 항목과 digest까지 일치한다.

| reference | receipt 수 |
|---|---|
| `node:22-alpine@sha256:c610fcdf…aa32` | 1,589 |
| `golang:1.26-alpine@sha256:28d89ee9…b468` | 573 |
| `python:3.12-alpine@sha256:d09d15e6…dc31` | 117 |

**이 검사가 왜 별도인가.** 서버의 `receiptVerifierImageIsPinned`는 *모양*만 본다 —
`<alias>@sha256:<64 hex>`이고 `digest` 필드가 그 접미사와 같으면 통과다. 모양이 완벽한
`node:22-alpine@sha256:aaaa…`는 존재한 적 없는 바이트를 가리키면서도 저장된다. 이것은
버그가 아니라 의도다: 서버보다 새 registry를 도는 worker의 정직한 receipt를 거절하지
않기 위해 모르는 digest를 막지 않는다. 그래서 "published된 digest인가"는 **문 앞에서
막을 규칙이 아니라 저장된 receipt에서 측정할 성질**이고, 08-23·08-24·08-29 세 번 모두
손으로 다시 계산해야 했던 항목이다. §6이 그것을 코드로 옮겼다.

세 lane 모두 공개 페이지에서 같은 값으로 조회된다 (이번 run 실측, HTTP 200).

| sample | 페이지가 보여주는 값 |
|---|---|
| `sha256:c2352fe9…0d3a` | `golang:1.26-alpine@sha256:28d89ee9cc0f…` (전체 reference는 `title`에 보존) |
| `sha256:86a9448a…cddb` | `node:22-alpine@sha256:c610fcdfb1d5…` |
| `sha256:dd8d37bd…0d68` | `python:3.12-alpine@sha256:d09d15e60962…` |

## 4. mutable tag — receipt 0건, 그리고 docker가 실제로 받은 argv

receipt 쪽은 전수 0건이다. 전체 7,204건 중 이미지를 가진 2,279건이 **전부** digest
고정이고(`digestPinned == withImage`), reference와 digest가 어긋난 건도 0이다.

receipt는 자기 신고다. 그래서 신고가 아닌 것을 하나 더 읽었다 — **farm이 docker에게
실제로 건넨 명령줄**이다. worker가 남기는 stage log에 그대로 있다.

```
$ docker run --rm --name csx-… --network=none --memory=512m --pids-limit=256 … \
    golang:1.26-alpine@sha256:28d89ee9cc0ff9fec75c82ca201e6bf7fdf9a679d4b7b24dfa04f2bb766bb468 \
    go run ./test
```

살아 있는 로그 50개(2026-08-20 ~ 08-29) 전부를 훑은 결과다.

| stage log의 `docker run` | 건수 | 시점 |
|---|---|---|
| digest 고정 | 19 | 2026-08-21T10:32 이후 전부 |
| 태그만 (`@sha256:` 없음) | 47 | **전부 2026-08-20 ~ 08-21T09:09** |

v0.1.43이 farm에 올라온 것은 2026-08-22T16:46Z다. **그 이후 로그에 남은 `docker run`
16건은 예외 없이 digest 고정이고, 태그로 돈 47건은 전부 그 이전이다.** 08-21T10:32의
gradle 3건이 먼저 고정된 것은 Java lane이 R2C-64보다 앞서 고정된 이력과 일치한다.

여기서 확보하지 못한 것도 적는다. farm의 로컬 image store는 지금 비어 있어
(`docker images -q | wc -l` = 0) "고정 digest가 실제로 pull되어 있다"는 08-23 문서식
증거는 이번에 다시 얻을 수 없었다. stage log도 최근 50건만 남으므로 위 표는 2,279건
전부가 아니라 **살아남은 창(window)**이다.

## 5. Windows lane — production 실행 증거 없음, R2C-146으로 분리

production receipt **7,204건 전부** `environment.os = linux`이고, `os <> 'linux'`인
receipt는 0건이다. Windows verifier lane은 registry digest만 있고 production 실행
증거가 없다는 서술이 지금도 정확하다. production에 Windows worker가 생기기 전에는
어느 머신에서 돌려도 production 증거가 되지 않으므로, 이 문서는 그 항목을 주장하지
않고 [R2C-146 / #76](https://github.com/r2cuerdame/CodeSampleX/issues/76)이 계속 추적한다.

## 6. 이번에 코드로 옮긴 것

증거는 이미 얻을 수 있었다. 얻을 수 **없었던** 것은 그 증거를 다시 얻는 방법이다.
같은 질문을 세 번 답하는 동안 세 번 모두 덤프를 디코드하고 peer id를 다시 유도하고
문서를 다시 정규화하고 digest를 비교하는 일회용 스크립트를 새로 썼고, 세 번 모두
버려서 남은 것은 숫자뿐이었다. 그리고 §3의 "published된 digest인가"는 registry 조회가
`internal/sandbox` 밖으로 나와 있지 않아, 감사 코드를 그 패키지 안에 심거나 digest를
다시 타이핑해야만 물어볼 수 있었다 — 후자는 `images.go`가 존재하는 이유인 drift 그
자체다.

그래서 최소한만 옮겼다.

* `sandbox.PublishedImage` / `sandbox.PublishedReferences` — registry 조회를 export.
  admission 규칙이 아니라 조회다. 서버는 지금처럼 모양만 보고 통과시킨다.
* `verifier.AuditReceipts` / `verifier.ReadReceiptDump` — §2·3의 여덟 검사와 덤프
  포맷. 검사가 실제로 잡는지는 합성 receipt로 검증한다: 모양만 맞는 pin, mutable tag,
  서명 후 수정, 키가 유도하지 않는 peer id, 문서와 어긋난 컬럼, 이미지 없는 receipt.
* `TestProductionReceiptsRanPublishedDigests` — `CSX_RECEIPT_DUMP`를 주면 위 감사를
  덤프 전체에 돌린다. 덤프 없으면 skip한다. production 데이터는 이 저장소에 없고
  앞으로도 없지만, **검사는 있다.**
* `docs/operations.md`에 read-only 덤프 쿼리와 실행 절차, 그리고 stage log를 읽는 법.

다음 재검증은 스크립트를 다시 쓰는 일이 아니라 명령 두 줄이다.

---

## Acceptance 대비 현재 상태

| 완료 조건 | 상태 |
|---|---|
| production receipt에 immutable image digest identity가 있다 | **충족** — 2,279건, 전부 `<alias>@sha256:<64hex>` |
| 서명·바인딩·content hash가 검증된다 | **충족** — 7,204/7,204, 다섯 검사 전부 실패 0 |
| 실행된 이미지가 published/reference digest와 같다 | **충족** — 2,279/2,279가 registry 항목과 digest까지 일치 |
| mutable tag가 최종 실행 정체로 받아들여지지 않는다 | **충족** — receipt 0건, farm stage log의 v0.1.43 이후 `docker run` 16건 전부 digest 고정 |
| production 증거가 없는 platform lane은 명시적으로 분리한다 | **충족** — Windows receipt 0건, R2C-146(#76)로 분리 |

## 이 문서가 주장하지 않는 것

* Windows verifier lane이 동작한다는 것. registry digest만 있고 실행 증거는 없다.
* amd64 외 아키텍처에서 base/libc가 registry 표와 일치한다는 것. 측정하지 않았다.
* stage log 표가 2,279건 전부를 덮는다는 것. 살아 있는 로그 50건의 창이다.
* farm에 고정 digest가 pull되어 있다는 것. 이번에는 image store가 비어 있었다.
* 이번 run이 production에 무엇이든 썼다는 것. 접근은 전부 SELECT와 GET이다.
