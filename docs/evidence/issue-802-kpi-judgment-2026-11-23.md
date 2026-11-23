# #802 KPI 판정 기록: 7일 활성 설치 10 — 안 됨

- 판정: **안 됨** (목표 10, 값 4, 격차 -6)
- 확정 시각: **2026-11-23 16:36 KST**. 의종 결정(춈스케 전달)으로 예정 판정 시각(2026-11-24 09:00 KST)보다 먼저 확정했다.
- 판정 대상 결정: `.loop/guidelines.md` 2026-11-16 19:37 KST 행 (7일 활성 설치 3 → 10, 판정 2026-11-24 09:00 KST, 10 이상이면 달성)
- 업데이트 빈도: '가끔' 그대로 둔다.
- 새 KPI: 정하지 않았다. PD 제안 #825 (목표 4 → 6, 판정 2026-12-03 09:00 KST)는 의종 확인 전에는 적용하지 않는다.

## 판정 값

`https://codesamplex.dev/v1/stats` (2026-11-23 16:38 KST에 읽음)

| 필드 | 값 |
| --- | --- |
| `activeInstallations7d` | 4 |
| `activeInstallations1d` | 4 |
| `activeInstallations30d` | 4 |
| `day` | 2026-11-21 |
| `generatedAt` | 2026-11-21T22:31:54Z |

LoopOffice 코어 측정(`status-export.json` 의 codesamplex `kpi`, `measuredAt` 2026-11-23T07:04:22Z): `current` 4, `history` 11-16 4 · 11-18 4 · 11-21 4.

## 추세

| 구간 | 값 | 출처 |
| --- | --- | --- |
| 7일 (11-16 → 11-21) | 4 → 4 → 4, 변화 없음 | 코어 `kpi.history` |
| 판정 기준값 (11-16 22:30 KST) | 7일 3 · 1일 2 · 30일 4 | #802 본문 |
| 30일 이상 | 10-09 4 · 10-16 3 · 10-20 4 · 11-16 3~4 · 11-21 4 | `docs/distribution.md` (#565·#586), `docs/evidence/583-growth-investigation.md`, 코어 |

6주 넘게 3~4 사이에서 움직이지 않았다.

보조 지표 Pulse production DAU (`pp_codesamplex_f2f2ab10`, CLI·MCP·웹 합):

| 기간 (UTC) | 일별 | 평균 |
| --- | --- | --- |
| 11-16 ~ 11-22 | 7, 7, 7, 7, 6, 8, 6 | 6.86 |
| 11-10 ~ 11-15 | 5, 6, 3, 6, 8, 6 | 5.67 |

## 값을 읽을 때 주의

- `activeInstallations7d` 는 `ordinary`·`external` 클라이언트가 보낸 익명 presence 토큰 수다. `farm`·`ci` 등 내부 클래스는 빠진다. 사람 수도, 성공한 설치 수도 아니다.
- 7일 창은 미끄러지는 창이 아니라 UTC 정렬 구간(Unix-day ÷ 7)이다. 경계는 목요일 00:00 UTC(= 09:00 KST)다. 2026-11-24 09:00 KST 는 2026-11-19~11-25 UTC 구간의 6일째라 그 시각 값은 5일치였다.
- 설치는 community 모드일 때만 하루 한 번 보고한다.
- 루프를 돌리는 집 PC 설치도 클래스 지정이 없어 `ordinary` 로 들어간다. 그래서 외부 사용자 설치는 4보다 적을 수 있다.
- 공개 통계는 2026-11-21T22:31:54Z 이후 새로 생성되지 않았다(CodeSampleX-Farm KPI '공개 통계 24시간 내 갱신' 0). 위 값은 11-21 집계다.

## 이번 주기(11/16~11/23)에 한 일

- README 첫 화면 정리 (#801)
- MCP 목록: 공식 MCP 레지스트리·Glama 는 이미 등록, mcp.so·mcp.directory 제출 (#802 댓글)
- 멈춤(의종 확인): Glama 소유권 등록, awesome-mcp-servers 기존 PR, Smithery·MCP Market 로그인 등

목록 제출 뒤 7일 동안 값은 그대로였다.
