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

The evidence model is not limited to libraries. Engines, SDKs, operating
systems, built-in commands and standalone CLIs are fixed public `generic`
targets in the Wanted/search path. For example, npm-the-registry package
`pkg:npm/npm@...` and npm-the-command `pkg:generic/cli/npm@...` are different
subjects. The latter does not claim A4 until a pinned `system-cli` verifier
image for that exact tool/OS combination exists; recording demand early is
not the same as pretending it has already been verified.

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
| maven | gradle-java@1 | Gradle | – | – | – | – | ✓ | UNKNOWN |

## Verifier images

Every stage runs in a **digest-pinned** image, and the receipt records both
the image's environment and the image itself. The environment is recorded
rather than the host's — a contract that ran in a Linux container proves
nothing about the Windows machine that started it. Images use Alpine where
that verifier provides it; the exact Java matrix uses Amazon Linux 2023 and
records `amzn`/`glibc` instead.

The tag is an alias for humans; the digest is what runs. `docker run
node:22-alpine` is whatever the registry points at today, and on a worker
that has run before it is whatever already sits in the local cache under that
name — docker does not re-check a tag it already has. Two honest workers
could therefore sign receipts naming the same environment for runs of
different software. Every selector instead returns `<alias>@sha256:<digest>`,
which docker resolves by content, so a stale local tag is unreachable and the
same lane runs the same bytes on every worker.

`internal/sandbox/images.go` is the single registry. Nothing else in the tree
holds a digest, and the base/libc a receipt reports lives beside the digest it
describes rather than in a parallel table that can drift from it.

| Ecosystem | Runtime | Image (alias @ pinned digest) | Base |
|-----------|---------|-------------------------------|------|
| npm | node | `node:22-alpine@sha256:c610fcdfb1d5…` | alpine/musl |
| npm | bun | `oven/bun:1-alpine@sha256:07235578f79e…` | alpine/musl |
| npm | deno | `denoland/deno:alpine@sha256:b49ac52f05c3…` | alpine/musl |
| npm | node + Chrome 134 (browser) | `ghcr.io/puppeteer/puppeteer:24.4.0@sha256:ca2087099ad5…` | debian/glibc |
| pypi | python 3.12 (default) | `python:3.12-slim@sha256:09f7da3bc104…` | debian/glibc |
| pypi | python 3.14 | `python:3.14-slim@sha256:cae66f2ef0ec…` | debian/glibc |
| golang | go | `golang:1.26-alpine@sha256:28d89ee9cc0f…` | alpine/musl |
| cargo | rust | `rust:1-alpine@sha256:a10e64dd139b…` | alpine/musl |
| composer | php | `composer:2@sha256:4d71c3c2109c…` | alpine/musl (despite the tag) |
| gem | ruby | `ruby:3@sha256:364bd08657bc…` | debian/glibc |
| pub | dart | `dart:3.13.0@sha256:8b6175f6c6b8…` | debian/glibc |
| hex | elixir | `elixir:1.20.1-alpine@sha256:f50894ff69b0…` | alpine/musl |
| maven | Java (runtime omitted; legacy default) | Maven `3.9`, Java 21, `maven:3.9.11-eclipse-temurin-21-alpine@sha256:922927df2c66…` | alpine/musl |
| maven | Java 8 / 11 / 17 / 21 / 25 (exact opt-in) | Maven `3.9.11`, `maven:3.9.11-amazoncorretto-<jdk>-al2023@sha256:…` | `amzn` 2023/glibc |
| maven | Java 8 / 11 / 17 / 21 (Gradle, exact) | Gradle `8.14.3`, `gradle:8.14.3-jdk<jdk>-corretto-al2023@sha256:…` | `amzn` 2023/glibc |
| maven | Java 25 (Gradle, exact) | Gradle `9.7.0`, `gradle:9.7.0-jdk25-corretto-al2023@sha256:bb35f0164972…` | `amzn` 2023/glibc |

On a Windows container daemon, Go and Python are verified against
`golang:1.26-windowsservercore-ltsc2022@sha256:2a3365ef5cb3…`,
`python:3.12-windowsservercore-ltsc2022@sha256:035418c04b5e…` and
`python:3.14-windowsservercore-ltsc2022@sha256:f7af89224fd7…`. Node publishes
no official Windows image, and Java and browser work have none either, so a
Windows worker skips that work and keeps scanning rather than verifying it
against an image this project would have to build itself.

The digest is the multi-platform **index** digest, not one platform's
manifest. Nothing passes `--platform`, so an arm64 worker runs the same
pinned entry on its own architecture; which architecture it actually was is
recorded separately in the receipt's environment.

### Which image ran: the receipt says so

A `schemaVersion` 2 receipt carries `verifierImage`:

```json
"verifierImage": {
  "reference": "node:22-alpine@sha256:c610fcdfb1d5…",
  "digest": "sha256:c610fcdfb1d5…"
}
```

It is inside the canonical JSON the peer signature covers, so it cannot be
attached, changed or stripped by anyone relaying the document, and the sample
page shows it beside the run it belongs to.

The field is **optional and omitted when not established**. The native
fallback runner never enters a container, and receipts signed before this
field existed carry none. An absent `verifierImage` means *this receipt does
not say which bytes ran* — never *the default image*. Old receipts keep their
exact content hash, and the server still accepts them; what it refuses is a
present claim that contradicts itself (a mutable tag, a malformed digest, or a
`digest` that is not the one in `reference`), and any `verifierImage` on a
`schemaVersion` 1 document.

`digest` is the registry content digest, deliberately **not** the daemon's
local image ID. The local ID is a property of the image store — a
containerd-backed daemon reports the index digest there while a classic graph
driver reports the config digest — so signing it would make two workers
running byte-identical images disagree, which is the exact property the field
exists to establish.

### Refreshing a verifier image

Moving a pin is a deliberate, reviewed change. A tag moving upstream cannot
reach the registry on its own, which is the point.

1. Resolve the candidate: `docker buildx imagetools inspect <alias>` and take
   the top-level `Digest:` — the index digest, not one of the per-platform
   manifests listed underneath.
2. Edit the single entry in `internal/sandbox/images.go`. The alias stays
   readable; the digest is the only line that decides what runs.
3. Re-measure the base. `go test ./internal/sandbox/ -run
   TestImageBaseMatchesTheRealImage` runs the pinned reference and fails if
   the recorded bucket/libc no longer describes it. A name is not evidence:
   `composer:2` is an Alpine image whose tag does not say so, and for as long
   as the base was inferred from the name every PHP receipt this project
   produced claimed glibc for a run on musl.
4. Land it as a reviewed change with the measurement in the commit.

A digest change is a new measurement environment, not a correction of the old
one. Receipts signed against the previous digest keep saying exactly what they
said: that contract passed in *those* bytes. Nothing rewrites them, and
nothing about the new pin makes the old statement less true.

npm keys on the **runtime**, not just the ecosystem, because "does this
package work on Bun" is exactly the question this project exists to answer.
Keying on ecosystem alone verified every npm sample under Node and made the
execution-context axis (docs/execution-context.md) unusable: every sample in
the network claimed `node` because nothing else could be produced.

PyPI additionally keys on the manifest's runtime version. An omitted version
or `3.12` keeps the established Python 3.12 verifier; exact `3.14` selects the
digest-pinned Python 3.14 Alpine image. Other Python runtime lines are rejected
before a worker claims the job rather than being run under a different Python.

Java also keys on the manifest's exact runtime version. For `maven-java@1`, an
omitted runtime retains the original Java 21 Temurin/Alpine lane for backward
compatibility. Explicit `8`, `11`, `17`, `21`, or `25` selects the matching
Maven 3.9.11 Corretto/AL2023 image. `gradle-java@1` always requires one of
those exact versions: Java 8–21 use Gradle 8.14.3, while Java 25 uses Gradle
9.7.0 because Gradle 8.14.3 cannot run there. That Java 25 receipt therefore
records a package-manager change as well as a JDK change; it is not presented
as a pure-JDK comparison.

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

Gradle support keeps that boundary rather than weakening it. The network-on
stage does **not** run the sample's `build.gradle`, `settings.gradle`, init
scripts, wrapper, `gradle.properties`, or plugins. The host writes a trusted
Central-only Gradle resolver, runs it from `/tmp` with an empty
`GRADLE_USER_HOME`, disables transitive expansion, and fails unless the exact
resolved coordinate set equals the manifest's complete Maven purl closure.
Ranges, `SNAPSHOT`s, Gradle's dynamic `+` selectors, classifiers and non-JAR
packaging are rejected. Each resolved JAR is copied into a coordinate-shaped
workspace path and hashed for receipt evidence.

Build and contract are also fixed generated Gradle projects, but run in fresh
containers with `--network=none` and Gradle `--offline`. The supported source
convention is `src/main/java` plus a default-package `test/Contract.java`.
The generated build targets the manifest's declared Java language version, or
the selected runtime line when that field is omitted (`sourceCompatibility`
and `targetCompatibility` on Java 8; `--release` on newer JDKs), and a built-in
`JavaExec` task runs `Contract`. This proves Gradle itself performed the
offline compile and contract without granting sample build logic network
access.

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
  complete exact Maven Central JAR set are verified through generated Maven or
  Gradle projects. Arbitrary sample builds/plugins, classifiers, SNAPSHOTs,
  dynamic Gradle selectors and Kotlin compiler plugins remain unsupported. See
  docs/kotlin-evaluation.md.

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
verifier adapter, `ecosystem`, `runtime`, exact runtime version, execution
context, browser family/version/engine, and any installed engine/SDK
frameworks). A worker polls both independent-cross and matrix work, examines a
bounded queue window, and claims only a job its local runner can prepare
exactly. Matrix work keeps the content-addressed artifact immutable: the
requested environment is overlaid on an in-memory execution manifest, never
written into the artifact. Thus broad Wanted collection never sends an
unsupported browser, Unity, JDK, or other engine job to an ordinary
Docker-only worker merely because that job was first in the queue.

Browser execution evidence is not inferred from an npm package name. The
current pinned browser lane accepts only `browser / chrome 134 / chromium 134`
jobs and runs them in the Puppeteer 24.4.0 image containing Chrome for Testing
134. Node remains the harness runtime while the receipt records the actual
execution context as browser. Unknown Chrome majors and Firefox/WebKit jobs
are skipped before claim until a corresponding measured image exists.
