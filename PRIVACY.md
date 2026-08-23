# CodeSampleX Privacy Policy

**Effective 2026-08-23.** This policy covers the `csx` binary — CLI, daemon,
MCP server, peer node and contributor worker — the `codesamplex-mcp.mcpb`
bundle that ships the same binary, and the service at `https://codesamplex.dev`.

Everything below is a statement about code in this repository. Where a
sentence describes a boundary, the file that enforces it is named, so the
claim can be checked rather than believed.

---

## 1. The short version

- `csx` runs on your machine and answers from a cache on your machine.
- **Your source code, file paths, project names, dependency-tree context, the
  question you typed and your raw build logs are never transmitted.** Not in
  any mode, not to us, not to anyone.
- What community mode does transmit is a fixed, schema-bounded set of public
  facts: a public package coordinate, a public symbol name, a coarse
  environment fingerprint, one build stage, PASS or FAIL, and a rotating
  anonymous id.
- `csx init --local-only` transmits nothing at all, and makes no network
  request of any kind.
- There is no account, no login, no email address and no analytics SDK. We do
  not sell or share anything, because the only thing collected is anonymous
  public-package compatibility data.

---

## 2. Three modes, and what each one does

`csx init` asks exactly one question, and the answer is stored in
`$CSX_HOME/config.json` (default `~/.csx`).

| Mode | Set by | Network behaviour |
|---|---|---|
| **Uninitialized** | never running `csx init` | Uploads nothing. Automatic update checks are not made. |
| **local-only** | `csx init --local-only` | **No request of any kind.** `csx sync` is a complete no-op and reports `warmed shard keys: 0`; no public registry is contacted; no automatic update check is made. |
| **community** (default) | `csx init --community` | Downloads compatibility shards and samples, and uploads the anonymous evidence described in §4. |

The mode gate is enforced twice on purpose: at each operation, and again in
the HTTP transport itself (`persistedModeTransport` in
`internal/mcp/deps.go`), so a code path that forgets to check still cannot
reach the wire. `config.MayContactRegistries` is the same gate for public
package registries. The local-only contract is held by tests, including
`internal/daemon/localonly_test.go`.

Both modes are re-runnable. `csx init --local-only` at any later time stops
all transmission from that point; it does not recall data already uploaded,
because uploaded evidence is anonymous and carries nothing that identifies
you to recall it by.

---

## 3. What never leaves your machine, in any mode

- Source code and source snippets.
- File names, directory names and absolute paths.
- Repository, project and product names.
- The prose query you typed into a search.
- Your dependency tree as context. Only packages you explicitly named in a
  request are eligible to be reported as unanswered (`maxWantedPackages`,
  `internal/evidence/wanted.go`).
- Raw compiler, test and runtime logs.
- Secrets, tokens, environment variables and private URLs.
- Private, internal or unresolvable packages. A package is eligible only after
  it is confirmed to exist on its public registry
  (`internal/registry/publiccheck.go`); anything unconfirmed stays `UNKNOWN`,
  and `UNKNOWN` is excluded from evidence.
- Your raw User-Agent, when a browser execution context is recorded. It is
  parsed locally into a family and a major version and then discarded
  (`internal/environment/useragent.go`).
- Sample source you have written but not published. Publishing is a CLI step
  that requires the human to type `yes`, and it is deliberately **not** an MCP
  capability. Leakage findings — secrets, paths, project names, private URLs —
  refuse publication with no override flag.

Local state lives under `$CSX_HOME` (default `~/.csx`): `config.json`,
`identity.json`, `csx.db`, `cas/`, `samples/`, `logs/`. Deleting that
directory deletes all of it.

---

## 4. What community mode transmits, exactly

Every upload is one of the following documents and nothing else. The wire
schemas are checked into this repository under `schemas/`, and the server
rejects anything that does not validate.

### 4.1 Observation evidence — `POST /v1/evidence/batches`

Filed when you wrap a build through `csx run` or `run_observed_command`.
Schema: [`schemas/v1/observation-batch.json`](schemas/v1/observation-batch.json).

| Field | What it is |
|---|---|
| `epoch` | the calendar day, `YYYY-MM-DD` |
| `anonId` | 16 hex chars, rotates daily (§5) |
| `projectBucket` | 12 hex chars, rotates monthly (§5) |
| `package` | a **public** package purl, e.g. `pkg:npm/axios@1.12.0` |
| `symbol`, `symbolConfidence` | a public API symbol family, labelled `EXACT`/`PROBABLE`/`UNKNOWN` |
| `environment` | the coarse fingerprint below |
| `stage` | which build stage this is about: `PROJECT_COMPILE`, `PROJECT_TEST`, … |
| `result` | `PASS` or `FAIL` |
| `observationCount` | an integer |
| `errorFingerprint`, `errorCode` | a SHA-256 of a locally sanitized error, and a short code |

`additionalProperties` is `false`. There is no field for a path, a project
name, a log or a query, so there is nowhere for one to travel.

The environment fingerprint
([`schemas/v1/environment.json`](schemas/v1/environment.json)) is deliberately
coarse: ecosystem, OS and a version *bucket*, architecture, runtime and
runtime version, language, compiler, package manager, module system,
execution context, browser family and major version, engine, libc, distro id,
virtualization, container runtime, and a CI flag. No hostname, no username, no
serial number, no MAC address, no screen or hardware fingerprint.

**Errors are reduced before they are used, not before they are sent.** The
sanitizer strips paths, tokens and usernames locally and produces a
fingerprint plus a short error code (`internal/sanitizer`); the raw text is
dropped on the floor by construction and never reaches the search request, let
alone the network.

### 4.2 Unanswered questions — `POST /v1/wanted`

When a search finds nothing, the fact that *someone* asked about a public
package is worth counting. The wire shape is fixed
(`WantedReport`, `internal/evidence/wanted.go`): schema version, epoch,
`anonId`, up to ten public package purls **you named**, optionally the public
symbols — and only when your request named exactly one unambiguous package —
and optionally one of `linux`, `windows`, `darwin`.

**The question itself is never sent.** Not the prose, not the error text, not
the rest of your lockfile.

### 4.3 Search-hit counts — `POST /v1/search-hits`

When a search *does* find something, a count is filed so "how many of the
answers we gave were used" has a denominator (`internal/evidence/searchhit.go`):
schema version, epoch, `anonId`, the match grade as a bucket, how many results
were shown, an opaque local offer id, and the public sample id.

**It carries no question.** Not the query, not the packages, not the symbols,
not the environment. The local hits table that `list_local_hits` and `csx ui`
read — which does hold your query — stays on the machine.

### 4.4 Adoption reports — `POST /v1/adoptions`

Filed when you call `report_sample_adoption` or the CLI equivalent: schema
version, evidence class, epoch, `anonId`, the public sample id, whether you
applied it, and whether the build then passed.

### 4.5 Verification receipts — contributor worker only

If you run `csx worker`, results of server-assigned verification jobs are
uploaded as ed25519-signed receipts. Raw stage logs stay local. The queue
never sends an arbitrary shell command; jobs run network-off in disposable
Docker workspaces.

### 4.6 Peer announcements — peer serving only

If you enable peer serving (`peerListen`, off by default), your peer key,
port and the ids of **published** samples you hold are announced to the
tracker so others can fetch those public artifacts from you. Drafts are never
announced. This also exposes your IP address to peers that fetch from you,
which is inherent to serving files.

### 4.7 Published sample source — human-approved, CLI only

`csx sample publish` uploads the sample project you wrote, under MIT-0. This
is the one path where source you authored leaves the machine, and it happens
only when a human types `yes` at the CLI after a leakage scan passes. No MCP
tool can do it.

---

## 5. Identifiers, and what they can and cannot link

There is no account and no user id. `identity.json` holds a locally generated
ed25519 key and a random seed; nothing about it is derived from your machine,
your name or your network.

- **`anonId`** = `HMAC(seed, "anon|" + day)`, truncated to 16 hex characters.
  It changes every calendar day, so two uploads a week apart are not linkable
  by it.
- **`projectBucket`** = `HMAC(seed, "proj|" + absolute path + "|" + month)`,
  truncated to 12 hex characters. It exists so one project cannot be counted
  as many, and it changes every month. The path is HMAC input only and is
  **not recoverable** from the 12-hex output.
- **Peer id** = the first 16 hex characters of the SHA-256 of your ed25519
  public key. Used only when peer serving is on.

These are pseudonyms with a bounded lifetime, not anonymity against a
determined adversary who already knows what you built and when. We say that
plainly rather than claiming more.

---

## 6. Network requests that are not uploads

In community mode `csx` also makes ordinary read requests. They carry no
document about you, but — like any HTTPS request — the server on the other end
sees your IP address.

| Request | To | Why |
|---|---|---|
| `GET /v1/shards/{key}` | codesamplex.dev | download the compatibility shard for a package you asked about. This does tell our server which public package you are interested in. |
| `GET /v1/samples/{id}/artifact`, `GET /v1/peers/for-sample/{id}` | codesamplex.dev, then peers | fetch a public sample by content id |
| `GET /v1/stats` | codesamplex.dev | the public network rollup |
| package existence probes | npmjs.org, pypi.org, crates.io, proxy.golang.org, rubygems.org, repo.packagist.org, hex.pm, pub.dev | confirm a package is public **before** it is eligible to be reported. Community mode only; these registries see the package names, under their own privacy policies. |
| `GET .../releases/latest/download/csx-update-stable.json` and the release asset | github.com | the signed update manifest and binary. Community mode only; GitHub sees the request under its own privacy policy. `csx config set autoUpdate off` stops it. |

`csx ui` opens a local dashboard with a **privacy preview** that shows the
exact pending payloads before they leave.

---

## 7. What the server stores

- **Evidence, wanted rows, adoption reports and receipts** are stored as the
  anonymous documents in §4 and aggregated into the public compatibility data
  the website and `/v1/*` serve. They are the product; they are not profiles.
- **Published sample source** is public, MIT-0 by default.
- **Web and API access logs** are deliberately reduced at the edge: the safe
  log drops `remote_ip` entirely and keeps only status, a coarse method bucket
  and a fixed route label, with the whole request object deleted before bytes
  reach disk (`deploy/caddy/Caddyfile`). Rolls are bounded to 31 days.
- **API activity counters** store keyed, epoch-scoped pseudonyms derived from
  the IP address rather than the address itself (`internal/activity`). We state
  the residual risk rather than rounding it away: **anyone who compromises both
  that database and the colocated HMAC key could enumerate the IPv4 space and
  recover the addresses those buckets represent.** Buckets are pruned to 35
  daily and 13 monthly epochs.
- **Optional sign-in.** `csx login` uses a GitHub device flow and exists only
  for sample authoring and seeding. It stores a token locally and a login name
  in `config.json`. Ordinary use — search, evidence, samples, receipts, the
  wanted board — needs no account at all.

---

## 8. Third parties

- **GitHub** — releases, update manifests, the repository, and the optional
  authoring sign-in.
- **Public package registries** — npm, PyPI, crates.io, the Go module proxy,
  RubyGems, Packagist, Hex, pub.dev — contacted in community mode to confirm
  that a package is public.
- **Peers** — other CodeSampleX installations, when they fetch a public sample
  you serve.

There is no analytics provider, no advertising network, no tag manager and no
third-party script on the website.

---

## 9. Children

CodeSampleX is a developer tool and is not directed at children. It collects
no name, email address, age or contact information from anyone.

---

## 10. Your controls

| You want to | Do this |
|---|---|
| stop all transmission | `csx init --local-only --yes` |
| see exactly what would be sent | `csx ui`, privacy preview |
| exclude a package from evidence | `csx config set excludedPackages '["pkg:npm/name@1.0.0"]'` |
| stop automatic update checks | `csx config set autoUpdate off` |
| delete everything local | delete `$CSX_HOME` (default `~/.csx`) and the binary |
| never register with an agent | `csx init --no-agents` |

Uploaded evidence carries no identifier tied to you, so there is nothing to
look up and nothing to delete on request — which is the point of collecting it
this way. If you published a sample and want it withdrawn, open an issue.

---

## 11. Changes, and how to reach us

This file is the policy. It is versioned in the repository, so every change to
it is a diff with a date and an author, and the MCPB bundle's
`privacy_policies` array points here rather than at a page that can be edited
without a trace.

- Questions or a withdrawal request: <https://github.com/r2cuerdame/CodeSampleX/issues>
- Security or privacy vulnerabilities: use the private reporting channel in
  [SECURITY.md](SECURITY.md), not a public issue.

Related reading: [README.md](README.md) ("The contract"),
[goal.md](goal.md) §5 (usage modes and the network contract), §8.5 (error sanitization) and §8.6 (anonymity),
[docs/operations.md](docs/operations.md).
