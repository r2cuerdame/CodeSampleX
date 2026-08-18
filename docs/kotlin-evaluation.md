# Java/Maven verification adopted narrowly; Gradle/Kotlin still excluded

**Current verdict:** Java libraries from Maven Central now have a narrow A4
verifier. Gradle and arbitrary Maven builds are still not adopted. The original
measurements below remain the reason for that boundary.

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

## Why ordinary Maven builds are still unsafe

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

## The adopted JVM-specific runner

The implementation is the earlier option 3, constrained to what it can prove:

1. `pom.xml`, `.mvn`, project settings, extensions and build plugins are never
   used in the network-enabled stage.
2. Every runtime JAR is listed as an exact canonical Maven purl in
   `csx.json`. That complete list is the lock; SNAPSHOTs, ranges, classifiers
   and non-JAR packaging are rejected.
3. The host writes a fresh resolver POM and settings below `.csx-vendor`.
   Settings mirror every repository to Maven Central. Maven runs the pinned
   `maven-dependency-plugin:3.9.0` from `/tmp`, with strict checksums and
   transitive expansion disabled.
4. Only the generated JAR directory crosses into the network-off build and
   contract stages. Samples compile and run with plain `javac`/`java`; they do
   not need surefire or any project plugin.
5. The resolve log records the exact coordinate list and SHA-256 of every JAR,
   and the receipt reports every manifest package actually found in the fresh,
   Central-only local repository.

This supports normal Java library contracts. It does not claim that arbitrary
Maven applications, annotation-processor builds, Kotlin compiler plugins,
Gradle projects or Maven plugin APIs are supported.

## Options considered before adoption

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

Option 3 is now implemented for the exact-JAR Maven Central subset above. The
adapter matrix claims only A4 for that subset; it makes no local-project A0/A1/
A2 claim and no Gradle/Kotlin claim.

## Why the earlier adapters were smaller

For comparison, adding an ecosystem that fits the model is small: a pinned
image, a resolve command, and cache environment variables pointing inside
the mounted workspace. That is all pypi, golang and cargo needed.
