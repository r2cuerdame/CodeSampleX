# Java verification adopted narrowly through generated Maven and Gradle lanes

**Current verdict:** Java libraries from Maven Central now have a narrow A4
verifier through `maven-java@1` and `gradle-java@1`. Arbitrary Maven/Gradle
builds and Kotlin remain unsupported. The original measurements below remain
the reason the adapters generate trusted projects instead of running the
sample's projects.

## What the sandbox guarantees today

Verification runs in two containers (`internal/sandbox/docker.go`):

| stage | network | may sample code run? |
|---|---|---|
| resolve | **on** | no — `npm ci --ignore-scripts`, `pip install --no-deps`, `go mod download`, `cargo fetch` all fetch without executing project code |
| build / contract | **off** (`--network=none`) | yes |

That ordering is the whole point: untrusted code only ever executes with no
network. A JVM ecosystem has to fit it.

## Why the sample's Gradle build is still forbidden during resolve

`build.gradle.kts` is a Kotlin program that Gradle executes at
configuration time, before any dependency is resolved. A resolve stage that
invokes Gradle therefore runs attacker-controlled code **with the network
on** — precisely the exposure `--ignore-scripts` exists to remove on npm.

Measured: `gradle --no-daemon dependencies` resolved fine inside
`--memory=512m` in 2m11s, so resource limits are not the obstacle. The
obstacle is that resolving through the sample project means executing the
sample. `gradle-java@1` does not do that.

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

## The adopted JVM-specific runners

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

`gradle-java@1` applies the same exact-JAR lock with a different trusted tool:

1. The host generates a Gradle resolver project and Central-only settings.
   It never copies or evaluates the sample's `build.gradle(.kts)`,
   `settings.gradle(.kts)`, init scripts, wrapper, `gradle.properties`, or
   plugins in the network-enabled stage.
2. Gradle runs from `/tmp` in the digest-pinned Gradle 8.14.3/JDK 21 image,
   with an empty workspace `GRADLE_USER_HOME`, transitive expansion disabled,
   and dynamic `+` selectors rejected.
3. Resolve fails unless Gradle's exact resolved coordinate set equals the
   manifest closure. JARs are copied to coordinate-shaped paths and hashed.
4. Network-off build and contract use a second generated Gradle project with
   only the built-in Java plugin. It compiles `src/main/java` and the
   default-package `test/Contract.java` with release 21, then runs `Contract`
   through a built-in `JavaExec` task under `--offline --network=none`.

This supports normal Java library contracts while leaving the sample's Gradle
build files inert. It does not claim arbitrary Maven/Gradle applications,
sample plugins, annotation-processor build pipelines, Kotlin compiler plugins,
or Maven/Gradle plugin APIs.

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

Option 3's security property is now implemented for the exact-JAR Maven Central
subset in two lanes: plain Maven tooling and real Gradle tooling both operate
only on host-generated projects. The adapter matrix claims only A4; it makes no
local-project A0/A1/A2 claim and no Kotlin or arbitrary-build claim.

## Why the earlier adapters were smaller

For comparison, adding an ecosystem that fits the model is small: a pinned
image, a resolve command, and cache environment variables pointing inside
the mounted workspace. That is all pypi, golang and cargo needed.
