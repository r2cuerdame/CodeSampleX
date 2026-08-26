# CodeSampleX Operations

Production host: AWS Lightsail `csx-prod-1` in account **160122452281 (profile
`r2cuerdame`)** — this is the only production account; nothing lives in other
profiles. ap-northeast-2a, bundle `small_3_0` (2 vCPU / 2GB RAM / 60GB SSD /
3TB transfer, $12/mo), static IP **54.116.158.230**.
The server runs site/API/PostgreSQL/registry/search/Main Seeder/tracker/aggregation
only — build and verification runners live on peers, never here.

## Provision (already done for csx-prod-1)

```powershell
.\deploy\lightsail\provision.ps1 -Name csx-prod-1 -Profile r2cuerdame -Region ap-northeast-2
```

Creates the instance (ubuntu 24.04 + userdata: docker, compose plugin, 2G swap,
swappiness 20), allocates+attaches static IP, opens 22/80/443. SSH key:
`aws lightsail download-default-key-pair --region ap-northeast-2` (profile
r2cuerdame) → `%USERPROFILE%\.ssh\lightsail-csx-r3`.

## Deploy / upgrade

```powershell
.\deploy\lightsail\deploy.ps1 -Ip 54.116.158.230 -KeyPath $env:USERPROFILE\.ssh\lightsail-csx-r3 -KnownHostsPath $env:USERPROFILE\.ssh\known_hosts
```

Builds the linux/amd64 image locally (the 2GB host never builds), ships it +
compose bundle over SSH, `docker load`, `docker compose up -d`. The server `.env`
(holds the generated PostgreSQL password) is created once and never overwritten.
The deploy also installs `backup.sh` and `restore-check.sh`, restores executable
permissions, and keeps `/opt/codesamplex/backups` writable by the `ubuntu` cron
user.
Release binaries for `/dl/` + `/install.*` go to `/opt/codesamplex/dist/`.
The exact release set also includes `csx-update-stable.json`.

The SSH host key is pinned. `deploy.ps1` uses `StrictHostKeyChecking=yes` and
never learns a first-seen key during a deployment. Populate `known_hosts` from
the Lightsail host-key fingerprint verified through the AWS console/account
surface; do not make `ssh-keyscan` over the same untrusted network the source
of trust.

### Automatic production rollout after `main`

`.github/workflows/production-deploy.yml` is separate from `release.yml`.
ProjectOps dispatches it only after the immutable target SHA is in canonical
`main`, the required `Test` check succeeded, and there is a successful
`Release` run for that exact target SHA. A green `Release` run includes the
observed farm rollout; a missing farm dispatch token, a rollout that never
starts, or a target that does not return healthy makes the release red. The
production eligibility job reads that same-target evidence before it can reach
the production Environment. Auditor `MergeVerdict=pass`,
`requires_human_decision=no`, and a `safe` or `additive-migration` side effect
class are still required. The dispatch also names the currently served
known-good SHA. The workflow rejects drift between that SHA and the host before
changing anything, and `codesamplex-production` concurrency serializes all
rollouts.

The `codesamplex-production` GitHub Environment owns only:

- secret `CSX_PRODUCTION_SSH_KEY` — the dedicated deploy identity;
- secret `CSX_PRODUCTION_KNOWN_HOSTS` — the verified host-key line;
- variable `CSX_PRODUCTION_HOST` — the production address;
- optional variable `CSX_PRODUCTION_USER` — defaults to `ubuntu`.

It must not contain the updater signing seed, registry identity, release-write
token, DNS/ruleset credentials, or a general AWS administrator credential.
Conversely, the release signing Environment must not contain the production
SSH identity. Only the workflow's `deploy` job enters the production
Environment; eligibility has no secret access.

`cmd/csx-deploy-gate` is the shared fail-closed eligibility check used by the
workflow and available to ProjectOps before dispatch. A migration recorded in
production `schema_migrations` may not be edited or removed. A pending
migration may be corrected before its first rollout, and its actual file must
pass the deploy-gate regression test. Migrations declared `additive-migration`
may add columns and run the bounded evidence-quality backfill used by the
R2C-152 `0024_failure_evidence.sql` fixture. Destructive derived-data cleanup
is a separate manual lifecycle and is not embedded in that migration. The
automatic path uses an allowlist, so
every other statement shape (including DROP, TRUNCATE, DELETE, arbitrary
UPDATE, column type/rename, GRANT and REVOKE) forces a manual gate.

The deploy transaction verifies the running `CSX_VERSION`, the OCI revision
label and the revision the server reports at `GET /version` against the
dispatched SHA, the latest `schema_migrations` row against the checked-out
migration set, `/healthz`, the public page/API/install smokes, and monotonic
PASS/FAIL/published-sample/failure-cluster totals. The first two say what was
configured and what was built; only `/version` says what the process now
answering requests was built from. See "Build identity" below. The pgx
v5.10.0 `ParseConfig` PASS/FAIL totals are a named invariant. Any mismatch
before commit enters the existing exact image/config/environment rollback.
The automatic wrapper also refuses to start if the retired query-bearing
access logs still exist. Removing those logs is an irreversible privacy
cleanup and therefore remains a named manual operation; a `safe` or
`additive-migration` dispatch never deletes them as a side effect.

The failure-cluster total in that check counts **current** clusters, not every
row in the table. `failure_clusters` is derived data and migration 0024
preserved its pre-contract rows instead of deleting them, so the rebuilt
evidence-gap rows now sit beside the old fingerprinted ones and a raw
`SUM(observation_count)` counts the same failures twice — which is what took
the reported ledger from 17,737 to 35,488 on the 0024 rollout while the FAIL
total stayed at 16,755. Both `deploy.ps1` and
`collect-production-evidence.sh` compute it with
`serverstore.CurrentFailureClusterPredicateSQL`, and
`deploy/lightsail/failure_cluster_ledger_test.go` fails the build if either
script drifts from the predicate the server itself reads with. See
[schema.md](schema.md) for why the preserved rows stay.

`modern_failure_clusters` in the same evidence file counts clusters carrying
structured termination and a normalized error. It is zero until a client
release that emits structured failure evidence records a failure; no
deployment of the server can raise it, because legacy evidence is never
promoted to a modern fingerprint. Read a zero there as "no modern producer has
failed yet", and raise it by shipping a producer, not by rebuilding.

Every run uploads `production-deploy-evidence.json`, including run URL/id,
target and previous SHA, image digest, migration version, health/smoke result,
the served `/version` revision, before/after invariants, and rollback outcome.
`servedRevision` reads `unavailable` for a build older than `/version`, which
is what the pre-deploy read of the outgoing server reports; the post-deploy
read must name the dispatched commit or the run fails into rollback. ProjectOps and the independent
Auditor must read that artifact and the GitHub run conclusion; an Agent comment
alone is not production evidence. A failed SHA is not redispatched in a loop:
ProjectOps deduplicates by target SHA plus failure fingerprint and observes its
cooldown, while an explicit new dispatch remains an auditable recovery action.

**A normal release is unattended.** Pushing a `v*` tag is the decision; nothing
after it waits for a person. The release either completes or fails closed, and
a failure never publishes a partial release. The workflow runs as four stages
so that the updater seed is reachable from exactly one of them: `build`
(tests, monotonic guard, cross-compile, MCPB bundle — no credential, no
environment), `sign` (the protected `codesamplex-release-signing` Environment,
`contents: read` only), `publish` (`contents: write` + registry OIDC, no
environment, no seed) and `farm` (rollout dispatch only). Only `sign` may name
that Environment, and `scripts/release_workflow_test.go` fails the build if any
other job does, if `sign` gains write authority, if a trigger other than a `v*`
tag push is added, or if any action stops being pinned to a full commit SHA.

`sign` derives the updater public key from the environment-scoped
`CSX_UPDATE_SIGNING_KEY_B64` GitHub secret and signs a 90-day stable manifest
binding the versioned GitHub URLs, sizes and SHA-256 digests. The public half
is committed as `.github/updater-public-key.b64`, which is what `build` stamps
into all six client binaries — a trust root kept only in settings cannot be
reviewed in a diff, and editing the committed file cannot change what ships,
because `sign` refuses unless the seed derives exactly that key. The secret is a base64-encoded 32-byte
Ed25519 seed (or internally consistent 64-byte private key); it must never be stored in the repository
or copied to the production host. A missing secret fails the `sign` stage, so
nothing is published — the binaries are already built at that point, but no
manifest is signed and no GitHub release or registry version is created.
Rotating it requires a separately designed signed trust-root transition;
simply replacing the secret would strand installed clients, and it must change
`.github/updater-public-key.b64` and the pinned Environment variable in the
same transition or every release fails closed. The
same protected Environment must define `CSX_UPDATE_PUBLIC_KEY_B64` as the pinned
public variable. Three values must agree before anything is signed: the key
derived from the seed, that pinned Environment variable, and the key the build
job stamped into the binaries. Any disagreement stops the release before a
GitHub release exists, preventing an accidental silent trust-root rotation.
Because `sign` no longer builds what it signs, it also rebinds the downloaded
artifact to the build that produced it — the digest of `SHA256SUMS.txt` travels
as a job output rather than inside the artifact, and both `sign` and `publish`
re-check it and every file it lists.

Treat the updater seed as an offline-grade release credential: keep the
Environment's deployment branch policy restricted to `v*` tags, keep an
encrypted recovery copy outside GitHub, and review Environment access/audit
events after each release. Required reviewers are deliberately **not** part of
that protection: a per-release approval button was a human rubber stamp on a
diff already reviewed at merge, and it made every patch release wait. What
protects the seed now is scope, not attendance — one job, no write authority,
tag-only refs, and the machine checks above.

That Environment lives in GitHub settings, not in this repository, so no diff
can show it still matches the contract above. Read it back instead:

```
gh api repos/r2cuerdame/CodeSampleX/environments/codesamplex-release-signing \
  --jq '[.protection_rules[].type]'
# ["branch_policy"] — a "required_reviewers" entry means releases wait again

gh api repos/r2cuerdame/CodeSampleX/environments/codesamplex-release-signing/deployment-branch-policies \
  --jq '[.branch_policies[] | .type + ":" + .name]'
# ["tag:v*"] — anything else means a ref other than a release tag can reach the seed
```

Both are worth reading after any change to repository settings, and after
anyone with admin access touches Environments.

There is no trust-root delegation in updater v1. A planned rotation therefore
requires publishing a final old-key-signed transition release before changing
the key; if that is impossible, existing clients must rerun the official
installer to receive the new pinned public key. On suspected compromise, stop
release publication, revoke the Environment secret and affected repository
credentials, replace the signing seed, the pinned public variable and the
committed `.github/updater-public-key.b64` under review, publish a new launcher/payload installer, and notify users that manual
reinstallation is required. A leaked signing key alone cannot replace GitHub
assets; a leaked key plus release-write authority is a full updater compromise
and must be handled as such.
The Windows launcher trust boundary assumes the first-party install root keeps
its default per-user ACL and rejects reparse points in the root/payload path.
An already-compromised process running as that same user is outside this v1
threat model; other-user/group-writable ACLs or moved/junctioned install roots
must be repaired by rerunning the installer before updates are enabled.
The workflow also refuses a canonical tag below the currently published latest
release and marks a newly created release explicitly as latest. An equal tag is
accepted only as repair of that exact release. Keep GitHub's `v*` tag ruleset
(no deletion, no update) and the Environment's `v*` tag deployment policy
enabled; repository code alone cannot enforce who is allowed to mint a tag.
Minting a tag is therefore the release decision, and the review that used to
happen at the approval button belongs at the pull request that reaches `main`:
inspect the workflow and manifest signer diff there, because after the tag no
one is asked again.
`deploy.ps1` records the served release tag and skips re-downloading identical
GitHub assets on code-only deploys, so GitHub's download counters are not
inflated by our own rollout loop (CSX_DIST_DIR=/data/dist in compose).

### Enable or rotate the private admin dashboard

The admin route is fail-closed: it stays indistinguishable from an unknown path
(`404`) until a valid password verifier is configured. Enable it during a normal
deployment with:

```powershell
.\deploy\lightsail\deploy.ps1 -Ip 54.116.158.230 -KeyPath $env:USERPROFILE\.ssh\lightsail-csx-r3 -KnownHostsPath $env:USERPROFILE\.ssh\known_hosts -ConfigureAdmin
```

`-ConfigureAdmin` creates a 256-bit random password with the Windows CSPRNG and
stores it locally as current-user DPAPI ciphertext under LocalAppData. It never
prints or copies the password. Only its SHA-256 verifier is sent on SSH stdin
and atomically installed in the host's mode-0600 `.env`. Subsequent deployments
preserve and reuse the same credential. A failed first deployment keeps a
pending DPAPI credential so the next `-ConfigureAdmin` run can finish safely.

When the running image already contains the dashboard and only its deployment
wiring is being activated, the same operation may reuse the installed image:

```powershell
.\deploy\lightsail\deploy.ps1 -Ip 54.116.158.230 -KeyPath $env:USERPROFILE\.ssh\lightsail-csx-r3 -KnownHostsPath $env:USERPROFILE\.ssh\known_hosts -SkipImage -ConfigureAdmin
```

The deploy verifies `/healthz` and then requires an unauthenticated `/admin`
request to return `401` (a missing/malformed verifier returns `404`). It then
uses the DPAPI credential through a local .NET HTTP client and requires an
authenticated `200`; neither the Basic header nor response body is printed.
Then open `https://codesamplex.dev/admin` and use username `recuerdame`. Copy the password to
the clipboard only when you are ready to paste it:

```powershell
.\deploy\lightsail\reveal-admin-password.ps1
```

That explicit command decrypts through current-user DPAPI and copies the value;
it still does not print it or place it in argv. Clear the clipboard after use.
Never put the password in a URL or `curl -u` command, where it can enter browser
history, shell history, process listings, or logs.

The internal sample-worker panel can issue 1–16 independent authoring sessions
for a selected model and reasoning level. The copy action returns complete
prompts containing a ready-to-run `csx sample-worker refresh` command; it does
not return a bare token field. Each session is listed by operator label, model,
last refresh, idle expiry, reported computer name, and last fail-closed external IP, and can be revoked
individually. There is no fixed total lifetime, but a session expires after one
hour without a successful refresh. The session bearer authorizes refresh only:
it is never accepted as an admin, seeder, sample publish, verification-job, or
receipt credential. Run authoring agents with a new credential-free `CSX_HOME`,
and stop at `csx sample preview`; publishing remains a separate human action.
The production registry survives server restarts. PostgreSQL stores only each
token's SHA-256 digest and the last refresh IP for the private operator list;
it never stores the bearer. Expired or revoked rows are removed after a short
audit tail.

Each active row has a per-worker re-copy action. Because plaintext bearers are
never stored, re-copy atomically rotates that worker to a newly generated token,
copies a fresh complete prompt and CLI command, and invalidates the old command.

Re-run the authenticated status-only smoke at any time with:

```powershell
.\deploy\lightsail\verify-admin.ps1
```

Rotate the password with a full deploy (omit `-SkipImage` when a new image is
also required):

```powershell
.\deploy\lightsail\deploy.ps1 -Ip 54.116.158.230 -KeyPath $env:USERPROFILE\.ssh\lightsail-csx-r3 -KnownHostsPath $env:USERPROFILE\.ssh\known_hosts -SkipImage -ConfigureAdmin -RotateAdmin
```

Smoke: `ssh ... 'curl -fsS http://127.0.0.1/healthz'` → `ok`, and
`docker compose ps` shows caddy/server/db healthy.

### Reading the flow KPIs

The top of **운영 요약** carries three windowed rates. Everything else on that
page is stock — how much has accumulated — and stock cannot say whether the
line is running: a corpus that stopped growing an hour ago looks exactly like
one that is still growing.

* **No match · 최근 24시간** — the share of searches the server saw that
  returned nothing, with its numerator and denominator beside it. The
  denominator is HITs (`search_hits`, one row per anonymous offer) plus MISSes
  (`search_misses`, one row per question). Both sides deduplicate per reporter
  per UTC day, so retrying the same search all afternoon counts once, and a
  miss that named ten packages is one question rather than ten. Searches
  answered from a client's local cache and installs outside community mode
  reach neither side and are not in the rate.
* **검증 완료 · 최근 1시간** — every completed receipt, not only PASS. A FAIL is
  a verifier doing its job; counting passes alone makes a stalled farm and a
  failing farm the same zero. The card turns amber when work is claimable and
  nothing finished in the hour — that pair is the stall, and neither number
  shows it alone.
* **샘플 수용 · 최근 1시간** — samples uploaded in the window that the server
  currently serves. **보류** is the same window's still-quarantined uploads: a
  draft waiting on its cross verification, or one that was withdrawn.

A zero is only readable next to the three ages under the cards (마지막 검증
완료 / 샘플 수용 / 검색 결과). Zero with a six-minute-old receipt is a lull;
the same zero with a day-old one is a stopped lane. A window with nothing to
measure shows `—` and `표본 없음`, never `0%`.

Every window opens and never closes at the reading clock, because rows are
stamped by PostgreSQL and the window is cut by the server process. Where those
two clocks differ, closing the window would silently drop the newest rows —
which are the only ones this panel is about.

Migration `0023_search_misses.sql` adds the miss counter and is additive; it
records nothing the `wanted` tables did not already hold, and it starts empty,
so the rate reads `표본 없음` until the first reports arrive after deploy.

### Authoring work the queue is refusing to hand out

The farm panel's **보류된 좌표** list is every public coordinate the authoring
queue has stopped offering, with the reason, the last few attempts and the
writers' own notes. Two reasons exist and they need different actions:

* `no callable symbol` — two independent writers measured that nothing a
  contract could call exists there (a pom with no jar, a plugin marker, a
  per-platform `.node` binary). It never lapses; **다시 배포** is the only way
  back, and it is safe: the counters reset, the history stays, and a coordinate
  that genuinely cannot be authored earns its withholding again.
* `repeated no output` — it kept being handed out and produced nothing. This is
  an inference, not a measurement, so it lapses by itself after 30 days.

`health.withheldCoordinates` on `GET /admin/api/farm` is read from the same
predicate the picker uses, so the number an operator reads and the number the
fleet is acted on by are the same one. A withdrawn **sample** and a withheld
**coordinate** are different acts and are counted apart.

Thresholds, the classification a worker reports and the reasoning behind every
number are in [docs/authoring-quarantine.md](authoring-quarantine.md).

### Reading the farm backlog: two grains, both true

`backlog` on `GET /admin/api/farm` reports the coverage gap twice, and the two
figures disagree on purpose.

* `coverageHoles` counts **releases**: a purl the network watches people use
  and has never proven. This is what the queue works off.
* `matrixCells` counts **cells**: the symbol × version grid a package page
  actually draws, from the same stored snapshots the page renders.

At release grain production reads nearly covered; at cell grain, on
2026-08-24, 1,295 of 9,409 cells carried an observation. Neither number is
wrong — a release with forty symbols is forty cells, and one proven sample
covers one of them. An operator reading only the first would conclude the
fleet was nearly done.

`matrixCells` splits what is left into the two dashes a reader sees, because
they are answered by different work and pooling them hides which:

* `observed` — a real project build reached this coordinate.
* `verifiedNoObservation` — our sample passed here and nobody has been seen
  using it. On the page: `≡ —`, and the cell is a link. **The fleet cannot
  drain this.** Authoring another sample for a coordinate that already has one
  is duplicate work the queue refuses; only a `csx` install out in the world
  building that coordinate moves it.
* `unmeasured` — nothing recorded at all. On the page: a plain, unlinked `—`.
* `packagesShowingBothDashes` — pages rendering a linked and a plain dash at
  once. Rendered on the panel as **두 표기 공존 패키지** and marked when it is
  above zero.

A rising `verifiedNoObservation` beside a flat `observed` is the fleet working
and the field not following, which is a demand problem and not a queue one.
Details and the measured cost are in
[docs/coverage-scheduler.md](coverage-scheduler.md).

### Verification work no verifier lane can run

A cross job names the environment a reproduction needs, and it is built from
the sample manifest — which records the machine that WROTE the sample, not a
lane the fleet has. When one contributor's Windows machine moved to Go 1.27.0,
three Go drafts produced jobs asking for a Go 1.27 lane. The only Go image this
build pins is `golang:1.26-alpine`, so every worker skipped the rows in
`canPrepare` before claiming them. The queue reported open work, every worker
reported none, and both were telling the truth. It ran that way for three days.

Two things keep it visible now:

* `health.unsupportedJobs` on `GET /admin/api/farm`, rendered on the farm panel
  as **실행 불가 작업**, counts jobs recorded `unsupported` — work no verifier
  image in this build can run. It is deliberately not part of the queue depth:
  waiting does not consume it. Non-zero means a lane is missing or something
  asked for one that never existed.
* `csx worker start` prints, when it claimed nothing, the coordinates the queue
  offered that this build has no image for:

  ```
  no runnable work: the queue offered 2 coordinate(s) this build has no
  verifier image for: golang go 1.27, golang go 1.27 host
  ```

  An empty queue prints nothing extra, so "no work" and "no lane for the work"
  are different sentences.

A cross job may only ask for what the fleet can serve. The runtime version and
the execution context are relaxed when no image serves them — they describe the
author's machine, exactly as the OS does (see `crossJobOS`) — and the same
requirements are what the run executes against, so the receipt records the
runtime the container really had. What no relaxation can serve is created
`unsupported` instead of `open`.

`ReconcileCrossJobLanes` runs at boot and applies that rule to jobs already in
the table, both ways: it repairs a job asking for a lane nobody has, and it
reopens an unsupported job the moment a lane serves it. `unsupported` is a
statement about the images this build pins, never a verdict on the sample.

#### This rule has a server half and a client half — ship both

The paragraph above is two guarantees, and they live in different binaries.
The queue only asking for what the fleet can serve is the **server**. The run
executing against the job's requirements rather than the author's manifest is
`crossExecutionManifest` in the **client** (`csx worker`), and so is the
`no runnable work: …` sentence. Deploying the server alone is not half the fix
— it is a different, worse failure:

* Before, an unrunnable job sat `open` and no worker would claim it. Visible,
  reversible, and it cost the sample nothing.
* With only the server upgraded, the relaxed job becomes claimable, an older
  client claims it and still selects its image from the manifest, so it dies
  at `resolve` with `sandbox: verifier runtime version "1.26" cannot satisfy
  "1.27.0"` before entering the container. The receipt is `contract SKIPPED`
  with no `env` and no `verifierImage` — nothing was measured — and it spends
  one of the sample's four `maxCrossAttempts`.

Four such receipts and the sample is stranded for good: `requeueCrossVerification`
and `StrandedDrafts` both stop at `maxCrossAttempts`, so no fifth job is ever
queued and the draft waits on nothing. That is how deploying commit
`000a5aa` to the server while the fleet still ran `v0.1.44` consumed the last
two attempts of the x/sys and go-isatty drafts on 2026-08-23 without measuring
either one.

So the ordering matters: **complete the client rollout to every verifier and
verify the exact client version and capability marker there before restarting
or deploying the server that relaxes jobs.** Publishing the client and starting
the server rollout concurrently does not establish that order and is forbidden.
A receipt whose `stages` are `resolve=FAIL` with an empty `env` is the signature
of this mismatch — it says the verifier never started, not that the sample
failed. `health.unsupportedJobs` will not show it, because from the server's
point of view the job was runnable.

Read what the fleet is actually running before trusting the relaxation:

```
ssh -i ~/.ssh/csx-farm.pem ubuntu@43.200.78.1 \
  'sudo -u csxver /home/csxver/.local/bin/csx version'
# and confirm the client half is present in that binary:
ssh -i ~/.ssh/csx-farm.pem ubuntu@43.200.78.1 \
  'sudo grep -ac "no runnable work: the queue offered" /home/csxver/.local/bin/csx'
# 0 means the client half is missing however new the tag looks
```

Run both checks on every verifier host. Only after both checks pass on every
verifier host may the production server restart. The workflow form of the same
rule is fail-closed: `release.yml` cannot finish green without the farm rollout,
and `production-deploy.yml` accepts only a successful `Release` run whose head
SHA is the exact production target.

Migration `0006_wanted_versions.sql` is forward-only with respect to older
server binaries: it replaces the Wanted conflict key with
`(ecosystem,name,version,symbol)`. Roll forward to a fixed server if the new
image fails. Rolling back to a pre-0006 binary requires restoring the verified
pre-deploy database backup; otherwise its old Wanted upsert returns 500.

## The merge gate on `main`

`.github/workflows/ci.yml` runs on every pull request, and the `Test` job is
what proves the PostgreSQL suite actually executed rather than skipped. Running
it is not the same as obeying it: a workflow cannot stop anyone from merging
past its own red X. That part is a repository setting.

Branch ruleset **`Protect main`** (id `21240909`) supplies it, targeting
`~DEFAULT_BRANCH` with exactly two rules — a pull request is required, and the
`Test` status check must pass. Review approval is deliberately not required
(`required_approving_review_count: 0`), including for unattributed commits, so a
single maintainer keeps merging at full speed; the gate is about evidence, not
about attendance. Repository admins keep `bypass_mode: always`, which is the
escape hatch for an incident — using it is a decision, and it is recorded.

Like the signing Environment above, this lives in GitHub settings and no diff
can show it still matches. Read it back instead:

```
gh api repos/r2cuerdame/CodeSampleX/rules/branches/main \
  --jq '[.[] | .type]'
# ["pull_request","required_status_checks"] — an empty list means main is open

gh api repos/r2cuerdame/CodeSampleX/rules/branches/main \
  --jq '[.[] | select(.type=="required_status_checks")
         | .parameters.required_status_checks[].context]'
# ["Test"] — anything else means the PostgreSQL gate is not the required check
```

**The required check is bound to the job's `name:`, not to its key.** GitHub
identifies a status check by the name the job publishes, so
renaming the `Test` job in `ci.yml` does not fail loudly — it silently produces
a check nobody requires, while the required `Test` context never arrives and
every pull request waits forever on a check that will never report. Renaming
that job therefore means updating the ruleset in the same change:

```
gh api -X PUT repos/r2cuerdame/CodeSampleX/rulesets/21240909 --input <edited ruleset>
```

`scripts/ci_workflow_test.go` fails the build if the job name and this document
stop agreeing, so the rename cannot pass CI without the operator reading this
paragraph. It cannot reach into GitHub settings and check the ruleset itself —
the `gh api` readback above is the only thing that confirms the other half.

Both directions were observed on a throwaway pull request when the ruleset was
installed: with `Test` failing GitHub reported `mergeStateStatus: BLOCKED`, and
with `Test` green on the same branch the same pull request reported `CLEAN`.
That is the reading to trust, and calling the merge API is not a substitute for
it: an admin holds `bypass_mode: always`, so the API would have merged the red
commit and proved the bypass rather than the gate.

## Build identity

The site footer ends with the identity of the process that rendered the page:

```text
· server v0.1.44-66 · 2a6af6a · production
```

The version is `git describe --tags --always` with the redundant `-g<abbrev>`
tail removed, the middle field is the short commit, and the last is the
deployment. Hovering shows the full 40-character revision and the build time.
`GET /version` serves the same values as JSON, plus the full revision:

```json
{"service":"csx-server","version":"v0.1.44-66",
 "revision":"2a6af6a…","shortRevision":"2a6af6a",
 "environment":"production","builtAt":"2026-08-26T00:11:02Z"}
```

Nothing here is written by hand. `deploy.ps1` derives the values at build time
and passes them as image build arguments, so a redeploy moves them, a rollback
moves them back to the previous image's values, and no page or template has a
version string in it:

```text
CSX_VERSION        the immutable 40-character deploy revision. Its meaning is
                   fixed: the deploy transaction, the OCI
                   org.opencontainers.image.revision label and the evidence
                   collector all compare against it.
CSX_BUILD_VERSION  git describe --tags --always at build time
CSX_BUILT_AT       RFC3339 UTC build time
CSX_ENV            the deployment name; production is passed only by
                   deploy.ps1
```

They are baked into the image, not set in the compose `.env`, so the artifact
carries its own identity. `CSX_ENV` defaults to `development` in
`Dockerfile.server`: an image built by a laptop or by CI cannot render a
production footer, and `development` on the live site means a real problem
rather than a missing default. An unstamped build renders no identity line at
all rather than inventing one.

`/version` reads only these stamps — no database, no blob store. That is why a
deploy can use it to decide whether the right commit is serving: an answer
that could fail for an unrelated reason could not decide that. `/healthz` is
still the endpoint that proves the database is reachable.

The stylesheet cache-busting token is the short revision, so a rollout also
invalidates `site.css` for every visitor.

The csx client a visitor downloads has its own release version on its own
cadence. It is never this value, and the landing page's `SoftwareApplication`
structured data deliberately publishes no `softwareVersion` rather than
advertising a server commit as the CLI's release.

## DNS — codesamplex.dev (Gabia)

The Lightsail DNS zone `codesamplex.dev` (account 160122452281, us-east-1 API
region) already carries:

```text
codesamplex.dev      A   54.116.158.230
www.codesamplex.dev  A   54.116.158.230
```

At Gabia (My가비아 → 도메인 관리 → 네임서버 설정), replace the nameservers with:

```text
ns-1740.awsdns-25.co.uk
ns-395.awsdns-49.com
ns-650.awsdns-17.net
ns-1250.awsdns-28.org
```

Propagation: minutes to a few hours. Verify with
`nslookup codesamplex.dev ns-395.awsdns-49.com` (authoritative, immediate) and
`nslookup codesamplex.dev` (recursive, after propagation). Caddy retries ACME
automatically, so HTTPS turns on by itself once DNS resolves.

## Backup / restore

Nightly cron on the host (`crontab -e`):

```text
15 3 * * * /opt/codesamplex/deploy/backup.sh >> /opt/codesamplex/backup.log 2>&1
```

Produces `backups/<UTC date>/csx.pgdump` + `blobs.tar.gz`, pruned after 14 days.
Copy off-host periodically (S3-compatible target is a post-v1 improvement).
The weekly `restore-check.sh` restores the latest dump into a disposable database,
compares critical table counts with production, reads the complete blob archive,
and removes the disposable database even on failure.

Restore:

```bash
cd /opt/codesamplex/deploy
docker compose up -d db
docker compose exec -T db pg_restore -U csx -d csx --clean --if-exists < ../backups/<date>/csx.pgdump
docker run --rm -v codesamplex_blobs:/blobs -v /opt/codesamplex/backups/<date>:/backup alpine:3.22 \
  sh -c 'cd /blobs && tar xzf /backup/blobs.tar.gz'
docker compose up -d
```

## Sizing / scale-up

PostgreSQL is tuned for the 2GB host (shared_buffers 256MB, max_connections 40);
csx-server is capped at 768MB. To move up a bundle: Lightsail snapshot →
create larger instance from snapshot → move the static IP. Nothing else changes.

## Database timeouts and the connection pool

The site has eight PostgreSQL connections, and until R2C-58 anything holding
one could hold it forever. That is what took the site down in R2C-55: the
`/wanted` query grew to 8.3s, ten concurrent visitors took all eight
connections, the ninth and tenth waited until the 60s `WriteTimeout` and
became 502s, and `/healthz` — which the container healthcheck believes —
could not reach the database at all. The query was fixed; the shape that
turned one slow query into a whole-site outage was not, and any future slow
query would have done the same thing.

Two ceilings now stand between a slow query and an outage.

**`statement_timeout`, per class of work.** A query a visitor or an agent is
waiting on gets 8 seconds. PostgreSQL cancels the statement itself
(SQLSTATE 57014) and the connection goes straight back to the pool, which is
why a timeout costs nothing beyond the failed request. Ingest, migrations and
the aggregation pass get **no** ceiling: some of them legitimately take
minutes, and a blanket timeout would have started failing deployments to
protect page views.

**A bounded share of the pool, per class.** The shares overlap so spare
capacity stays usable, but the arithmetic guarantees a floor nobody can take:

| class | what it is | cap | guaranteed |
|---|---|---|---|
| interactive | website pages, public API reads, search | 6 | 2 |
| background | evidence ingest, sample upload, authoring, the aggregation pass, `/admin` | 5 | 1 |
| probe | `/healthz` only | — | 1 reserved |

A read that cannot get a connection within 3 seconds is refused with
`ErrPoolBusy` and answered as **503 with `Retry-After`** rather than left to
queue into a 502. `/healthz` never queues behind either of the other classes.

Which class a request belongs to is decided in `cmd/csx-server/dbclass.go` by
path, not by HTTP verb — `POST /v1/search` is a read an agent is blocked on,
and classifying by verb would have left the busiest reads unbounded. Anything
unlisted is interactive: an unclassified read is merely capped, while an
unclassified long job would start dying at eight seconds.

### Watching it

The private `/admin` dashboard has a **데이터베이스 커넥션 풀** panel: occupancy,
per-class in-use against the cap, how many acquisitions had to wait, the
longest wait, how many were refused, and how many statements were cancelled.
It reads counters the pool keeps in memory and issues no query, so it renders
during the incident it describes. The panel opens itself when any class is
under pressure.

Requests that ran into the pool also write one line, at most one per second
per class:

```text
csx-server: db pressure path=/wanted class=interactive cause=query_timeout pool_busy=0 query_timeout=1 waited=90ms
csx-server: db pressure path=/records class=interactive cause=pool_busy pool_busy=3 query_timeout=0 waited=3s
```

`cause=query_timeout` is one query that outlived its ceiling; `cause=pool_busy`
is the pool saturated and refusing to queue. The path never carries a query
string.

### Settings and rollback

All optional; each one named changes only itself.

```text
CSX_DB_POOL_GUARD    "off" restores the pre-R2C-58 pool entirely
CSX_DB_MAX_CONNS     total connections                       (default 8)
CSX_DB_PROBE_RESERVE connections only /healthz may take       (default 1)
CSX_DB_READ_CONNS    cap on user-facing reads                 (default 6)
CSX_DB_WRITE_CONNS   cap on ingest and background work        (default 5)
CSX_DB_READ_TIMEOUT  statement_timeout for reads, 0 = none    (default 8s)
CSX_DB_READ_WAIT     how long a read queues before 503, 0 = forever (default 3s)
CSX_DB_PROBE_TIMEOUT statement_timeout for /healthz           (default 2s)
```

An unparsable value leaves the default in place rather than failing the boot:
a typo in a timeout must not take the server down, and the value it falls back
to is the one the deployment was already tested with.

**Rollback is one variable.** Add `CSX_DB_POOL_GUARD=off` to the compose `.env`
and `docker compose up -d server`; the pool goes back to one shared cap of eight
with no ceiling, no wait budget and no class share — the exact behaviour R2C-55
ran on. Nothing is migrated and nothing is persisted, so it is reversible in the
other direction by deleting the line and running the same command. To loosen
rather than disable, raise `CSX_DB_READ_TIMEOUT` first: it is the setting that
decides whether a slow page fails or merely is slow.

Name the service. `docker compose up -d` with no argument prints
`Container codesamplex-server-1 Running` for a container it decided not to
touch, which during an incident reads exactly like a container it restarted;
`up -d server` recreates the one service whose environment changed. Confirm the
rollback landed rather than assuming it: the `/admin` pool panel states
`CSX_DB_POOL_GUARD=off` in its own line when the ceilings are off, and
`docker inspect codesamplex-server-1 --format '{{.Config.Env}}'` shows the
variable the process actually received.

Every `CSX_DB_*` setting is listed in the compose `server` service so that
writing it into `.env` reaches the process. Compose expands `.env` only into
`${...}` references, so a knob no service names is silently unreachable — which
is what R2C-110 measured on production before this was wired:
`CSX_DB_POOL_GUARD=off` in `.env` left the guard fully on and the panel
correctly said so. `deploy/lightsail/poolguard_rollback_test.go` fails the build
if a pool setting the server reads stops being forwarded, or if this runbook
and the compose file stop describing the same act.

Two settings must be moved together with the HTTP timeouts in
`cmd/csx-server/main.go`: `CSX_DB_READ_WAIT + CSX_DB_READ_TIMEOUT` has to stay
well under `WriteTimeout` (60s), or a read reaches the proxy as a 502 before
its own ceiling fires, and `CSX_DB_PROBE_TIMEOUT` plus the probe's wait has to
stay within the 3s deadline `handleHealthz` sets on itself, or the Go side
cancels first and burns a connection on every slow probe.
`internal/serverstore/pool_test.go` fails the build if either stops holding.

## Environment variables (compose `.env`)

```text
CADDY_SITE       "codesamplex.dev, www.codesamplex.dev"  (":80" for local)
CSX_PUBLIC_URL   https://codesamplex.dev
POSTGRES_PASSWORD generated at first deploy
CSX_PUBLIC_CHECK strict            (trust only for dev/e2e)
CSX_GITHUB_CLIENT_ID/SECRET        optional; GitHub identity is 501 until set
CSX_ADMIN_TOKEN_SHA256             optional; enables private operator /admin
CSX_LISTEN       ":8080"           host-less on purpose: Caddy reaches the
                                   service over the compose network
CSX_DB_*                           database pool ceilings; unset is the
                                   shipped policy. See "Database timeouts and
                                   the connection pool" above for the list and
                                   for the one-variable rollback.
```

The build-identity variables (`CSX_VERSION`, `CSX_BUILD_VERSION`,
`CSX_BUILT_AT`, `CSX_ENV`) are not here: they are baked into the image at
build time so the artifact carries its own identity. See "Build identity".

## Structured failure evidence rollout

Migration `0024_failure_evidence.sql` is additive. It adds termination,
normalized summary, quality, environment-variant, and diagnostic-candidate
columns. Existing FAIL rows default to `legacy-evidence-incomplete`; existing
PASS rows are reset to an empty quality. The migration does not fabricate an
exit code or error summary and does not rewrite historical counts.

After deployment, rebuild compatibility snapshots and compare these invariants
on a known fixture such as `github.com/jackc/pgx/v5@v5.10.0 / ParseConfig`:

1. PASS and FAIL totals are unchanged.
2. `complete + partial + missing + legacy-evidence-incomplete = FAIL`.
3. legacy rows render as Evidence gaps, not “error code not recorded”.
4. a new failing command renders its structured termination, normalized
   summary, and recorded environment variant.
5. repeated missing/legacy clusters are marked diagnostic candidates.

Rollback the application before rolling back the schema. The new columns are
additive and safe to leave in place; dropping them would destroy newly captured
evidence and is intentionally not part of an automatic rollback.

### Running csx-server on Windows

Production csx-server is a Linux container binary; the Windows builds are the
ones developers and agents make to test against a local PostgreSQL. There, a
host-less `CSX_LISTEN` such as `:8080` or `:8097` is **narrowed to
`127.0.0.1`**, and the server says so on startup.

Two reasons. Windows Defender Firewall raises its consent dialog for a bind to
every interface and identifies the program by *executable path*, so a locally
built server — which lands in a new scratch directory every build — prompts
again on every rebuild under whatever filename that build used, and the allow
decision can never be remembered. R2C-84 was reported as an unknown app called
`csx-server-new` for exactly this reason. The second reason is that the bind
otherwise puts an admin-capable API and its database on the local network for
the length of the session.

Naming a host is still honoured verbatim, `0.0.0.0` included — write
`CSX_LISTEN=0.0.0.0:8080` to serve the local network deliberately. Linux and
macOS are untouched, so the container contract above is unchanged.

### When the Windows launcher loses its current payload

`%LOCALAPPDATA%\csx\active.json` names the payload the stable `csx.exe`
executes, and every descriptor in it carries the SHA-256 the launcher verifies
before running anything. A verified payload does not stay verified: on this
project's own Windows workstation, Microsoft Defender classified
`csx-payload.exe` as `Trojan:Win32/Bearfoos.*!ml` and quarantined it minutes
after a correctly staged, hashed and self-tested update had committed it
(2026-08-24 for v0.1.44, 2026-08-25 for v0.1.43 and again for v0.1.41). The
pointer was right; the file was simply gone, leaving `payloads/<current>/`
present and empty.

The launcher treats that as recoverable rather than fatal. When `current` fails
verification — or becomes unstartable after verification but before the OS
opens it — the launcher verifies the `previous` descriptor, retries it at most
once, and repairs `active.json` so `csx update` and every ownership check in
`internal/update` see a consistent install too. `rollbackHold` is rejection
metadata for updater suppression and sequence floors, never an execution
candidate. Payload directories left on disk by older releases are **not**
candidates: this pointer never recorded a hash for them.

What operators see:

```
csx launcher: recovered: payload-missing: current payload v0.1.43 is unusable; running last-known-good v0.1.41
```

That line always goes to stderr, never stdout — an MCP host reads stdout as
JSON-RPC framing. The repaired pointer keeps the failed version as
`rollbackHold`, which holds the automatic updater back from reinstalling the
payload that just failed to run while still letting a genuinely newer release
through, and preserves the sequence floor `mergeLauncherFloor` reads.

With no verified fallback left, the launcher exits **126** with a stable reason
code as the first field — `payload-missing`, `payload-corrupt`,
`payload-not-regular`, `payload-unreadable`, `descriptor-invalid`,
`pointer-unreadable`, `payload-start-failed` — and writes nothing to stdout. It
never exits 0 without having executed a payload; a caller that cannot start csx
must not be able to read that as the command having succeeded.

An invalid current is recovered by the stable launcher before it starts any
payload command. `csx update rollback` remains the explicit rollback command
for a healthy pointer; it is not the entry point for an install whose current
payload cannot start. Editing `active.json` directly is a last resort: write it
as UTF-8 **without a BOM** (the launcher rejects one as
`invalid character 'ï'`), and drop `previous` entirely rather than zeroing it,
since a descriptor with `sequence: 0` or a non-hex digest fails validation and
takes the whole file down with it.

Defender's verdict is a false positive on an unsigned Go binary and is the
trigger behind every occurrence so far; an install-root exclusion or Authenticode
signing of the payload is the fix for the cause, and is tracked separately from
the launcher's own resilience.
