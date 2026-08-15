# CodeSampleX

> **Хватит решать один и тот же код дважды.** (Stop solving the same code twice.)

**Languages:** [English](../../README.md) · [한국어](README.ko.md) · [日本語](README.ja.md) · [简体中文](README.zh-CN.md) · [Español](README.es.md) · [Français](README.fr.md) · [Deutsch](README.de.md) · [Português (BR)](README.pt-BR.md) · [Русский](README.ru.md)

CodeSampleX — это **локально-ориентированный распределённый кэш рассуждений** для кодинговых LLM. Вместо того чтобы каждый агент на планете заново выводил, как работает публичная библиотека, — и заново натыкался на те же несовместимости версий, — CodeSampleX собирает анонимные **свидетельства** (Evidence) о совместимости из реальных сред разработки и выдаёт **проверенные минимальные примеры** (Samples) с точной дельтой между заведомо рабочим ответом и вашим проектом.

- Сайт и обозреватель совместимости: **https://codesamplex.dev**
- Один вопрос, который вашей LLM больше не придётся решать заново: *действительно ли `axios.post` работает на axios 1.12 + Node 22 + pnpm + Windows 11 — а если нет, то на каком именно этапе всё ломается?*
- Работает с **Claude Code, Codex, Gemini CLI, OpenCode** — и с любым MCP-клиентом (Cursor, Windsurf, Cline, Zed, VS Code).

## Установка

Windows (PowerShell):

```powershell
irm https://codesamplex.dev/install.ps1 | iex
```

macOS / Linux:

```bash
curl -fsSL https://codesamplex.dev/install.sh | sh
```

Один бинарник, один вопрос. `csx init` показывает соглашение с сообществом и предлагает единственный выбор — **JOIN COMMUNITY** или **LOCAL ONLY**. Всё остальное (демон, регистрация MCP для Claude Code / Codex / Gemini CLI / OpenCode, правила для агентов) происходит автоматически.

## Соглашение

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

Это не скрытая телеметрия — это сам протокол. Участники сообщества являются одновременно потребителями **и** производителями. Режим local-only никогда ничего не отправляет. Предпросмотр приватности в `csx ui` показывает точное содержимое пакетов данных до того, как они покинут вашу машину.

## Как это работает

```text
you build/test through csx (or your agent does)
→ local analysis: public packages, lockfile-resolved versions, symbols, environment
→ raw errors sanitized locally into fingerprints (paths/names/secrets stripped)
→ anonymous evidence batches → Compatibility Graph on codesamplex.dev
→ your LLM asks CSX first: nearest verified Sample + environment delta
→ it reasons about the DELTA, not the whole problem
```

Четыре слоя, честно разделённые между собой:

| Слой | Что это | Доверие |
|-------|------------|-------|
| Evidence Network | анонимные факты вида пакет/версия/символ/окружение/этап/результат | от слабого к сильному, с пометкой класса |
| Compatibility Graph | агрегированная вероятностная карта по окружениям (включая контекст выполнения: Node/Chrome/Safari/Electron/…) | производное представление |
| Sample Pool | одобренные пользователем, «чистые», контентно-адресуемые минимальные проекты | проверены по контракту, перекрёстно верифицированы |
| Agent Delivery | MCP/CLI: ближайший пример + дельта + известные сбои | градация от EXACT до NO_SAFE_MATCH |

Успешная компиляция проекта никогда не выдаётся за работоспособность символа. Причины, которые не удалось установить, остаются `UNKNOWN`. Ложный HIT хуже, чем MISS, — поэтому `NO_SAFE_MATCH` здесь не недостаток, а осознанная возможность.

## Интеграция с агентами (MCP)

**Настраивается автоматически через `csx init`:** Claude Code · Codex · Gemini CLI · OpenCode.

**Любой другой MCP-клиент** (Cursor, Windsurf, Cline, Zed, VS Code) тоже подойдёт: `csx` — обычный stdio MCP-сервер:

```sh
csx mcp-config          # JSON: Cursor, Cline, Windsurf, Zed, VS Code
csx mcp-config --toml   # TOML: Codex
```

Не пишите конфигурацию вручную — вставьте то, что печатает эта команда. Она подставляет **абсолютный путь** вашей установки, и это главное: MCP-клиент запускается не из login-оболочки, а наследует окружение редактора. Поэтому простое `"command": "csx"` не запустится, даже если вы уже поправили свой `PATH`.

Не зависит от модели: одни и те же данные о совместимости используют Claude, GPT и Codex, Gemini, Llama — любая модель, умеющая вызывать MCP-инструмент.

Инструменты: `search_known_solution`, `get_sample`, `explain_compatibility`, `run_observed_command`, `report_sample_adoption`, `propose_public_sample`, `list_local_hits`, `get_local_stats`. Публикация примера намеренно **не является** возможностью MCP — она требует вашего явного подтверждения в CLI после полного предпросмотра.

```bash
csx run -- pnpm build      # observed build → evidence
csx search "axios multipart upload"
csx sample propose --goal "upload a file with axios"
csx ui                     # dashboard + privacy preview
```

## Экосистемы (Public v1)

Node/TypeScript (npm, pnpm, yarn — эталонная реализация), Python (pip, uv), Go, Rust/Cargo. Честная матрица возможностей: [docs/adapters.md](../adapters.md) — ни один адаптер в v1 не заявляет инструментирование символов во время выполнения, а уверенность в разрешении символов всегда помечается явно (`EXACT`/`PROBABLE`/`UNKNOWN`).

## Архитектура

Один бинарник на Go (`csx`: демон + CLI + MCP + пиринговый узел + верификатор) и небольшой сервер (`csx-server`: PostgreSQL + обозреватель с серверным рендерингом за Caddy). Примеры адресуются по содержимому (`sha256`) и распространяются по схеме «сначала локальный кэш → пиры → основной сидер». Скачанные примеры никогда не выполняются напрямую на вашем хосте: разрешение зависимостей идёт с `--ignore-scripts`, компиляция и проверка контракта выполняются в песочнице без доступа к сети, а квитанции подписываются ed25519. См. [goal.md](../../goal.md) (спецификация продукта), [docs/execution-context.md](../execution-context.md), [docs/operations.md](../operations.md).

## Сборка из исходников

```bash
go build ./cmd/csx && go build ./cmd/csx-server
go test ./...
```

## Лицензия

Код: Apache-2.0. Публикуемые примеры по умолчанию распространяются под **MIT-0**.
