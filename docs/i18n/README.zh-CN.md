# CodeSampleX

> **别再重复解决同一段代码。**（Stop solving the same code twice.）

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX 是一个面向编程 LLM 的**本地优先分布式推理缓存**。与其让地球上每个智能体都重新推导一遍某个公共库的用法——并且反复踩中同样的版本兼容性坑——CodeSampleX 从真实开发环境中收集匿名的兼容性**证据（Evidence）**，并提供**经过验证的最小化样例（Sample）**，附带已知可用答案与你的项目之间的精确差异（delta）。

- 官网与兼容性浏览器：**https://codesamplex.dev**
- 一个你的 LLM 不必再反复回答的问题：*`axios.post` 在 axios 1.12 + Node 22 + pnpm + Windows 11 上到底能不能用——如果不能，具体在哪个阶段出问题？*

## 安装

Windows（PowerShell）：

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux：

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

一个二进制文件，一个问题。`csx init` 会展示社区契约，并只让你做一个选择——**JOIN COMMUNITY** 或 **LOCAL ONLY**。其余一切（守护进程、为 Claude Code / Codex / Gemini CLI / OpenCode 注册 MCP、智能体规则）都是自动完成的。

## 契约

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

这不是暗中的遥测——它就是协议本身。社区节点既是消费者**也是**生产者。仅本地（local-only）模式绝不发送任何数据。`csx ui` 中的隐私预览会在数据离开你的机器之前，展示将要发送的确切载荷。

## 工作原理

```text
you build/test through csx (or your agent does)
→ local analysis: public packages, lockfile-resolved versions, symbols, environment
→ raw errors sanitized locally into fingerprints (paths/names/secrets stripped)
→ anonymous evidence batches → Compatibility Graph on codesamplex.dev
→ your LLM asks CSX first: nearest verified Sample + environment delta
→ it reasons about the DELTA, not the whole problem
```

四个层次，严格分离、各司其职：

| 层次 | 是什么 | 可信度 |
|-------|------------|-------|
| Evidence Network | 匿名的包/版本/符号/环境/阶段/结果事实 | 弱→强，按类别标注 |
| Compatibility Graph | 按环境聚合的概率化地图（含执行上下文：Node/Chrome/Safari/Electron/……） | 派生视图 |
| Sample Pool | 经用户批准、洁净室构建、内容寻址的最小化项目 | 契约验证、交叉验证 |
| Agent Delivery | MCP/CLI：最近的样例 + 差异 + 已知失败 | 分级 EXACT→NO_SAFE_MATCH |

项目能编译，绝不会被当成某个符号可用来呈现。原因不明的情况保持为 `UNKNOWN`。错误的 HIT 比 MISS 更糟——`NO_SAFE_MATCH` 是一项特性，而非缺陷。

## 智能体集成（MCP）

`csx init` 会自动注册 MCP 服务器。工具包括：`search_known_solution`、`get_sample`、`explain_compatibility`、`run_observed_command`、`report_sample_adoption`、`propose_public_sample`、`list_local_hits`、`get_local_stats`。发布样例被有意设计为**不是** MCP 能力——它需要你在完整预览之后通过 CLI 明确批准。

```bash
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

## 生态系统（Public v1）

Node/TypeScript（npm、pnpm、yarn——参考实现）、Python（pip、uv）、Go、Rust/Cargo。诚实的能力矩阵见 [docs/adapters.md](../adapters.md)——v1 中没有任何适配器声称支持运行时符号插桩，符号解析的置信度始终带有标注（`EXACT`/`PROBABLE`/`UNKNOWN`）。

## 架构

单个 Go 二进制文件（`csx`：守护进程 + CLI + MCP + 对等节点 + 验证器）加一个小型服务端（`csx-server`：PostgreSQL + 由 Caddy 反向代理的服务端渲染浏览器）。样例采用内容寻址（`sha256`），分发顺序为本地缓存优先 → 对等节点 → 主种子节点。下载的样例绝不会直接在你的主机上运行——使用 `--ignore-scripts` 解析依赖，在断网沙箱中编译并执行契约测试，回执使用 ed25519 签名。详见 [goal.md](../../goal.md)（产品规格）、[docs/execution-context.md](../execution-context.md)、[docs/operations.md](../operations.md)。

## 从源码构建

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## 许可证

代码：Apache-2.0。发布的样例默认采用 **MIT-0**。
