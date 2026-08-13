# CodeSampleX Operations

Production host: AWS Lightsail `csx-prod-1`, ap-northeast-2a, bundle `small_3_0`
(2 vCPU / 2GB RAM / 60GB SSD / 3TB transfer, $12/mo), static IP **3.36.174.198**.
The server runs site/API/PostgreSQL/registry/search/Main Seeder/tracker/aggregation
only — build and verification runners live on peers, never here.

## Provision (already done for csx-prod-1)

```powershell
.\deploy\lightsail\provision.ps1 -Name csx-prod-1 -Region ap-northeast-2
```

Creates the instance (ubuntu 24.04 + userdata: docker, compose plugin, 2G swap,
swappiness 20), allocates+attaches static IP, opens 22/80/443. SSH key:
`aws lightsail download-default-key-pair` → `%USERPROFILE%\.ssh\lightsail-csx.pem`.

## Deploy / upgrade

```powershell
.\deploy\lightsail\deploy.ps1 -Ip 3.36.174.198 -KeyPath $env:USERPROFILE\.ssh\lightsail-csx.pem
```

Builds the linux/amd64 image locally (the 2GB host never builds), ships it +
compose bundle over SSH, `docker load`, `docker compose up -d`. The server `.env`
(holds the generated PostgreSQL password) is created once and never overwritten.
Release binaries for `/dl/` + `/install.*` go to `/opt/codesamplex/dist/`
(deploy.ps1 uploads `dist/` when present; CSX_DIST_DIR=/data/dist in compose).

Smoke: `ssh ... 'curl -fsS http://127.0.0.1/healthz'` → `ok`, and
`docker compose ps` shows caddy/server/db healthy.

## DNS — codesamplex.dev (Gabia)

The Lightsail DNS zone `codesamplex.dev` (us-east-1 API region) already carries:

```text
codesamplex.dev      A   3.36.174.198
www.codesamplex.dev  A   3.36.174.198
```

At Gabia (My가비아 → 도메인 관리 → 네임서버 설정), replace the nameservers with:

```text
ns-1151.awsdns-15.org
ns-459.awsdns-57.com
ns-990.awsdns-59.net
ns-1537.awsdns-00.co.uk
```

Propagation: minutes to a few hours. Verify with
`nslookup codesamplex.dev ns-459.awsdns-57.com` (authoritative, immediate) and
`nslookup codesamplex.dev` (recursive, after propagation). Caddy retries ACME
automatically, so HTTPS turns on by itself once DNS resolves.

## Backup / restore

Nightly cron on the host (`crontab -e`):

```text
15 3 * * * /opt/codesamplex/deploy/backup.sh >> /opt/codesamplex/backup.log 2>&1
```

Produces `backups/<UTC date>/csx.pgdump` + `blobs.tar.gz`, pruned after 14 days.
Copy off-host periodically (S3-compatible target is a post-v1 improvement).

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
```
