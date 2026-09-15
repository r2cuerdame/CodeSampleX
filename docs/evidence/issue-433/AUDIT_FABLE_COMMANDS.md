# Issue #433 audit (FABLE): commands used

Everything here is read-only. SQL files referenced below live in `sql/`
next to this document. Run them from a Windows Git Bash or any POSIX shell
with the production SSH key; `$KEY` is `~/.ssh/lightsail-csx-r3`, the host
is `ubuntu@54.116.158.230`. Keep each SSH session short: on the starved
host a session longer than a few minutes was closed by the remote three
times during this audit.

```sh
SSH='ssh -o BatchMode=yes -o ServerAliveInterval=10 -i ~/.ssh/lightsail-csx-r3 ubuntu@54.116.158.230'
PSQL="docker exec -i codesamplex-db-1 psql -U csx -d csx -q -A -F'|'"
```

## Host and container state

```sh
$SSH 'docker ps --format "{{.Names}} {{.Status}}"; uptime; free -m; nproc'
$SSH 'vmstat 1 5; top -bn1 | head -25; docker stats --no-stream'
$SSH 'cat /proc/pressure/cpu /proc/pressure/memory /proc/pressure/io; head -1 /proc/stat'
$SSH 'for c in codesamplex-server-1 codesamplex-db-1 codesamplex-caddy-1; do id=$(docker inspect -f "{{.Id}}" $c); echo $c; egrep "throttled|usage_usec" /sys/fs/cgroup/system.slice/docker-$id.scope/cpu.stat; done'
$SSH 'docker inspect codesamplex-server-1 --format "{{.RestartCount}} {{.State.StartedAt}} {{.State.OOMKilled}}"'
$SSH 'docker inspect codesamplex-server-1 --format "{{range .Config.Env}}{{println .}}{{end}}" | grep -E "^CSX_(DB|SNAPSHOT|VERSION|BUILD)"'
$SSH 'd=$(docker inspect codesamplex-server-1 --format "{{index .Config.Labels \"com.docker.compose.project.working_dir\"}}"); grep -hE "^CSX_(DB|SNAPSHOT)" "$d/.env"'
$SSH 'iostat -dx 5 2 | tail -3'
```

`/proc/stat` fields: user nice system idle iowait irq softirq steal. Steal
share = steal / sum(all).

## Lightsail metrics (needs the `r2cuerdame` AWS profile)

```sh
export AWS_PROFILE=r2cuerdame
aws lightsail get-instance --instance-name csx-prod-1 --region ap-northeast-2 \
  --query 'instance.{bundle:bundleId,cpu:hardware.cpuCount,ram:hardware.ramSizeInGb}'
S=$(date -u -d '-14 days' +%Y-%m-%dT%H:%M:%SZ); E=$(date -u +%Y-%m-%dT%H:%M:%SZ)
for m in BurstCapacityPercentage CPUUtilization; do
  aws lightsail get-instance-metric-data --instance-name csx-prod-1 --region ap-northeast-2 \
    --metric-name $m --period 21600 --start-time $S --end-time $E --unit Percent \
    --statistics Average Minimum Maximum --query 'metricData[].{t:timestamp,avg:average,min:minimum,max:maximum}' --output text | sort -k4
done
```

## Public HTTP probes (from this workstation)

Three rounds over 17 endpoints; server time is approximated as
`time_starttransfer - time_appconnect - time_connect`.

```sh
for round in 1 2 3; do for u in /version /robots.txt / /stats /v1/stats /compatibility /samples /findings /gaps /dependencies \
  /npm/lru-cache /pypi/sqlalchemy /golang/github.com%2Fjackc%2Fpgx%2Fv5 \
  "/npm/lru-cache/11.5.2/samples/resolve-synchronous-fetchmethod-returns-and-trigger-dispose-d990f985" \
  /v1/wanted "/v1/registry/packages/pkg:npm%2Flru-cache@11.5.2" /sitemap.xml; do
  curl -s -o /dev/null -m 60 -w "$round\t$u\t%{http_code}\t%{time_connect}\t%{time_appconnect}\t%{time_starttransfer}\t%{time_total}\t%{size_download}\n" "https://codesamplex.dev$u"
done; done
```

Twenty distinct package pages, 2 s apart (run from a shell that does not
rewrite leading slashes; in Git Bash prefix the URL list with
`MSYS_NO_PATHCONV=1`):

```sh
for u in /npm/express /pypi/requests /golang/github.com%2Fgoogle%2Fuuid /npm/react /cargo/serde /npm/semver \
  /golang/golang.org%2Fx%2Fnet /pypi/sqlalchemy /npm/vitest /gem/sinatra /npm/hono /golang/github.com%2Fstretchr%2Ftestify \
  /npm/typescript /pypi/onnxruntime /npm/three /golang/google.golang.org%2Fprotobuf /npm/electron /npm/yaml /npm/nanoid /composer/monolog%2Fmonolog; do
  curl -s -o /dev/null -m 60 -w "$u %{http_code} %{time_starttransfer}\n" "https://codesamplex.dev$u"; sleep 2
done
```

Admin API (HTTP Basic; see docs/operations.md for the credential path):

```powershell
. deploy\lightsail\admin-credential.ps1
$secret = Read-CSXAdminCredential (Get-CSXAdminCredentialPaths).Active
# build the Basic header from the SecureString (Windows PowerShell 5.1 has no -Authentication Basic)
Invoke-WebRequest -Uri https://codesamplex.dev/admin/api/farm -Headers @{ Authorization = $header } -UseBasicParsing -TimeoutSec 90
```

## Server and proxy logs

```sh
$SSH 'docker logs --since 40m codesamplex-server-1 2>&1 | grep "db pressure" | grep -o "class=[a-z]* cause=[a-z_]*" | sort | uniq -c'
$SSH 'docker logs --since 40m codesamplex-server-1 2>&1 | grep "db pressure" | grep -o "path=[^ ]*" | sed -E "s#path=/(npm|pypi|golang|cargo|composer|gem|pub|hex|maven)/.*/samples/.*#samplepage#; s#path=/(npm|pypi|golang|cargo|composer|gem|pub|hex|maven)/.*#packagepage#" | sort | uniq -c | sort -rn'
$SSH 'docker logs codesamplex-server-1 2>&1 | grep -E "compatibility: phase|builder pass|authoring candidate|analytics write" | cut -c1-200'
$SSH 'docker logs --since 90m codesamplex-caddy-1 2>&1 | grep -v "handled request" | grep -io "dial tcp[^\"]*\|lookup server[^\"]*" | sort | uniq -c | sort -rn'
# safe access log: API routes only, status/method/route, no duration
$SSH 'docker exec codesamplex-caddy-1 sh -c "cat /var/log/caddy-safe/access-safe.log" | grep -o "\"status\":[0-9]*\|\"csx_route\":\"[a-z_]*\"" | paste - - | sed "s/\"//g" | sort | uniq -c | sort -rn'
$SSH 'docker exec codesamplex-caddy-1 sh -c "cat /var/log/caddy-safe/access-safe.log" | grep "\"status\":502" | grep -o "\"ts\":[0-9]*" | cut -d: -f2 | awk "{print strftime(\"%H\", \$1, 1)}" | sort | uniq -c'
$SSH 'docker exec codesamplex-caddy-1 sh -c "zcat /var/log/caddy-safe/access-safe-2026-09-15T00-00-00.001-time.log.gz" | grep -o "\"status\":[0-9]*" | sort | uniq -c | sort -rn'
```

## Database (all SELECT / EXPLAIN)

Pipe each file over stdin; do not use `-c` quoting.

| file | what it measures | cost on production |
|---|---|---|
| `sql/01-host-and-pgss.sql` | settings, `pg_stat_activity`, `pg_stat_statements` by mean and calls, table scan counters, unused indexes, `pg_stat_database` | seconds |
| `sql/02-pgss-top-and-indexes.sql` | top 30 statements by total time, temp spill, checkpointer, index definitions, full normalized text of the hot statements | seconds |
| `sql/04-corpus-integrity.sql` | live/quarantine base, receipt linkage, resolved-vs-declared, subject/symbol shape, contract shape, template goals, exact duplicates, density, ecosystems | ~1 min |
| `sql/05-verification-spelling-quality.sql` | latest non-PASS receipts (the 217), purl spelling, runtime versions, contract-line census, verification jobs, drafts | ~1 min |
| `sql/06-demand-coverage.sql` | FAIL env vs PASS env, wanted coverage (raw join), evidence packages without samples, search hit/miss/adoption summaries | ~2 min; the last two queries in this file were cut off by an SSH drop and are superseded by 07 |
| `sql/07-staleness-bloat.sql` | normalized wanted coverage, staleness against observed versions, table churn, cluster split | ~2 min; the final symbol-coverage query exceeded 100 s and is reported as not measured |
| `sql/08-explain-hot-queries.sql` | `EXPLAIN (ANALYZE, BUFFERS)` for the snapshot-by-purl, clusters-by-package and `/dependencies` statements | ~15 s |
| `sql/09-export-candidates.sql` | the JSON candidate lists in `AUDIT_FABLE_DATA.json` | ~1 min |

```sh
$SSH "$PSQL" < sql/04-corpus-integrity.sql > out4.txt
$SSH "docker exec -i codesamplex-db-1 psql -U csx -d csx -q" < sql/09-export-candidates.sql > out9.txt
```

`pg_stat_statements` on this server tracks nested statements, so the
`builder_purl_coord` SQL function appears as its own row (`WITH parsed AS
…`). Query text in the outputs is the normalized form with `$n`
placeholders; no literals from user requests were exported.

## Code cross-references

| statement family | source |
|---|---|
| failure cluster upsert, snapshot upsert, `SnapshotKeys` | `internal/serverstore/pg.go` (`upsertFailureClusterSQL`, `putSnapshotSQL`) |
| cluster loop, `fullPassEvery`, pass shape | `internal/compatibility/builder.go` |
| `builder_purl_coord` function and expression indexes | `internal/serverstore/pg_builder_prestage.go`, `migrations/0036_builder_projections.sql` |
| authoring candidate CTE | `internal/serverstore/dependencyclosure_pg.go`; cache and retry in `internal/httpapi/authoring_work.go` |
| per-package cluster read and page cache | `cmd/csx-server/webstore.go` (`FailureClusters`, `packageDetailCacheTTL`) |
| `/dependencies` aggregate | `internal/serverstore/dependencyatlas.go` |
| pool classes and ceilings | `cmd/csx-server/dbclass.go`, docs/operations.md "Database timeouts and the connection pool" |
| boot reconcile before listen | `cmd/csx-server/main.go:178-219` |
| verified badge rule | `internal/web/explorer.go` (`levelBadge`) |
| CROSS_PASS promotion on first PASS receipt | `internal/serverstore/pg.go` (`UPDATE samples SET status='CROSS_PASS' … WHERE status='DRAFT'`) |
