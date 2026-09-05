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
migration set, `/healthz`, the public page/API/install smokes, monotonic
PASS/FAIL/published-sample source ledgers, and a fresh, internally consistent
failure-cluster materialization. The first two say what was
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

**That total is derived, not monotonic.** `RunLoop` makes the builder's first
pass after any restart a full one, so a deploy may legitimately repair a stale
materialized count while no source evidence moved. The transaction waits for
the new server's full-pass completion marker before it samples the table, then
requires every current cluster to have a positive observation count and a
non-negative, known-quality breakdown whose values sum to that count. A
missing materialization while FAIL evidence remains still enters rollback.
`deploy/lightsail/failure_cluster_ledger_test.go` pins the source/derived split
and the completion-marker ordering.

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

**A normal release is unattended.** Creating a `v*` tag is the decision; nothing
after it waits for a person. If GitHub accepts the tag but drops its Actions
event, an operator may replay the same `Release` workflow with that exact tag
as the dispatch ref. The workflow's first job rejects branches and non-`v*`
tags before tests or signing can start, and the signing Environment separately
allows only `v*` tags. The release either completes or fails closed, and
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

### Several homes on one machine

Give each home its own `CSX_HOME` and nothing else: since v0.1.70 a daemon
whose configured port is already taken binds an ephemeral one and publishes it
in `$CSX_HOME/daemon.addr`, which is how every client finds it anyway.

Before that, it could not. Every home carries the same default `daemonPort`, so
only the FIRST daemon on a machine bound it and the rest exited on `listen
tcp`. That is not a missing status endpoint: the upload loop, the syncer and
the verification loop all live inside a running daemon, so those homes stopped
draining their own evidence entirely. And a client with no published address
fell back to that same shared port, so `csx stats`, `csx search`, `csx sync`
and the build-failure hook for those homes were answered by whichever daemon
did get it — from a store that was not theirs.

On a farm node with three worker slots plus a default home that was the normal
state, not an edge case. It was reported as all four homes showing identical
numbers — 28/14 hits, 6 known packages, 0 batches — which is indistinguishable
from three stores that had been wiped, and was read as one. Nothing had been
wiped: `Home()` is `$CSX_HOME` or `~/.csx` with no migration, `config.Load`
never rewrites, and the updater removes only staged binaries and locks.

Assigning each home a distinct `daemonPort` still works and is still honoured;
it is simply no longer required.

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
* `matrixCells` counts **cells**: the unbounded PUBLIC symbol x version corpus
  cross-product from the same stored snapshots the page reads. It deliberately
  does not apply the package page's six-version/ten-symbol load window or the
  later rendered-axis caps; display limits cannot shrink the completeness
  denominator.

At release grain production reads nearly covered; at cell grain, on
2026-08-24, 1,295 of 9,409 cells carried an observation. Neither number is
wrong — a release with forty symbols is forty cells, and one proven sample
covers one of them. An operator reading only the first would conclude the
fleet was nearly done.

`matrixCells` splits what is left into the two dashes a reader sees, because
they are answered by different work and pooling them hides which:

* `observed` — a real project build reached this coordinate.
* `verifiedNoObservation` — the snapshot has `CONTRACT.pass > 0`, no
  `CONTRACT.fail`, and nobody has been seen using the coordinate. Failed or
  mixed receipts are not included. When visible on the page this is a green
  document beside `—`, and the cell is a link. **The fleet cannot
  drain this.** Authoring another sample for a coordinate that already has one
  is duplicate work the queue refuses; only a `csx` install out in the world
  building that coordinate moves it.
* `unmeasured` — nothing recorded at all for the inferred corpus coordinate.
  When visible on the page this is a plain, unlinked `—`.
* `packagesShowingBothDashes` — legacy JSON name for packages whose full
  corpus contains both states; it does not promise both survive the bounded
  UI window. Rendered on the panel as **두 상태 공존 패키지** and marked when
  it is above zero.

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

### Re-verifying that production ran published digests

R2C-81 asks whether production verifiers really executed immutable
digest-pinned images and whether the receipts truthfully record which. It is
not answerable from code review — a merged implementation is not a run — so it
is answered by re-verifying stored receipts offline with the same code that
signs them. Dump read-only, then run the audit:

```bash
# 1. dump (SELECT only). Base64 because COPY's text format escapes
#    backslashes and corrupts every JSON string inside a manifest.
cat > /tmp/dump.sql <<'SQL'
COPY (
  SELECT replace(encode(convert_to(
      json_build_object('receiptId', receipt_id, 'peerId', peer_id,
                        'sampleId', sample_id, 'envHash', env_hash,
                        'createdAt', to_char(created_at at time zone 'UTC',
                                             'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
                        'receipt', receipt)::text, 'UTF8'), 'base64'), chr(10), '')
  FROM receipts ORDER BY created_at
) TO STDOUT;
SQL
ssh -i ~/.ssh/lightsail-csx-r3 ubuntu@54.116.158.230 \
  "docker exec -i codesamplex-db-1 psql -U csx -d csx -q" < /tmp/dump.sql > receipts.b64

# 2. audit (offline, no network, no production write)
CSX_RECEIPT_DUMP=receipts.b64 go test ./internal/verifier/ \
  -run TestProductionReceiptsRanPublishedDigests -v -count=1
```

The audit (`internal/verifier/receiptaudit.go`) checks eight things, and they
are eight different statements. Five are about the document — signature, peer
id derived from the signing key, stored id equal to the content hash, canonical
round trip, and the store's own columns agreeing with the signed body. Three
are about the image — the reference is `<alias>@sha256:<64 hex>` and not a
mutable tag, the standalone digest is that reference's, and the reference is an
entry **this build's registry publishes**.

That last one is the check nothing else makes. `POST /v1/verifications` accepts
any well-formed pin on purpose: a worker may run a newer registry than the
server, and rejecting an unrecognised digest would refuse honest receipts. So
`node:22-alpine@sha256:aaaa…` — perfect shape, bytes that never existed — is
admitted, and only this audit would see it.

A receipt with no image is not a failure. The native fallback entered no
container and a pre-v0.1.43 peer could not record one; absent means NOT
ESTABLISHED, never "the default image".

Two things the dump cannot show, both read directly on the farm:

* **The argv docker actually received.** The receipt is self-reported. The
  worker's own stage log is not: `sudo bash -c 'cd /home/csxver/.csx/verify-logs
  && grep -h "docker run" *.log'` prints the literal command line, digest and
  all. Only the last 50 runs survive there.
* **A stage's failure text.** Receipts keep a digest, the farm keeps the words —
  `sandbox: verifier runtime version "1.26" cannot satisfy "1.27.0"` came from
  one of those files and no receipt field held it.

### The anomaly feedback channel (`report_anomaly`)

An agent that used a CSX answer and then watched its own machine contradict it
can file that through the `report_anomaly` MCP tool, which reaches
`POST /v1/anomalies`. A report is a **verification request**. It is not
evidence, it is not a finding, and nothing about it reaches a public page: the
only thing a reader ever sees is the signed receipt a verifier writes
afterwards, which travels the ordinary receipt path.

The pipeline is `report → normalize → dedupe → cross job → claim → receipt →
verdict`, and it deliberately owns none of its own infrastructure. An accepted
report that names a sample this server holds queues an ordinary **cross job**
on the same queue `csx worker` already polls, so workers already deployed in
the field pick it up with no new version. A new job `reason` would have been
invisible to every one of them, and the report would have sat "queued" forever
against a queue nothing would ever offer it — which is the failure documented
one section above, in a different disguise.

What to look at, on the admin dashboard's **이상 신고 채널** panel:

* **Duplicate rate.** The fingerprint is the exact public coordinate plus the
  shape of the mismatch, and it is UNIQUE in the table. Many agents meeting one
  wrong answer therefore produce one report and one re-run. A high duplicate
  rate with confirmations is the channel working; a high duplicate rate with a
  single anonymous bucket behind most of it — the panel flags this — is a
  client in a retry loop, and the response is the client, not the queue.
* **Confirmed ratio**, over reports that reached a verdict, and the
  `confirmed-csx-defect` count inside it. That count is the one that means work
  for us: an independent clean container did not support a conclusion this
  network served.
* **검증 lane 없음.** Reports nothing in the fleet can reproduce — usually
  because they name no published sample. They are told so in the response and
  shown here with the reason. They are NOT pending, and must not be read as
  backlog.
* **신고 → 판정 소요.** Report to verdict. It is `—`, never `0`, when nothing
  has been decided yet.

Verdicts are computed from the receipt alone (`domain.AnomalyVerdictFromReceipt`):
the reporter's `llmHypothesis`, its confidence and how certain it sounded are
stored, shown to a human, and read by nothing that decides. A receipt whose
contract never ran decides nothing at all — that measured the verifier, not the
sample — and the existing cross-verification retry sends it out again.

Only `confirmed-csx-defect`, `confirmed-compatibility-boundary` and
`confirmed-new-evidence` may promote anything, and the promotion is already
done by the time the verdict is written: the confirming receipt entered the
graph through the normal receipt path. The report row records the link so an
operator can find it.

Privacy: free-text fields are redacted on the client and again on arrival
(`sanitizer.Redact`), raw error output never leaves the reporting machine —
only the sanitizer's code, template and fingerprint travel — and a package this
server cannot confirm is public is refused rather than stored. PRIVACY.md §4.5
states the same thing field by field.

### Product-defect reports (`report_csx_issue`)

The second half of the same channel, and deliberately a different thing.
`report_anomaly` says "your data is wrong about the world" and is settled by
running the world again. This says "this product behaved wrongly" — an answer
that hid the caller's own failure, a recommendation from an ecosystem the
question never mentioned, a tool contract that made a model act wrongly — and
nothing in a container can settle that.

They share ingest, redaction and dedupe. They share nothing after it:
separate table (`csx_issue_reports`), separate verdicts, separate queue. A
defect in this product must never be able to reach the compatibility graph,
and one table for both is how that boundary would eventually leak.

The policy is conservative by instruction: **no automatic ticket**, no agent
guidance to call it after a failure, and no target for report volume. Zero is
a fine number, and report count is explicitly not a success KPI.

- A defect many agents meet is **one row** whose `occurrences` count rises.
  Once an operator sets its `canonical_ref`, every later report answers with
  that reference instead of creating anything.
- `canonical_ref` can only be set on a report already carrying the
  `confirmed-csx-defect` verdict. That check lives in the UPDATE statement
  rather than in a handler, so no second caller can forget it — linking an
  unconfirmed candidate to a bug is how a candidate quietly becomes a claim.
- **Replay is narrow, and the reason is the useful part.** Only a
  server-surface report whose entire input is public coordinates on a public
  READ route can be re-run. Everything else is triaged by a person and says
  so. In particular, for the defect class this channel most wants to catch — a
  search that answered the wrong question — the input that would have to be
  replayed *is* the user's prompt, which this network deliberately never
  receives. A stable fingerprint can travel; a fingerprint cannot be re-run.
  `domain.CSXIssuePublicInput` has no field for a query, which is what makes
  that a property of the type rather than a rule someone has to remember.

The regression fixture is the GPTBrowser incident behind R2C-51: a
typecheck npm-script failure (that project's script, not one of this repo's —
verification here is `go test ./...`) displaced by an unrelated Dart
recommendation,
accepted as a candidate, deduped across two differently-worded reports into
one row with two occurrences, and linked to canonical `R2C-51`
(`TestTheGPTBrowserDefectIsAcceptedDedupedAndLinkedToItsCanonicalBug`).

### Dogfooding decision traces

For a CodeSampleX or ProjectOps diagnostic session, set `CSX_DEBUG=1` on the
MCP server process so `search_known_solution` and `run_observed_command` use
the same `csx.debug.v1` trace without relying on a per-call option. Leave it
unset for normal users. A trace is local diagnostic context and is not stored
or uploaded as evidence.

When a trace surfaces an unrelated candidate, evidence-scope promotion,
environment normalization error, or incomplete failure lineage, record the
request ID and stable reason/gap code. Do not submit `report_anomaly` for a
trace gap or `NO_SAFE_MATCH` alone; submit only a concrete conflict between a
measured local outcome and a CSX fact. The privacy and field contract is in
[diagnostics.md](diagnostics.md).

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

## Search SERP measurement (sample CTR)

The instrument is `cmd/csx-seo-report`, the stored measurement is
`docs/seo/serp-baseline-2026-08-27.json`, and the question is narrow: did
pages that were ALREADY ranking start getting clicked.

The 2026-08-27 Search Console export recorded 187 sample pages with 1,546
impressions and 0 clicks, 157 of them averaging inside the first ten results
and carrying 1,393 of those impressions. That is a snippet problem, not a
ranking problem, and the two are only distinguishable if they are measured
apart.

**Taking a measurement.** Export Pages and Queries from Search Console for
the period you want, then:

```bash
go run ./cmd/csx-seo-report \
  -pages Pages.csv -queries Queries.csv \
  -label "2026-09-24 export, 28 days" \
  -baseline docs/seo/serp-baseline-2026-08-27.json
```

Add `-out docs/seo/serp-<date>.json` to store the measurement as the next
baseline. The tool reads files and nothing else: no network, no database.

**Reading the result.** Three rules, all of them enforced by the report
rather than left to the reader.

1. **Compare inside a position band, never across.** A page that fell from
   rank 4 to rank 12 loses clicks for a reason that has nothing to do with
   its title. Bands are `1-3 / 4-10 / 11-20 / 21+`, and a row with no
   recorded position lands in the last band, not the first.
2. **Read impressions and position before CTR.** A cohort that lost half its
   impressions and climbed eight ranks has proven nothing about its
   snippets. Those two columns sit to the right of the CTR change for
   exactly that reason.
3. **CTR moves in points.** 0.00% to 1.60% is `+1.60` points. Expressed as a
   ratio against a zero baseline it is a division by zero being published as
   a triumph.

**The cohort is the page, not the address.** Sample pages answer at both
`/samples/sha256:*` and `/{ecosystem}/{name}/{version}/samples/{slug}`, and
`seoreport.Classify` counts both as `sample`. Matching one shape would show
the sample cohort collapsing to zero impressions on the day the canonical
moved, which is a reporting artifact and not a result.

**The stored baseline is marked `partial`.** Its numbers were transcribed
from the figures recorded on R2C-205, not parsed from the CSV — the export
file lives on the operator's machine, not in this repository. What it
establishes is the sample cohort total, the page-one sample population and
four query rows; what it does not establish is the `1-3` against `4-10`
split, the package and site cohorts, and the mean position. A comparison
against it prints `not established` for those rows instead of subtracting
from a zero. Anyone holding the original export should regenerate the file
with `-out` and drop the flag.

**When to re-measure.** Search Console lags by two to three days and the
cohort needs enough impressions to say anything, so the first useful
re-measurement is roughly four weeks after the deploy that changed the
snippets. Re-measure the same period length as the baseline.

## Sitemap freshness and health

`/sitemap.xml` is a sitemap index over section shards (`/sitemaps/static-1.xml`,
`packages-1.xml`, `samples-1.xml`, …), generated from the same store queries
the pages render from and cached in-process for **15 minutes**
(`sitemapTTL`, `internal/web/sitemap.go`). That TTL is the freshness
contract: a new package or published sample is in the served sitemap at most
15 minutes after the store starts returning it, and a quarantined sample
leaves the same way. Nobody regenerates or deploys a sitemap file, ever — if
a procedure seems to require that, the bug is in the generator.

**Staleness probe.** The index and every shard answer with two headers:

```bash
curl -sI https://codesamplex.dev/sitemap.xml | grep -i x-sitemap
# X-Sitemap-Built: 2026-08-29T07:15:26Z
# X-Sitemap-Urls: 3299
```

`X-Sitemap-Built` older than the TTL plus a few minutes while the site is
taking traffic means rebuilds are failing and a previous snapshot is being
served (the server logs `web: sitemap rebuild failed: …` with the cause).
`X-Sitemap-Urls` is the advertised URL count — the number to hold against
Search Console's discovered count and against the corpus.

**Count ledger.** Every successful rebuild logs one line with both sides of
every count:

```text
web: sitemap rebuilt urls=3299 shards=3 static=13 packages=204/204
  samples=3283/3283 unroutable_packages=0 malformed_sample_ids=0
  sample_bound_hit=false
```

`packages=a/b` and `samples=a/b` are advertised against the store corpus
read by the same criteria. When Search Console's number disagrees with
production, this line names the cause: `unroutable_packages` are ranked
packages whose ecosystem the router does not serve (no canonical page, so
not indexable), `malformed_sample_ids` are ids that are not content
addresses (skipped rather than escaped into a guess), and
`sample_bound_hit=true` means the corpus read came back exactly at
`sitemapSampleBound` (50,000) — the corpus has outgrown the bound and the
oldest samples may be missing until the bound is raised or paged.

**Limits.** A section splits into numbered shards at 40,000 URLs or 40MB,
under the protocol's 50,000/50MB — growth adds `samples-2.xml` to the index
instead of overflowing a file. There is no findings shard because findings
have no detail URLs; they are rows of `/findings`, which the static shard
lists.

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

**Neither ceiling is a ceiling on a request.** R2C-159 found the gap in
production: `GET /v1/stats` issues the stats read and then the shard-warming
hint, and the hint is four whole-corpus reads. Every one of those statements
stayed inside its own 8-second limit and every checkout inside its 3-second
wait, while the request as a whole crossed ten seconds — which is the ceiling
the production deploy's first proxied request carries, so three of four
unattended rollouts failed there or survived at nine seconds. Measured against
production with an idle database, `/healthz` (the same stats read) answers in
36ms and `/v1/stats` in 337-458ms: about ninety percent of the endpoint was
the optional hint.

So an endpoint that adds optional work to a read owns that work's budget. The
hint now waits 2 seconds and no longer, is shared by simultaneous callers,
is reused for `CSX_SNAPSHOT_INTERVAL`, and is omitted rather than waited for
— which is what `withHotShards` already did for every other failure. The
read a caller stopped waiting for is not cancelled: it keeps its own 30-second
budget and publishes, so the hint returns during pressure without any request
paying for it twice. An empty answer is deliberately not remembered, because
"no shard built yet" is the one state a first builder pass is about to change.

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

### Evidence upload queue health

`csx daemon status`, `csx stats`, the local dashboard, and their JSON forms
distinguish the last successful upload from the last attempt. A current
`lastUploadError` means the daemon retained the rejected/failed rows and will
retry on the next normal tick; it does not mean the daemon exited. The
background loop also writes the bounded error to the local daemon log instead
of discarding it. On recovery, `lastUpload` advances, `lastUploadAttempt`
records the successful attempt, and `lastUploadError` clears.

A deterministic queue whose depth remains high while `lastUpload` is old must
be inspected for a repeated current error. In particular, clients predating
the signed-int32 exit-code boundary may hold Windows DWORD `4294967295`; the
current client normalizes such durable rows on upload and the current server
normalizes the same legacy wire value before PostgreSQL ingest. Do not widen
the PostgreSQL columns or delete the local queue to recover it. During a
rolling client update, local SQLite keeps the last server-acknowledged combined
count. If an older in-memory uploader drains either component afterward, the
next current-client pass detects the larger durable raw+canonical total and
requeues it; unchanged totals do not generate repeat uploads.

Rollback the application before rolling back the schema. The new columns are
additive and safe to leave in place; dropping them would destroy newly captured
evidence and is intentionally not part of an automatic rollback.

Migration `0025_failure_stage_lineage.sql` is additive. It adds the complete
outer-command set → actual-toolchain → actual-stage decision lineage to
`evidence_agg` and materialized failure clusters. After deployment, run one
known `go test` compile failure and one assertion failure and confirm that the
first appears under `PROJECT_COMPILE`/`go/compiler`, the second under
`PROJECT_TEST`/`go/test`, and neither changes historical PASS/FAIL totals.
Rows carrying only `[build failed]` must show `diagnostic-missing`; they must
not publish that aggregate marker as a root diagnostic. Application rollback
may leave these additive columns in place.

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

With no verified fallback left, the launcher first tries to refetch one from
the official release (next section). If that cannot be done, it exits **126**
with a stable reason code as the first field — `payload-missing`, `payload-corrupt`,
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

Defender's verdict is the trigger behind every occurrence so far. The launcher
surviving it and the release causing it are separate problems; the next section
owns the second one.

Recovery is not silent any more, and that matters because recovery *works*. The
command that triggered it exits 0, the pointer is repaired, and from the next
run on nothing says a released payload was destroyed on this machine. So the
launcher also writes `launcher-recovery.json` into the install root, and
`csx update status` reads it back:

```
payload recovery: payload v0.1.44 was unusable (payload-missing); ran
last-known-good v0.1.22 instead; seen 3 times since 2026-08-24 08:07:14
payload recovery last seen: 2026-08-26 08:26:05
```

The record keeps the latest incident rather than a log — an editor can invoke
the launcher hundreds of times a day — and a *different* failed version starts
a new incident instead of extending the old count. `observations` is a floor:
several csx processes can start at once and the last write wins. When the
pointer could not be repaired the line says `the active pointer was NOT
repaired` and why, because an install that recovers on every single run is a
different fault from one that recovered once and healed.

### When there is nothing left to fall back to

On 2026-08-29 the same machine lost both. Defender took v0.1.62, the launcher
recovered onto the recorded fallback v0.1.47 and repaired the pointer as
designed, and then Defender took v0.1.47 too. The pointer was still exactly
right and every payload it named was gone:

```
csx launcher: payload-missing: launcher: current payload v0.1.47: payload file
is missing: ...\payloads\v0.1.47\csx-payload.exe; no verified fallback payload
remains
```

Exit 126, and with it the MCP server every other project's agents depend on --
including the `csx update` that would have fetched a working payload, because
that command lives *inside* the payload that will not start. The local recovery
set was exhausted and nothing on the machine could rebuild it.

So the launcher refetches. When resolution fails with `payload-missing`,
`payload-corrupt`, `payload-not-regular` or `payload-start-failed`, it
downloads the payloads this pointer already records from the official release
path they came from, then resolves again.

The trust boundary is the descriptor's own SHA-256. It was recorded by this
install from a signed manifest when the payload was committed, and it is the
only thing refetched bytes are accepted against -- a hash mismatch, an
interrupted transfer, a substituted file and a redirect off
`github.com/r2cuerdame/CodeSampleX/releases/download/<version>/` all end the
same way, with the payload path untouched and the launcher still exiting 126
with its original reason code first. Nothing local can move where a repair
downloads from. A payload directory that merely happens to sit on this disk is
still never adopted, exactly as `Resolve` refuses to.

**`active.json` is not written by a repair at all.** The pointer already named
the right payload; only the bytes were gone. Every pointer change stays with
the verified-only paths that own it -- `CommitPayload`, `Rollback`, and the
launcher's own healing -- so a repair cannot promote anything, and cannot
resurrect a `rollbackHold` version the operator rejected.

Two things bound it. It runs **once per process**, and after a failure it backs
off for five minutes (`launcher-rehydrate.json` records the attempt), because
an offline machine restarts csx all day and each restart would otherwise pay a
full network timeout. `CSX_LAUNCHER_NO_REPAIR=1` disables it outright for an
install that must never reach the network; the launcher then fails exactly as
before and says `automatic repair skipped: disabled by CSX_LAUNCHER_NO_REPAIR`.

An operator can also ask for it directly, which ignores the cooldown:

```
> %LOCALAPPDATA%\csx\csx.exe --repair-payload
repaired v0.1.47: refetched v0.1.47 from the official release
no local fallback payload: this pointer records no verified previous version
```

A repair restores the recorded `previous` as well as `current`, so the install
gets its minimum recovery set back rather than only the payload it needs to
boot -- the next quarantine is then handled locally, with no network. When the
pointer records no previous, the repair says so on that second line: this is
the state a heal leaves behind, since promoting the fallback to current is
exactly what consumes it.

**Retention.** Nothing in csx deletes a payload directory; every version an
install has ever committed stays on disk, and the quarantine took the files out
from under them. Depth beyond one recorded fallback would need a new field in
`active.json`, and an older launcher rejects an unknown key
(`DisallowUnknownFields`) as `pointer-unreadable` -- a schema change there
bricks every install running the previous launcher, which is a worse outage
than the one it would mitigate. It would also not have helped here: Defender
matches on content, and on this machine it had emptied 14 of 19 payload
directories, consecutive versions included. The standing fallback is therefore
the refetch itself, which quarantine cannot exhaust.

`csx update status` reads the record back, and keeps it separate from the
local-recovery line because "there was no spare" is a much stronger fact about
a release than "a spare was used":

```
payload repair: refetched the payload from the official release (v0.1.47);
v0.1.47 had no verified fallback left on this machine
payload repair last attempted: 2026-08-30 00:43:40
```

A failed repair reports `payload refetch FAILED for <version>: <why>` and, once
it has happened more than once in a row, how many consecutive attempts -- an
install that cannot repair itself is still down, and that is the more urgent of
the two lines.

## Defender and the Windows release payload

Microsoft Defender has quarantined this project's Windows payload repeatedly on
its own workstation. What follows is what was measured on 2026-08-26, not what
the shape of the problem suggested — the two turned out to disagree.

### Two different false positives wearing the same threat name

Both report as `Trojan:Win32/Bearfoos.A!ml` (ThreatID 2147731250; v0.1.39 drew
`Bearfoos.B!ml`, 2147731849). They are unrelated.

**The released payload.** `%LOCALAPPDATA%\csx\payloads\<version>\csx-payload.exe`
was quarantined on 2026-08-24 (v0.1.44), 2026-08-25 (v0.1.44, v0.1.43, v0.1.41,
v0.1.39) and 2026-08-26 (v0.1.44, v0.1.37). Every one of those came from
real-time protection, `Detection Type: fast path`, with
`%LOCALAPPDATA%\csx\csx.exe` — the stable launcher starting the payload — as the
acting process. The staged `.csx-update-*.exe` downloads were caught the same
way.

**The repository's own test binary.** On 2026-08-30, adding
`internal/launcher/restore.go` — ordinary Go that hashes a file and renames it,
with no exec and no network — made Defender block that package's compiled test
binary at execution as `Trojan:Win32/Bearfoos.B!ml` (ThreatID 2147731849):

```
fork/exec ...\go-build<n>\b001\launcher.test.exe: Operation did not complete
successfully because the file contains a virus or potentially unwanted software.
```

It is the compiled content, not the file's behaviour. Removing that one source
file makes `go test ./internal/launcher/` pass and putting it back makes it
fail, at the same definition build, in the same minute. The same code inside the
shipping artifacts is clean: `defender-release-check.ps1` scanned
`csx-windows-amd64.exe` and `csx-launcher-windows-amd64.exe` CLEAN at
1.457.396.0, and both execute. And the verdict is dated like every other one
here — the identical test binary (`sha256:0ff7b041...`) ran all 35 tests green
at 00:41 and was blocked from 00:47 on.

There is no honest fix in the source: unlike an eleven-byte fixture string,
these bytes are a Go program and cannot be shuffled until a classifier stops
objecting. Two things that do work while the verdict stands, neither of which
weakens anything:

```
# build the test binary and run it directly -- same bytes, different writer
go test -c -o C:\tmp\launcher.bin ./internal/launcher && C:\tmp\launcher.bin
```

and reading the result narrowly: `ci.yml` is ubuntu-only, so pull-request CI is
unaffected, and the release workflow's `windows-test` job is where a genuine
regression would surface.

**The repository's own test fixture.** Until 2026-08-26 the Windows launcher
fixtures wrote a file whose entire content was the eleven ASCII bytes
`old-payload`. Defender classifies exactly those eleven bytes as
`Trojan:Win32/Bearfoos.A!ml`. No PE header, no code, eleven characters of
lowercase text — and `old-payloa` is clean, `old-payload2` is clean, so nothing
about the string predicted it.

That second one had two costs. On any Windows machine with real-time protection
on, `go test ./internal/update` failed six tests at once with

```
open ...\payloads\v1.0.0\csx-payload.exe: Operation did not complete
successfully because the file contains a virus or potentially unwanted
software.
```

which reads as an updater bug and is not one. GitHub's `windows-latest` runner
did not reproduce it — v0.1.45's `windows-test` job passed on the same fixture
— so the pipeline stayed green while every developer machine with real-time
protection on could not run the suite at all. And every such run filed its own
`csx-payload.exe` quarantine record into the machine's Defender history — the
same history this project reads as evidence about released payloads. The test
suite was contaminating the measurement it feeds.

The fixture bodies are now named constants in
`internal/update/defenderfixture_windows_test.go`, and
`TestWindowsFixtureBodiesAreNotDefenderFalsePositives` asks Defender about them
directly. If Microsoft's classifier ever objects to the new ones, that test
says so in one line instead of six tests failing for a reason that names the
wrong component. Note where it runs: `ci.yml` is ubuntu-only, so this guard
executes in the release workflow's `windows-test` job and on Windows
developer machines, not on pull-request CI.

### What a scan can and cannot tell you

`internal/defender` and `scripts/defender-release-check.ps1` both drive
`MpCmdRun.exe -Scan -ScanType 3 -File <path> -DisableRemediation`. Three facts
about that instrument, all measured:

- **It is real.** It flags the eleven-byte fixture with the same threat name
  real-time protection used, so it is not a weaker engine.
- **It never remediates.** `-DisableRemediation` leaves the file in place; a
  measurement that destroys its subject is not a measurement. Real-time
  protection may still act on the file afterwards, independently.
- **Its verdict is dated, not permanent.** v0.1.39's payload was quarantined as
  `Bearfoos.B!ml` on 2026-08-25 and scanned **clean** with the same engine on
  2026-08-26, at security intelligence 1.457.332.0. The bytes did not move; the
  definitions did. So a verdict is only meaningful with the definition version
  beside it, and both the Go API and the script carry it.

And one limit that decides the shape of the release gate: **a clean pre-release
scan does not predict the post-install outcome.** The exact published v0.1.45
`csx-windows-amd64.exe` and `csx-launcher-windows-amd64.exe`, checksum-verified
against the release's own `SHA256SUMS.txt`, scan clean — while payloads from
four earlier releases were being quarantined on the same machine in the same
week. A static scan of the artifact is worth running because it catches a build
that is *already* flagged. It cannot promise the build stays unflagged, because
the detections happen at execution and the classifier moves underneath.

```powershell
# the pre-release half: does Defender object to what we are about to ship?
powershell -NoProfile -File scripts/defender-release-check.ps1 -Tag v0.1.46

# the post-install half: did any install have to recover from a lost payload?
csx update status
```

Exit codes: 0 clean, 1 flagged, 2 *unmeasured* — no Defender, no scanner, a
failed download or a checksum mismatch. Unmeasured is never reported as clean.

To read the machine's own history directly:

```powershell
Get-MpThreatDetection | Sort-Object InitialDetectionTime |
  Select-Object -Last 25 InitialDetectionTime, ThreatID, ThreatStatusID,
    @{n='Res';e={ $_.Resources -join ' | ' }} | Format-List

# with the acting process and detection source, which Get-MpThreatDetection omits
Get-WinEvent -LogName 'Microsoft-Windows-Windows Defender/Operational' |
  Where-Object { $_.Id -in 1116,1117 } | Select-Object -First 5 | Format-List
```

### Updater manifest signing is not Authenticode, and neither substitutes

These get conflated because both are called "signing", and the release pipeline
does one of them thoroughly.

`csx-update-stable.json` is signed with an Ed25519 key held only in the
`codesamplex-release-signing` environment, and the public half is pinned in
`.github/updater-public-key.b64` and stamped into every client binary at
compile time. Three values must agree before anything is signed (see the header
comment in `.github/workflows/release.yml`). That protects **what an installed
csx will accept as an update**: an attacker who replaces a release asset cannot
mint a signature, and the updater refuses the asset.

Windows Authenticode is a different claim to a different verifier. It is what
Defender, SmartScreen and enterprise policy look for, and CodeSampleX ships
**none of it** — every Windows binary in every release is unsigned. Nothing in
the updater trust root is visible to Defender, and no amount of hardening there
changes a single Defender verdict. Conversely an Authenticode certificate would
say nothing about which updates the client accepts.

So: the updater signing story is complete and the code-signing story has not
started. Reporting the first as if it covered the second is the specific
mistake this section exists to prevent.

### The mitigation ladder, and who may decide each rung

| Mitigation | Cost | Automatable | Key exposure | Release UX | Decision |
| --- | --- | --- | --- | --- | --- |
| Stop the fixture false positive | none | done | none | none | engineering — **done** |
| Measure the artifact before release | none | yes | none | one more check | engineering — **done, and wired into the release on 2026-09-01** |
| Record and surface post-install recovery | none | yes | none | `csx update status` line | engineering — **done** |
| Report the false positive to Microsoft | free, days to weeks, per-build | no — the portal is interactive | none | fixes one build, not the next | human gate — **submitted 2026-09-01**, see below |
| Authenticode certificate (OV) | annual fee, organisation identity verification | signing yes, obtaining no | a new signing key in CI, next to the updater seed | reputation builds over releases; does not start clean | **human gate: purchase + organisation identity** |
| Authenticode certificate (EV) | higher fee, hardware token or cloud HSM | token models cannot be automated at all | HSM or token custody | immediate SmartScreen reputation | **human gate: purchase, custody, and it can break unattended release** |
| Build-characteristic tuning (version resource, unstripped binaries) | none | yes | none | larger binaries | **not done — see below** |
| Defender exclusion on the install root | none | technically yes | none | none | **refused: never added to a user's machine** |

**"Measure the artifact before release" was marked done and ran nowhere.**
`scripts/defender-release-check.ps1` was written on 2026-08-26 and this table
called the mitigation done, but no workflow invoked it: it was a script a
person had to remember. On 2026-09-01 nobody did, v0.1.89 published a Windows
payload that current definitions quarantine, and the first anyone knew was a
user who could not install. The release now runs it in a `defender-scan` job
on a Windows runner after the binaries exist, and a test asserts that wiring
so it cannot quietly come loose again.

It is deliberately not a gate. `continue-on-error: true`, and the verdict goes
to the job summary. The classification is a model output and a dated one --
the same bytes have scanned clean and been quarantined on consecutive
definition builds -- so a blocking check would have stopped every release on
the day the shipped payload was flagged. Knowing before deploying is the
value; refusing to ship is not.

**The submission was made on 2026-09-01.** Until then this row had a decision
and no action: the analysis finished on 2026-08-26 and the portal step, being
interactive and being the act of sending a public artifact to a third party,
waited for a person. The ticket is:

```
Submission ID   3e13330b-dcee-4708-9436-f59a5dca3399
Status          Submitted
Submitted       2026-09-01 21:51:55 KST
Detection name  Trojan:Win32/Bearfoos.A!ml
Definition      1.457.441.0
File            csx-windows-amd64.exe (v0.1.89)
                sha256 bd90c12a0483869ca089b89590dfbbd70f2663dac39ed2bb37d2f2e38a43eea8
```

The sample reached the portal without changing a Defender setting or restoring
anything from quarantine: the plain binary does not survive on disk here, which
is the defect being reported, so it was fetched and hashed on the Linux
production host and carried in a password-protected archive -- the convention
the portal itself asks for, and one Defender does not scan inside.

**External vendor reporting remains strictly a manual human gate.** Automated
processes must never file false-positive reports to Microsoft WDSI or submit
unreviewed artifacts to third parties. Any follow-up submission (such as for
subsequent definition regressions or new variants like `Bearfoos.B!ml`) must be
submitted manually by an authorized operator through the WDSI portal, and the
submission ID recorded on GitHub issue #70.

**Launcher recovery and updater resilience enhancements (2026-09-05):**
1. **Quarantined launcher restoration**: `replaceLauncherIfStale` and
   `installLauncher` handle an absent `csx.exe` on disk (quarantined or deleted),
   allowing updates to cleanly restore the launcher without failing on missing
   file errors.
2. **Rehydrate repairability for `payload-unreadable`**: When Windows Defender
   blocks access to a payload file via real-time protection (`ERROR_VIRUS_INFECTED`),
   the launcher classifies the failure as `payload-unreadable`. `repairable` now
   routes `payload-unreadable` to `update.RehydrateInstall`, enabling automatic
   restoration from official release assets instead of skipping repair.
3. **Safe installer & launcher UX**: When security software blocks downloads in
   `install.ps1` or execution in `csx-launcher`, the user is explicitly informed
   that security software intervened, referenced to GitHub issue #70, and cautioned
   never to disable antivirus or add unsafe exclusions.

Two rows deserve their reasoning written down.

**Build-characteristic tuning was not implemented, deliberately.** The obvious
theory was that Defender objects to what a Go release binary looks like —
unsigned, `-s -w` stripped, no `VERSIONINFO` resource, self-updating, running
from `%LOCALAPPDATA%`. Adding a version resource and dropping `-s -w` are free
and would be easy to ship. They were not shipped because the eleven-byte
fixture demolishes the theory: a file with no PE header at all draws the same
threat name, so "what the binary looks like" is not what is being scored, and
nothing available here can measure whether either change helps. Shipping an
unmeasurable change and calling it a mitigation is the one thing this project
does not do. If a future release is flagged by
`scripts/defender-release-check.ps1`, that build is a testable subject and the
theory becomes checkable against it.

**An install-root exclusion is refused outright.** It would work, and it would
mean shipping software that turns off a user's virus protection for its own
directory. `csx` will not do that on a machine it does not own. An operator may
of course choose it for their own workstation; that is their decision and it is
not the product's to make.

### When it happens after a release

1. `csx update status` names the failed version, the reason code, how many
   times it has recurred and whether the pointer was repaired. The launcher's
   stderr line says the same thing once, at the moment it happens.
2. `scripts/defender-release-check.ps1 -Tag <the failed version>` against the
   published artifact, which separates "this build is flagged" from "this
   machine flagged it once".
3. The Defender queries above for the acting process and the definition
   version. A detection whose acting process is a `go-build` temp binary is the
   test suite, not the release.
4. Report the result on the release issue with the definition version attached.
   Reporting the sample to Microsoft, and anything involving a certificate, are
   human gates from the table above — do not take them unilaterally.

Do not disable Defender, do not add an exclusion, and do not treat a green
release pipeline as evidence that Windows users can run the artifact: the
pipeline never executed the payload on a machine with real-time protection on.

