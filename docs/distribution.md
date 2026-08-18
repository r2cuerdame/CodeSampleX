# Distribution dossier

Everything needed to list CodeSampleX in the MCP directories that will take it.
Each destination below is a paste-ready block: where it goes, how it is
submitted, every field it asks for with the value already filled in, and what
blocks it.

**Nothing here has been submitted.** This document is preparation only. Every
site was re-checked on **2026-08-14**; directories change their forms often, so
re-read the destination page before pasting.

Two ground rules for anything pasted from here:

- The project has **zero external users**. The adoption detector shows one peer
  bucket, which is this machine. No text in this document implies otherwise, and
  none should be edited to.
- Every claim here was checked against the repository or the live site. If a
  field wants something we cannot honestly assert, the block says so instead of
  supplying a value.

---

## 1. Current state, verified

| Destination | State on 2026-08-14 | Evidence |
|---|---|---|
| Official MCP Registry | **Published.** `io.github.r2cuerdame/codesamplex`, versions 0.1.0, 0.1.1, 0.1.2; 0.1.2 is `isLatest`, status `active` | `GET https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.r2cuerdame/codesamplex` |
| Glama | **Auto-indexed, UNCLAIMED.** Score 17%. "If you are the server author, claim this server…", "Unclaimed servers have limited discoverability", "This server cannot be installed" | `https://glama.ai/mcp/servers/@r2cuerdame/CodeSampleX` and `/score` |
| Smithery | **Not listed.** Search for "codesamplex" returns nothing | `https://smithery.ai/search?q=codesamplex` |
| GitHub repo | 0 stars, 0 forks, 0 watchers. Releases v0.1.0, v0.1.1, v0.1.2 | `https://github.com/r2cuerdame/CodeSampleX` |
| Release assets (v0.1.2) | `codesamplex-mcp.mcpb`, `codesamplex-mcp.mcpb.sha256`, six `csx-*` binaries, `csx-server-linux-amd64`, `SHA256SUMS.txt` | release assets listing |

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

### B1 — There is no logo. Blocks Cline; degrades four others.

`find` over the working tree returns no `.png`, `.svg` or `.ico` outside
`.git/`. There is no `assets/` directory. `internal/web/static/` contains
`site.css` and nothing else.

Cline's submission form makes a **400×400 PNG required**. Glama, mcp.so, MCP
Market and Anthropic's desktop-extension form all take an icon and look
unfinished without one.

Needed: a 400×400 PNG at a stable public URL. The install track is adding an
`assets/` directory; if a logo lands there, the raw GitHub URL
(`https://raw.githubusercontent.com/r2cuerdame/CodeSampleX/main/assets/<name>.png`)
is the value to use. **Not verified to exist as of this writing.**

### B2 — No privacy policy in the MCPB manifest or the README. Blocks Anthropic's desktop-extension form.

Anthropic's submission docs, on local connectors:

> Local connectors must include:
> 1. "Privacy Policy" section in README.md
> 2. `privacy_policies` array in manifest.json (manifest_version 0.2+)
> 3. HTTPS URLs to privacy policies
>
> **Missing or incomplete privacy policies result in immediate rejection.**

Checked: `dist/codesamplex-mcp.mcpb` → `manifest.json` has
`manifest_version: "0.3"` and **no `privacy_policies` key**
(`scripts/make-mcpb.py`, `manifest()`, lines 60–94). `README.md` has a section
called "The contract" that describes exactly what is and is not transmitted, but
no section titled "Privacy Policy" and no policy URL.

This one is close to done in substance and only missing in form — the contract
table is already a better privacy disclosure than most listings carry. What is
missing is (a) a `Privacy Policy` heading in the README, (b) an HTTPS URL
serving the policy, and (c) `"privacy_policies": ["<https url>"]` added to the
manifest dict in `scripts/make-mcpb.py`, followed by a rebuilt bundle and a new
release.

`scripts/make-mcpb.py` is not in this track's area. Flagged, not edited.

### B3 — MCP tools carry no annotations. Blocks the Anthropic directory; recommended everywhere.

Anthropic's submission requirement 2:

> **Tool annotations**: All tools must include a `title` and the applicable
> `readOnlyHint` or `destructiveHint`

Checked: grep for `readOnlyHint|destructiveHint|Annotations|Title` across
`internal/mcp/` returns **no matches**. The eight tools in
`internal/mcp/tools.go` declare `Name`, `Description` and a schema only.

Deciding the hints is a judgement call for whoever writes them, and one worth
making carefully rather than in bulk:

- `run_observed_command` runs an arbitrary user command. It is the one tool that
  cannot honestly be marked read-only or non-destructive — a `destructiveHint`
  is the accurate annotation.
- `propose_public_sample` creates a workspace directory. Not read-only.
- `report_sample_adoption` records ADOPTION_EVIDENCE. Not read-only.
- `get_sample`, `explain_compatibility`, `list_local_hits` and `get_local_stats`
  are queries. `search_known_solution` is a query too, but it records a local hit
  (that is what `list_local_hits` lists), so whether it earns `readOnlyHint` is a
  call about whether local bookkeeping counts as modifying the environment.

`internal/mcp/` is not in this track's area. Flagged, not edited.

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
bundle URL and its sha256, while the registry's latest published record is 0.1.2.

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
which reads 0.1.2 from the registry and the release. `server.json` is not in this
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

**Paragraph** (1,210 characters, inside every form's limit including Anthropic's 2,000):

```
CodeSampleX is a stdio MCP server that answers a question documentation cannot: does this library API actually work on these exact versions, package manager, runtime and OS - and if not, at which stage does it break? Instead of an agent re-deriving a public library's usage from memory, it asks CodeSampleX first and gets either a verified minimal sample graded against the caller's environment, or NO_SAFE_MATCH, which is a deliberate answer rather than a plausible guess. Every published sample is a tiny project with a contract test; that contract is built and executed in a pinned container with the network off, and the result is an ed25519-signed receipt. Compile observations and execution proof are recorded separately and never summed. It is local-first: the tool that runs your build reduces the result to anonymous package/version/symbol/environment facts on your machine, and source code, file paths, project names and raw logs are never transmitted; local-only mode transmits nothing at all. Nine package ecosystems are covered (npm, PyPI, Go modules, Cargo, Composer, RubyGems, pub, Hex, Maven), verified across thirteen runtime images including Bun, Deno and Java 21 with both Maven and Gradle verification lanes. Apache-2.0; published samples default to MIT-0.
```

Notes on the numbers in that paragraph:

- "Nine package ecosystems" and "thirteen runtime images" come from the two tables
  in `docs/adapters.md`, counted.
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
| Latest version | `0.1.2` |
| MCPB bundle | `https://github.com/r2cuerdame/CodeSampleX/releases/download/v0.1.2/codesamplex-mcp.mcpb` |
| Transport | stdio |
| Category | Developer Tools (or "Development" where that is the option) |
| Tags | `compatibility`, `dependencies`, `verification`, `evidence`, `npm`, `pypi`, `golang`, `cargo`, `maven`, `developer-tools` |
| Contact email | **Operator supplies.** Not written into this file — it is a public repo, and forms that want a contact address should get one the operator chooses. |
| Install (Windows) | `irm https://codesamplex.dev/install.ps1 \| iex` |
| Install (macOS/Linux) | `curl -fsSL https://codesamplex.dev/install.sh \| sh` |

Use the **release asset URL** for the bundle, never `dist/codesamplex-mcp.mcpb`
in the working tree: the local file's embedded `manifest.json` says
`"version": "0.1.0"`, so it is two releases stale.

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
| Bundle | `codesamplex-mcp.mcpb` from the v0.1.2 release (download it; do not use `dist/`) |
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

Repo root is outside this track's area, so the file was **not created**. It is a
two-line addition.

After adding or changing `glama.json`, re-run the Claim ownership flow so the
service re-reads the repo.

**Current score, and what it says:** 17%. The listing page reports, verbatim in
substance: no Glama release, so "Users cannot deploy this server"; server
coherence and Tool Definition Quality Score both require a release before they
can be computed; "No tool usage detected in the last 30 days"; missing
`glama.json`; author unverified. Positives it already records: Apache-2.0
license, README present, maintenance graded A with 59 recent commits and no
security vulnerabilities.

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
  "version": "0.1.2",
  "description": "Compatibility answers for coding agents, each verified by running its test in an offline container.",
  "author": { "name": "r2cuerdame", "url": "https://codesamplex.dev" },
  "homepage": "https://codesamplex.dev",
  "repository": "https://github.com/r2cuerdame/CodeSampleX",
  "license": "Apache-2.0",
  "keywords": ["compatibility", "dependencies", "verification", "npm", "pypi", "golang", "cargo", "maven"]
}
```

`.mcp.json`:

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
- **Artifact:** `codesamplex-mcp.mcpb` from the v0.1.2 release.

**Blockers, both real:**

- **B2, hard.** No `privacy_policies` array in the MCPB manifest and no "Privacy
  Policy" section in the README. Anthropic: "Missing or incomplete privacy
  policies result in immediate rejection." Submitting before fixing this wastes
  the submission.
- **B3.** Tools carry no `title`/`readOnlyHint`/`destructiveHint` annotations.
  Listed under "Submission requirements" for connectors submitted to the
  directory; whether it is enforced on the MCPB path was **not verified** — the
  desktop-extension form itself was not opened. Fix it regardless: it is correct
  MCP practice and `run_observed_command` genuinely warrants a hint.

**Could not verify:** the form at `clau.de/desktop-extention-submission` was not
opened, so its field list is unknown. The requirements above come from
`https://claude.com/docs/connectors/building/submission`, which did render in
full. Expect the form to ask for the bundle, a description, an icon (B1) and a
privacy policy URL (B2).

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

1. **Glama claim** (§7) — the listing already exists and is losing
   discoverability while unclaimed. GitHub sign-in; add `glama.json` while there.
2. **Smithery** (§5) — the bundle is already built and the path is designed for it.
3. **mcpservers.org** (§10) and **mcp.so** (§6) — two forms, no prerequisites, free.
4. **Fix B1 (logo), B2 (privacy policy) and B3 (tool annotations)** — three small
   changes that between them unblock §15 and improve every listing above.
5. **Anthropic desktop-extension form** (§15) — once B2 is genuinely fixed.
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
