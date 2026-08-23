# Security policy

## Reporting a vulnerability

**Report privately, not in a public issue.**

Use GitHub's private vulnerability reporting:
[github.com/r2cuerdame/CodeSampleX/security/advisories/new](https://github.com/r2cuerdame/CodeSampleX/security/advisories/new).
It is the only channel with a private thread attached to this repository, and
it is the one to use even if you are unsure whether what you found counts.

If that form is unavailable to you, open a public issue that says only *"I
have a security report, please open a private channel"* — with no details —
and wait to be contacted.

This is a small project with no security team and no paid triage. There is no
bug bounty. What there is: an acknowledgement, and a fix or a written reason
there will not be one.

## What is in scope

The parts of this project that execute code, verify signatures, or decide what
leaves a machine:

- **The installers** — `install.sh`, `install.ps1`, and what they fetch from
  `codesamplex.dev`. Anything that lets a third party change what a user
  installs, or that weakens the SHA-256 check on the downloaded binaries.
- **The signed updater** — the Ed25519 manifest, its verification, the atomic
  replace, and `csx update rollback`. Anything that lets an unsigned or
  downgraded binary be installed.
- **The verification sandbox** — resolve, compile and contract stages run
  downloaded sample code. Anything that escapes the Docker workspace, reaches
  the network where the stage is meant to be network-off, or runs sample code
  on the host.
- **`run_observed_command`** — the MCP tool that runs a command. Anything that
  lets a *remote* input choose or alter the command that runs.
- **The sanitizer and the upload path** — anything that causes source, paths,
  project names, secrets, raw logs or private package names to leave a machine,
  in any mode; and anything that causes an upload at all in `local-only` mode.
- **The public server** — `csx-server`, its REST API and the explorer:
  authentication and authorization on the write endpoints, receipt signature
  verification, and anything that lets an unverified sample present as
  verified.
- **Sample publication** — the leakage scan that blocks publishing. A way to
  publish source that the scan should have refused is a vulnerability, not a
  false negative to be tuned.

## What is not a vulnerability

- **A sample that is wrong, or evidence that is wrong.** The network reports
  what ran; a wrong answer is a bug, and belongs in a normal issue.
- **A missing grade.** `NO_SAFE_MATCH` is a real answer.
- **Volumetric denial of service** against the public server. Rate limits
  exist and are documented as best-effort. A *logic* denial of service — one
  cheap request that costs the server unboundedly — is in scope.
- **Findings from an automated scanner with no demonstrated impact.**

## Handling

Reports are read by one maintainer. You will get a first response as soon as
one is realistically possible, and it will be an honest one — including "I do
not know yet". Fixes ship in a normal signed release; the advisory is
published when the fix is available, and credits you unless you ask otherwise.

Please give the fix a chance to reach installed clients before disclosing.
`csx update` is automatic in community mode, so that window is short.
