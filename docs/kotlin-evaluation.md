# Kotlin / JVM as a fifth ecosystem — evaluated, not adopted

**Verdict: not adopted.** Both JVM build tools break the two-phase sandbox
guarantee that npm, PyPI, Go and Cargo all satisfy, and the break is
structural rather than a missing flag. Everything below was measured, not
reasoned about.

## What the sandbox guarantees today

Verification runs in two containers (`internal/sandbox/docker.go`):

| stage | network | may sample code run? |
|---|---|---|
| resolve | **on** | no — `npm ci --ignore-scripts`, `pip install --no-deps`, `go mod download`, `cargo fetch` all fetch without executing project code |
| build / contract | **off** (`--network=none`) | yes |

That ordering is the whole point: untrusted code only ever executes with no
network. A JVM ecosystem has to fit it.

## Gradle: the build script is executable code

`build.gradle.kts` is a Kotlin program that Gradle executes at
configuration time, before any dependency is resolved. A resolve stage that
invokes Gradle therefore runs attacker-controlled code **with the network
on** — precisely the exposure `--ignore-scripts` exists to remove on npm.

Measured: `gradle --no-daemon dependencies` resolved fine inside
`--memory=512m` in 2m11s, so resource limits are not the obstacle. The
obstacle is that resolving at all means executing the sample.

## Maven: declarative, but its plugins resolve lazily

`pom.xml` is declarative, so Maven clears the bar Gradle fails. It fails a
different one: surefire resolves its *provider* tree when the test goal
runs, not when dependencies are declared, so a warm-up cannot see it coming.

Measured, each step fixing the previous error and revealing the next:

```
mvn dependency:go-offline          → offline test fails: surefire-junit-platform absent
mvn -DskipTests test               → same (skipping tests skips the resolution too)
declare surefire-junit-platform    → offline test fails: junit-platform-commons:1.9.3 absent
```

Surefire pins provider dependencies at versions the project never names, so
the list is not derivable from the pom. Producing a complete offline
repository means running the tests once with the network up — which is the
Gradle problem again.

## What would make it work

Three options, all requiring a decision rather than more code:

1. **Accept a weaker guarantee for JVM only** — resolve runs the full
   `mvn test` with network on, contract re-runs it offline. Honest, but it
   means JVM samples execute with network access, and the receipt should
   then say so rather than implying the same isolation as the others.
2. **Vendor the repository** — ship each sample with its `.m2` subset. Blows
   past the 256KB artifact limit (contract C13) immediately.
3. **A JVM-specific runner** — resolve with a dependency resolver that is
   not the build tool (Coursier resolves Maven coordinates without
   executing project code), then run the contract with a plain
   `java -cp`/`kotlinc` invocation. This is the only option that keeps the
   guarantee intact, and it is real work: a resolver integration plus a
   contract runner that does not go through Gradle or Maven at all.

Option 3 is the recommendation whenever JVM support is worth that cost.
Until then the adapter matrix should not list a JVM ecosystem, because
claiming A4 for it would claim an isolation property the pipeline cannot
deliver.

## Cost of the four ecosystems that do work

For comparison, adding an ecosystem that fits the model is small: a pinned
image, a resolve command, and cache environment variables pointing inside
the mounted workspace. That is all pypi, golang and cargo needed.
