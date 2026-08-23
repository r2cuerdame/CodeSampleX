# CodeSampleX

[![Release](https://img.shields.io/github/v/release/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/r2cuerdame/CodeSampleX/total)](https://github.com/r2cuerdame/CodeSampleX/releases)
[![License](https://img.shields.io/github/license/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/LICENSE)
[![Release pipeline](https://img.shields.io/github/actions/workflow/status/r2cuerdame/CodeSampleX/release.yml?label=release)](https://github.com/r2cuerdame/CodeSampleX/actions/workflows/release.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/go.mod)

> **Tested. Not guessed.**（实测为证，而非猜测。）

<p align="center">
  <img src="../../internal/web/static/inspector-hero-v1.webp" alt="CodeSampleX 兼容性检查器" width="560">
</p>

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX 是一个面向开发者库、运行时与工具链的**开放兼容性测试网络**。它不做文档摘要，也不收集道听途说：它在真实且被完整记录的环境中运行真实的构建与契约测试——然后告诉你哪些东西真的能用、哪些坏了，以及对这两者各有多大把握。

- 兼容性地图：**https://codesamplex.dev**
- 它回答的问题：*在那个环境里能跑吗？*——这个 API，在这个版本、这个操作系统、这个运行时之上。
- 它给出的答案：*我们实际测过了，结果就是这样。*

## 在那个环境里能跑吗？

每一条结果都是一次附带完整环境记录的真实执行，因此这些数据可以透视成兼容性矩阵——OS × 运行时、版本 × 架构、符号 × OS。下面是 2026-08-23 从线上网络原样抄下的切片：

```text
                                         v5.10.0     v5.9.2    v5.7.3
github.com/jackc/pgx/v5                  ◆ 82% 1209  ◆ 100% 2  ◆ —
Batch                                    ◆ 80% 689   —         —
ParseConfig                              ◆ 82% 1188  —         —
```

这个网格不是示意图，而是[真实的线上页面](https://codesamplex.dev/golang/github.com%2Fjackc%2Fpgx%2Fv5)——所以上面的数字在抄下来之后已经变了。

**单元格只带比率和标记，从不给出裁决。** 百分比和旁边的数字是观测：已记录的 1,209 条观测中有 82% 通过。一条观测是一次构建到达的一个阶段（编译、类型检查、测试），因此一次构建会留下多条；它既不是构建数，也不是机器数或人数。`◆` 标记只说一件事：本网络**在这个环境里**跑了自己的契约，并且干净地结束。这个版本和 API 有没有代码，是另一个标记（文档图标）——样例不会因为你切换操作系统筛选就消失。它刻意不是勾号——勾号是批准印章，而本网络不作评级。我们的运行若失败，标记会变成 `✕`。`◆ —` 表示：我们的契约在这里跑过并干净地结束，而这个坐标上还没有任何构建被报告。`—` 保持未知。

## 为什么实测很重要

```text
Documentation   → what should work
Code search     → how somebody used it
Community       → what somebody says worked
CodeSampleX     → what was actually tested
```

让这张地图保持诚实的规则：

- 项目能编译，**绝不会**被呈现为某个符号可用。观察（observation）与验证（verification）分开计数，从不相加。
- 原因不明就保持 `UNKNOWN`。错误的 HIT 比 MISS 更糟——`NO_SAFE_MATCH` 本身就是一个真正的答案。
- 证据不会衰减，也没有单元格会被标记为过期。一条观测是一个固定的发行版、一个固定的环境分桶、一个阶段，三者都不会移动：构建在那里失败过，一年之后同样为真。会变的是环境，而不同的环境就是不同的单元格。
- 失败原因以概率分布的形式报告，绝不编造确定性。

## 安装 CLI

CLI 就是本地测试器：它包裹你的真实构建，把构建结果转化为匿名证据，并用网络中的数据回答你的问题。

Windows（PowerShell）：

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux：

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

这行命令需要 `curl` 和 CA 证书，而极简镜像（debian-slim、alpine、多数智能体容器）默认并没有它们——并且当 curl 缺失时，`curl … | sh` **会以退出码 0 结束**，因为管道汇报的是最后一个命令的状态。请先安装先决依赖，或改用 wget：

```bash
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

二进制文件会安装到 `~/.local/bin`，而这个目录默认不在任何人的 `PATH` 里：

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # the install check — `csx --version` is not a spelling csx accepts
```

一个二进制文件，一个问题。`csx init` 会展示下文的契约，并只让你做一个选择——**JOIN COMMUNITY** 或 **LOCAL ONLY**。若通过管道交给 `sh` 执行，stdin 已被下载管道占用，此时 `init` 会采用其声明的默认值：JOIN COMMUNITY。任何时候都可以用 `csx init --local-only` 退出；两个模式标志都可重复运行且无需交互。脚本化或 CI 环境可用：`csx init --community --yes --no-agents`。

## 测试与查询

```bash
csx run -- pnpm build              # wrap any build/test — its result becomes evidence
csx search "axios multipart upload"  # a verified answer, graded for YOUR environment
csx scan                           # record which public packages a project uses, no build
csx stats                          # local dashboard: hits, adoptions, queue
csx ui                             # browser dashboard + privacy preview
csx sync                           # warm the shard cache — once, right after install
```

`csx sync` 不是可有可无的点缀：全新安装的本地没有缓存任何分片（shard），在同步之前每次搜索都会返回 `NO_SAFE_MATCH`。之后守护进程会在后台持续预热。

`csx search` 会依据你被记录的环境为每条结果分级——`EXACT`、`COMPATIBLE`、`ADAPTATION_REQUIRED`、`REFERENCE_ONLY` 或 `NO_SAFE_MATCH`——并列出答案被证明之处与你所在环境之间的确切差异（`different`、`adaptationNeeded`）。

## 经过验证的样例

样例（sample）不是代码片段。它是一个最小化、内容寻址的项目（以其规范化构件的 `sha256:<hex>` 标识），并带有一份**契约**：一组在固定容器中离线执行并通过的断言。固定的是镜像 digest，而不是标签——标签只是给人读的别名，真正运行的是 digest——而且签名回执会记录运行时的确切镜像引用，因此任何人都可以重新运行同样的字节，而不必只凭一句话相信（[docs/adapters.md](../adapters.md#verifier-images)）。洁净室创作流程只能通过 CLI 完成：

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief, empty workspace
csx sample create <dir>      # ingest the clean-room project
csx sample verify <id>       # resolve → compile → contract, sandboxed
csx sample publish <id>      # requires typing exactly "yes"; leakage findings hard-refuse
```

发布前会扫描密钥、路径、项目名和私有 URL——一旦发现问题就会**阻断**发布，且没有任何可绕过的标志。上传样例源码被有意排除在 MCP 能力之外；只有在 CLI 前的人才能发布。

## 发现（Findings）

到底在哪里出问题？[Findings](https://codesamplex.dev/findings) 是一份经过实测的矛盾清单：文档（或普遍认知）怎么说，契约实测的结果又是什么——文档与现实不符、特定环境下的失败、版本边界。每一条都链接到以其契约证明该结论的已发布样例，你可以重新运行这次测量，然后提出异议。

机器推导的发现，源自作者在发布样例时记录下的“它所纠正的既有认知”；没有人通过编辑页面来添加它们。

## 证据与分级

凭什么相信一个单元格？每条结果都带有它的证据等级，从弱到强：

| 等级 | 实际发生了什么 |
|-------|------------------------|
| `USAGE_OBSERVATION` | 一个真实项目使用该包完成了构建/类型检查/测试——仅为观察，弱证据 |
| `ADOPTION_EVIDENCE` | 有人采用了某个样例，并报告了之后的构建是否通过 |
| `SAMPLE_VERIFICATION` | 样例的契约在固定容器中执行并通过 |
| `CROSS_PASS` | 一个不同于发布方的对等端密钥重新运行并再次通过 |
| `MATRIX_PASS` | 通过的回执跨越了 ≥2 个操作系统/运行时主版本/浏览器族边界 |
| `STABLE` | ≥3 个不同的对等端密钥通过，且 30 天内没有失败记录 |

对等端是一把密钥，不是一个人，也不是一台机器。对等端 id 是自行生成、无需注册的 ed25519 密钥的哈希，所以一位运维者运行多少 worker 就可以持有多少把。“不同的对等端密钥”意味着同一个坐标被从不止一个地方报告过；它绝不是人数，这里也没有任何东西能识别是谁运行的。

样例页面还会标注验证阶梯 `L0_SOURCE_ONLY` → `L5_MATRIX_PASS`；矩阵单元格带有置信度（`HIGH`/`MEDIUM`/`LOW`）、失败率偏高标记以及最近一次观测日期。只有经过签名的 **v2 回执**才可以声明 `resolvedPackages`——即验证器实际安装的版本，而不是作者手写的版本；快照会把每份回执归档到真正运行过的那个版本之下。

公开计数器是一份汇总数据，无需账号即可以 JSON 形式获取：

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

| 字段 | 统计的是什么 |
|-------|----------------|
| `packages` / `symbols` | 覆盖范围：兼容性数据中的公共包名与被观测到的符号 |
| `evidence` | 已接受的观察记录；不是用户数、项目数或已验证样例数 |
| `verifiedSamples` | 拥有沙箱契约 PASS 回执的不同样例数 |
| `peers` / `projectsMonth` | 按日/按月去重的匿名贡献者分桶数 |
| `postHitBuildsReported` | 包含实测 PASS 或 FAIL 的采用报告数 |

CodeSampleX **目前还无法可靠地统计独立/活跃用户数、存活的 MCP 进程数或成功安装数**。stats 响应中的任何 `estimated*` 字段都明确是按公式推算的，绝不能当作实测计数来解读。

## 贡献者工作节点

这个网络的环境就是其他人的机器。一台空闲的机器无需触碰任何 MCP 或智能体配置，即可贡献以 Docker 隔离的验证算力：

```bash
csx init --community --yes --no-agents --no-daemon
csx worker start                         # idle-aware, 2 Docker lanes
csx worker start --parallel 4 --budget 15m
```

工作节点只接受由服务器指派的 VERIFY 任务（`cross` / `matrix`）——队列绝不会下发任意 shell 命令。构件经过内容寻址并做哈希校验；依赖解析（resolve）在容器中进行；编译与契约阶段在一次性 Docker 工作区中断网运行，并施加固定的 `512m` 内存 / `256` PID 上限；Docker 守护进程缺失时会被硬性拒绝，绝不回退到宿主机执行。结果是 ed25519 签名的 v2 回执；各阶段的原始日志留在本地。

## API

网站所呈现的同一份数据，以 JSON 提供，无需账号：

| 端点 | 提供什么 |
|----------|----------------|
| `GET /v1/stats` | 每日网络汇总 |
| `POST /v1/search`, `POST /v2/search` | 针对查询 + 环境指纹的分级答案 |
| `GET /v1/registry/packages/{purl}` | 包详情 + 包级快照 |
| `GET /v1/registry/symbols/{eco}/{package}/{family}` | 单个符号的逐版本快照 |
| `GET /v1/shards/{eco}/{package}/{major}` | 预先物化的兼容性分片（ETag 缓存） |
| `GET /v1/samples/{id}`, `…/artifact` | 样例元数据、回执及 tar.gz 源码包 |
| `GET /v1/wanted` | 需求队列：被问到但尚未得到解答的问题 |
| `GET /v1/adapters` | 各生态系统的能力矩阵 |

## 智能体适配器（MCP）

编码智能体通过一个适配器消费同一个网络——MCP 是架在 CLI 和 API 之上的连接器，而不是产品本身：

```text
CodeSampleX
├─ CLI   ← primary local tester
├─ API   ← automation / integration
├─ Web   ← compatibility map / reports
└─ MCP   ← agent adapter
```

`csx init` 会自动配置 Claude Code、Codex、Gemini CLI 和 OpenCode。其他任何 stdio MCP 客户端（Cursor、Windsurf、Cline、Zed、VS Code）只需使用 `csx mcp-config` 打印的配置即可接入（Codex 用 `--toml`）——它输出的是二进制文件的绝对路径，而这正是由编辑器启动的客户端所需要的。服务器本身就是 `csx mcp`。共八个工具：`search_known_solution`、`get_sample`、`explain_compatibility`、`run_observed_command`、`report_sample_adoption`、`propose_public_sample`、`list_local_hits`、`get_local_stats`——并且有意不提供发布工具。

面向智能体的安装步骤（包括 MCPB 捆绑包，以及附带 `SHA256SUMS.txt` 的二进制直接下载）：[llms-install.md](../../llms-install.md)。独立的社区安装通过 Ed25519 签名的清单自动更新，并可用 `csx update rollback` 回滚；`local-only` 安装不会发出任何更新请求。

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

这不是暗中的遥测——它就是协议本身。社区对等节点既是消费者，**也是**生产者。仅本地（local-only）模式绝不发送任何数据。错误在被使用之前就已在本地脱敏为指纹；私有包和未知包绝不离开你的机器；`csx ui` 中的隐私预览会在数据离开之前展示确切的载荷。一次 `NO_SAFE_MATCH` 会贡献一条隐私安全的 Wanted 元组——内容为该公共包、其确切版本，以及在请求明确指向唯一一个包时所请求的公共符号——绝不包含用户的提示词。公开部署中**只有种子节点可以上传样例源码**；搜索、证据、回执和 wanted 看板无需账号即可访问。

## 隐私政策

上面的契约是代码实际在做的事。[PRIVACY.md](../../PRIVACY.md) 把同一件事逐字段写成一份政策，并点名每一条边界由哪个文件强制执行：社区模式上传的确切文档、属于下载而非上传的请求、服务器存什么以及存多久，还有 `local-only` 说“什么都不发送”时到底是什么意思。它在本仓库中版本化管理，而不是放在一个可以无痕修改的页面上；它也就是 MCPB 包的 `privacy_policies` 数组所指向的 URL。

## 生态系统（Public v1）

**扫描 + 验证**——可检测项目、按锁文件解析包版本、端到端验证样例：Node/TypeScript（npm、pnpm、yarn——参考实现）、Python（pip、uv）、Go、Rust/Cargo。Node 样例在其声明的运行时上运行，因此 Bun 和 Deno 的结果是实测而非假设。

**仅验证**——尚无项目扫描器，但已发布的样例会在固定容器中完成构建和契约测试：PHP/Composer、Ruby/Bundler、Dart/pub、Elixir/Hex。Java（Maven/Gradle）的契约验证固定使用精确的 JDK 8/11/17/21/25 通道。

诚实的能力矩阵：[docs/adapters.md](../adapters.md)——符号解析的置信度始终带有标注（`EXACT`/`PROBABLE`/`UNKNOWN`）。

## 架构

单个 Go 二进制文件（`csx`：守护进程 + CLI + MCP + 对等节点 + 验证器）加一个小型服务端（`csx-server`：PostgreSQL + 由 Caddy 反向代理的服务端渲染兼容性浏览器）。样例采用内容寻址，分发顺序为本地缓存优先 → 对等节点 → 主种子节点。下载的样例绝不会直接在宿主机上运行：依赖解析在固定沙箱中进行，并在生态支持时禁用安装脚本；解析完成后会对构件重新哈希；编译与契约阶段断网运行。详见 [goal.md](../../goal.md)、[docs/execution-context.md](../execution-context.md)、[docs/operations.md](../operations.md)。

## 从源码构建

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## 许可证

代码：Apache-2.0。发布的样例默认采用 **MIT-0**。
