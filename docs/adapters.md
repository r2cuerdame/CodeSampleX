# Ecosystem Adapter Capability Matrix (Public v1)

No adapter pretends a capability it does not have (goal.md §13). Levels:

```text
A0  Package/version detection (lockfile-resolved)
A1  Build/typecheck/test observation
A2  Static symbol resolution
A3  Runtime symbol instrumentation
A4  Clean Sample + Contract verification
```

Two of these describe different halves of the system, and the split is the
most useful thing this table says. A0–A2 are what the **local scanner** does
to *your* project. A4 is what the **verifier** does to a *published sample*.
An ecosystem can have one without the other, and five of them do.

| Ecosystem | Adapter | Package managers | A0 | A1 | A2 | A3 | A4 | Symbol confidence |
|-----------|---------------------|--------------------|----|----|----|----|----|-------------------|
| npm | node-typescript@1 | npm, pnpm, yarn | ✓ | ✓ | ✓ | – | ✓ | PROBABLE |
| pypi | python@1 | pip, uv (+poetry lock best-effort) | ✓ | ✓ | ✓ | – | ✓ | PROBABLE |
| golang | go@1 | go modules | ✓ | ✓ | ✓ | – | ✓ | PROBABLE |
| cargo | rust@1 | cargo | ✓ | ✓ | ✓ | – | ✓ | PROBABLE |
| composer | php@1 | composer | – | – | – | – | ✓ | UNKNOWN |
| gem | ruby@1 | bundler | – | – | – | – | ✓ | UNKNOWN |
| pub | dart@1 | pub | – | – | – | – | ✓ | UNKNOWN |
| hex | elixir@1 | mix | – | – | – | – | ✓ | UNKNOWN |
| maven | maven-java@1 | Maven | – | – | – | – | ✓ | UNKNOWN |

## Verifier images

Every stage runs in a pinned image, and the receipt records the image's
environment rather than the host's — a contract that ran in a linux
container proves nothing about the Windows machine that started it. The
images are alpine where one exists, so results carry `musl`, the dimension
that most often decides whether a package with a native module loads at all.

| Ecosystem | Runtime | Image |
|-----------|---------|-------|
| npm | node | `node:22-alpine` |
| npm | bun | `oven/bun:1-alpine` |
| npm | deno | `denoland/deno:alpine` |
| pypi | python | `python:3.12-alpine` |
| golang | go | `golang:1.26-alpine` |
| cargo | rust | `rust:1-alpine` |
| composer | php | `composer:2` |
| gem | ruby | `ruby:3-alpine` |
| pub | dart | `dart:3.13.0` |
| hex | elixir | `elixir:1.20.1-alpine` |
| maven | Java | `maven:3.9.11-eclipse-temurin-21-alpine@sha256:922927…` |

npm keys on the **runtime**, not just the ecosystem, because "does this
package work on Bun" is exactly the question this project exists to answer.
Keying on ecosystem alone verified every npm sample under Node and made the
execution-context axis (docs/execution-context.md) unusable: every sample in
the network claimed `node` because nothing else could be produced.

## How the two stages stay honest

Resolve runs with the network **on** and must never execute sample code.
Build and contract run with `--network=none`. Only the workspace is shared
between the two containers, so every toolchain cache is redirected inside it
(`/work/.csx-vendor`) — an image's `site-packages`, `GOMODCACHE` or
`CARGO_HOME` would simply be gone by the time the contract runs. npm only
appeared to work without this because `node_modules` is already a workspace
directory.

Keeping sample code from running during resolve is per-ecosystem work, not a
flag: `--ignore-scripts` for npm, `--no-scripts --no-plugins` for composer,
metadata-only fetches elsewhere. Elixir is the hard case — `mix.exs` is
executable code and every mix task evaluates it — so its resolve works from a
scratch directory with no `mix.exs` in sight, reads the pinned package set
out of `mix.lock` as text, and fetches each package with `mix hex.package
fetch`, which verifies its checksum. A hex sample without a committed
`mix.lock` cannot be verified, and because that path skips mix's own `.hex`
marker file, hex samples build and test with `--no-deps-check`.

Maven uses the same rule by refusing the sample's Maven project altogether
during resolve. Exact Maven purls in `csx.json` are the complete runtime lock.
CodeSampleX generates a resolver project and Central-only settings, invokes the
pinned dependency plugin from `/tmp` with transitive expansion disabled, and
passes only the resulting JAR directory to offline `javac`/`java` stages.

## Honest limitations, stated on purpose

- **PROBABLE, not EXACT**: static import/member analysis without a type
  checker cannot claim EXACT symbol resolution (goal.md §7.2). EXACT is
  reserved for a future TypeScript type-info / go-types integration.
- **A3 is absent everywhere**: no Public v1 adapter observes real symbol
  execution. The `SYMBOL_EXECUTED`/`SYMBOL_CALL` stages exist in the schema
  for future instrumentation (browser/worker contexts included — see
  docs/execution-context.md) and the server rejects them from clients today.
- **Five ecosystems verify but do not scan**: composer, gem, pub, hex and Maven have
  no local project adapter, so a PHP or Elixir project on your machine
  produces no evidence and no local hits. Their published samples are still
  fully verified, so an agent that names the packages it is about to use gets
  a real answer.
- **Dynamic usage degrades confidence**: Python getattr/importlib and Rust
  macro-expanded usage report `UNKNOWN` rather than guessing (goal.md §13.3,
  §13.5).
- **Deliberately narrow JVM support**: plain Java library contracts backed by a
  complete exact Maven Central JAR set are verified; Gradle, arbitrary Maven
  builds/plugins, classifiers, SNAPSHOTs and Kotlin compiler plugins remain
  unsupported. See docs/kotlin-evaluation.md.

Machine-readable source of truth: `schemas/v1/adapters.json`, served at
`GET /v1/adapters` and rendered at `/adapters`.

## Wider Wanted-only coordinates

Maven coordinates such as
`pkg:maven/org.apache.commons/commons-lang3@3.17.0` are accepted after the
exact release is confirmed on Maven Central and can back A4 samples. Maven has
no local scanner, so merely opening a Java project still produces no automatic
usage evidence.

Engines and platform SDKs are environment, not ordinary dependencies. A miss
with a fixed public `frameworks` entry such as `unity@6000.0.24f1`,
`unreal@5.6`, `android-sdk@35`, `jdk@21`, or
`windows-sdk@10.0.26100` becomes a Wanted-only `pkg:generic/engine/...` or
`pkg:generic/sdk/...` coordinate, even when no registry package was named.
The conversion uses a closed public-name allowlist; arbitrary framework
strings never leave the machine. Callers can also use open-vocabulary
execution contexts such as `unity-editor`, `unity-player`, `unreal-editor`,
`unreal-game`, `jvm`, or `android`. None of these coordinates is verification
evidence until a dedicated adapter and runner produce a signed passing
receipt.

Verification jobs carry closed worker requirements (`sandboxCapability`,
`ecosystem`, `runtime`, browser execution context/family/version/engine, and
any installed engine/SDK frameworks). A worker examines a bounded queue window
and claims only a job its local runner can prepare. Thus broad Wanted
collection never sends an unsupported browser, Unity, or other engine job to
an ordinary Docker-only worker merely because that job was first in the queue.

Browser execution evidence is not inferred from an npm package name. The
current pinned browser lane accepts only `browser / chrome 134 / chromium 134`
jobs and runs them in the Puppeteer 24.4.0 image containing Chrome for Testing
134. Node remains the harness runtime while the receipt records the actual
execution context as browser. Unknown Chrome majors and Firefox/WebKit jobs
are skipped before claim until a corresponding measured image exists.
