# CodeSampleX

> **Tested. Not guessed.** (Testado. Não adivinhado.)

<p align="center">
  <img src="../../internal/web/static/inspector-hero-v1.webp" alt="Inspetor de compatibilidade CodeSampleX" width="560">
</p>

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

O CodeSampleX é uma **rede aberta de testes de compatibilidade** para bibliotecas, runtimes e toolchains de desenvolvimento. Ele não resume documentação nem coleciona relatos: ele executa builds reais e testes de contrato em ambientes reais e registrados — e então mostra onde as coisas de fato funcionaram, onde quebraram e quão seguro ele está de ambas as coisas.

- Mapa de compatibilidade: **https://codesamplex.dev**
- A pergunta que ele responde: *isso roda lá?* — esta API, nesta versão, neste sistema operacional, sob este runtime.
- A resposta que ele dá: *nós testamos; foi isto que aconteceu.*

## Isso roda lá?

Cada resultado é uma execução registrada com o seu ambiente anexado, então os dados se articulam em matrizes de compatibilidade — OS × runtime, versão × arquitetura, símbolo × OS. Um recorte real, vindo da rede ao vivo (`axios.post`, medido em agosto de 2026):

```text
axios.post · axios 1.12.2                node 22            node 24
linux                                    ■ verified 4/4       —
windows                                  ○ observed 3/9 ! ?
```

Aquela linha não é uma ilustração — é [a página ao vivo](https://codesamplex.dev/npm/axios/1.12.2/axios.post).

**Uma célula indica uma taxa e nomeia sua base. Nunca um veredito.** `■ verified` significa que nós mesmos executamos um contrato em um contêiner fixado; `○ observed` significa que máquinas reais registraram execuções e as relataram. O número é a medição — acertos por execução — de modo que um `1/1` solitário diz o quão fina é a evidência, em vez de se esconder atrás de uma marca idêntica à de cem execuções concordantes. Não há mais `PASS`: PASS era lido como a afirmação geral *isto funciona aqui*, quando o que se mediu é *quatro execuções, quatro acertos*.

A base nunca se embaça, porque é a distinção que importa. As contagens de observação superam em muito as de verificação, então duas taxas nuas fariam uma célula anônima parecer mais autorizada que uma comprovada. O glifo é sólido para uma execução nossa e vazado para um relato recebido, distinguíveis sem cor; a cor carrega apenas como a taxa saiu, porque uma falha é o evento raro e de alta informação e precisa chamar a atenção. `!` marca uma anomalia medida, `?` evidência fraca ou antiga, `—` permanece desconhecido. Nada é inferido do ecossistema do pacote nem de sua documentação.

O explorador web trata cada grade 2D como uma fatia de um cubo N-dimensional: escolha duas dimensões quaisquer como eixos (OS, runtime, versão do pacote, símbolo, arquitetura, gerenciador de pacotes, contexto de execução, libc), fixe as demais como filtros e clique em uma célula para descer um nível a mais — até as combinações exatas que foram medidas, cujas páginas de símbolo guardam os recibos assinados.

## Por que testar importa

```text
Documentation   → what should work
Code search     → how somebody used it
Community       → what somebody says worked
CodeSampleX     → what was actually tested
```

As regras que mantêm o mapa honesto:

- Um projeto que compila **nunca** é apresentado como um símbolo que funciona. Observações e verificações são contadas separadamente e nunca somadas.
- Causas desconhecidas permanecem `UNKNOWN`. Um HIT errado é pior do que um MISS — `NO_SAFE_MATCH` é uma resposta de verdade.
- A evidência decai: o peso de um resultado cai pela metade a cada 90 dias, e as células desatualizadas dizem isso.
- Causas de falha são reportadas como distribuições de probabilidade, nunca como certezas inventadas.

## Instale a CLI

A CLI é o testador local: ela envolve os seus builds reais, transforma os resultados deles em evidência anônima e responde a partir da rede.

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Essa linha precisa de `curl` e de certificados CA, que imagens mínimas (debian-slim, alpine, a maioria dos contêineres de agentes) não têm — e `curl … | sh` **sai com código 0 quando o curl está faltando**, porque um pipeline reporta o status do último comando. Instale os pré-requisitos primeiro, ou use wget:

```bash
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

O binário é instalado em `~/.local/bin`, que por padrão não está no `PATH` de ninguém:

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # the install check — `csx --version` is not a spelling csx accepts
```

Um binário, uma pergunta. O `csx init` mostra o contrato abaixo e pede uma única escolha — **JOIN COMMUNITY** ou **LOCAL ONLY**. Canalizado para o `sh`, o stdin é consumido pelo pipe do download, então o `init` assume o padrão anunciado: JOIN COMMUNITY. Você pode sair a qualquer momento com `csx init --local-only`; as duas flags de modo podem ser reexecutadas e são não interativas. Para setups com script ou em CI: `csx init --community --yes --no-agents`.

## Teste e consulte

```bash
csx run -- pnpm build              # wrap any build/test — its result becomes evidence
csx search "axios multipart upload"  # a verified answer, graded for YOUR environment
csx scan                           # record which public packages a project uses, no build
csx stats                          # local dashboard: hits, adoptions, queue
csx ui                             # browser dashboard + privacy preview
csx sync                           # warm the shard cache — once, right after install
```

O `csx sync` não é um enfeite opcional: uma instalação nova tem zero shards em cache, então toda busca retorna `NO_SAFE_MATCH` até que ele sincronize. Depois disso, o daemon reaquece o cache em segundo plano.

O `csx search` classifica cada resultado em relação ao seu ambiente registrado — `EXACT`, `COMPATIBLE`, `ADAPTATION_REQUIRED`, `REFERENCE_ONLY` ou `NO_SAFE_MATCH` — e lista o delta exato (`different`, `adaptationNeeded`) entre onde a resposta foi comprovada e onde você está.

## Samples verificados

Um sample não é um snippet. É um projeto mínimo, endereçado por conteúdo (`sha256:<hex>` do seu artefato canônico), com um **contrato**: asserções que foram executadas offline em um contêiner fixado e passaram. O ciclo de autoria clean-room é exclusivo da CLI:

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief, empty workspace
csx sample create <dir>      # ingest the clean-room project
csx sample verify <id>       # resolve → compile → contract, sandboxed
csx sample publish <id>      # requires typing exactly "yes"; leakage findings hard-refuse
```

A publicação varre segredos, caminhos, nomes de projeto e URLs privadas — qualquer achado **bloqueia** a publicação, sem flag de override. Enviar o código-fonte de um sample deliberadamente não é uma capacidade do MCP; apenas um humano na CLI pode publicar.

## Achados

Onde é que quebra? [Achados](https://codesamplex.dev/findings) é a lista medida de contradições: o que a documentação (ou a crença comum) diz, ao lado do que o contrato mediu — divergências da documentação, falhas específicas de ambiente, fronteiras de versão. Cada linha aponta para o sample publicado cujo contrato a comprova, então você pode reexecutar a medição e discordar.

Os achados derivados por máquina crescem a partir de samples publicados cujos autores registraram a crença que eles corrigem; ninguém edita uma página para adicioná-los.

## Evidência e classificação

Por que confiar em uma célula? Cada resultado carrega a sua classe de evidência, da mais fraca à mais forte:

| Grau | O que de fato aconteceu |
|-------|------------------------|
| `USAGE_OBSERVATION` | um projeto real compilou/passou no typecheck/testou com o pacote — observado, fraco |
| `ADOPTION_EVIDENCE` | alguém aplicou um sample e reportou se o build passou em seguida |
| `SAMPLE_VERIFICATION` | o contrato do sample executou em um contêiner fixado e passou |
| `CROSS_PASS` | um peer independente o reexecutou e ele passou de novo |
| `MATRIX_PASS` | execuções aprovadas atravessam ≥2 fronteiras de OS/runtime/navegador |
| `STABLE` | ≥3 peers independentes o aprovam, sem falha registrada por 30 dias |

As páginas de sample também exibem o selo da escada de verificação `L0_SOURCE_ONLY` → `L5_MATRIX_PASS`, e as células da matriz carregam confiança (`HIGH`/`MEDIUM`/`LOW`), sinalizadores de falha elevada e datas de última observação. Apenas **recibos v2** assinados podem alegar `resolvedPackages` — as versões que o verificador de fato instalou, não as versões que um autor digitou; os snapshots arquivam cada recibo sob a versão que realmente rodou.

Os contadores públicos são um agregado, disponível como JSON sem conta:

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

| Campo | O que ele conta |
|-------|----------------|
| `packages` / `symbols` | cobertura: nomes de pacotes públicos e símbolos observados nos dados de compatibilidade |
| `evidence` | registros de observação aceitos; não usuários, projetos ou samples verificados |
| `verifiedSamples` | samples distintos com um recibo de contract-PASS em sandbox |
| `peers` / `projectsMonth` | buckets anônimos e distintos de contribuidores diários/mensais |
| `postHitBuildsReported` | relatos de adoção que incluíram um PASS ou FAIL medido |

O CodeSampleX **ainda não mede de forma confiável usuários únicos/ativos, processos MCP ao vivo ou instalações bem-sucedidas**. Qualquer campo `estimated*` na resposta de stats é explicitamente baseado em fórmula e não deve ser lido como uma contagem medida.

## Worker de contribuição

Os ambientes da rede são as máquinas de outras pessoas. Uma máquina sobrando pode contribuir com verificação isolada em Docker sem tocar no MCP nem na configuração de agentes:

```bash
csx init --community --yes --no-agents --no-daemon
csx worker start                         # idle-aware, 2 Docker lanes
csx worker start --parallel 4 --budget 15m
```

O worker aceita apenas jobs VERIFY atribuídos pelo servidor (`cross` / `matrix`) — a fila nunca envia um comando de shell arbitrário. Os artefatos são endereçados por conteúdo e conferidos por hash; o resolve é conteinerizado; os estágios de compilação e de contrato rodam sem rede em workspaces Docker descartáveis com limites fixos de `512m` de memória / `256` PIDs; a ausência do daemon do Docker é uma recusa dura, nunca um fallback para o host. Os resultados são recibos v2 assinados com ed25519; os logs brutos de cada estágio permanecem locais. Veja [Contribute](https://codesamplex.dev/contribute).

## API

Os mesmos dados que o site renderiza, como JSON, sem conta:

| Endpoint | O que ele serve |
|----------|----------------|
| `GET /v1/stats` | o rollup diário da rede |
| `POST /v1/search`, `POST /v2/search` | respostas classificadas para uma consulta + fingerprint de ambiente |
| `GET /v1/registry/packages/{purl}` | detalhes do pacote + snapshot no nível do pacote |
| `GET /v1/registry/symbols/{eco}/{package}/{family}` | snapshots por versão de um símbolo |
| `GET /v1/shards/{eco}/{package}/{major}` | o shard de compatibilidade pré-materializado (com cache por ETag) |
| `GET /v1/samples/{id}`, `…/artifact` | metadados do sample, recibos e o código-fonte em tar.gz |
| `GET /v1/wanted` | a fila de demanda: o que foi perguntado e não respondido |
| `GET /v1/adapters` | a matriz de capacidades por ecossistema |

## Adaptador de agentes (MCP)

Agentes de programação consomem a mesma rede por meio de um adaptador — o MCP é um conector em cima da CLI e da API, não o produto:

```text
CodeSampleX
├─ CLI   ← primary local tester
├─ API   ← automation / integration
├─ Web   ← compatibility map / reports
└─ MCP   ← agent adapter
```

O `csx init` configura Claude Code, Codex, Gemini CLI e OpenCode automaticamente. Qualquer outro cliente MCP stdio (Cursor, Windsurf, Cline, Zed, VS Code) funciona a partir do que o `csx mcp-config` imprime (`--toml` para o Codex) — ele emite o caminho absoluto do binário, que é o que um cliente iniciado por um editor precisa. O servidor em si é o `csx mcp`. Oito ferramentas: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats` — e, deliberadamente, nenhuma ferramenta de publicação.

Passos de instalação direcionados a agentes (incluindo o bundle MCPB e downloads diretos de binários com `SHA256SUMS.txt`): [llms-install.md](../../llms-install.md). Instalações standalone da comunidade se autoatualizam por meio de um manifesto assinado com Ed25519, com `csx update rollback` disponível; instalações `local-only` não fazem nenhuma requisição de atualização.

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

Isto não é telemetria escondida — é o protocolo. Os peers da comunidade são consumidores **e** produtores. O modo local-only nunca envia nada. Os erros são sanitizados localmente em fingerprints antes de qualquer uso; pacotes privados e desconhecidos nunca saem da máquina; a prévia de privacidade no `csx ui` mostra os payloads exatos antes de eles saírem. Um `NO_SAFE_MATCH` contribui com uma tupla Wanted segura para a privacidade — o pacote público, a sua versão exata e, quando a requisição nomeou um único pacote inequívoco, os símbolos públicos solicitados — nunca o prompt do usuário. A implantação pública é **seeded-only para o código-fonte de samples**; a busca, a evidência, os recibos e o quadro de wanted são abertos, sem conta.

## Ecossistemas (Public v1)

**Escaneados e verificados** — projetos detectados, pacotes resolvidos via lockfile, samples verificados de ponta a ponta: Node/TypeScript (npm, pnpm, yarn — referência), Python (pip, uv), Go, Rust/Cargo. Os samples de Node rodam no runtime que declaram, então os resultados de Bun e Deno são reais, e não presumidos.

**Apenas verificados** — ainda sem scanner de projetos, mas os samples publicados são compilados e testados por contrato em um contêiner fixado: PHP/Composer, Ruby/Bundler, Dart/pub, Elixir/Hex. A verificação de contrato de Java (Maven/Gradle) fixa lanes exatas de JDK 8/11/17/21/25.

Matriz de capacidades honesta: [docs/adapters.md](../adapters.md) — a confiança da resolução de símbolos é sempre rotulada (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Arquitetura

Um único binário Go (`csx`: daemon + CLI + MCP + nó peer + verificador) e um servidor pequeno (`csx-server`: PostgreSQL + explorador de compatibilidade renderizado no servidor atrás do Caddy). Os samples são endereçados por conteúdo e distribuídos priorizando o cache local → peers → seeder principal. Samples baixados nunca rodam diretamente no host: o resolve roda em um sandbox fixado com os scripts de instalação desabilitados onde o ecossistema suporta isso, o artefato é re-hasheado depois do resolve, e os estágios de compilação e de contrato rodam sem rede. Veja [goal.md](../../goal.md), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md).

## Compilando a partir do código-fonte

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## Licença

Código: Apache-2.0. Samples publicados usam **MIT-0** por padrão.
