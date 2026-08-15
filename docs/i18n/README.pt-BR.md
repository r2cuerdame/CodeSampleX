# CodeSampleX

> **Pare de resolver o mesmo código duas vezes.** (Stop solving the same code twice.)

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

O CodeSampleX é um **cache de raciocínio distribuído local-first** para LLMs de programação. Em vez de cada agente do planeta rederivar como uma biblioteca pública funciona — e esbarrar de novo nas mesmas incompatibilidades de versão —, o CodeSampleX coleta **Evidências** anônimas de compatibilidade de ambientes de desenvolvimento reais e entrega **Samples mínimos verificados** com o delta exato entre uma resposta comprovadamente boa e o seu projeto.

- Site e Compatibility Explorer: **https://codesamplex.dev**
- Uma pergunta que o seu LLM para de responder repetidamente: *o `axios.post` realmente funciona com axios 1.12 + Node 22 + pnpm + Windows 11 — e, se não, em qual estágio ele quebra?*
- Funciona com **Claude Code, Codex, Gemini CLI, OpenCode** — e com qualquer cliente MCP (Cursor, Windsurf, Cline, Zed, VS Code).

## Instalação

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Um binário, uma pergunta. O `csx init` mostra o contrato da comunidade e pede uma única escolha — **JOIN COMMUNITY** ou **LOCAL ONLY**. Todo o resto (daemon, registro MCP para Claude Code / Codex / Gemini CLI / OpenCode, regras de agente) é automático.

## O contrato

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

Isto não é telemetria escondida — é o protocolo. Os peers da comunidade são consumidores **e** produtores. O modo local-only nunca envia nada. A prévia de privacidade no `csx ui` mostra os payloads exatos antes de eles saírem da sua máquina.

## Como funciona

```text
you build/test through csx (or your agent does)
→ local analysis: public packages, lockfile-resolved versions, symbols, environment
→ raw errors sanitized locally into fingerprints (paths/names/secrets stripped)
→ anonymous evidence batches → Compatibility Graph on codesamplex.dev
→ your LLM asks CSX first: nearest verified Sample + environment delta
→ it reasons about the DELTA, not the whole problem
```

Quatro camadas, mantidas honestamente separadas:

| Camada | O que é | Confiança |
|-------|------------|-------|
| Evidence Network | fatos anônimos de pacote/versão/símbolo/ambiente/estágio/resultado | fraca→forte, rotulada por classe |
| Compatibility Graph | mapa probabilístico agregado por ambiente (incl. contexto de execução: Node/Chrome/Safari/Electron/…) | visão derivada |
| Sample Pool | projetos mínimos aprovados pelo usuário, clean-room, endereçados por conteúdo | verificados por contrato, verificados de forma cruzada |
| Agent Delivery | MCP/CLI: sample mais próximo + delta + falhas conhecidas | graduada de EXACT a NO_SAFE_MATCH |

Um projeto que compila nunca é apresentado como um símbolo que funciona. Causas desconhecidas permanecem `UNKNOWN`. Um HIT errado é pior do que um MISS — `NO_SAFE_MATCH` é uma feature.

## Integração com agentes (MCP)

**Configurado automaticamente pelo `csx init`:** Claude Code · Codex · Gemini CLI · OpenCode.

**Qualquer outro cliente MCP** (Cursor, Windsurf, Cline, Zed, VS Code) também funciona: o `csx` é um servidor MCP stdio padrão:

```sh
csx mcp-config          # JSON: Cursor, Cline, Windsurf, Zed, VS Code
csx mcp-config --toml   # TOML: Codex
```

Nao escreva a configuracao a mao: cole o que este comando imprime. Ele coloca o **caminho absoluto** da sua instalacao, que e o que importa: um cliente MCP nao e iniciado a partir de um shell de login, ele herda o ambiente do editor. Por isso um simples `"command": "csx"` nao sobe, mesmo depois de voce corrigir o seu `PATH`.

Independente de modelo: a mesma evidência de compatibilidade serve a Claude, GPT e Codex, Gemini, Llama — qualquer modelo que consiga chamar uma ferramenta MCP.

Ferramentas: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats`. Publicar um sample deliberadamente **não** é uma capacidade do MCP — exige a sua aprovação explícita via CLI depois de uma prévia completa.

```bash
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

## Ecossistemas (Public v1)

Node/TypeScript (npm, pnpm, yarn — referência), Python (pip, uv), Go, Rust/Cargo. Matriz de capacidades honesta: [docs/adapters.md](../adapters.md) — nenhum adapter alega instrumentação de símbolos em runtime na v1, e a confiança da resolução de símbolos é sempre rotulada (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Arquitetura

Um único binário Go (`csx`: daemon + CLI + MCP + nó peer + verificador) e um servidor pequeno (`csx-server`: PostgreSQL + explorer renderizado no servidor atrás do Caddy). Os samples são endereçados por conteúdo (`sha256`) e distribuídos priorizando o cache local → peers → seeder principal. Samples baixados nunca rodam diretamente no seu host — a resolução usa `--ignore-scripts`, a compilação e a execução do contrato acontecem sem rede em um sandbox, e os recibos são assinados com ed25519. Veja [goal.md](../../goal.md) (especificação do produto), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md).

## Compilando a partir do código-fonte

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## Licença

Código: Apache-2.0. Samples publicados usam **MIT-0** por padrão.
