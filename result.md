# Issue #544 Result: MCP Registry metadata v0.2.5 등록 1회 실행 및 공개 관측 (#541 남은 완료조건)

- Canonical issue: https://github.com/r2cuerdame/CodeSampleX/issues/544
- Related issue: https://github.com/r2cuerdame/CodeSampleX/issues/541 (PR #542)
- Branch: `issue/544`
- Target tag: `v0.2.5`
- Target server: `io.github.r2cuerdame/codesamplex`

## Result Summary

`.github/workflows/registry-metadata.yml` (workflow id 377902307)을 `tag=v0.2.5`로 정확히 1회 dispatch하여 MCP Registry에 `io.github.r2cuerdame/codesamplex` v0.2.5 등록을 성공적으로 완료하였습니다. 공개 GET을 통해 등록 상태(`active`, `isLatest: true`)를 즉시 관측하였습니다.

코드 및 workflow 수정은 0건이며, 기존 GitHub token과 GitHub OIDC(`id-token: write`) 경로만 사용하였습니다.

## Execution & Observation Details

### 1. 사전 공개 GET 관측 (Step 1)
- 요청: `GET https://registry.modelcontextprotocol.io/v0/servers/io.github.r2cuerdame%2Fcodesamplex/versions/0.2.5`
- 시각: `2026-10-07T23:25:49 GMT`
- 응답: `404 Not Found` (`{"title":"Not Found","status":404,"detail":"Server not found"}`)
- 최신 등록 버전 확인: `GET .../versions/latest` → `0.2.4` (publishedAt: `2026-10-05T16:22:10.05403Z`)
- 판정: v0.2.5 미등록 상태 확인 후 dispatch 진행.

### 2. 기존 인증 경로 확인 (Step 2)
- `github.token` 및 GitHub OIDC (`id-token: write`) 사용 확인
- `mcp-publisher login github-oidc`를 통한 OIDC JWT 인증 수행
- 신규 secret, 신규 권한, 신규 비용: 0건

### 3. Registry metadata 단일 dispatch (Step 3)
- Workflow: `.github/workflows/registry-metadata.yml` (Run ID: [37702266227](https://github.com/r2cuerdame/CodeSampleX/actions/runs/37702266227))
- Event: `workflow_dispatch` (ref: `main`, input: `tag=v0.2.5`)
- 실행 시각: `2026-10-07T23:26:04Z` ~ `2026-10-07T23:26:27Z`
- Run 결론: `success`
- 세부 단계:
  - `Require an existing version tag`: PASS
  - `Require a public GitHub release`: PASS
  - `actions/checkout@v5`: PASS (`refs/tags/v0.2.5`)
  - `Fill the tagged server.json from the published checksum metadata`: PASS
  - `Install mcp-publisher`: PASS (v1.8.1, sha256 checksum verified)
  - `Publish to the MCP Registry`: PASS (`✓ Successfully logged in`, `✓ Successfully published`, `✓ Server io.github.r2cuerdame/codesamplex version 0.2.5`)

### 4. 사후 공개 GET 관측 (Step 4, 관측 1회차 성공)
- 요청 1: `GET https://registry.modelcontextprotocol.io/v0/servers/io.github.r2cuerdame%2Fcodesamplex/versions/0.2.5`
  - 시각: `2026-10-07T23:26:40 GMT`
  - 응답: `200 OK`
  - 버전: `0.2.5`
  - 메타데이터: `status: active`, `isLatest: true`, `publishedAt: 2026-10-07T23:26:26.536215Z`
  - 패키지 MCPB: `https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.2.5/codesamplex-mcp.mcpb` (sha256: `c11a20c543a2a551495fc47ddb4656a079598c634808f8eddfbe994a9d11d887`)
- 요청 2: `GET https://registry.modelcontextprotocol.io/v0/servers/io.github.r2cuerdame%2Fcodesamplex/versions/latest`
  - 시각: `2026-10-07T23:26:44 GMT`
  - 응답: `200 OK`
  - 버전: `0.2.5`

## 검증 방법 (재실행 가능)

```sh
curl -s -i https://registry.modelcontextprotocol.io/v0/servers/io.github.r2cuerdame%2Fcodesamplex/versions/0.2.5
curl -s -i https://registry.modelcontextprotocol.io/v0/servers/io.github.r2cuerdame%2Fcodesamplex/versions/latest
gh run view 37702266227 --json status,conclusion
```

No code or workflow changed in this issue: the operation was executed via workflow dispatch 37702266227; this file is the delivery evidence.
