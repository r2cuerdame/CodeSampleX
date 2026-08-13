# CodeSampleX

> **同じコードを二度解くのは、もうやめよう。（Stop solving the same code twice.）**

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX は、コーディング LLM のための**ローカルファーストな分散推論キャッシュ**です。世界中のあらゆるエージェントが公開ライブラリの動作を毎回イチから推論し直し、同じバージョン非互換に繰り返しぶつかる——そんな無駄をなくすために、CodeSampleX は実際の開発環境から匿名の互換性 **Evidence** を収集し、既知の正解とあなたのプロジェクトとの正確な差分（デルタ）を添えた**検証済みの最小 Sample** を提供します。

- Web サイト & Compatibility Explorer: **https://codesamplex.dev**
- あなたの LLM が二度と答え直さなくてよくなる問いの一例: *`axios.post` は axios 1.12 + Node 22 + pnpm + Windows 11 で本当に動くのか——動かないなら、どの段階で壊れるのか？*

## インストール

Windows（PowerShell）:

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

バイナリはひとつ、質問もひとつだけ。`csx init` はコミュニティ契約を提示し、**JOIN COMMUNITY** か **LOCAL ONLY** かの選択を一度だけ尋ねます。それ以外（デーモン、Claude Code / Codex / Gemini CLI / OpenCode への MCP 登録、エージェントルール）はすべて自動です。

## 契約（The contract）

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

これは隠れたテレメトリではありません——これがプロトコルそのものです。コミュニティのピアは消費者であると**同時に**生産者です。ローカル専用モードでは何も送信されません。`csx ui` のプライバシープレビューでは、マシンの外に出る前のペイロードをそのまま確認できます。

## 仕組み

```text
you build/test through csx (or your agent does)
→ local analysis: public packages, lockfile-resolved versions, symbols, environment
→ raw errors sanitized locally into fingerprints (paths/names/secrets stripped)
→ anonymous evidence batches → Compatibility Graph on codesamplex.dev
→ your LLM asks CSX first: nearest verified Sample + environment delta
→ it reasons about the DELTA, not the whole problem
```

4 つのレイヤーを、誠実に分離したまま保ちます:

| レイヤー | 中身 | 信頼度 |
|-------|------------|-------|
| Evidence Network | パッケージ／バージョン／シンボル／環境／ステージ／結果に関する匿名の事実 | weak→strong、クラスラベル付き |
| Compatibility Graph | 環境ごとに集約された確率的マップ（実行コンテキストを含む: Node/Chrome/Safari/Electron/…） | 派生ビュー |
| Sample Pool | ユーザー承認済み・クリーンルーム・コンテンツアドレス化された最小プロジェクト | 契約検証済み、相互検証済み |
| Agent Delivery | MCP/CLI: 最近傍サンプル + デルタ + 既知の失敗 | EXACT→NO_SAFE_MATCH で段階評価 |

プロジェクトがコンパイルできたことを、シンボルが動作した証拠として提示することは決してありません。原因が不明なものは `UNKNOWN` のままにします。誤った HIT は MISS より有害です——`NO_SAFE_MATCH` は仕様であり、機能です。

## エージェント統合（MCP）

`csx init` が MCP サーバーを自動登録します。ツール: `search_known_solution`、`get_sample`、`explain_compatibility`、`run_observed_command`、`report_sample_adoption`、`propose_public_sample`、`list_local_hits`、`get_local_stats`。サンプルの公開は意図的に MCP の機能から**外して**あります——完全なプレビューの後、CLI であなたが明示的に承認する必要があります。

```bash
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

## 対応エコシステム（Public v1）

Node/TypeScript（npm、pnpm、yarn — リファレンス実装）、Python（pip、uv）、Go、Rust/Cargo。誠実なケイパビリティマトリクスはこちら: [docs/adapters.md](../adapters.md) — v1 ではどのアダプターもランタイムのシンボル計装を謳わず、シンボル解決の信頼度には常にラベルが付きます（`EXACT`/`PROBABLE`/`UNKNOWN`）。

## アーキテクチャ

単一の Go バイナリ（`csx`: デーモン + CLI + MCP + ピアノード + 検証器）と、小さなサーバー（`csx-server`: PostgreSQL + Caddy 背後のサーバーレンダリング Explorer）で構成されます。サンプルはコンテンツアドレス化（`sha256`）され、ローカルキャッシュ優先 → ピア → メインシーダーの順で配布されます。ダウンロードしたサンプルがホスト上で直接実行されることはありません——`--ignore-scripts` で解決し、コンパイルと契約実行はネットワーク遮断のサンドボックス内で行われ、レシートは ed25519 で署名されます。詳細は [goal.md](../../goal.md)（プロダクト仕様）、[docs/execution-context.md](../execution-context.md)、[docs/operations.md](../operations.md) を参照してください。

## ソースからのビルド

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## ライセンス

コード: Apache-2.0。公開サンプルのデフォルトは **MIT-0** です。
