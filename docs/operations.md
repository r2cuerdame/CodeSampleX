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
.\deploy\lightsail\deploy.ps1 -Ip 54.116.158.230 -KeyPath $env:USERPROFILE\.ssh\lightsail-csx-r3
```

Builds the linux/amd64 image locally (the 2GB host never builds), ships it +
compose bundle over SSH, `docker load`, `docker compose up -d`. The server `.env`
(holds the generated PostgreSQL password) is created once and never overwritten.
The deploy also installs `backup.sh` and `restore-check.sh`, restores executable
permissions, and keeps `/opt/codesamplex/backups` writable by the `ubuntu` cron
user.
Release binaries for `/dl/` + `/install.*` go to `/opt/codesamplex/dist/`.
The exact release set also includes `csx-update-stable.json`.

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
.\deploy\lightsail\deploy.ps1 -Ip 54.116.158.230 -KeyPath $env:USERPROFILE\.ssh\lightsail-csx-r3 -ConfigureAdmin
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
.\deploy\lightsail\deploy.ps1 -Ip 54.116.158.230 -KeyPath $env:USERPROFILE\.ssh\lightsail-csx-r3 -SkipImage -ConfigureAdmin
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
.\deploy\lightsail\deploy.ps1 -Ip 54.116.158.230 -KeyPath $env:USERPROFILE\.ssh\lightsail-csx-r3 -SkipImage -ConfigureAdmin -RotateAdmin
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
