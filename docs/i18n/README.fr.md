# CodeSampleX

> **Arrêtez de résoudre deux fois le même code.** (*Stop solving the same code twice.*)

**Langues :** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX est un **cache de raisonnement distribué, local d'abord**, destiné aux LLM de programmation. Plutôt que chaque agent sur Terre redécouvre le fonctionnement d'une bibliothèque publique — et se heurte aux mêmes incompatibilités de versions — CodeSampleX collecte des **Evidence** de compatibilité anonymes issues de vrais environnements de développement et fournit des **Samples minimaux vérifiés**, avec le delta exact entre une réponse réputée fiable et votre projet.

- Site web et explorateur de compatibilité : **https://codesamplex.dev**
- Une question que votre LLM cesse de re-résoudre : *est-ce que `axios.post` fonctionne vraiment avec axios 1.12 + Node 22 + pnpm + Windows 11 — et sinon, à quelle étape ça casse ?*
- Fonctionne avec **Claude Code, Codex, Gemini CLI, OpenCode** — et avec n'importe quel client MCP (Cursor, Windsurf, Cline, Zed, VS Code).

## Installation

Windows (PowerShell) :

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux :

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Un binaire, une question. `csx init` affiche le contrat communautaire et ne demande qu'un seul choix — **JOIN COMMUNITY** ou **LOCAL ONLY**. Tout le reste (daemon, enregistrement MCP pour Claude Code / Codex / Gemini CLI / OpenCode, règles d'agent) est automatique.

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

Ce n'est pas de la télémétrie cachée — c'est le protocole. Les pairs de la communauté sont à la fois consommateurs **et** producteurs. Le mode local seul n'envoie jamais rien. L'aperçu de confidentialité dans `csx ui` montre les payloads exacts avant qu'ils ne quittent votre machine.

## Comment ça marche

```text
you build/test through csx (or your agent does)
→ local analysis: public packages, lockfile-resolved versions, symbols, environment
→ raw errors sanitized locally into fingerprints (paths/names/secrets stripped)
→ anonymous evidence batches → Compatibility Graph on codesamplex.dev
→ your LLM asks CSX first: nearest verified Sample + environment delta
→ it reasons about the DELTA, not the whole problem
```

Quatre couches, maintenues honnêtement séparées :

| Couche | Ce que c'est | Confiance |
|--------|--------------|-----------|
| Evidence Network | faits anonymes package/version/symbole/env/étape/résultat | faible→forte, étiquetée par classe |
| Compatibility Graph | carte probabiliste agrégée par environnement (y c. contexte d'exécution : Node/Chrome/Safari/Electron/…) | vue dérivée |
| Sample Pool | projets minimaux approuvés par l'utilisateur, clean-room, adressés par contenu | vérifiés par contrat, vérifiés de façon croisée |
| Agent Delivery | MCP/CLI : sample le plus proche + delta + échecs connus | gradé EXACT→NO_SAFE_MATCH |

Un projet qui compile n'est jamais présenté comme un symbole qui fonctionne. Les causes inconnues restent `UNKNOWN`. Un HIT erroné est pire qu'un MISS — `NO_SAFE_MATCH` est une fonctionnalité, pas un défaut.

## Intégration agent (MCP)

**Configuré automatiquement par `csx init`** : Claude Code · Codex · Gemini CLI · OpenCode.

**Tout autre client MCP** (Cursor, Windsurf, Cline, Zed, VS Code) fonctionne aussi : `csx` est un serveur MCP stdio standard :

```json
{"mcpServers": {"csx": {"command": "csx", "args": ["mcp"]}}}
```

Indépendant du modèle : les mêmes preuves de compatibilité servent à Claude, GPT et Codex, Gemini, Llama — tout modèle capable d'appeler un outil MCP.

Outils : `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats`. La publication d'un sample n'est délibérément **pas** une capacité MCP — elle exige votre approbation explicite en CLI après un aperçu complet.

```bash
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

## Écosystèmes (Public v1)

Node/TypeScript (npm, pnpm, yarn — référence), Python (pip, uv), Go, Rust/Cargo. Matrice de capacités sans complaisance : [docs/adapters.md](../adapters.md) — aucun adaptateur ne revendique l'instrumentation des symboles à l'exécution en v1, et le niveau de confiance de la résolution de symboles est toujours étiqueté (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Architecture

Un seul binaire Go (`csx` : daemon + CLI + MCP + nœud pair + vérificateur) et un petit serveur (`csx-server` : PostgreSQL + explorateur rendu côté serveur derrière Caddy). Les samples sont adressés par contenu (`sha256`) et distribués cache-local d'abord → pairs → seeder principal. Les samples téléchargés ne s'exécutent jamais directement sur votre hôte — résolution avec `--ignore-scripts`, compilation et exécution du contrat réseau coupé dans un bac à sable, reçus signés en ed25519. Voir [goal.md](../../goal.md) (spécification produit), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md).

## Compiler depuis les sources

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## Licence

Code : Apache-2.0. Les samples publiés sont par défaut sous **MIT-0**.
