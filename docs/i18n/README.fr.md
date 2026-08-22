# CodeSampleX

[![Release](https://img.shields.io/github/v/release/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/r2cuerdame/CodeSampleX/total)](https://github.com/r2cuerdame/CodeSampleX/releases)
[![License](https://img.shields.io/github/license/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/LICENSE)
[![Release pipeline](https://img.shields.io/github/actions/workflow/status/r2cuerdame/CodeSampleX/release.yml?label=release)](https://github.com/r2cuerdame/CodeSampleX/actions/workflows/release.yml)
[![Go](https://img.shields.io/github/go-mod/go-version/r2cuerdame/CodeSampleX)](https://github.com/r2cuerdame/CodeSampleX/blob/main/go.mod)

> **Tested. Not guessed.** (*Testé, pas deviné.*)

<p align="center">
  <img src="../../internal/web/static/inspector-hero-v1.webp" alt="Inspecteur de compatibilité CodeSampleX" width="560">
</p>

**Langues :** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX est un **réseau ouvert de tests de compatibilité** pour les bibliothèques, runtimes et chaînes d'outils des développeurs. Il ne résume pas la documentation et ne collecte pas d'anecdotes : il exécute de vrais builds et de vrais tests de contrat dans des environnements réels et enregistrés — puis vous montre où les choses ont réellement fonctionné, où elles ont cassé, et à quel point il est sûr des deux.

- Carte de compatibilité : **https://codesamplex.dev**
- La question à laquelle il répond : *est-ce que ça tourne là-bas ?* — cette API, dans cette version, sur cet OS, sous ce runtime.
- La réponse qu'il donne : *nous l'avons testé ; voici ce qui s'est passé.*

## Est-ce que ça tourne là-bas ?

Chaque résultat est une exécution enregistrée accompagnée de son environnement, si bien que les données pivotent en matrices de compatibilité — OS × runtime, version × architecture, symbole × OS. Une tranche réelle, issue du réseau en direct (`axios.post`, mesurée en août 2026) :

```text
github.com/jackc/pgx/v5                  v5.10.0     v5.9.2    v5.7.3
whole package                            ✓ 85% 1156  ✓ 100% 2  ✓ —
Batch                                    ✓ 91% 240   —         —
Identifier                                 60% 15    —         ✓ —
```

Cette grille n’est pas une illustration : c’est [la page en direct](https://codesamplex.dev/golang/github.com%2Fjackc%2Fpgx%2Fv5).

**Une cellule porte un taux et une marque, jamais un verdict.** Le pourcentage et le nombre à côté sont ce qu’ont fait de vraies machines ; la coche signifie que notre exemple y fonctionne. Nos exécutions sont un conteneur épinglé répété, mille exécutions signalées sont mille situations différentes : elles ne s’additionnent pas. `✓ —` veut dire : du code qui fonctionne existe, et personne n’a encore été vu s’en servir. `—` reste inconnu.

## Pourquoi les tests comptent

```text
Documentation   → what should work
Code search     → how somebody used it
Community       → what somebody says worked
CodeSampleX     → what was actually tested
```

Les règles qui gardent la carte honnête :

- Un projet qui compile n'est **jamais** présenté comme un symbole qui fonctionne. Observations et vérifications sont comptées séparément et jamais additionnées.
- Les causes inconnues restent `UNKNOWN`. Un HIT erroné est pire qu'un MISS — `NO_SAFE_MATCH` est une vraie réponse.
- La preuve se périme : le poids d'un résultat est divisé par deux tous les 90 jours, et les cellules obsolètes le disent.
- Les causes d'échec sont rapportées comme des distributions de probabilité, jamais comme des certitudes inventées.

## Installer la CLI

La CLI est le testeur local : elle enveloppe vos vrais builds, transforme leurs résultats en preuves anonymes et répond à partir du réseau.

Windows (PowerShell) :

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux :

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Cette ligne a besoin de `curl` et de certificats CA, que les images minimales (debian-slim, alpine, la plupart des conteneurs d'agents) n'ont pas — et `curl … | sh` **sort avec le code 0 quand curl est absent**, car un pipeline rapporte le statut de sa dernière commande. Installez d'abord les prérequis, ou utilisez wget :

```bash
apt-get install -y curl ca-certificates            # debian / ubuntu slim
apk add --no-cache curl ca-certificates            # alpine
wget -qO- https://codesamplex.dev/install.sh | sh  # needs neither
```

Le binaire atterrit dans `~/.local/bin`, qui n'est dans le `PATH` de personne par défaut :

```bash
export PATH="$HOME/.local/bin:$PATH"
csx version    # the install check — `csx --version` is not a spelling csx accepts
```

Un binaire, une question. `csx init` affiche le contrat ci-dessous et ne demande qu'un seul choix — **JOIN COMMUNITY** ou **LOCAL ONLY**. Lancé via un pipe dans `sh`, stdin est consommé par le pipe de téléchargement, donc `init` prend le défaut annoncé : JOIN COMMUNITY. Retirez-vous à tout moment avec `csx init --local-only` ; les deux drapeaux de mode sont réexécutables et non interactifs. Pour les installations scriptées ou en CI : `csx init --community --yes --no-agents`.

## Tester et vérifier

```bash
csx run -- pnpm build              # wrap any build/test — its result becomes evidence
csx search "axios multipart upload"  # a verified answer, graded for YOUR environment
csx scan                           # record which public packages a project uses, no build
csx stats                          # local dashboard: hits, adoptions, queue
csx ui                             # browser dashboard + privacy preview
csx sync                           # warm the shard cache — once, right after install
```

`csx sync` n'est pas une garniture optionnelle : une installation fraîche n'a aucun shard en cache, donc chaque recherche renvoie `NO_SAFE_MATCH` tant que la synchronisation n'a pas eu lieu. Le daemon réchauffe ensuite le cache en arrière-plan.

`csx search` note chaque résultat par rapport à votre environnement enregistré — `EXACT`, `COMPATIBLE`, `ADAPTATION_REQUIRED`, `REFERENCE_ONLY` ou `NO_SAFE_MATCH` — et liste le delta exact (`different`, `adaptationNeeded`) entre l'endroit où la réponse a été prouvée et l'endroit où vous êtes.

## Échantillons vérifiés

Un échantillon (sample) n'est pas un extrait de code. C'est un projet minimal, adressé par contenu (`sha256:<hex>` de son artefact canonique), doté d'un **contrat** : des assertions qui ont été exécutées hors ligne dans un conteneur épinglé et qui ont réussi. Épinglé par digest d'image, pas par tag — le tag est un alias pour les lecteurs, le digest est ce qui s'exécute — et le reçu signé nomme la référence exacte de l'image, de sorte que n'importe qui peut réexécuter les mêmes octets au lieu de nous croire sur parole ([docs/adapters.md](../adapters.md#verifier-images)). La boucle de rédaction clean-room passe exclusivement par la CLI :

```bash
csx sample propose --goal "upload a file with axios"   # sanitized brief, empty workspace
csx sample create <dir>      # ingest the clean-room project
csx sample verify <id>       # resolve → compile → contract, sandboxed
csx sample publish <id>      # requires typing exactly "yes"; leakage findings hard-refuse
```

La publication recherche les secrets, chemins, noms de projet et URL privées — toute détection **bloque** la publication, sans drapeau de contournement. Téléverser la source d'un échantillon n'est délibérément pas une capacité MCP ; seul un humain à la CLI peut publier.

## Constats

Où est-ce que ça casse ? [Findings](https://codesamplex.dev/findings) est la liste des contradictions mesurées : ce que dit la documentation (ou la croyance commune), à côté de ce que le contrat a mesuré — écarts avec la documentation, échecs propres à un environnement, frontières de version. Chaque ligne renvoie à l'échantillon publié dont le contrat la prouve, pour que vous puissiez rejouer la mesure et la contester.

Les constats dérivés automatiquement naissent d'échantillons publiés dont les auteurs ont consigné la croyance qu'ils corrigent ; personne n'édite une page pour en ajouter.

## Preuves et notation

Pourquoi faire confiance à une cellule ? Chaque résultat porte sa classe de preuve, de faible à forte :

| Grade | Ce qui s'est réellement passé |
|-------|-------------------------------|
| `USAGE_OBSERVATION` | un vrai projet a compilé/typé/testé avec le paquet — observé, faible |
| `ADOPTION_EVIDENCE` | quelqu'un a appliqué un échantillon et a rapporté si le build passait ensuite |
| `SAMPLE_VERIFICATION` | le contrat de l'échantillon s'est exécuté dans un conteneur épinglé et a réussi |
| `CROSS_PASS` | un pair indépendant l'a réexécuté et il a de nouveau réussi |
| `MATRIX_PASS` | des exécutions réussies couvrent ≥ 2 frontières OS/runtime/navigateur |
| `STABLE` | ≥ 3 pairs indépendants le réussissent, aucun échec enregistré depuis 30 jours |

Les pages d'échantillon affichent aussi le badge de l'échelle de vérification `L0_SOURCE_ONLY` → `L5_MATRIX_PASS`, et les cellules de la matrice portent un niveau de confiance (`HIGH`/`MEDIUM`/`LOW`), des indicateurs de taux d'échec élevé et des dates de dernière observation. Seuls les **reçus v2** signés peuvent revendiquer `resolvedPackages` — les versions que le vérificateur a réellement installées, pas celles qu'un auteur a tapées ; les instantanés classent chaque reçu sous la version qui a réellement tourné.

Les compteurs publics sont un agrégat, disponible en JSON sans compte :

```bash
curl -fsSL https://codesamplex.dev/v1/stats
```

| Champ | Ce qu'il compte |
|-------|-----------------|
| `packages` / `symbols` | couverture : noms de paquets publics et symboles observés dans les données de compatibilité |
| `evidence` | enregistrements d'observation acceptés ; pas des utilisateurs, des projets ni des échantillons vérifiés |
| `verifiedSamples` | échantillons distincts disposant d'un reçu contract-PASS obtenu en sandbox |
| `peers` / `projectsMonth` | compartiments anonymes distincts de contributeurs quotidiens/mensuels |
| `postHitBuildsReported` | rapports d'adoption incluant un PASS ou un FAIL mesuré |

CodeSampleX ne mesure **pas encore de façon fiable les utilisateurs uniques/actifs, les processus MCP vivants ni les installations réussies**. Tout champ `estimated*` de la réponse stats est explicitement issu d'une formule et ne doit pas être lu comme un décompte mesuré.

## Worker contributeur

Les environnements du réseau sont les machines des autres. Une machine disponible peut contribuer de la vérification isolée dans Docker sans toucher à MCP ni à la configuration d'agent :

```bash
csx init --community --yes --no-agents --no-daemon
csx worker start                         # idle-aware, 2 Docker lanes
csx worker start --parallel 4 --budget 15m
```

Le worker n'accepte que des jobs VERIFY assignés par le serveur (`cross` / `matrix`) — la file n'envoie jamais de commande shell arbitraire. Les artefacts sont adressés par contenu et contrôlés par hachage ; la résolution est conteneurisée ; les étapes de compilation et de contrat tournent réseau coupé dans des espaces de travail Docker jetables avec des limites fixes de `512m` de mémoire / `256` PID ; un daemon Docker absent est un refus net, jamais un repli sur l'hôte. Les résultats sont des reçus v2 signés en ed25519 ; les journaux bruts des étapes restent locaux.

## API

Les mêmes données que celles affichées par le site web, en JSON, sans compte :

| Endpoint | Ce qu'il sert |
|----------|---------------|
| `GET /v1/stats` | l'agrégat quotidien du réseau |
| `POST /v1/search`, `POST /v2/search` | des réponses notées pour une requête + une empreinte d'environnement |
| `GET /v1/registry/packages/{purl}` | détail d'un paquet + instantané au niveau du paquet |
| `GET /v1/registry/symbols/{eco}/{package}/{family}` | instantanés par version pour un symbole |
| `GET /v1/shards/{eco}/{package}/{major}` | le shard de compatibilité pré-matérialisé (mis en cache via ETag) |
| `GET /v1/samples/{id}`, `…/artifact` | métadonnées de l'échantillon, reçus, et la source en tar.gz |
| `GET /v1/wanted` | la file de la demande : ce qui a été demandé sans recevoir de réponse |
| `GET /v1/adapters` | la matrice de capacités par écosystème |

## Adaptateur agent (MCP)

Les agents de code consomment le même réseau à travers un adaptateur — MCP est un connecteur posé sur la CLI et l'API, pas le produit :

```text
CodeSampleX
├─ CLI   ← primary local tester
├─ API   ← automation / integration
├─ Web   ← compatibility map / reports
└─ MCP   ← agent adapter
```

`csx init` configure automatiquement Claude Code, Codex, Gemini CLI et OpenCode. Tout autre client MCP stdio (Cursor, Windsurf, Cline, Zed, VS Code) fonctionne à partir de ce qu'affiche `csx mcp-config` (`--toml` pour Codex) — la commande émet le chemin absolu du binaire, dont un client lancé par un éditeur a besoin. Le serveur lui-même est `csx mcp`. Huit outils : `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats` — et délibérément aucun outil de publication.

Les étapes d'installation destinées aux agents (y compris le bundle MCPB et les téléchargements directs de binaires avec `SHA256SUMS.txt`) : [llms-install.md](../../llms-install.md). Les installations communautaires autonomes se mettent à jour automatiquement via un manifeste signé Ed25519, avec `csx update rollback` disponible ; les installations `local-only` n'émettent aucune requête de mise à jour.

## Le contrat

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

Ce n'est pas de la télémétrie cachée — c'est le protocole. Les pairs de la communauté sont consommateurs **et** producteurs. Le mode local seul n'envoie jamais rien. Les erreurs sont assainies localement en empreintes avant tout usage ; les paquets privés et inconnus ne quittent jamais la machine ; l'aperçu de confidentialité dans `csx ui` montre les payloads exacts avant qu'ils ne partent. Un `NO_SAFE_MATCH` contribue un tuple Wanted respectueux de la vie privée — le paquet public, sa version exacte et, lorsque la requête a nommé un seul paquet sans ambiguïté, les symboles publics demandés — jamais le prompt de l'utilisateur. Sur le déploiement public, **seul le seeder fournit la source des échantillons** ; la recherche, les preuves, les reçus et le tableau des demandes (wanted) sont ouverts sans compte.

## Écosystèmes (Public v1)

**Scannés et vérifiés** — projets détectés, paquets résolus par lockfile, échantillons vérifiés de bout en bout : Node/TypeScript (npm, pnpm, yarn — référence), Python (pip, uv), Go, Rust/Cargo. Les échantillons Node s'exécutent sur le runtime qu'ils déclarent, si bien que les résultats Bun et Deno sont réels et non supposés.

**Vérifiés uniquement** — pas encore de scanner de projet, mais les échantillons publiés sont compilés et testés par contrat dans un conteneur épinglé : PHP/Composer, Ruby/Bundler, Dart/pub, Elixir/Hex. La vérification de contrat Java (Maven/Gradle) épingle des couloirs JDK exacts : 8/11/17/21/25.

Matrice de capacités sans complaisance : [docs/adapters.md](../adapters.md) — le niveau de confiance de la résolution de symboles est toujours étiqueté (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Architecture

Un seul binaire Go (`csx` : daemon + CLI + MCP + nœud pair + vérificateur) et un petit serveur (`csx-server` : PostgreSQL + explorateur de compatibilité rendu côté serveur derrière Caddy). Les échantillons sont adressés par contenu et distribués cache-local d'abord → pairs → seeder principal. Les échantillons téléchargés ne s'exécutent jamais directement sur l'hôte : la résolution tourne dans un sandbox épinglé avec les scripts d'installation désactivés là où l'écosystème le permet, l'artefact est re-haché après résolution, et les étapes de compilation et de contrat tournent réseau coupé. Voir [goal.md](../../goal.md), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md).

## Compiler depuis les sources

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## Licence

Code : Apache-2.0. Les échantillons publiés sont par défaut sous **MIT-0**.
