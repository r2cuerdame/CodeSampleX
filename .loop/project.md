---
stage: 안정화/PLC
platform: cli
productStage: released
platforms: []
version: 0.2.23
deployWorkflow: not_required
smokeUrl: ""
versionNeedle: CodeSampleX
---

# CodeSampleX PD

공개 라이브러리 사용법을 실제 계약 실행 근거로 제공하는 프로젝트다. 한 프로젝트에 PD 하나를 두며 기존 LoopOffice 자원 관리자의 슬롯·사용량 창·쿨다운을 지킨다. 공급자/모델을 고정하지 않고 자원이 없으면 같은 작업을 대기·재개한다. Owner AI나 별도 오케스트레이터를 만들지 않는다.

canonical checkout은 `C:\_Project\CodeSampleX`다. 새 worktree/clone 없이 dirty와 branch/HEAD drift를 보존한다. 읽기 전용 PLC 조사는 소스·테스트·GitHub 근거만 읽으며 빌드·설치·포맷·캐시 생성 명령을 실행하지 않는다. 적격 일감이 없으면 다음 마일스톤·버전·안정화·아이디어를 계속 조사한다. PLC가 없으면 singleton 한 장 계획을 제안하고 정본 `.loop/plc.md`에 기록한다.

버그·안정성·기존 마일스톤·유지보수·오너 요청은 계획 감사와 수용 기준을 갖춘 이슈→구현→테스트→PR→독립 리뷰→머지로 진행한다. 새 기능은 제안에 근거·수용 기준·`needsApproval`을 명시한다. 작은 PLC/UI 개선 등 비용이나 중대한 방향 변경이 없는 작업은 별도 오너 승인 없이 실행한다. 새 비용 또는 중대한 제품 방향 변경은 `needsApproval: true`와 `reason: cost` 또는 `reason: direction`으로 승인을 기다린다. 과거 Growth KPI/진단 기록은 그대로 보존하고 자동 실행 일감이나 과거 오케스트레이터 정책으로 사용하지 않는다.

## 구현 Worker 검증

Windows Go 1.26.5에서 CSX 관찰 경로로 `go build ./...`, `go test ./...`를 실행한다. 실제 PR merge gate는 CI의 `Test`다. `Windows`는 push/수동 CI용이며 PR 필수 검사가 아니다. PostgreSQL 통합 검사는 격리된 `CSX_TEST_DSN`이 필요하다. CI는 PostgreSQL 17·Chrome과 skip 검사로 통합 검증한다. 로컬 환경 의존 skip을 통합 통과로 보고하지 않으며 테스트 수를 줄이거나 skip을 늘리지 않는다. 운영 DSN을 사용하지 않는다.

## 배포·비용 경계

`deployWorkflow: not_required`이며 CI는 배포가 아니다. PD/Worker는 `production-deploy.yml`, `release.yml`, AWS 또는 유료 인프라 워크플로를 실행하지 않는다. production-deploy의 workflow_dispatch와 사람의 운영 게이트는 오너 영역이다. 태그·공개 릴리스·운영 배포도 이 자율 운영에 포함하지 않는다.

새 유료 클라우드 자원 생성, AWS 증설, 유료 API 지출은 반드시 `needsApproval: true`, `reason: cost` 제안으로 오너 승인을 기다린다. 기존 구독 AI 사용만 자원 관리자 한도 안에서 허용한다. 기존 SLO 이슈는 로컬 원인 분석·검증을 우선하며 새 유료 인프라를 해결책으로 만들지 않는다. CodeSampleX-Farm 또는 DevHotel 노드를 추가하지 않는다.
