# CodeSampleX

[![Release](https://img.shields.io/github/v/release/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/r2cuerdame/CodeSampleX/total)](https://github.com/r2cuerdame/CodeSampleX/releases)
[![License](https://img.shields.io/github/license/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/LICENSE)
[![Release pipeline](https://img.shields.io/github/actions/workflow/status/r2cuerdame/CodeSampleX/release.yml?label=release)](https://github.com/r2cuerdame/CodeSampleX/actions/workflows/release.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/go.mod)

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

Cada resultado é uma execução registrada com o seu ambiente anexado, então os dados se articulam em matrizes de compatibilidade — OS × runtime, versão × arquitetura, símbolo × OS. Um recorte copiado tal como estava na rede ao vivo em 2026-08-23:

```text
                                         v5.10.0     v5.9.2    v5.7.3
github.com/jackc/pgx/v5                  ≡ 82% 1209  ≡ 100% 2  ≡ —
Batch                                    ≡ 80% 689   —         —
ParseConfig                              ≡ 82% 1188  —         —
```

Esta grade não é uma ilustração: é [a página ao vivo](https://codesamplex.dev/golang/github.com%2Fjackc%2Fpgx%2Fv5) — então os números acima já se moveram desde que foram copiados.

**Uma célula carrega uma taxa e uma marca, nunca um veredito.** A porcentagem e o número ao lado são observações: 82% de 1.209 observações registradas passaram. Uma observação é uma etapa que um build alcançou — compilar, checar tipos, testar — então um único build deixa várias, e o número não conta builds, nem máquinas, nem pessoas. A marca `≡` é a nossa própria amostra: ela rodou aqui e terminou limpa. Deliberadamente não é um sinal de visto, porque um visto é um carimbo de aprovação e esta rede não dá notas; uma execução nossa que falhou carrega `✕` no lugar. `≡ —` significa: existe código funcionando e ninguém foi visto usando ainda. `—` permanece desconhecido.

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
- A evidência não decai, e nenhuma célula é marcada como desatualizada. Uma observação é uma release fixada, em um bucket de ambiente fixado, em uma etapa, e nenhum dos três se move: que um build tenha falhado ali continua igualmente verdadeiro um ano depois. O que pode mudar é o ambiente, e um ambiente diferente é uma célula diferente.
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

Um sample não é um snippet. É um projeto mínimo, endereçado por conteúdo (`sha256:<hex>` do seu artefato canônico), com um **contrato**: asserções que foram executadas offline em um contêiner fixado e passaram. Fixado pelo digest da imagem, não pela tag — a tag é um apelido para quem lê, o digest é o que executa — e o recibo assinado registra a referência exata da imagem, de modo que qualquer pessoa pode reexecutar os mesmos bytes em vez de acreditar na palavra ([docs/adapters.md](../adapters.md#verifier-images)). O ciclo de autoria clean-room é exclusivo da CLI:

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief + scaffolded workspace
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
| `CROSS_PASS` | uma chave de peer diferente da que o publicou o reexecutou e ele passou de novo |
| `MATRIX_PASS` | recibos aprovados atravessam ≥2 fronteiras de OS/major de runtime/família de navegador |
| `STABLE` | ≥3 chaves de peer distintas o aprovam, sem falha registrada por 30 dias |

Um peer é uma chave, não uma pessoa e não uma máquina. Um id de peer é o hash de uma chave ed25519 gerada pelo próprio cliente, sem registro por trás, então um mesmo operador pode ter tantas quantos workers rodar. “Chaves de peer distintas” significa que a mesma coordenada foi reportada de mais de um lugar; nunca é uma contagem de pessoas, e nada aqui identifica quem rodou o quê.

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

O worker aceita apenas jobs VERIFY atribuídos pelo servidor (`cross` / `matrix`) — a fila nunca envia um comando de shell arbitrário. Os artefatos são endereçados por conteúdo e conferidos por hash; o resolve é conteinerizado; os estágios de compilação e de contrato rodam sem rede em workspaces Docker descartáveis com limites fixos de `512m` de memória / `256` PIDs; a ausência do daemon do Docker é uma recusa dura, nunca um fallback para o host. Os resultados são recibos v2 assinados com ed25519; os logs brutos de cada estágio permanecem locais.

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

O `csx init` configura Claude Code, Codex, Gemini CLI e OpenCode automaticamente. Qualquer outro cliente MCP stdio (Cursor, Windsurf, Cline, Zed, VS Code) funciona a partir do que o `csx mcp-config` imprime (`--toml` para o Codex) — ele emite o caminho absoluto do binário, que é o que um cliente iniciado por um editor precisa. O servidor em si é o `csx mcp`. Dez ferramentas: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `report_anomaly`, `report_csx_issue`, `propose_public_sample`, `list_local_hits`, `get_local_stats` — e, deliberadamente, nenhuma ferramenta de publicação. `report_csx_issue` é a mesma ideia apontada para nós e não para um pacote: uma resposta que deslocou a falha que você estava de fato olhando, uma recomendação de um ecossistema que a pergunta nunca mencionou, um contrato de ferramenta que fez um modelo agir errado. É opt-in e deliberadamente silencioso — nada manda um agente chamá-lo após uma falha, nenhum ticket é criado, e uma semana sem relatos é uma semana normal. Um defeito que cem agentes encontram é UMA linha cujo contador de ocorrências sobe, e uma vez que essa linha está ligada a um bug, todo relato posterior responde com o link. Os dois canais compartilham ingestão, redação e deduplicação e nada depois disso: um defeito deste produto nunca pode virar evidência de compatibilidade.

`report_anomaly` aponta na direção oposta. Quando uma resposta do CSX e a própria máquina do agente se contradizem de forma **concreta** — a rede serviu uma conclusão de sucesso para uma coordenada que falhou aqui, uma assinatura de símbolo devolvida não é a que o pacote exporta — o agente pode registrar isso como um pedido de verificação. Não é um relatório de bug: um relatório enfileira uma reexecução independente na mesma frota que produz todos os outros recibos, e só esse recibo pode confirmá-lo. Um envio sem nada medido por trás é recusado, o mesmo descompasso relatado duas vezes é um relatório e uma reexecução, e nada do que um relatório diz chega a uma página pública antes de um verificador concordar. O palpite de causa de quem relata viaja em um campo próprio e nunca decide o veredicto.

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

## Política de Privacidade

O contrato acima é o que o código faz. O [PRIVACY.md](../../PRIVACY.md) diz a mesma coisa como política, campo a campo, nomeando o arquivo que impõe cada limite: os documentos exatos que o modo comunidade envia, as requisições que são downloads e não uploads, o que o servidor guarda e por quanto tempo, e o que `local-only` significa quando diz que não envia nada. Ele é versionado neste repositório em vez de servido por uma página editável sem deixar rastro, e é a URL para a qual o array `privacy_policies` do pacote MCPB aponta.

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
