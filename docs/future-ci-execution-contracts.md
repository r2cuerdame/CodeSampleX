# Future: CI Execution Contracts and Recovery Graphs

Status: deferred product exploration. This is deliberately outside the current
30-day sample-production scope; recording it now prevents the idea from being
lost without distracting the active factory.

## Thesis

Automation already spends money and time producing useful intermediate facts,
then commonly retains only a final green/red result. CodeSampleX may eventually
recover those already-paid-for facts for reusable development components that
are not package registries.

The general identity is:

```text
reusable component × exact version/ref × execution environment × contract result
```

An npm package is one component type. Potential later types include:

- GitHub Actions such as `actions/setup-node@v4`
- runner images such as `ubuntu-latest` resolved to an exact image release
- container images and immutable digests
- compiler/toolchain versions
- SDKs and CLIs
- build plugins and declarative framework configuration

Do not refactor the current package model merely to anticipate this. Preserve a
future migration path to a generic `ComponentIdentity` with a typed namespace.

## Product candidates

### 1. GitHub Actions execution contracts

Capture whether an exact action/ref and option set actually worked on a recorded
runner image and toolchain. For example:

```yaml
component: github-action/actions/setup-node@v4
runner: ubuntu-latest@<resolved-image>
inputs:
  node-version: "22"
  cache: npm
result: PASS | FAIL
```

The reusable result is the declarative contract and sanitized environment—not a
repository's source, secrets, paths, workflow logs, or private dependency names.

### 2. Recovery Graph

Link a failed execution to the later passing execution that actually recovered
it:

```text
failure fingerprint
  → dependency/config/toolchain change
  → next execution result
  → confirmed recovery edge
```

A recovery edge is evidence only when the before/after executions are correlated
and the later contract passes. Temporal adjacency alone must never be presented
as causation.

### 3. Reasoning/build decision cache

Reuse a prior classification for the same normalized failure and environment so
an agent does not repeatedly analyze an already-known flaky network error,
unsupported version, or invalid option combination. This complements artifact
caches: it caches a measured decision, not build output.

### 4. Waste Ledger

Privately measure already-paid work that was repeated or discarded, for example:

- identical failure fingerprints re-analyzed
- dependencies or environments reconstructed without reusable cache hits
- previously rejected fixes retried under the same conditions
- LLM approaches repeated after an equivalent measured failure

The ledger must report measured counts and durations only. It must not invent
time-saved or money-saved estimates unless their inputs and assumptions are
shown separately.

## Privacy and trust boundary

The first useful version should be private/local or organization-scoped. Public
contribution may include only allowlisted public component coordinates, resolved
versions, sanitized environment dimensions, stage verdicts, and bounded error
fingerprints. Never upload source, workflow text, raw logs, repository identity,
paths, environment variables, secrets, or private component names.

The server must never send arbitrary shell commands to a contributor. Execution
jobs remain declarative and are interpreted by fixed, sandboxed adapters.

## Revisit gate

Revisit after the current producer is stable and the package corpus has an
evidence-backed depth baseline. A first experiment should use one public GitHub
Action family and prove all of the following before widening scope:

1. exact action/ref and resolved runner image identity;
2. deterministic, declarative contract execution;
3. failure-to-pass correlation without false recovery claims;
4. zero private repository or raw-log leakage;
5. a local dashboard showing a measured avoided-repeat event;
6. clear separation between canonical contracts, execution evidence, and
   inferred recovery edges.

## Research questions for later

- Which CI systems expose enough exact runner/action identity without parsing
  private workflow source?
- What constitutes the smallest safe recovery correlation token?
- Which outcomes remain project-private, and which public component facts are
  safe and useful to aggregate?
- Can runner-image and action-ref changes be isolated one variable at a time?
- Does a private Recovery Graph deliver value before any public network effect?

