# Ecosystem Adapter Capability Matrix (Public v1)

No adapter pretends a capability it does not have (goal.md §13). Levels:

```text
A0  Package/version detection (lockfile-resolved)
A1  Build/typecheck/test observation
A2  Static symbol resolution
A3  Runtime symbol instrumentation
A4  Clean Sample + Contract verification
```

| Ecosystem | Adapter | Package managers | A0 | A1 | A2 | A3 | A4 | Symbol confidence |
|-----------|---------------------|--------------------|----|----|----|----|----|-------------------|
| npm | node-typescript@1 | npm, pnpm, yarn | ✓ | ✓ | ✓ | – | ✓ | PROBABLE |
| pypi | python@1 | pip, uv (+poetry lock best-effort) | ✓ | ✓ | ✓ | – | – | PROBABLE |
| golang | go@1 | go modules | ✓ | ✓ | ✓ | – | – | PROBABLE |
| cargo | rust@1 | cargo | ✓ | ✓ | ✓ | – | – | PROBABLE |

Honest limitations, stated on purpose:

- **PROBABLE, not EXACT**: static import/member analysis without a type checker
  cannot claim EXACT symbol resolution (goal.md §7.2). EXACT is reserved for a
  future TypeScript type-info / go-types integration.
- **A3 is absent everywhere**: no Public v1 adapter observes real symbol
  execution. The `SYMBOL_EXECUTED`/`SYMBOL_CALL` stages exist in the schema for
  future instrumentation (browser/worker contexts included — see
  docs/execution-context.md) and the server rejects them from clients today.
- **A4 verifier is node-only**: sample contract verification runs the
  `node-typescript@1` verifier adapter (Docker `CONTAINER_RUN` when available,
  `COMPILE_ONLY` natively otherwise — receipts say which honestly). Python, Go
  and Rust samples verify only where a contract fits the shared container
  verifier; their receipts never overstate.
- **Dynamic usage degrades confidence**: Python getattr/importlib and Rust
  macro-expanded usage report `UNKNOWN` rather than guessing (goal.md §13.3, §13.5).

Machine-readable source of truth: `schemas/v1/adapters.json`, served at
`GET /v1/adapters` and rendered at `/adapters`.
