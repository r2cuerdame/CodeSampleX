# Distribution dossier

Everything needed to list CodeSampleX in the MCP directories that will take it.
Each destination below is a paste-ready block: where it goes, how it is
submitted, every field it asks for with the value already filled in, and what
blocks it.

**This document is a dated snapshot, not a live status page.** Every
destination state, version number, asset list and blocker below was first
checked on **2026-08-14** and has been drifting ever since. Read every
present-tense sentence in it as "as of 2026-08-14" unless a later date is
written next to it; where one is, that line was re-checked then and the rest
was not.

Re-checked on **2026-08-23** (R2C-62): §1's table, §2's B1/B2/B3 and the new
B8, §4's shared values, §7 (Glama), §15 (Anthropic desktop-extension form) and
§17's order. Everything else in this file still carries its 2026-08-14 date.

Three ground rules for anything pasted from here:

- **Re-verify before you paste.** Directories change their forms often, the
  release train moves the versions, and a blocker listed here may already be
  closed. Check the destination page and the live values first — the copy
  blocks are drafts to edit, not values to trust.
- **Do not inflate adoption.** As of 2026-08-14 the project had zero external
  users and the adoption detector showed one peer bucket, which was this
  machine. (`GET /v1/stats` still reported `"peers":1` on 2026-08-23.) No text
  in this document implies otherwise, and none should be edited to.
- **Every claim was checked against the repository or the live site** at the
  time it was written. If a field wants something we cannot honestly assert,
  the block says so instead of supplying a value.

---

## 1. Current state, verified

| Destination | State on **2026-08-23** (re-measured) | Evidence |
|---|---|---|
| Official MCP Registry | **Published and current.** `io.github.r2cuerdame/codesamplex`, 31 versions from 0.1.0 to **0.1.44**; 0.1.44 is `isLatest`, status `active`, published 2026-08-23T04:44Z. Publishing is automated by the release workflow and needs no attention. | `GET https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.r2cuerdame/codesamplex&limit=100` |
| Glama | **Auto-indexed, still UNCLAIMED, and stale.** "This server cannot be installed"; Schema tab shows `No tools`, "Server capabilities have not been inspected yet", "This server publishes no instructions"; Maintainers empty despite `glama.json` being checked in; quality "Not graded — not tested". Its README snapshot predates the R2C-61 merge (it still draws the retired ✓ PASS grid and the 90-day half-life). The directory API returns `"tools": []` and a **product description we did not write** — "helps coding LLMs avoid re-solving common problems by serving verified minimal code samples" — which is the old reasoning-cache framing. | `https://glama.ai/mcp/servers/@r2cuerdame/CodeSampleX`, `/schema`, and `GET https://glama.ai/api/mcp/v1/servers/r2cuerdame/CodeSampleX` |
| Smithery | Not re-checked on 2026-08-23. Was **not listed** on 2026-08-14. | `https://smithery.ai/search?q=codesamplex` |
| GitHub repo | **2 stars**, 0 forks, 0 watchers. Latest release **v0.1.44** (2026-08-23). | `GET https://api.github.com/repos/r2cuerdame/CodeSampleX` |
| Release assets (v0.1.44) | 13 assets: `codesamplex-mcp.mcpb` (19.7 MB) + `.sha256`, eight `csx-*` binaries, two `csx-launcher-windows-*`, `csx-server-linux-amd64`, `csx-update-stable.json`, `SHA256SUMS.txt` | `GET https://api.github.com/repos/r2cuerdame/CodeSampleX/releases/latest` |
| Shipped MCPB (**v0.1.44**) | Downloaded from the release, `sha256sum -c` **OK** (`df3627a0…`, the same digest the registry publishes as `fileSha256`, which is what a client verifies before installing). `mcpb validate` **passes**. `privacy_policies` = the PRIVACY.md URL. Its bundled `csx-windows-amd64.exe` answers `initialize` → `tools/list` (8 tools, **every one with `title` and full `annotations`**) → `tools/call get_local_stats`. B2 and B3 are closed **in the published artifact**, not only in the tree. | `npx @anthropic-ai/mcpb@2.1.2 unpack` + `validate`, then a piped stdio handshake |
| Shipped MCPB (v0.1.43, superseded) | Same procedure on 2026-08-23 before the release: **no `privacy_policies`**, tool entries `name`/`description` only, `tools/list` with **no `title` and no `annotations`**. Recorded because it is the state B2 and B3 were written against. | as above |

Two facts that follow from this and that several blocks below depend on:

- **There is no remote MCP endpoint.** `csx-server` serves a REST API
  (`/v1/evidence/batches`, `/v1/samples`, `/v1/search`, `/v1/stats`,
  `/v1/peers/*`) and the explorer. There is no streamable-HTTP or SSE MCP
  transport anywhere in `internal/httpapi`. Every "remote MCP server"
  destination is therefore out on technical grounds, not just policy.
- **The MCPB bundle is the distribution artifact.** `csx` is a stdio MCP server
  shipped as one binary per platform inside `codesamplex-mcp.mcpb`, and three
  destinations (Smithery, Anthropic's desktop-extension form, the MCP Registry)
  take that bundle directly.

---

## 2. Blockers that gate more than one destination

Fix these once and several blocks unblock together. Each was checked, not assumed.

### ~~B1 — There is no logo.~~ CLOSED 2026-08-23.

Was: `find` over the working tree returned no `.png`, `.svg` or `.ico` outside
`.git/`, and there was no `assets/` directory. Cline's submission form makes a
**400×400 PNG required**; Glama, mcp.so, MCP Market and Anthropic's
desktop-extension form all take an icon and look unfinished without one.

The directory landed. `assets/logo-400.png` (400×400) and `assets/favicon-32.png`
are checked in, generated from primitives by `assets/make-logo.py`, and
documented in [`assets/README.md`](../assets/README.md). The value to paste:

```text
https://raw.githubusercontent.com/r2cuerdame/CodeSampleX/main/assets/logo-400.png
```

Both are RGBA with transparent rounded corners; a destination that demands a
fully opaque image needs it composited over `#0f1317` first.

Still open, and a different problem: **the brand surface is not one thing.**
`assets/README.md` describes the mark as a check inside an X, the site header
and favicon render a shield-and-check SVG, and the README hero is an inspector
illustration. Three surfaces, three marks. Deciding which is the primary logo,
which is the mascot, and which is the UI mark — and then making OG image,
favicon and directory assets agree — is tracked separately and is not
something to resolve while filling in a submission form.

### ~~B2 — No privacy policy in the MCPB manifest or the README.~~ CLOSED 2026-08-23.

Anthropic's submission docs, on local connectors:

> Local connectors must include:
> 1. "Privacy Policy" section in README.md
> 2. `privacy_policies` array in manifest.json (manifest_version 0.2+)
> 3. HTTPS URLs to privacy policies
>
> **Missing or incomplete privacy policies result in immediate rejection.**

Was: `manifest_version: "0.3"` and no `privacy_policies` key; a README section
called "The contract" that describes exactly what is and is not transmitted,
but no section titled "Privacy Policy" and no policy URL.

All three now exist:

1. [`PRIVACY.md`](../PRIVACY.md) — the policy, written from the code rather
   than from the marketing copy. Every upload document is listed field by
   field against its checked-in wire schema, and the file that enforces each
   boundary is named.
2. `## Privacy Policy` in `README.md` **and in all eight translations** —
   `internal/web/publiccopy_test.go` treats the nine READMEs as one document,
   so a section that lands in one of them and not the others is a test
   failure.
3. `"privacy_policies": ["https://github.com/r2cuerdame/CodeSampleX/blob/main/PRIVACY.md"]`
   in `scripts/make-mcpb.py`, pinned by
   `internal/mcp/mcpbcatalog_test.go`.

**Why the GitHub URL and not `codesamplex.dev/privacy`.** The site has no
`/privacy` route (checked: `https://codesamplex.dev/privacy` → 404 on
2026-08-23), and adding one means a manual Lightsail deploy — there is no
deploy workflow, `deploy/lightsail/deploy.ps1` is run by hand with the SSH
key. A manifest is reviewed against the URL it contains, so it must contain a
URL that is live. The repository URL is live the moment a change to the policy
merges, and it is versioned: every edit to it is a diff with a date and an
author, which a served page is not. A `codesamplex.dev/privacy` page that
renders the same source is a reasonable later addition; it is not a
prerequisite, and it must not be listed here before it is deployed.

**One thing the audit changed in the policy's favour, and it was a real
finding.** `list_local_hits` said its rows were "never uploaded". That stopped
being true when the search path started queuing a hit COUNT
(`internal/evidence/searchhit.go`): grade, results shown, offer id and the
public sample id, under the rotating `anonId`. The rows themselves — query,
packages, environment — still never leave. The tool description and §4.3 of
the policy now draw that line explicitly instead of leaving a reader to
assume the wider claim.

### ~~B3 — MCP tools carry no annotations.~~ CLOSED 2026-08-23.

Anthropic's submission requirement 2:

> **Tool annotations**: All tools must include a `title` and the applicable
> `readOnlyHint` or `destructiveHint`

Was: grep for `readOnlyHint|destructiveHint|Annotations|Title` across
`internal/mcp/` returned no matches.

All eight tools now carry a `title` and a full `annotations` object, set from
what the implementation does rather than from what the tool sounds like:

| Tool | readOnly | destructive | idempotent | openWorld | Why |
|---|---|---|---|---|---|
| `search_known_solution` | no | no | no | yes | records a local hit row and an offer token; community mode may fetch shards, file a Wanted tuple and queue a hit count |
| `get_sample` | no | no | yes | yes | content-addressed, but the first call downloads the artifact into the local CAS |
| `explain_compatibility` | **yes** | — | yes | **no** | `explainFromShards` reads local shard tables; no HTTP client is wired into it |
| `run_observed_command` | no | **yes** | no | yes | executes the argv it is handed, in the user's project |
| `report_sample_adoption` | no | no | yes | yes | writes the local correlation and queues one anonymous adoption event; the offer token is one-use |
| `propose_public_sample` | no | no | no | **no** | creates a fresh clean-room directory per call; sends nothing |
| `list_local_hits` | **yes** | — | yes | **no** | local dashboard read |
| `get_local_stats` | **yes** | — | yes | **no** | local dashboard read |

`destructiveHint` is only meaningful when `readOnlyHint` is false, so it is a
pointer in Go and is absent on the read-only three rather than asserting a
default. `TestToolAnnotationsDescribeWhatTheToolsActuallyDo` pins each of
those judgements to the function that justifies it.

**Annotations cannot travel in the MCPB manifest, and that is a schema fact,
not an omission.** `mcpb-manifest-v0.3.schema.json` (and v0.4) declares each
`tools[]` entry as `additionalProperties: false` with only `name` and
`description` permitted, so a `title` or an `annotations` object placed there
fails `mcpb validate`. Clients read annotations from `tools/list`; the bundle
carries names and one-line descriptions.

**The bundle and the server no longer keep two lists.** `make-mcpb.py` had its
own `TOOLS` array, so a renamed tool would have shipped a manifest naming a
tool the binary inside that same manifest does not answer — and the manifest
is what a directory renders when it cannot run the server, which is every
directory that has ever rendered this one. `scripts/mcp-tools.json` is now
generated from `toolDefs()` and read by the packer; the golden test regenerates
it with:

```
CSX_UPDATE_GOLDEN=1 go test ./internal/mcp -run TestTheMCPBToolCatalogMatchesToolDefs
```

### ~~B8 — the repository published a broken install recipe as machine-readable fact.~~ CLOSED 2026-08-23.

Not in the original list, and it belongs here because indexers read it.

`.mcp.json` at the repository root registered the server as
`{"command": "csx", "args": ["mcp"]}` — resolved from PATH. That is precisely
the entry `llms-install.md` opens by measuring as broken
(`env -i /bin/sh -c "csx mcp"` → `csx: not found`), because an MCP client
inherits its editor's environment rather than a login shell's and the POSIX
installer only prints how to extend PATH. A `_comment` field was added in
R2C-61 telling humans not to copy it, which does nothing for a scraper.

It was also redundant: `csx init` writes the correct entry, with the absolute
path, into `~/.claude.json` (verified on the development machine —
`"command": "C:\Users\...\csx.exe"`), and `csx mcp-config` prints it for
every other client. The file is deleted.
`TestNoCheckedInMCPConfigRegistersThisServerFromPATH` keeps a well-meaning
re-add from reintroducing it: a checked-in MCP config may exist, but the
command it names must be an absolute path or a client-expanded placeholder
(`${__dirname}`, `${CLAUDE_PLUGIN_ROOT}`).

Note for §8 and §9: the plugin manifests those blocks sketch both contain an
`mcp.json` with a bare `csx`. That test now fails on it, which is the point —
whichever of `${CLAUDE_PLUGIN_ROOT}` or a `SETUP.md` skill that design lands
on has to be decided before the file is written, not after.

The MCPB bundle's own `mcp_config` was never affected: it uses
`${__dirname}/server/<binary>`, which the client expands.

### B4 — Cline's attestation is the operator's to make, and nobody here has run Cline.


Verbatim from `cline/mcp-marketplace/.github/ISSUE_TEMPLATE/mcp-server-submission.yml`:

```yaml
  - type: checkboxes
    id: testing
    attributes:
      label: Installation Testing
      description: Please confirm you've tested the installation process
      options:
        - label: I have tested that Cline can successfully set up this server using only the README.md and/or llms-install.md file
          required: true
        - label: The server is stable and ready for public use
          required: true
```

Both checkboxes are `required: true`. The first is a statement of fact about an
experiment nobody in this repository has run: no one here has installed Cline,
pointed it at the README, and watched it set up the server. **Do not tick it on
our say-so.** The dossier's job stops at "here is the form"; the attestation is
the operator's, and it should be made only after actually running Cline against
the README (and `llms-install.md`, which the install track is producing) on a
clean machine. If that run fails, the fix is the README, not the checkbox.

### B5 — `server.json` in the repo reads 0.1.0. Not a bug; know why before "fixing" it.

`server.json` at repo root declares `"version": "0.1.0"` and points at the v0.1.0
bundle URL and its sha256, while the registry's latest published record was
0.1.2 when this was written and is 0.1.44 as of 2026-08-23.

This is by design, and it was checked rather than assumed.
`.github/workflows/release.yml` rewrites the file inside the runner before
publishing:

```yaml
      - name: Point server.json at this release
        run: |
          jq --arg v "$V" --arg url "$URL" --arg sha '${{ steps.mcpb.outputs.sha256 }}' '
          ...
          ' server.json > server.tmp && mv server.tmp server.json
          test "$(jq -r '.description | length' server.json)" -le 100
```

then publishes with `./mcp-publisher login github-oidc` and
`./mcp-publisher publish`. The checked-in copy is a template; the registry is
authoritative. The only real consequence is that a human reading `server.json`
gets the wrong version — do not copy version or bundle values out of it. Use §4,
which reads the current version from the registry and the release.
`server.json` is not in this
track's area and was not edited.

Worth noting for the copy in §3: that workflow already enforces
`description | length <= 100`, which is the same 100-character budget the
directories ask for.

### B6 — There is no plugin manifest.

No `.claude-plugin/` and no `.cursor-plugin/` directory exists. The Claude
plugin directory and the Cursor Marketplace both require one. Exact contents are
given in blocks §8 and §9; creating them is new work, not a paste.

### B7 — Zero adoption is a stated review criterion at one destination.

Cline's README lists the approval criteria, first of which is **"Community
Adoption: GitHub metrics, engagement, and ecosystem presence."** The repo has 0
stars and 0 forks. This is not a reason to inflate anything; it is a reason to
expect a decline there and to prefer the destinations that index on merit or
mechanically (MCP Registry, Glama, Smithery, mcp.so) first.

---

## 3. Listing copy

Written from `README.md`, `docs/adapters.md` and `internal/mcp/tools.go`. Lengths
were measured, not estimated.

**25 words** (24 words, 148 characters):

```
MCP server answering whether a library API works on your exact versions and runtime, backed by samples whose tests run offline in pinned containers.
```

**100 characters** (99 characters):

```
Compatibility answers for coding agents, each verified by running its test in an offline container.
```

Alternate, if a form wants a question (92 characters):

```
Does this library API work on your exact versions? Answers verified by running them offline.
```

**55-character tagline** — Anthropic's listing step caps a tagline at 55 (51 characters):

```
Verified answers to library compatibility questions
```

**Paragraph** (inside every form's limit including Anthropic's 2,000):

```
CodeSampleX is a stdio MCP server that answers a question documentation cannot: does this library API actually work on these exact versions, package manager, runtime and OS - and if not, at which stage does it break? Instead of an agent re-deriving a public library's usage from memory, it asks CodeSampleX first and gets either a verified minimal sample graded against the caller's environment, or NO_SAFE_MATCH, which is a deliberate answer rather than a plausible guess. Every published sample is a tiny project with a contract test; that contract is built and executed in a pinned container with the network off, and the result is an ed25519-signed receipt. Compile observations and execution proof are recorded separately and never summed. It is local-first: the tool that runs your build reduces the result to anonymous package/version/symbol/environment facts on your machine, and source code, file paths, project names and raw logs are never transmitted; local-only mode transmits nothing at all. Nine package ecosystems are covered (npm, PyPI, Go modules, Cargo, Composer, RubyGems, pub, Hex, Maven), with pinned Bun and Deno lanes plus Maven and Gradle Java verification across exact JDK 8, 11, 17, 21 and 25 environments. Apache-2.0; published samples default to MIT-0.
```

Notes on the claims in that paragraph:

- "Nine package ecosystems" and the exact Java lines come from the capability
  and verifier-image tables in `docs/adapters.md`.
- No sample count appears in the copy on purpose: the live landing page read
  **104 Verified Samples** on 2026-08-14 and the number moves. If a form wants
  one, read it off https://codesamplex.dev first.
- The paragraph uses `-` rather than an em dash so it survives forms that mangle
  non-ASCII.

**Tool list**, for forms that ask what the server exposes:

```
search_known_solution, get_sample, explain_compatibility, run_observed_command,
report_sample_adoption, propose_public_sample, list_local_hits, get_local_stats
```

(Exactly eight: `search_known_solution`, `get_sample`, `explain_compatibility`,
`run_observed_command`, `report_sample_adoption`, `propose_public_sample`,
`list_local_hits`, `get_local_stats`. Publishing a sample is deliberately not an
MCP capability.)

---

## 4. Shared field values

Reuse these anywhere. All verified against the repo or the live site.

| Field | Value |
|---|---|
| Name | `CodeSampleX` |
| Registry name / namespace | `io.github.r2cuerdame/codesamplex` |
| Slug | `codesamplex` |
| Repository | `https://github.com/r2cuerdame/CodeSampleX` |
| Website | `https://codesamplex.dev` |
| Documentation | `https://github.com/r2cuerdame/CodeSampleX#readme` |
| Support / issues | `https://github.com/r2cuerdame/CodeSampleX/issues` |
| License | `Apache-2.0` (published samples default to MIT-0) |
| Author | `r2cuerdame` |
| Latest version | `0.1.44` (2026-08-23). It moves several times a week — read it off the release API before pasting. |
| MCPB bundle | `https://github.com/r2cuerdame/CodeSampleX/releases/latest/download/codesamplex-mcp.mcpb` (the `/latest/` form does not go stale; a pinned tag URL does) |
| Privacy policy | `https://github.com/r2cuerdame/CodeSampleX/blob/main/PRIVACY.md` |
| Logo (400×400 PNG) | `https://raw.githubusercontent.com/r2cuerdame/CodeSampleX/main/assets/logo-400.png` |
| Transport | stdio |
| Category | Developer Tools (or "Development" where that is the option) |
| Tags | `compatibility`, `dependencies`, `verification`, `evidence`, `npm`, `pypi`, `golang`, `cargo`, `maven`, `developer-tools` |
| Contact email | **Operator supplies.** Not written into this file — it is a public repo, and forms that want a contact address should get one the operator chooses. |
| Install (Windows) | `irm https://codesamplex.dev/install.ps1 \| iex` |
| Install (macOS/Linux) | `curl -fsSL https://codesamplex.dev/install.sh \| sh` |

Use the **release asset URL** for the bundle, never `dist/codesamplex-mcp.mcpb`
in the working tree: `dist/` is gitignored and whatever is sitting there is
whatever someone last built locally, at whatever version they passed to
`make-mcpb.py`.

---

## 5. Smithery

Best first destination: it takes the MCPB bundle we already ship, needs no
Dockerfile, no hosting, and no `smithery.yaml`.

- **Destination:** https://smithery.ai/new — choose the **Local (MCPB Bundle)** tab.
- **Mechanism:** web form after sign-in, or CLI.
- **Sign-in:** required. `smithery.ai/new` 307-redirects to WorkOS AuthKit
  (`api.workos.com/user_management/authorize`). Operator action.
- **Documented paths** (https://smithery.ai/docs/build/publish): URL-based, for a
  remote streamable-HTTP server; or **Local (MCPB Bundle)**, which takes a
  pre-built `.mcpb`. Only the second applies — see §1 on the absence of a remote
  endpoint.

Fields:

| Field | Value |
|---|---|
| Bundle | `codesamplex-mcp.mcpb` from the latest release (download it; do not use `dist/`) |
| Qualified name | `r2cuerdame/codesamplex` |
| Display name | `CodeSampleX` |
| Short description | the 100-character line from §3 |
| Long description | the paragraph from §3 |
| Repository | `https://github.com/r2cuerdame/CodeSampleX` |
| Homepage | `https://codesamplex.dev` |
| License | `Apache-2.0` |

CLI alternative, documented on the same page:

```
smithery mcp publish ./codesamplex-mcp.mcpb -n r2cuerdame/codesamplex
```

The underlying API is `PUT /servers/{qualifiedName}/releases`, multipart, with a
JSON `payload` field and a binary `bundle` field, authenticated by a Smithery API
key bearer token.

**Blockers:** none technical. Sign-in is the only gate. The bundle's
`manifest.json` supplies name, description, long description, keywords, license,
homepage and per-platform commands already.

**Note:** the bundle is ~45 MB (three platform binaries, ~15 MB each). Smithery's
API reference does not publish a size limit; if the upload is rejected for size,
that is the reason, and the fallback is a bundle carrying one platform.

---

## 6. mcp.so

- **Destination A (verified open):** comment on
  https://github.com/chatmcp/mcpso/issues/1 — "Submit Your MCP Servers here",
  open since 2024-12-06 and still accepting comments.
- **Destination B:** the web form at https://mcp.so/submit.
- **Mechanism:** the issue takes a free-form comment. Per mcp.so's own text,
  submissions support **public GitHub MCP servers only**; you complete a draft
  after submitting and saving publishes it.

Paste-ready comment for the issue:

```
CodeSampleX - https://github.com/r2cuerdame/CodeSampleX

Compatibility answers for coding agents, each verified by running its test in an offline container.

Website: https://codesamplex.dev
Transport: stdio (single Go binary; MCPB bundle available)
License: Apache-2.0

CodeSampleX is a stdio MCP server that answers a question documentation cannot: does this library API actually work on these exact versions, package manager, runtime and OS - and if not, at which stage does it break? Instead of an agent re-deriving a public library's usage from memory, it asks CodeSampleX first and gets either a verified minimal sample graded against the caller's environment, or NO_SAFE_MATCH, which is a deliberate answer rather than a plausible guess. Every published sample is a tiny project with a contract test; that contract is built and executed in a pinned container with the network off, and the result is an ed25519-signed receipt.

Tools: search_known_solution, get_sample, explain_compatibility, run_observed_command, report_sample_adoption, propose_public_sample, list_local_hits, get_local_stats

Install: irm https://codesamplex.dev/install.ps1 | iex   (Windows)
         curl -fsSL https://codesamplex.dev/install.sh | sh   (macOS/Linux)
```

**Blockers:** none. An icon URL improves the listing (B1).

**Could not verify:** `https://mcp.so/submit` and `https://mcp.so/` both returned
**HTTP 403** to automated fetches (edge protection), so the web form's exact
fields were not read first-hand. The issue path above was verified directly and
is the safer route. Check the form in a browser before assuming it asks for the
same things.

---

## 7. Glama — claim the existing listing

Already indexed; the work is claiming it, not submitting it.

- **Destination:** https://glama.ai/mcp/servers/@r2cuerdame/CodeSampleX
- **Mechanism:** "Claim ownership" flow on the listing page, authenticated with
  GitHub OAuth. Glama verifies write/admin access to the repository.
- **The repository is under a personal account** (`r2cuerdame`), and Glama's
  documented behaviour is that signing in with GitHub as the repo owner
  associates the account automatically. The `glama.json` file is documented as
  required for **organization**-owned repos. Adding it anyway is harmless and
  makes the claim deterministic.

`glama.json`, at the repository root — schema confirmed by fetching
`https://glama.ai/mcp/schemas/server.json`, which requires exactly one property,
`maintainers`, an array of GitHub usernames:

```json
{
  "$schema": "https://glama.ai/mcp/schemas/server.json",
  "maintainers": ["r2cuerdame"]
}
```

`glama.json` **exists at the repository root** with exactly that content. It
has not changed the listing: Glama's own API still returns an empty
`maintainers` and the page still shows "Maintainers –". The file is documented
as the mechanism for organization-owned repos, and this repo is personally
owned, so the claim flow is what actually associates the account — the file
alone does nothing.

After adding or changing `glama.json`, re-run the Claim ownership flow so the
service re-reads the repo.

**Re-measured 2026-08-23, and this is the part R2C-62 was opened for.** The
listing is not merely unclaimed, it is *wrong* in three separate ways, and
only one of them is fixable from this repository:

1. **`No tools`.** The Schema tab lists no tools, no prompts, no resources, and
   says "Server capabilities have not been inspected yet". The directory API
   agrees: `"tools": []`. This is **not** a manifest problem — the shipped
   MCPB manifest has named all eight tools since v0.1.0. Glama populates that
   tab by **running** the server in its own sandbox, and the same page says
   "This server cannot be installed", so it has never had a process to ask.
   Nothing in `internal/mcp/` or `scripts/make-mcpb.py` changes that; a Glama
   **release** (listing page → Deploy → Make Release) is the only mechanism
   that gives it something to inspect, and that needs a signed-in owner.
2. **A product description nobody here wrote.** The API returns "MCP server
   that helps coding LLMs avoid re-solving common problems by serving verified
   minimal code samples and compatibility evidence, with tools to search,
   retrieve, and explain known solutions across languages and environments."
   That is the reasoning-cache framing the project moved away from. It is
   Glama-generated, not read from any file here, so it changes when the
   listing is claimed and edited — not when the README changes.
3. **A stale README snapshot.** The rendered overview still shows the ✓ PASS
   grid, "we tested it; this is what happened", and the 90-day evidence
   half-life — all removed by R2C-61 earlier the same day. It also lists the
   repository's files without `SECURITY.md`, which dates the crawl. This one
   heals by itself on the next crawl; the other two do not.

**What it already records correctly:** Apache-2.0, README present, maintenance
graded A, 33 releases in twelve months, no known vulnerabilities.

**Operator action, in order:** claim ownership (GitHub OAuth, as the repo
owner) → correct the description on the listing → then try Deploy/Make
Release, which is what could turn `No tools` into eight.

Creating a Glama release (listing page → **Deploy**, then **Make Release** with a
version once the build test succeeds) is what unlocks the two quality scores.

**Blocker / unknown:** Glama's release build compiles the server in its own
sandbox. Whether a Go-binary MCP server distributed as prebuilt release assets
builds there was **not tested** — nobody here has run it. Treat the Deploy step
as an experiment, and claim ownership first so a failed build is not a public
mark against an unclaimed listing.

---

## 8. Claude plugin directory

The realistic Anthropic-side destination for an individual author. It is a
**separate directory** from the Connectors Directory, which is a non-starter for
its own reasons (§16).

- **Destination:** https://platform.claude.com/plugins/submit (Console) —
  requires a Developer, Admin or Owner role on a Console organization, and the
  docs state explicitly that "individual authors who aren't part of a claude.ai
  Team or Enterprise organization can sign up for Console at platform.claude.com
  and submit there." This is the path that is not org-gated.
- Alternative, if the operator has a Team/Enterprise org:
  https://claude.ai/admin-settings/directory/submissions/plugins/new
- **Mechanism:** in-app form. "To submit a plugin to the directory, share a
  GitHub link to your plugin. The repo must be public — closed-source plugins are
  not accepted."
- **Pre-submit check:** `claude plugin validate`.
- Once published, "updates pushed to your GitHub repo are picked up
  automatically" — no resubmission per release.

Fields:

| Field | Value |
|---|---|
| GitHub link | `https://github.com/r2cuerdame/CodeSampleX` |
| Plugin name | `codesamplex` |
| Display name | `CodeSampleX` |
| Description | the 100-character line from §3 |
| Long description | the paragraph from §3 |

**Blocker (B6): the plugin manifest does not exist.** Anthropic's docs confirm a
plugin may bundle "any MCP, including remote MCPs, local MCPs, and MCPBs". Two
files are needed at the repo root:

`.claude-plugin/plugin.json`:

```json
{
  "name": "codesamplex",
  "displayName": "CodeSampleX",
  "version": "0.1.44",
  "description": "Compatibility answers for coding agents, each verified by running its test in an offline container.",
  "author": { "name": "r2cuerdame", "url": "https://codesamplex.dev" },
  "homepage": "https://codesamplex.dev",
  "repository": "https://github.com/r2cuerdame/CodeSampleX",
  "license": "Apache-2.0",
  "keywords": ["compatibility", "dependencies", "verification", "npm", "pypi", "golang", "cargo", "maven"]
}
```

`.claude-plugin/.mcp.json` — **this draft does not pass, and that is
deliberate.** `TestNoCheckedInMCPConfigRegistersThisServerFromPATH` (added in
R2C-62, see B8) fails on a checked-in MCP config whose command is a bare name:

```json
{
  "mcpServers": {
    "csx": {
      "command": "csx",
      "args": ["mcp"]
    }
  }
}
```

Resolve the design question below **before** writing that file, and write
whatever it resolves to — `${CLAUDE_PLUGIN_ROOT}/...`, or a `SETUP.md` skill
that installs csx and then writes the absolute path `csx mcp-config --path`
prints.

**Unresolved design question, and it is the same one the README already names.**
`command` must be an executable on `PATH`, and the README is explicit that a bare
`{"command": "csx"}` is exactly what fails, because an MCP client inherits its
editor's environment rather than a login shell's. The plugin runtime offers
`${CLAUDE_PLUGIN_ROOT}` (absolute path to the plugin's install directory), which
solves this **only if the plugin ships the binary** — and a repo-based plugin
would then have to carry ~45 MB of platform binaries in git, or the plugin has to
assume `csx` is already installed and on `PATH`.

Neither option was tested. The honest recommendation is to add a `SETUP.md`
skill — Anthropic's docs describe exactly this: "Plugins can include a `SETUP.md`
skill to guide Claude through configuring and connecting any MCP servers bundled
in the plugin" — that runs the install script and then `csx mcp-config` to obtain
the absolute path, and to try the plugin locally before submitting. This
destination is **not paste-ready**; it is a small piece of work plus one design
decision.

---

## 9. Cursor Marketplace

- **Destination:** https://cursor.com/marketplace/publish
- **Mechanism:** web form. Plugins are distributed as Git repositories.
- **Stated requirements:** "All plugins must be open source", "Every plugin is
  manually reviewed before it's listed", "we review each update before
  publishing."
- **Structure:** `.cursor-plugin/plugin.json` (only `name` is required) plus an
  `mcp.json` at the plugin root; components are discovered from default
  directories or from custom paths named in the manifest.

`.cursor-plugin/plugin.json`:

```json
{
  "name": "codesamplex"
}
```

`mcp.json` — same content and same unresolved `PATH` question as §8.

Fields for the form:

| Field | Value |
|---|---|
| Repository | `https://github.com/r2cuerdame/CodeSampleX` |
| Name | `CodeSampleX` |
| Description | the 100-character line from §3 |
| License | `Apache-2.0` |

**Blockers:** B6 (no `.cursor-plugin/` manifest) and the same command-path
question as §8.

**Could not verify:** `https://cursor.com/marketplace/publish` returned only the
site chrome to an automated fetch — the form's actual fields were never rendered,
and `https://cursor.com/docs/plugins/publish` is a 404. The mechanism, the
manifest layout and the review rules above come from
`https://cursor.com/docs/plugins`, which did render. **Open the publish page in a
browser and expect fields this dossier does not list.**

The separate community site `cursor.directory` also accepts plugin submissions.
It was not examined and is not covered here.

---

## 10. mcpservers.org

- **Destination:** https://mcpservers.org/submit
- **Mechanism:** web form. Not a GitHub PR or issue.
- **Cost:** "Listings on mcpservers.org are free." An optional "Premium Submit,
  $39 one-time review fee" buys faster review, an "Official badge" and a dofollow
  link. **The free listing is the one to use**; a paid badge on a project with no
  users buys nothing but the appearance of one.

Fields, verbatim labels from the form:

| Field | Value |
|---|---|
| Server Name | `CodeSampleX` |
| Short Description | `Compatibility answers for coding agents, each verified by running its test in an offline container.` |
| Link (GitHub or docs) | `https://github.com/r2cuerdame/CodeSampleX` |
| Category | `Development` |
| Contact Email | operator supplies |

**Blockers:** none. Operator must supply the email.

---

## 11. MCP Market (mcpmarket.com)

- **Destination:** https://mcpmarket.com/submit
- **Mechanism:** web form taking a GitHub repository URL, reviewed before listing.
- Documented convenience: adding a `LAUNCHGUIDE.md` to the repository lets the
  submit form auto-fill listing details, tags and setup requirements.

Fields (expected — see the caveat):

| Field | Value |
|---|---|
| GitHub repository | `https://github.com/r2cuerdame/CodeSampleX` |
| Name | `CodeSampleX` |
| Description | the 100-character line from §3, long form from the paragraph |
| Category | Developer Tools |

**Could not verify.** `https://mcpmarket.com/submit`, `https://mcpmarket.com/`
and `https://docs.mcpmarket.com/docs/submit` returned **HTTP 429** on every
attempt across the session. The mechanism above is from search-result summaries
and the docs index, **not** from the page itself, and the field list is inferred.
Lowest-confidence block in this document. Treat it as "this destination exists
and takes a GitHub URL"; read the form before filling it.

The `LAUNCHGUIDE.md` behaviour is likewise unverified and should not drive any
repository change until someone has seen the form.

---

## 12. Cline MCP Marketplace

Everything here is ready except the one thing the dossier must not supply. Read
B4 before opening the form.

- **Destination:** https://github.com/cline/mcp-marketplace/issues/new — the
  **MCP Server Submission** issue template
  (`.github/ISSUE_TEMPLATE/mcp-server-submission.yml` on `main`).
- **Mechanism:** GitHub issue. Title is prefilled `[Server Submission]: `; label
  `server-submission`.
- **Review criteria**, from the repo README: community adoption (GitHub metrics,
  engagement, ecosystem presence), developer credibility, project maturity,
  security considerations. Stated turnaround "within a couple of days".

Fields, matching the template exactly:

| Template field | Required | Value |
|---|---|---|
| GitHub Repository URL | yes | `https://github.com/r2cuerdame/CodeSampleX` |
| Logo Image (400×400 PNG) | yes | **Missing — see B1.** Upload the PNG in the issue body or link it. |
| Installation Testing — checkbox 1 | yes | **Operator only — see B4.** |
| Installation Testing — checkbox 2 | yes | **Operator only — see B4.** |
| Additional Information | no | text below |

Additional Information, paste-ready:

```
csx is a single Go binary. The install script places it in ~/.local/bin, and `csx init`
registers the MCP server with Claude Code, Codex, Gemini CLI and OpenCode automatically.
For any other MCP client, `csx mcp-config` prints ready-to-paste JSON containing the
ABSOLUTE path of the install - which is the part that matters, because an MCP client
inherits its editor's environment rather than a login shell's, so a bare
{"command": "csx"} fails even on a machine where PATH is correct.

Install:
  Windows:      irm https://codesamplex.dev/install.ps1 | iex
  macOS/Linux:  curl -fsSL https://codesamplex.dev/install.sh | sh

No API keys, no accounts, no environment variables are required. `csx init` asks one
question - JOIN COMMUNITY or LOCAL ONLY - and local-only mode transmits nothing.

Clients that install MCPB bundles can use codesamplex-mcp.mcpb from the latest release
instead: https://github.com/r2cuerdame/CodeSampleX/releases/latest
```

**Blockers:**

- **B1** — the required 400×400 PNG does not exist.
- **B4** — both required checkboxes are factual attestations about a Cline
  install that nobody here has performed. **Do not tick them on this dossier's
  authority.** The correct order is: run Cline on a clean machine, give it only
  `README.md` (and `llms-install.md` once the install track lands it), watch what
  it does, fix whatever breaks, and only then tick the box because it is true.
- **B7** — "Community Adoption" is the first listed review criterion and the repo
  has 0 stars. Expect this destination to be the hardest, which is why §17 puts
  it last.

---

## 13. Official MCP Registry — maintenance only

Already published; nothing to submit. Recorded here so the next release does not
skip it.

- **Registry:** https://registry.modelcontextprotocol.io
- **Namespace:** `io.github.r2cuerdame/codesamplex` (GitHub auth requires the
  `io.github.<username>/` prefix, which this matches).
- **Publishing is already automated.** `.github/workflows/release.yml` rewrites
  `server.json` with the tag's version, bundle URL and sha256, verifies the
  published bundle's digest, then runs `./mcp-publisher login github-oidc` and
  `./mcp-publisher publish`. Cutting a release publishes to the registry. No
  manual step is needed.
- **Manual fallback**, if the workflow ever has to be bypassed:

```
mcp-publisher login github     # device flow at https://github.com/login/device
mcp-publisher publish          # publishes ./server.json
```

Installing `mcp-publisher` on Windows, from the official quickstart:

```powershell
$arch = if ([System.Runtime.InteropServices.RuntimeInformation]::ProcessArchitecture -eq "Arm64") { "arm64" } else { "amd64" }; Invoke-WebRequest -Uri "https://github.com/modelcontextprotocol/registry/releases/latest/download/mcp-publisher_windows_$arch.tar.gz" -OutFile "mcp-publisher.tar.gz"; tar xf mcp-publisher.tar.gz mcp-publisher.exe; rm mcp-publisher.tar.gz
```

Verify a publish:

```
curl "https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.r2cuerdame/codesamplex"
```

**Not an open item:** the checked-in `server.json` declaring 0.1.0 is expected —
see B5.

**Note:** the registry is in preview — its own docs warn that "breaking changes
or data resets may occur before general availability". A data reset would mean
re-publishing, not a lost namespace.

---

## 14. Docker mcp-registry — recommendation: skip

Docker's registry was researched in full and the honest answer is that it should
not be pursued.

**What it requires.** From `docker/mcp-registry` CONTRIBUTING: local servers
"Require a Dockerfile in the source repository, Are built and hosted as Docker
images, Run locally with full container isolation." Submission is a PR adding
`servers/<name>/server.yaml`, created via `task wizard`, then `task build --
--tools <name>`, `task catalog -- <name>`, tested through Docker Desktop's MCP
Toolkit, and reviewed by the Docker team. The license requirement ("MIT or Apache
2 are great") we satisfy.

**Why a containerised csx would be a crippled server, and worse than crippled.**

`run_observed_command` is the tool that makes the network work: it runs the
user's real build (`pnpm build`, `go test`, `cargo build`) and reduces the result
to evidence. Inside `mcp/codesamplex` it would face three problems, and the third
is disqualifying:

1. **No toolchain.** The container has whatever base image Docker builds it from.
   The user's pnpm, their Node 22, their Rust, their Python 3.12 are on the host.
   A `docker run` MCP server cannot invoke them.
2. **No project.** Docker's local servers reach the host filesystem only through
   explicitly configured bind mounts. Every project path the scanner needs would
   have to be mounted by the user, per project.
3. **The evidence would be wrong, not merely absent.** Suppose 1 and 2 were
   solved by mounting the project and baking toolchains into the image. The
   evidence CodeSampleX records is *the environment the build ran in* — OS, libc,
   runtime version, package manager. A build run inside `mcp/codesamplex` would
   be honestly recorded as alpine-plus-whatever-Docker-baked, which is nobody's
   development environment. `docs/adapters.md` already states the principle for
   the verifier: "a contract that ran in a linux container proves nothing about
   the Windows machine that started it." Shipping a container that reports the
   container's environment as the user's would emit systematically wrong evidence
   into the network. That is the one failure mode this project is built to
   prevent.

There is also an operational tangle: the verifier itself shells out to Docker, so
a containerised csx implies docker-in-docker for one of its own subsystems, and
`csx init` writes MCP config and identity keys into the user's home, which a
container does not have.

**Recommendation: skip.** Not "not yet" — the local-container shape is
structurally wrong for this server.

**The one path that would reopen it:** Docker's `type: remote` submission needs
no Dockerfile ("Don't require a Dockerfile (already deployed somewhere)") and
takes `remote.transport_type` (`streamable-http` or `sse`) plus an HTTPS URL. If
`csx-server` ever exposes a streamable-HTTP MCP endpoint — it does not today, see
§1 — a remote listing becomes a two-file PR (`server.yaml`, `tools.json`,
`readme.md`). Revisit only then, and only for the read-only tools; the remote
shape has the same environment problem for `run_observed_command`.

---

## 15. Anthropic desktop-extension (MCPB) form

The survey classified "Anthropic's connectors portal" as a non-starter. That is
correct for the portal, and **there is a second Anthropic door that is not
org-gated**, which the survey missed.

From Anthropic's submission docs: "Desktop extensions (MCPB) use a separate
submission form and don't require the portal", and the portal's own introduction
states it "accepts remote MCP servers only. Local servers are distributed as
desktop extensions or plugins instead."

- **Destination:** https://clau.de/desktop-extention-submission
  (Anthropic's own spelling of the URL; reproduced exactly.)
- **Mechanism:** submission form. No Team/Enterprise organization required — this
  is the difference from the connectors portal in §16.
- **Artifact:** `codesamplex-mcp.mcpb` from the latest release — **v0.1.44**
  as of 2026-08-23, which carries both the privacy policy and the tool
  annotations. This is the first release that does.

**Blockers: both closed in code on 2026-08-23, and one release away.**

- **B2 — closed.** `privacy_policies` is in the manifest and `## Privacy
  Policy` is in all nine READMEs. Verified on a locally built bundle:
  `mcpb validate` passes and the manifest carries
  `["https://github.com/r2cuerdame/CodeSampleX/blob/main/PRIVACY.md"]`.
- **B3 — closed.** All eight tools carry `title` and full annotations in
  `tools/list`, verified by piping a handshake into the binary inside the
  built bundle. They are deliberately **not** in the manifest: the MCPB schema
  forbids it (see B3).

**Verified on the published artifact, 2026-08-23.** v0.1.44 was released and
checked as the asset a reviewer would download, not as a local build:
`sha256sum -c` OK, `mcpb validate` passes, `privacy_policies` present, and the
bundled binary's `tools/list` returns eight tools each with `title` and
annotations. **This destination is now unblocked; nothing in this repository
is holding it up.**

Re-run the same check after any later release, against the published asset:

```sh
curl -fsSL https://github.com/r2cuerdame/CodeSampleX/releases/latest/download/codesamplex-mcp.mcpb -o b.mcpb
npx @anthropic-ai/mcpb@2.1.2 unpack b.mcpb out && npx @anthropic-ai/mcpb@2.1.2 validate out/manifest.json
jq -r '.privacy_policies[]' out/manifest.json
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | out/server/csx-linux-amd64 mcp | jq '.result.tools[].annotations'
```

**Could not verify:** the form at `clau.de/desktop-extention-submission` was not
opened, so its field list is unknown. The requirements above come from
`https://claude.com/docs/connectors/building/submission`, which did render in
full. Expect the form to ask for the bundle, a description, an icon and a
privacy policy URL — all four now have values in §4.

---

## 16. Confirmed non-starters

Checked today, set aside, with the reason each is closed. Recorded so the next
person does not re-research them.

| Destination | Status | Evidence |
|---|---|---|
| **PulseMCP** | Submissions **paused**. "Submissions and changes are temporarily paused. Until mid-August, we are not accepting new MCP server or client submissions, and we are not making changes to existing listings." Points submitters at the official MCP Registry instead — which we are already on (§13). | https://www.pulsemcp.com/submit |
| **modelcontextprotocol/servers README** | **Closed.** CONTRIBUTING: "The README no longer contains a list of third-party MCP servers — that list has been retired in favor of the MCP Server Registry." | repo CONTRIBUTING.md |
| **appcypher/awesome-mcp-servers** | **Archived.** "This repository was archived by the owner on Aug 1, 2026. It is now read-only." | repo page |
| **Anthropic Connectors Directory portal** | **Two independent blocks.** (1) "The portal accepts remote MCP servers only" and CodeSampleX has no remote MCP endpoint (§1). (2) The portal lives in claude.ai admin settings and needs "A Team or Enterprise organization. Admin settings aren't available on individual plans." | https://claude.com/docs/connectors/building/submission |

The PulseMCP notice says "until mid-August" and today is 14 August 2026. It is
worth re-checking that one page in a week; if it reopens, the fields it will want
are all in §3 and §4.

---

## 17. Suggested order

Sequenced by effort against likelihood, not by prestige.

0. ~~**Fix B1 (logo), B2 (privacy policy) and B3 (tool annotations)**, then
   cut a release~~ — done. **v0.1.44** is published, and B2/B3 were verified on
   the downloaded asset rather than on a local build. Everything below can
   proceed.
1. **Glama claim** (§7) — the listing already exists, is unclaimed, shows
   `No tools`, and carries a description nobody here wrote. GitHub sign-in as
   the repo owner. `glama.json` is already checked in and did not help.
2. **Smithery** (§5) — the bundle is already built and the path is designed for it.
3. **mcpservers.org** (§10) and **mcp.so** (§6) — two forms, no prerequisites, free.
4. **Anthropic desktop-extension form** (§15) — unblocked as of v0.1.44. The
   bundle, the description, the icon and the privacy policy URL all have
   values in §4.
6. **MCP Market** (§11) — after reading the form in a browser.
7. **Claude plugin directory** (§8) and **Cursor** (§9) — real work: a manifest,
   a `SETUP.md`, and a decision about the binary path.
8. **Cline** (§12) — last, and only after the operator has actually run Cline
   against the README on a clean machine. The attestation is a factual claim,
   and B4 is the reason this is not a paste job.
9. **Docker** (§14) — skip.

---

## 18. What this document could not verify

Stated plainly so nothing here is mistaken for a checked fact.

- `https://mcp.so/` and `https://mcp.so/submit` — HTTP 403 to automated fetches.
  The GitHub-issue route in §6 was verified; the web form's fields were not.
- `https://mcpmarket.com/*` — HTTP 429 on every attempt. §11 is built from
  search-result summaries and is the weakest block here.
- `https://cursor.com/marketplace/publish` — rendered as site chrome only; field
  list unknown. `https://cursor.com/docs/plugins/publish` is a 404.
- `https://clau.de/desktop-extention-submission` — not opened; requirements in
  §15 come from Anthropic's docs rather than the form.
- **Glama's Deploy/release build** — whether a Go-binary MCP server builds in
  Glama's sandbox was not tested.
- **The Cline attestation** — nobody here has run Cline. See B4. This is the one
  item in this document that cannot be resolved by more research.
- **Claude Desktop's own MCPB inspection** — R2C-62's brief asked for
  validation through Claude Desktop's current bundle-inspection flow. That is a
  GUI action on an installed desktop app, not something reachable from this
  repository's tooling. What was done instead, and is reproducible: the
  official `@anthropic-ai/mcpb@2.1.2` CLI (`validate`, `unpack`, `info`) plus a
  real stdio handshake against the binary inside the bundle. `mcpb info`
  reports `WARNING: Not signed` — bundle signing (`mcpb sign`) has never been
  part of this release train and is a separate decision.
- **Smithery, mcp.so, mcpservers.org, MCP Market, Cursor** — not re-checked on
  2026-08-23. Only the Official Registry, Glama and GitHub were.
