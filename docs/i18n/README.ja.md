# CodeSampleX

> **Tested. Not guessed.**（テスト済み。推測ではありません。）

<p align="center">
  <img src="../../internal/web/static/inspector-hero-v1.webp" alt="CodeSampleX 互換性インスペクター" width="560">
</p>

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX は、開発者向けライブラリ・ランタイム・ツールチェーンのための**オープンな互換性テストネットワーク**です。ドキュメントを要約することも、伝聞を集めることもしません。実在し、記録された環境で本物のビルドと契約テストを実行し、実際にどこで動き、どこで壊れたのか、そしてその双方をどれだけ確信しているのかを示します。

- 互換性マップ: **https://codesamplex.dev**
- 答える問い: *そこで動くのか？* — この API は、このバージョンで、この OS で、このランタイムの下で。
- 返す答え: *テストした。結果はこうだった。*

## そこで動くのか？

すべての結果は環境情報付きで記録された実行なので、データは互換性マトリクスへとピボットできます — OS × ランタイム、バージョン × アーキテクチャ、シンボル × OS。稼働中のネットワークから取った実際のスライス（`axios.post`、2026 年 8 月測定）:

```text
axios.post · axios 1.12.2                node 22            node 24
linux                                    ■ verified 4/4       —
windows                                  ○ observed 3/9 ! ?
```

この行はイラストではなく、[実際のライブページ](https://codesamplex.dev/npm/axios/1.12.2/axios.post)です。

**セルは比率を示し根拠を明示するだけで、判定は下しません。** `■ verified` は固定したコンテナで私たち自身が契約を実行したこと、`○ observed` は実際のマシンが実行を記録して報告したことを意味します。数字こそが測定値 — 実行に対する合格数 — なので、単独の `1/1` は証拠がどれほど薄いかをそのまま語ります。以前はそれが百回一致した実行と同じ印の陰に隠れていました。`PASS` は廃しました。PASS は *ここでは動く* という一般的な主張に読まれますが、実際に測ったのは *四回実行して四回合格* だからです。

根拠は決して曖昧になりません。それこそが重要な区別だからです。観測数は検証数を圧倒するため、両方を裸の比率にすると匿名のセルのほうが証明済みのセルより権威があるように見えてしまいます。グリフは私たちが実行したものは塗りつぶし、報告を受けたものは中抜きで、色がなくても見分けられます。色は比率の出方だけを担います — 失敗は稀で情報量の大きい事象であり、目に留まらなければならないからです。`!` は測定された異常、`?` は弱いか古い証拠、`—` は不明のままです。パッケージの生態系や文書からは何も推論しません。

Web エクスプローラーは、あらゆる 2 次元グリッドを N 次元キューブのスライスとして扱います。任意の 2 つの次元（OS、ランタイム、パッケージバージョン、シンボル、アーキテクチャ、パッケージマネージャー、実行コンテキスト、libc）を軸として選び、残りをフィルターとして固定し、セルをクリックすれば一段深く掘り下げられます — 実際に測定された組み合わせまで。署名済みレシートは、その組み合わせのシンボルページに保管されています。

## なぜテストが重要なのか

```text
Documentation   → what should work
Code search     → how somebody used it
Community       → what somebody says worked
CodeSampleX     → what was actually tested
```

マップを誠実に保つためのルール:

- プロジェクトがコンパイルできたことを、シンボルが動作した証拠として提示することは**決して**ありません。観測と検証は別々に数えられ、合算されることはありません。
- 原因が不明なものは `UNKNOWN` のままにします。誤った HIT は MISS より有害です — `NO_SAFE_MATCH` は立派な答えのひとつです。
- 証拠は減衰します。結果の重みは 90 日ごとに半減し、古くなったセルにはその旨が表示されます。
- 失敗の原因は確率分布として報告されます。作り話の確信を述べることはありません。

## CLI のインストール

CLI はローカルのテスターです。実際のビルドをラップしてその結果を匿名の証拠に変え、ネットワークから答えを返します。

Windows（PowerShell）:

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

この 1 行には `curl` と CA 証明書が必要ですが、最小イメージ（debian-slim、alpine、大半のエージェント用コンテナ）には入っていません — しかも `curl … | sh` は、パイプラインが最後のコマンドの終了ステータスを返すため、**curl が無くても exit 0 になります**。先に前提パッケージをインストールするか、wget を使ってください:

```bash
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

バイナリは `~/.local/bin` に置かれますが、このディレクトリは既定ではどの環境の `PATH` にも入っていません:

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # the install check — `csx --version` is not a spelling csx accepts
```

バイナリはひとつ、質問もひとつだけ。`csx init` は後述の契約を提示し、**JOIN COMMUNITY** か **LOCAL ONLY** かの選択を一度だけ尋ねます。`sh` にパイプした場合、stdin はダウンロードのパイプに使われているため、`init` は告知どおりの既定値である JOIN COMMUNITY を選びます。オプトアウトはいつでも `csx init --local-only` で可能です。どちらのモードフラグも再実行可能かつ非対話的です。スクリプトや CI でのセットアップには: `csx init --community --yes --no-agents`。

## テストと確認

```bash
csx run -- pnpm build              # wrap any build/test — its result becomes evidence
csx search "axios multipart upload"  # a verified answer, graded for YOUR environment
csx scan                           # record which public packages a project uses, no build
csx stats                          # local dashboard: hits, adoptions, queue
csx ui                             # browser dashboard + privacy preview
csx sync                           # warm the shard cache — once, right after install
```

`csx sync` は省略してよい飾りではありません。インストール直後はシャードのキャッシュがゼロなので、同期が済むまではすべての検索が `NO_SAFE_MATCH` を返します。その後はデーモンがバックグラウンドで再ウォームします。

`csx search` は、記録されたあなたの環境に対してすべての結果を格付けし（`EXACT`、`COMPATIBLE`、`ADAPTATION_REQUIRED`、`REFERENCE_ONLY`、`NO_SAFE_MATCH`）、答えが証明された場所とあなたのいる場所との正確な差分（`different`、`adaptationNeeded`）を列挙します。

## 検証済みサンプル

サンプルはスニペットではありません。**契約（contract）**を持つ、コンテンツアドレス化された最小プロジェクト（正規アーティファクトの `sha256:<hex>`）です。契約とは、固定コンテナ内でオフライン実行され、合格したアサーションのことです。クリーンルームでの作成ループは CLI 専用です:

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief, empty workspace
csx sample create <dir>      # ingest the clean-room project
csx sample verify <id>       # resolve → compile → contract, sandboxed
csx sample publish <id>      # requires typing exactly "yes"; leakage findings hard-refuse
```

公開時にはシークレット、パス、プロジェクト名、プライベート URL がスキャンされ、検出があれば公開は**ブロック**されます。回避用のフラグはありません。サンプルソースのアップロードは意図的に MCP の機能に含めていません。公開できるのは、CLI の前にいる人間だけです。

## Findings（発見事項）

どこで壊れるのか？ [Findings](https://codesamplex.dev/findings) は、実測された矛盾のリストです。ドキュメント（あるいは通念）が言っていることの隣に、契約が測定した結果が並びます — ドキュメントとの不一致、環境固有の失敗、バージョン境界。各行は、それを証明する契約を持つ公開サンプルにリンクしているため、測定を再実行して異議を唱えることもできます。

機械由来の発見事項は、作者が「正そうとした通念」を記録した公開サンプルから増えていきます。誰かがページを編集して追加するものではありません。

## 証拠と格付け

なぜセルを信頼できるのか？ すべての結果には証拠クラスが付いています（weak → strong）:

| 格付け | 実際に起きたこと |
|-------|------------------------|
| `USAGE_OBSERVATION` | 実在のプロジェクトがそのパッケージでビルド／型チェック／テストされた — 観測のみ、弱い証拠 |
| `ADOPTION_EVIDENCE` | 誰かがサンプルを適用し、その後のビルドが通ったかどうかを報告した |
| `SAMPLE_VERIFICATION` | サンプルの契約が固定コンテナ内で実行され、合格した |
| `CROSS_PASS` | 独立したピアが再実行し、再び合格した |
| `MATRIX_PASS` | 合格した実行が 2 つ以上の OS／ランタイム／ブラウザ境界にまたがる |
| `STABLE` | 3 以上の独立したピアが合格させ、30 日間失敗が記録されていない |

サンプルページには検証ラダー `L0_SOURCE_ONLY` → `L5_MATRIX_PASS` のバッジも表示され、マトリクスの各セルには信頼度（`HIGH`/`MEDIUM`/`LOW`）、失敗率上昇フラグ、最終観測日が付きます。`resolvedPackages` を主張できるのは署名済みの **v2 レシート**だけです — これは作者が書き込んだバージョンではなく、検証器が実際にインストールしたバージョンです。スナップショットは各レシートを、実際に実行されたバージョンの下に整理します。

公開カウンターはロールアップ（集計値）で、アカウントなしで JSON として取得できます:

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

| フィールド | 数えているもの |
|-------|----------------|
| `packages` / `symbols` | カバレッジ: 互換性データに含まれる公開パッケージ名と観測されたシンボル |
| `evidence` | 受理された観測レコード。ユーザー数、プロジェクト数、検証済みサンプル数ではない |
| `verifiedSamples` | サンドボックスでの契約 PASS レシートを持つ、重複のないサンプル数 |
| `peers` / `projectsMonth` | 重複のない匿名の日次／月次コントリビューターバケット |
| `postHitBuildsReported` | 実測の PASS または FAIL を含む採用報告 |

CodeSampleX は、**信頼できるユニーク／アクティブユーザー数、稼働中の MCP プロセス数、インストール成功数をまだ測定していません**。stats レスポンス内の `estimated*` フィールドはすべて明示的に数式ベースであり、実測値として読んではいけません。

## コントリビューターワーカー

このネットワークの環境とは、他の人々のマシンのことです。余っているマシンが 1 台あれば、MCP やエージェント設定には一切触れずに、Docker で隔離された検証を提供できます:

```bash
csx init --community --yes --no-agents --no-daemon
csx worker start                         # idle-aware, 2 Docker lanes
csx worker start --parallel 4 --budget 15m
```

ワーカーが受け付けるのは、サーバーが割り当てた VERIFY ジョブ（`cross` / `matrix`）だけです — キューが任意のシェルコマンドを送ることはありません。アーティファクトはコンテンツアドレス化され、ハッシュ検査されます。resolve はコンテナ内で行われ、compile と contract の各ステージは、`512m` のメモリ制限と `256` の PID 上限を固定した使い捨ての Docker ワークスペース内で、ネットワークを遮断して実行されます。Docker デーモンが無ければ即座に拒否され、ホストへのフォールバックは決してありません。結果は ed25519 署名付きの v2 レシートで、ステージの生ログはローカルに残ります。詳細は [Contribute](https://codesamplex.dev/contribute) を参照してください。

## API

Web サイトが表示しているのと同じデータを、アカウントなしで JSON として取得できます:

| エンドポイント | 提供するもの |
|----------|----------------|
| `GET /v1/stats` | ネットワークの日次ロールアップ |
| `POST /v1/search`, `POST /v2/search` | クエリ + 環境フィンガープリントに対する格付け済みの答え |
| `GET /v1/registry/packages/{purl}` | パッケージ詳細 + パッケージレベルのスナップショット |
| `GET /v1/registry/symbols/{eco}/{package}/{family}` | 1 つのシンボルのバージョン別スナップショット |
| `GET /v1/shards/{eco}/{package}/{major}` | 事前実体化された互換性シャード（ETag キャッシュ対応） |
| `GET /v1/samples/{id}`, `…/artifact` | サンプルのメタデータ、レシート、tar.gz ソース |
| `GET /v1/wanted` | 需要キュー: 求められたのに答えられなかったもの |
| `GET /v1/adapters` | エコシステム別のケイパビリティマトリクス |

## エージェントアダプター（MCP）

コーディングエージェントは、アダプターを通じて同じネットワークを利用します — MCP は CLI と API の上に載るコネクターであって、プロダクトそのものではありません:

```text
CodeSampleX
├─ CLI   ← primary local tester
├─ API   ← automation / integration
├─ Web   ← compatibility map / reports
└─ MCP   ← agent adapter
```

`csx init` は Claude Code、Codex、Gemini CLI、OpenCode を自動設定します。その他の stdio MCP クライアント（Cursor、Windsurf、Cline、Zed、VS Code）は、`csx mcp-config` が出力する設定（Codex 用は `--toml`）で動きます — この出力にはバイナリの絶対パスが含まれており、エディタから起動されるクライアントにはそれが必要です。サーバー本体は `csx mcp` です。ツールは 8 つ: `search_known_solution`、`get_sample`、`explain_compatibility`、`run_observed_command`、`report_sample_adoption`、`propose_public_sample`、`list_local_hits`、`get_local_stats` — そして公開（publish）ツールは意図的にありません。

エージェント向けのインストール手順（MCPB バンドルや、`SHA256SUMS.txt` 付きのバイナリ直接ダウンロードを含む）: [llms-install.md](../../llms-install.md)。スタンドアロンのコミュニティインストールは、Ed25519 署名付きマニフェスト経由で自動更新され、`csx update rollback` も利用できます。`local-only` インストールは更新リクエストを一切行いません。

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

これは隠れたテレメトリではありません — これがプロトコルそのものです。コミュニティのピアは消費者であると**同時に**生産者です。ローカル専用モードでは何も送信されません。エラーは、いかなる利用よりも前にローカルでサニタイズされてフィンガープリントになります。プライベートなパッケージや不明なパッケージがマシンの外へ出ることはありません。`csx ui` のプライバシープレビューでは、送信前のペイロードをそのまま確認できます。`NO_SAFE_MATCH` は、プライバシーに安全な Wanted タプルを提供します — その内容は、公開パッケージと、その正確なバージョン、そしてリクエストが曖昧さのない 1 つのパッケージを名指しした場合には、要求された公開シンボルです — ユーザーのプロンプトが送られることは決してありません。公開デプロイメントでは、**サンプルソースの投入はシーダー専用**です。検索、証拠、レシート、wanted ボードはアカウントなしで開放されています。

## 対応エコシステム（Public v1）

**スキャン + 検証** — プロジェクトの検出、ロックファイルによるパッケージ解決、サンプルのエンドツーエンド検証まで対応: Node/TypeScript（npm、pnpm、yarn — リファレンス実装）、Python（pip、uv）、Go、Rust/Cargo。Node のサンプルは自身が宣言したランタイム上で実行されるため、Bun と Deno の結果は推測ではなく実測です。

**検証のみ** — プロジェクトスキャナーはまだありませんが、公開サンプルは固定コンテナ内でビルドされ、契約テストを受けます: PHP/Composer、Ruby/Bundler、Dart/pub、Elixir/Hex。Java（Maven/Gradle）の契約検証は、JDK 8/11/17/21/25 の正確なレーンを固定します。

誠実なケイパビリティマトリクス: [docs/adapters.md](../adapters.md) — シンボル解決の信頼度には常にラベルが付きます（`EXACT`/`PROBABLE`/`UNKNOWN`）。

## アーキテクチャ

単一の Go バイナリ（`csx`: デーモン + CLI + MCP + ピアノード + 検証器）と、小さなサーバー（`csx-server`: PostgreSQL + Caddy 背後のサーバーレンダリング互換性エクスプローラー）で構成されます。サンプルはコンテンツアドレス化され、ローカルキャッシュ優先 → ピア → メインシーダーの順で配布されます。ダウンロードしたサンプルがホスト上で直接実行されることはありません。resolve は固定サンドボックス内で行われ、エコシステムが対応していればインストールスクリプトは無効化されます。resolve の後にアーティファクトは再ハッシュされ、compile と contract の各ステージはネットワークを遮断して実行されます。詳細は [goal.md](../../goal.md)、[docs/execution-context.md](../execution-context.md)、[docs/operations.md](../operations.md) を参照してください。

## ソースからのビルド

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## ライセンス

コード: Apache-2.0。公開サンプルのデフォルトは **MIT-0** です。
