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
The exact release set also includes `csx-update-stable.json`. The release
workflow runs only through the protected `codesamplex-release-signing` GitHub
Environment (required reviewers and protected `v*` tag rules are mandatory), derives the updater public key from the environment-scoped
`CSX_UPDATE_SIGNING_KEY_B64` GitHub secret, stamps that public key into all six
client binaries, and signs a 90-day stable manifest binding the versioned
GitHub URLs, sizes and SHA-256 digests. The secret is a base64-encoded 32-byte
Ed25519 seed (or internally consistent 64-byte private key); it must never be stored in the repository
or copied to the production host. A missing secret fails the release before
cross-compilation. Rotating it requires a separately designed signed trust-root
transition; simply replacing the secret would strand installed clients. The
same protected Environment must define `CSX_UPDATE_PUBLIC_KEY_B64` as the pinned
public variable. The workflow refuses to build when the secret-derived public
key differs, preventing an accidental silent trust-root rotation.

Treat the updater seed as an offline-grade release credential: restrict the
Environment to the minimum release reviewers, keep an encrypted recovery copy
outside GitHub, and review Environment access/audit events at each release.
There is no trust-root delegation in updater v1. A planned rotation therefore
requires publishing a final old-key-signed transition release before changing
the key; if that is impossible, existing clients must rerun the official
installer to receive the new pinned public key. On suspected compromise, stop
release publication, revoke the Environment secret and affected repository
credentials, replace both the signing seed and pinned public variable under
review, publish a new launcher/payload installer, and notify users that manual
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
accepted only as repair of that exact release. Keep GitHub's `v*` tag protection
and Environment reviewer rules enabled; repository code alone cannot enforce
who is allowed to mint a tag or approve access to the signing secret.
Before approving that Environment, inspect the tagged workflow and manifest
signer diff, confirm all third-party Actions remain pinned to reviewed full
commit SHAs, and confirm the derived public-key preflight is unchanged.
The current single-maintainer Environment cannot provide independent review and
has self-review prevention disabled. When another maintainer is added, enable
prevent-self-review and require that independent reviewer for signing releases.
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

Migration `0006_wanted_versions.sql` is forward-only with respect to older
server binaries: it replaces the Wanted conflict key with
`(ecosystem,name,version,symbol)`. Roll forward to a fixed server if the new
image fails. Rolling back to a pre-0006 binary requires restoring the verified
pre-deploy database backup; otherwise its old Wanted upsert returns 500.

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
