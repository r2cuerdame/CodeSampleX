# CodeSampleX Public v1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Every implementation task follows superpowers:test-driven-development internally: write the failing test named in the task first, watch it fail, implement minimally, watch it pass, commit.

**Goal:** Implement the entire goal.md Public v1 scope — local client (daemon+CLI+MCP), central server (ingest/aggregation/search/samples/peers), website, four ecosystem adapters, sample verification pipeline, P2P distribution — and deploy the server stack via Docker Compose.

**Architecture:** One Go monorepo produces two binaries: `csx` (local daemon, CLI, MCP stdio server, peer node, verifier) and `csx-server` (HTTP API + server-rendered website + PostgreSQL). Local state lives in SQLite+FTS5 and a content-addressed filesystem cache; server state lives in PostgreSQL with materialized compatibility snapshots and a content-addressed blob dir. All schemas are versioned (`schemaVersion: 1`).

**Tech Stack:** Go ≥1.26, `modernc.org/sqlite` (pure-Go SQLite w/ FTS5), `github.com/jackc/pgx/v5` (PostgreSQL), `github.com/Microsoft/go-winio` (Windows named pipes), stdlib `net/http` (Go 1.22+ pattern routing), `html/template` + `embed` for web, hand-rolled MCP (JSON-RPC 2.0, newline-delimited stdio), Docker Compose (Caddy + csx-server + postgres:17-alpine).

## Global Constraints (from goal.md, binding on every task)

- Real project source is NEVER transmitted automatically (§3.1). No file names, paths, repo names, secrets, raw logs in any outbound payload (§2.2).
- Automatic evidence collection is restricted to packages that exist on public registries: npm, PyPI, crates.io, Go module proxy (§8.1). Private/`file:`/`link:`/`git+ssh`/workspace deps are fully excluded (§25.E); unknown publicness ⇒ treated as private.
- Evidence Network and Sample Pool remain independent systems (§3.4). Compatibility is a view derived from Evidence, not a Sample attribute (§2.3).
- Project compile success is never presented as symbol execution success (§3.5): observation stages are `PROJECT_*`, verification stages are the strong ones.
- Uncertain failure causes are `UNKNOWN` or probabilistic hypotheses, never definitive (§3.6, §6.4).
- Search prioritizes environment fit + verification strength over lexical/semantic similarity (§3.7). `NO_SAFE_MATCH` is better than a wrong HIT (§3.8).
- Local features keep working during server outages; evidence queues locally (§3.9, §25.F).
- No server-side LLM inference, no central build farm (§3.10). No Redis/Kafka/Kubernetes in v1 (§14.2). No blockchain/tokens (§3.13).
- Sample source publication and idle-CPU verification each require separate explicit consent (§3.11). MCP must not be able to publish autonomously (§12.4).
- Default sample license: `MIT-0` (§7.5). `sampleId = SHA-256(canonical artifact)`.
- Downloaded samples never run directly on host: sandbox pipeline with `--ignore-scripts` resolve, network-off compile/contract (§16).
- Rotating pseudonymous identity for automatic evidence; persistent explicit identity only for seeders/verifiers (§8.6).
- Go single binary per OS; server must fit one small VM: Docker Compose = Caddy + csx-server + PostgreSQL only (§14.2).
- **Production target (user-confirmed 2026-08-13): AWS Lightsail Linux, 2 vCPU / 2GB RAM / 60GB SSD / 3TB transfer, $12/mo.** Server runs site/API/Postgres/registry/search/Main Seeder/tracker/evidence aggregation ONLY — build/verification runners never run on the server; local peers own verification. Postgres tuned for 2GB (shared_buffers 256MB, max_connections 40); 2GB swapfile in ops setup.
- Domain: **codesamplex.dev** (purchased at Gabia). Caddy config targets it; local deploy uses `localhost`. After the server is live, connect the domain (Lightsail static IP; DNS at Gabia — automate if a session/API is available, otherwise document exact records and verify propagation).
- **i18n (user requirement): README and website in 9 languages: en, ko, ja, zh-CN, es, fr, de, pt-BR, ru.** Website: per-locale routes (`/?lang=` cookie + `/{lang}/` paths for indexable pages), `hreflang` alternates, localized meta descriptions. README.md (en) + `docs/i18n/README.{lang}.md`.
- SEO: server-rendered pages with canonical URLs, hreflang clusters, sitemap.xml (localized), robots.txt, JSON-LD (WebSite + SoftwareApplication + BreadcrumbList), OpenGraph/Twitter cards, descriptive titles like "axios 1.12 node 22 compatibility" (§14.6).
- Module path: `github.com/r2cuerdame/codesamplex`.
- **Ease of install is paramount (user directive 2026-08-13: "무엇보다 설치세팅이 쉬워야해").** One-line installs: `irm https://codesamplex.dev/install.ps1 | iex` and `curl -fsSL https://codesamplex.dev/install.sh | sh` download the single `csx` binary for the platform, add it to PATH, and launch `csx init`. `csx init` asks exactly ONE question (JOIN COMMUNITY / LOCAL ONLY per §5.4) and then does everything itself: config, identity, daemon autostart, MCP registration, agent rules/hooks for detected agents. Every other setting has a working default. No package manager, no dependencies, no config file editing required.

---

# Part I — Shared Contracts

Every task references these. Do not diverge; if a task needs a contract change, update this section in the same commit.

## C1. Repository layout

```
cmd/csx/main.go                 cmd/csx-server/main.go
internal/domain/                core types + pure logic (no I/O)
internal/config/                CSX_HOME, config.json load/save
internal/storage/               localdb (SQLite), cas (content-addressed store), blob interfaces
internal/environment/           fingerprint collection
internal/scanner/               project scanning orchestration (delegates to adapters)
internal/sanitizer/             error sanitization + fingerprints
internal/evidence/              local aggregation, batching, upload queue
internal/search/                local search engine + ranking + delta
internal/samples/               canonical artifact, leakage scan, clean-room workflow
internal/verifier/              verification engine + verifier adapters
internal/sandbox/               docker/native sandbox runners, capability detection
internal/peer/                  peer node: announce, serve, fetch
internal/identity/              ed25519 keys, rotating anon IDs, signing
internal/daemon/                daemon HTTP mux, pipe/socket listeners, ui
internal/mcp/                   MCP stdio server
internal/cli/                   cobra-less command dispatch (stdlib flag)
internal/registry/              server: package registry + publicness checks
internal/compatibility/         server: aggregation, snapshots, failure clusters
internal/httpapi/               server: /v1 API handlers
internal/web/                   server: templates, site handlers, embedded assets
internal/serverstore/           server: pgx store behind interface
adapters/node/  adapters/python/  adapters/goadapter/  adapters/rust/
schemas/v1/*.json               JSON Schemas for all wire formats
deploy/docker-compose.yml  deploy/docker-compose.e2e.yml  deploy/Dockerfile.server
deploy/caddy/Caddyfile  deploy/backup.ps1  deploy/backup.sh
docs/  test/e2e/  dist/
```

## C2. Domain types (`internal/domain`) — exact signatures

```go
type PURL struct{ Ecosystem, Name, Version string } // pkg:npm/axios@1.12.0; golang names may contain '/'
func ParsePURL(s string) (PURL, error)
func (p PURL) String() string
func (p PURL) Major() string        // "1"; golang: "v1"
func (p PURL) MajorMinor() string   // "1.12"

type EnvironmentFingerprint struct {
    SchemaVersion int      `json:"schemaVersion"`           // 1
    Ecosystem, OS, OSVersionBucket, Arch string
    Runtime, RuntimeVersion, Language, LanguageVersion string
    PackageManager, PackageManagerVersion, ModuleSystem string
    Frameworks []string    // all fields omitempty except SchemaVersion, Ecosystem, OS, Arch
}
func (e EnvironmentFingerprint) Hash() string // "sha256:<hex>" of canonical JSON (sorted keys, no spaces)
func (e EnvironmentFingerprint) Bucketed() EnvironmentFingerprint // versions → major.minor buckets for web display

type Stage string  // observation: USED, PROJECT_TYPECHECK, PROJECT_COMPILE, PROJECT_TEST, PROJECT_PROCESS
                   // verification: RESOLVE, SYMBOL, TYPECHECK, COMPILE, LOAD, EXECUTE, TEST, CONTRACT
type Result string // "PASS" | "FAIL"
type SymbolConfidence string // "EXACT" | "PROBABLE" | "UNKNOWN"
type EvidenceClass string    // "USAGE_OBSERVATION" | "ADOPTION_EVIDENCE" | "SAMPLE_VERIFICATION" | "RUNTIME_INSTRUMENTATION"
type VerificationLevel string // "L0_SOURCE_ONLY".."L5_MATRIX_PASS"
type FailureDomain string // CODE, API_REMOVED_OR_CHANGED, LIBRARY_REGRESSION, TRANSITIVE_DEPENDENCY, RUNTIME, OS, ARCH, TOOLCHAIN, CONFIGURATION, EXTERNAL_SERVICE, RESOURCE, SECURITY_POLICY, UNKNOWN

type ObservationBatch struct { // wire format, schemas/v1/observation-batch.json
    SchemaVersion int    `json:"schemaVersion"`
    Epoch string         `json:"epoch"`      // "2026-08-13" daily bucket
    AnonID string        `json:"anonId"`     // rotating, see C10
    ProjectBucket string `json:"projectBucket"` // rotating HMAC, 12 hex chars
    Package string       `json:"package"`    // PURL string
    Symbol string        `json:"symbol,omitempty"`
    SymbolConfidence SymbolConfidence `json:"symbolConfidence,omitempty"`
    Environment EnvironmentFingerprint `json:"environment"`
    Stage Stage          `json:"stage"`
    Result Result        `json:"result"`
    ObservationCount int `json:"observationCount"`
    ErrorFingerprint string `json:"errorFingerprint,omitempty"` // "sha256:<hex>"
    ErrorCode string     `json:"errorCode,omitempty"`           // e.g. "TS2345", "ERR_REQUIRE_ESM"
}

type Case struct {
    SchemaVersion int   `json:"schemaVersion"`
    CaseID string       `json:"caseId"` // "case:sha256:<hex>" of canonical JSON w/o caseId
    Kind string         `json:"kind"`   // HOW | FIX | MIGRATION | CONFIG
    Goal string         `json:"goal"`
    Packages []string   `json:"packages"`
    Symbols []string    `json:"symbols,omitempty"`
    Constraints map[string]string `json:"constraints,omitempty"`
    Contract []string   `json:"contract"`
}

type SampleManifest struct { // csx.json inside artifact
    SchemaVersion int  `json:"schemaVersion"`
    Case Case          `json:"case"`
    Packages []string  `json:"packages"`
    Symbols []string   `json:"symbols,omitempty"`
    Environment EnvironmentFingerprint `json:"environment"`
    License string     `json:"license"` // "MIT-0"
    ContractCommand []string `json:"contractCommand"` // e.g. ["node","test/contract.mjs"]
    BuildCommand []string    `json:"buildCommand,omitempty"`
    VerifierAdapter string   `json:"verifierAdapter"` // "node-typescript@1"
}

type VerificationReceipt struct { // schemas/v1/verification-receipt.json — §7.7 verbatim fields
    SchemaVersion int `json:"schemaVersion"`
    SampleID string   `json:"sampleId"`
    CaseID string     `json:"caseId"`
    EnvironmentHash string `json:"environmentHash"`
    Environment EnvironmentFingerprint `json:"environment"`
    Stages map[string]string `json:"stages"` // resolve/typecheck/compile/load/execute/test/contract → PASS|FAIL|SKIPPED
    VerifierAdapter string `json:"verifierAdapter"`
    SandboxCapability string `json:"sandboxCapability"` // COMPILE_ONLY | CONTAINER_RUN | STRONG_ISOLATION_RUN | LIVE_INTEGRATION
    LogsDigest string  `json:"logsDigest"`
    CreatedAt string   `json:"createdAt"` // RFC3339
    PeerID string      `json:"peerId"`    // ed25519 pubkey fingerprint "ed25519:<hex16>"
    PeerSignature string `json:"peerSignature"` // base64 ed25519 over canonical JSON w/o signature
}

type FailureHypothesis struct{ Domain FailureDomain `json:"domain"`; Confidence float64 `json:"confidence"` }
type MatchGrade string // EXACT | COMPATIBLE | ADAPTATION_REQUIRED | REFERENCE_ONLY | NO_SAFE_MATCH
```

Canonical JSON helper: `domain.CanonicalJSON(v any) []byte` — struct → map, sorted keys, no indentation, no trailing newline. All hashes/signatures use it.

## C3. Local SQLite DDL (`internal/storage/localdb`, file `$CSX_HOME/csx.db`)

```sql
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS packages(
  purl TEXT PRIMARY KEY, ecosystem TEXT NOT NULL, name TEXT NOT NULL, version TEXT NOT NULL,
  public INTEGER NOT NULL DEFAULT 0,           -- 1 public, 0 private/unknown ⇒ excluded
  publicness TEXT NOT NULL DEFAULT 'UNKNOWN',  -- PUBLIC | PRIVATE | UNKNOWN
  first_seen TEXT, last_seen TEXT);
CREATE TABLE IF NOT EXISTS symbol_usages(
  purl TEXT NOT NULL, symbol TEXT NOT NULL, confidence TEXT NOT NULL,
  project_bucket TEXT NOT NULL, last_seen TEXT,
  PRIMARY KEY(purl,symbol,project_bucket));
CREATE TABLE IF NOT EXISTS observations(   -- local aggregate, pre-upload
  epoch TEXT NOT NULL, purl TEXT NOT NULL, symbol TEXT NOT NULL DEFAULT '',
  symbol_confidence TEXT NOT NULL DEFAULT 'UNKNOWN', env_hash TEXT NOT NULL,
  stage TEXT NOT NULL, result TEXT NOT NULL, count INTEGER NOT NULL DEFAULT 0,
  error_fp TEXT NOT NULL DEFAULT '', error_code TEXT NOT NULL DEFAULT '',
  uploaded INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(epoch,purl,symbol,env_hash,stage,result,error_fp));
CREATE TABLE IF NOT EXISTS environments(hash TEXT PRIMARY KEY, json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS cases(case_id TEXT PRIMARY KEY, kind TEXT, goal TEXT, json TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS samples(
  sample_id TEXT PRIMARY KEY, case_id TEXT, manifest_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'LOCAL',  -- LOCAL | LOCAL_PASS | PUBLISHED | CROSS_PASS | MATRIX_PASS | STABLE
  origin_seeder TEXT, license TEXT, created_at TEXT,
  pinned INTEGER NOT NULL DEFAULT 0, hot_score REAL NOT NULL DEFAULT 0, last_used TEXT,
  has_artifact INTEGER NOT NULL DEFAULT 0);
CREATE VIRTUAL TABLE IF NOT EXISTS search_fts USING fts5(
  doc_id UNINDEXED, kind UNINDEXED, title, body, packages, symbols, error_codes);
CREATE TABLE IF NOT EXISTS shards(
  key TEXT PRIMARY KEY,                 -- "npm/axios/1"
  etag TEXT, json TEXT NOT NULL, synced_at TEXT);
CREATE TABLE IF NOT EXISTS upload_queue(
  id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, -- 'evidence'|'adoption'|'receipt'
  payload TEXT NOT NULL, created_at TEXT, attempts INTEGER NOT NULL DEFAULT 0, last_error TEXT);
CREATE TABLE IF NOT EXISTS receipts(receipt_id TEXT PRIMARY KEY, sample_id TEXT, json TEXT, created_at TEXT);
CREATE TABLE IF NOT EXISTS hits(
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT, query TEXT, grade TEXT,
  sample_id TEXT, adopted INTEGER DEFAULT 0, post_build_pass INTEGER);
CREATE TABLE IF NOT EXISTS excluded_packages(pattern TEXT PRIMARY KEY);
```

Store API: `localdb.Open(path string) (*DB, error)` with typed methods (`UpsertPackage`, `RecordObservation`, `PendingBatches(limit)`, `MarkUploaded`, `SaveSample`, `IndexDoc(docID, kind, title, body, pkgs, syms, codes string)`, `FTSQuery(q string, limit int) []DocHit`, …). All methods take `context.Context`.

## C4. Server PostgreSQL DDL (`internal/serverstore/migrations/0001_init.sql`)

```sql
CREATE TABLE packages(
  purl TEXT PRIMARY KEY, ecosystem TEXT NOT NULL, name TEXT NOT NULL, version TEXT NOT NULL,
  major TEXT NOT NULL, publicness TEXT NOT NULL DEFAULT 'UNKNOWN', checked_at TIMESTAMPTZ,
  first_seen TIMESTAMPTZ DEFAULT now(), last_seen TIMESTAMPTZ DEFAULT now());
CREATE TABLE symbols(
  id BIGSERIAL PRIMARY KEY, ecosystem TEXT NOT NULL, package_name TEXT NOT NULL,
  family TEXT NOT NULL, kind TEXT DEFAULT 'function',
  UNIQUE(ecosystem, package_name, family));
CREATE TABLE evidence_agg(               -- aggregated automatic evidence
  id BIGSERIAL PRIMARY KEY, purl TEXT NOT NULL, symbol TEXT NOT NULL DEFAULT '',
  symbol_confidence TEXT NOT NULL DEFAULT 'UNKNOWN', env_hash TEXT NOT NULL,
  env_json JSONB NOT NULL, stage TEXT NOT NULL, result TEXT NOT NULL,
  error_fp TEXT NOT NULL DEFAULT '', error_code TEXT NOT NULL DEFAULT '',
  observation_count BIGINT NOT NULL DEFAULT 0,
  unique_peer_buckets INT NOT NULL DEFAULT 0, unique_project_buckets INT NOT NULL DEFAULT 0,
  first_seen TIMESTAMPTZ DEFAULT now(), last_seen TIMESTAMPTZ DEFAULT now(),
  UNIQUE(purl,symbol,env_hash,stage,result,error_fp));
CREATE TABLE evidence_dedup(             -- rotating buckets, purged after 30d (§14.4)
  bucket_kind TEXT NOT NULL,             -- 'peer'|'project'
  bucket TEXT NOT NULL, agg_id BIGINT NOT NULL REFERENCES evidence_agg(id) ON DELETE CASCADE,
  epoch TEXT NOT NULL, PRIMARY KEY(bucket_kind,bucket,agg_id,epoch));
CREATE TABLE cases(case_id TEXT PRIMARY KEY, kind TEXT, goal TEXT, json JSONB NOT NULL,
  created_at TIMESTAMPTZ DEFAULT now());
CREATE TABLE samples(
  sample_id TEXT PRIMARY KEY, case_id TEXT REFERENCES cases(case_id),
  manifest JSONB NOT NULL, status TEXT NOT NULL DEFAULT 'PUBLISHED',
  origin_seeder TEXT, license TEXT NOT NULL DEFAULT 'MIT-0',
  size_bytes BIGINT NOT NULL, hot_score DOUBLE PRECISION NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ DEFAULT now());
CREATE TABLE receipts(
  receipt_id TEXT PRIMARY KEY,           -- sha256 of canonical receipt JSON
  sample_id TEXT NOT NULL REFERENCES samples(sample_id),
  peer_id TEXT NOT NULL, env_hash TEXT NOT NULL, receipt JSONB NOT NULL,
  contract_result TEXT, created_at TIMESTAMPTZ DEFAULT now());
CREATE TABLE compatibility_snapshots(    -- §7.8 read-optimized
  purl TEXT NOT NULL, symbol TEXT NOT NULL DEFAULT '',
  snapshot JSONB NOT NULL, generated_at TIMESTAMPTZ DEFAULT now(),
  PRIMARY KEY(purl,symbol));
CREATE TABLE failure_clusters(
  id BIGSERIAL PRIMARY KEY, ecosystem TEXT, package_name TEXT, symbol TEXT DEFAULT '',
  stage TEXT, error_fp TEXT, error_code TEXT, observation_count BIGINT DEFAULT 0,
  env_summary JSONB, hypotheses JSONB,   -- [{domain,confidence}]
  regression_candidate BOOLEAN DEFAULT false, versions JSONB,
  first_seen TIMESTAMPTZ DEFAULT now(), last_seen TIMESTAMPTZ DEFAULT now(),
  UNIQUE(ecosystem,package_name,symbol,stage,error_fp));
CREATE TABLE identities(
  login TEXT PRIMARY KEY, github_id BIGINT UNIQUE, display TEXT,
  token_hash TEXT, api_token_hash TEXT UNIQUE, created_at TIMESTAMPTZ DEFAULT now());
CREATE TABLE verification_jobs(
  id BIGSERIAL PRIMARY KEY, sample_id TEXT NOT NULL REFERENCES samples(sample_id),
  reason TEXT NOT NULL,                  -- 'cross'|'matrix'
  want_env JSONB,                        -- desired env deltas (§10.2), nullable
  status TEXT NOT NULL DEFAULT 'open',   -- open|claimed|done
  claimed_by TEXT, claimed_at TIMESTAMPTZ, created_at TIMESTAMPTZ DEFAULT now());
CREATE TABLE peers(
  peer_id TEXT PRIMARY KEY, addr TEXT NOT NULL, port INT NOT NULL,
  capabilities JSONB, sample_ids JSONB, announced_at TIMESTAMPTZ DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL);
CREATE TABLE shards(key TEXT PRIMARY KEY, etag TEXT NOT NULL, json JSONB NOT NULL,
  generated_at TIMESTAMPTZ DEFAULT now());
CREATE TABLE stats_daily(day DATE PRIMARY KEY, stats JSONB NOT NULL);
```

`serverstore.Store` interface wraps pgx; pure aggregation logic lives in `internal/compatibility` operating on plain structs so it unit-tests without a DB.

## C5. Server HTTP API (all JSON; versioned; §21 verbatim paths)

```
POST /v1/evidence/batches     body {batches:[ObservationBatch]} → 202 {accepted:n, rejected:[{index,reason}]}
GET  /v1/registry/packages/{purl}   → {purl, publicness, majors:[...], symbols:[...], snapshotSummary}
GET  /v1/registry/symbols/{ecosystem}/{package}/{family} → symbol detail + snapshot
POST /v1/search               body SearchRequest (C7) → SearchResponse
GET  /v1/shards/{ecosystem}/{package}/{major}   (If-None-Match / ETag)
POST /v1/samples              multipart: manifest(json) + artifact(tar.gz) → {sampleId,status}
GET  /v1/samples/{sampleId}   → metadata + receipts summary
GET  /v1/samples/{sampleId}/artifact → application/gzip
POST /v1/verifications        body VerificationReceipt → {status, sampleStatus}
GET  /v1/verification/jobs?peerId=&capability=&limit= → {jobs:[{id,sampleId,reason,wantEnv}]}
POST /v1/verification/jobs/{id}/claim   body {peerId}
POST /v1/peers/announce       body {peerId,port,capabilities,sampleIds,ttlSeconds} → {addr}
GET  /v1/peers/for-sample/{sampleId} → {peers:[{peerId,addr,port}]}
GET  /v1/stats                → NetworkStats (estimated fields flagged)
GET  /v1/adapters             → capability matrix (embedded schemas/v1/adapters.json)
POST /v1/auth/github/device   → {deviceCode,userCode,verificationUri,interval} (501 if unconfigured)
POST /v1/auth/github/poll     body {deviceCode} → {login, apiToken} (token shown once)
GET  /healthz                 → 200 "ok"
```

Auth: evidence/search/shards/samples GET are anonymous. `POST /v1/samples` and `/v1/verifications` accept optional `Authorization: Bearer <apiToken>` to attribute seeder/verifier; anonymous allowed (seeder "anonymous").

## C6. Shard format (`schemas/v1/shard.json`)

```json
{"schemaVersion":1,"key":"npm/axios/1","generatedAt":"RFC3339",
 "packages":[{"purl":"pkg:npm/axios@1.12.0","symbols":[
   {"family":"axios.post","stats":{"observationCount":123,"uniquePeerBuckets":9,
    "passRate":0.94,"byStage":{"PROJECT_COMPILE":{"pass":100,"fail":4}},
    "confidence":"HIGH","lastSeen":"RFC3339"},
    "failures":[{"errorCode":"ERR_REQUIRE_ESM","fingerprint":"sha256:..","count":7,
                 "envSummary":{"moduleSystem":"esm","runtime":"node@18"}}]}],
  "samples":[{"sampleId":"sha256:..","goal":"..","status":"CROSS_PASS","license":"MIT-0",
              "environment":{...},"contractStages":{"contract":"PASS"}}]}]}
```

Client warm priority (§11.2): current project deps → recently used → global HOT (server provides `GET /v1/shards/hot` list inside `/v1/stats`) → pinned.

## C7. Search request/response + ranking (§11)

```go
type SearchRequest struct {
    SchemaVersion int      `json:"schemaVersion"`
    Query string           `json:"query"`
    Packages []string      `json:"packages,omitempty"`
    Symbols []string       `json:"symbols,omitempty"`
    Environment EnvironmentFingerprint `json:"environment"`
    ErrorFingerprint string `json:"errorFingerprint,omitempty"`
    ErrorCode string        `json:"errorCode,omitempty"`
    Limit int               `json:"limit,omitempty"` // default 3
}
type SearchResult struct {
    Grade MatchGrade        `json:"match"`
    Confidence string       `json:"confidence"` // HIGH|MEDIUM|LOW
    Score float64           `json:"score"`
    Case *Case              `json:"case,omitempty"`
    SampleID string         `json:"sampleId,omitempty"`
    SampleStatus string     `json:"sampleStatus,omitempty"`
    Exact []string          `json:"exact"`       // matching dims, human strings
    Different []string      `json:"different"`   // differing dims
    Adaptation []string     `json:"adaptationNeeded"`
    Evidence EvidenceSummary `json:"evidence"`
    KnownFailures []KnownFailure `json:"knownFailures,omitempty"`
}
type SearchResponse struct{ SchemaVersion int; Results []SearchResult; Miss bool `json:"miss"` }
```

Pipeline (§11.3 order, implemented in `internal/search`): 1 exact package/version filter → 2 symbol match → 3 error fingerprint exact → 4 FTS5 BM25 → 5 (v1: token-overlap intent similarity, no embeddings) → 6 environment gate → 7 verification-strength rerank (L3+ receipts boost ×2, L4+ ×3) → 8 recency decay `0.5^(ageDays/90)` → 9 known-failure penalty (matching env failure cluster ⇒ grade cap REFERENCE_ONLY).

**Execution-context axis (docs/execution-context.md, binding):** `executionContext` (+`browserFamily` when browser-like) is ALWAYS a sensitive dimension. Context mismatch between request env and result env ⇒ grade cap ADAPTATION_REQUIRED with adaptation entry "verify in <ctx>"; elevated failure observed in the requester's context ⇒ REFERENCE_ONLY. `browserMajor` distance scores like minor-version distance; `engine` mismatch scores like context mismatch. Snapshot/aggregation row keys use `EnvironmentFingerprint.ContextLabel()` as the leading dimension; PROJECT_* observation counts and SYMBOL_*/CONTRACT evidence are never summed together. Ingest rejects SYMBOL_EXECUTED/SYMBOL_CALL batches (A3-only stages; no v1 adapter claims A3). Failure-cluster hypotheses may use BROWSER/ENGINE domains (engine-concentrated failures ⇒ `{ENGINE:0.6, BROWSER:0.25, LIBRARY_REGRESSION:0.15}`-style distributions).

Grades: EXACT = same package major.minor + all sensitive dims equal. COMPATIBLE = same major, sensitive dims equal. ADAPTATION_REQUIRED = sensitive dim differs but adaptation enumerable (moduleSystem, packageManager, minor version). REFERENCE_ONLY = major differs or elevated failure in requester env. NO_SAFE_MATCH = best score < 0.25 ⇒ `Miss: true`, empty Results. Sensitive dims per case: `sensitiveDimensions` estimated as: moduleSystem+runtime for npm; runtime for pypi; toolchain for cargo/golang; failure-cluster boundaries add dims.

Confidence formula (server + local identical, `internal/compatibility/confidence.go`):
```
weight: USAGE_OBSERVATION=1, ADOPTION_EVIDENCE=3, SAMPLE_VERIFICATION=10
wPass/wFail = Σ weight·count·0.5^(ageDays/90)
passRate = wPass/(wPass+wFail); independence = uniquePeerBuckets
HIGH: passRate≥0.9 ∧ independence≥3 ∧ (wPass+wFail)≥10
MEDIUM: passRate≥0.75 ∧ independence≥2
ELEVATED_FAILURE: failRate≥0.25 ∧ (wPass+wFail)≥5   (reported in snapshot)
LOW: otherwise
```

## C8. MCP server (`internal/mcp`)

Transport: stdio, newline-delimited JSON-RPC 2.0. Methods: `initialize` (protocolVersion "2025-06-18", capabilities.tools), `notifications/initialized`, `ping`, `tools/list`, `tools/call`. Tools (§12.4/§21):

```
search_known_solution {query, packages?, symbols?, environment?, errorText?} → SearchResponse rendered per §11.5 + JSON
get_sample {sampleId} → manifest + file list + file contents (≤64KB/file)
explain_compatibility {package, symbol?, environment?} → snapshot explanation text + JSON
run_observed_command {command:[...], cwd?} → runs via evidence loop, returns local failure first as {exitCode, stdout, stderr, stdoutTruncated, stderrTruncated, stage, result, sanitizedErrors}, then an optional separate recommendation
report_sample_adoption {sampleId, applied:bool, buildPass?:bool} → records ADOPTION_EVIDENCE + hits row
propose_public_sample {goal, packages, symbols?} → SanitizedSpec + clean-room instructions + workspace dir (NO publish capability)
list_local_hits {} → recent hits rows
get_local_stats {} → local dashboard stats JSON
```
`publish_public_sample` deliberately absent (§12.4). Every tool result includes `"evidenceClass"` labels; compile observations never presented as execution proof (§3.5).

## C9. CLI surface (`internal/cli`, stdlib `flag`, subcommand dispatch)

```
csx init [--community|--local-only] [--yes] [--server URL]   # contract screen §5.4, agent rules/hooks install
csx run -- <command...>                                       # §8.3 wrapper
csx search <query...> [--json] [--package purl]...
csx daemon run|start|stop|status
csx ui [--open]                                               # dashboard §12.5
csx sync                                                      # shard warm + upload queue flush
csx sample propose --goal G [--package P]... | create <dir> | preview <id> | publish <id> [--seeder name|--anonymous] | verify <id> | list
csx login github
csx stats [--json]
csx config get <key> | set <key> <value>
csx version
csx-server serve   (env: CSX_DSN, CSX_LISTEN, CSX_BLOB_DIR, CSX_PUBLIC_URL, CSX_GITHUB_CLIENT_ID/SECRET, CSX_PUBLIC_CHECK=strict|trust, CSX_SNAPSHOT_INTERVAL)
csx-server migrate
```

`CSX_HOME` env overrides default `~/.csx`. Daemon listens: Windows named pipe `\\.\pipe\csx-daemon` + `127.0.0.1:48619` (UI); Unix: `$CSX_HOME/daemon.sock` + localhost. Peer listener: `:48620` (opt-in via config `peerListen`).

## C10. Identity (`internal/identity`)

- `identity.json`: `{schemaVersion:1, ed25519Priv: base64, anonSeed: base64(32B)}`, created on init, file mode 0600.
- PeerID = `"ed25519:" + hex(sha256(pubkey))[:16]`.
- Rotating anon evidence ID: `hex(HMAC-SHA256(anonSeed, "anon|"+epochDay))[:16]`; project bucket: `hex(HMAC-SHA256(anonSeed, "proj|"+projectAbsPath+"|"+epochMonth))[:12]` — server cannot reverse either; buckets purged server-side after 30 days.
- `Sign(canonicalJSON []byte) string` / `Verify(peerID, sig, msg) bool` (pubkey embedded in receipt `peerId` — v1 receipts carry `peerPubkey` field too for verification: add `PeerPubkey string` to receipt).

## C11. Sanitizer (`internal/sanitizer`) — §8.5

`Sanitize(raw string, publicPkgs []string) SanitizedError{Template, Code, Fingerprint, PublicSymbols []string}`.
Strip order: (1) extract+keep error codes `TS\d{4,5}`, `ERR_[A-Z_]+`, `E[A-Z]{2,}\d*`, rustc `E\d{4}`, exit codes; (2) node_modules paths → keep trailing public package name; (3) all abs/rel paths (win `[A-Za-z]:\\[^\s"']+`, unix `/[^\s"':]+`, `\.{1,2}[\\/][^\s"']+`) → `<path>`; (4) URLs → `<url>`; (5) emails → `<email>`; (6) quoted string literals → `<str>`; (7) hex/base64 runs ≥20 chars → `<token>`; (8) current username + home dir → `<user>`; (9) line/col numbers → `<n>`. Fingerprint = `"sha256:"+hex(sha256("v1|"+stage+"|"+code+"|"+template))`. Raw logs never leave `internal/evidence` unpersisted beyond local `logs/` (which is never uploaded).

## C12. Adapter interface (`internal/scanner/adapter.go`)

```go
type ResolvedPackage struct{ PURL domain.PURL; Publicness string; Direct bool; Source string }
type SymbolUsage struct{ Package domain.PURL; Family string; Kind string; Confidence domain.SymbolConfidence }
type CommandProfile struct{ Stage domain.Stage; Known bool; Tool string }
type Adapter interface {
    Ecosystem() string                    // "npm"|"pypi"|"golang"|"cargo"
    Capabilities() []string               // e.g. ["A0","A1","A2"]
    Detect(dir string) bool
    ScanPackages(ctx, dir) ([]ResolvedPackage, error)   // lockfile resolved versions (§7.1)
    ScanSymbols(ctx, dir, pkgs []ResolvedPackage) ([]SymbolUsage, error)
    ClassifyCommand(argv []string) CommandProfile
    EnvironmentHints(ctx, dir) map[string]string        // runtime/language/pm versions, moduleSystem
}
```
Registry `scanner.All() []Adapter`. Publicness check via `internal/registry/publiccheck.go` client-side: npm `https://registry.npmjs.org/<name>` HEAD/GET, PyPI `https://pypi.org/pypi/<name>/json`, crates.io `https://crates.io/api/v1/crates/<name>`, Go `https://proxy.golang.org/<module>/@latest`; results cached in `packages.publicness`; network failure ⇒ UNKNOWN ⇒ excluded from upload (safe default); `file:`/`link:`/`git+ssh`/`workspace:`/path deps ⇒ PRIVATE immediately, never queried.

## C13. Sample artifact + verification

Canonical artifact: tar of files sorted by slash-path, mode 0644 (0755 none), uid/gid 0, mtime Unix 0, no PAX headers beyond required; gzip with zero MTime. `sampleId = "sha256:"+hex(sha256(tar.gz bytes))`. Limits (§16.4): ≤256KB compressed, ≤200 files, no symlinks/hardlinks, no path traversal, no binaries (NUL-byte heuristic), forbidden names: `node_modules/`, `.git/`, `venv/`, `target/`, `.env`.

Leakage scan (`internal/samples/leakage.go`): flags AWS/GH/OpenAI-style keys (`AKIA[0-9A-Z]{16}`, `ghp_\w{36}`, `sk-[A-Za-z0-9]{20,}`), private key blocks, emails, non-allowlisted URLs (allowlist: registry hosts, example.com, localhost), absolute paths, the contributing project's dir name + git remote repo name, `process.env.X=`-style literals. Result: `[]Finding{File,Line,Kind,Excerpt}`; publish blocked while findings > 0 unless `--force-reviewed` after preview.

Verifier pipeline (§16.1): static checks → sandbox resolve (network ON, `npm ci --ignore-scripts` / `pip install --no-deps -r` etc.) → typecheck/compile (network OFF) → contract (network OFF). Docker present ⇒ capability CONTAINER_RUN (two-phase: resolve container with network, then `--network=none` container reusing workdir volume; images: `node:22-alpine`, `python:3.12-alpine`, `golang:1.26-alpine`, `rust:1-alpine`); Docker absent ⇒ COMPILE_ONLY (native resolve+typecheck only, contract SKIPPED, receipt says so honestly). Receipt signed per C10, posted to server; server updates sample status: `PUBLISHED→CROSS_PASS` on first contract-PASS receipt from peer ≠ origin; `→MATRIX_PASS` when receipts span ≥2 distinct (os|runtime major) boundaries; `→STABLE` at ≥3 independent peers ∧ no FAIL receipt in 30d.

## C14. Evidence loop (`csx run -- cmd`)

1. Detect adapters in cwd → scan packages (lockfile resolved) + symbols; upsert local DB; private excluded at scan time from any uploadable table.
2. Classify command → stage; spawn child with inherited stdio + separate bounded stdout/stderr tees (capture only, raw stays local; truncation is explicit).
3. Exit code → PASS/FAIL; on FAIL sanitize stderr tail (last 200 lines) → fingerprints. Local exit/stdout/stderr remains the primary result; automatic search is a separate secondary recommendation and LOW/reference/unrelated results are reference candidates only.
4. Record observation rows: one per (public pkg × [symbol if used] × stage × result × error_fp), count += 1, epoch = today.
5. Environment fingerprint cached per dir (1h TTL).
6. Batch upload: `csx sync` or daemon ticker (15min) posts pending rows as ObservationBatch[] (Community mode only); server unreachable ⇒ stays queued (§25.F). Local-only mode: never uploads, rows kept for local stats.

## C15. Ports, paths, deploy topology

- Local: daemon 127.0.0.1:48619 (+pipe/socket), peer :48620.
- Server container: :8080; Caddy 80/443 → csx-server:8080; postgres internal only; blob volume `/data/blobs`; compose file `deploy/docker-compose.yml`; prod Caddyfile site `codesamplex.dev`, local override compose uses `localhost:80`.
- Backup: `deploy/backup.ps1|sh` = `pg_dump -Fc` + blob dir archive → `./backups/<date>/`; restore documented in `docs/operations.md`.
- DNS (manual, documented): Gabia A record `codesamplex.dev` + `www` → Lightsail static IP; Caddy auto-HTTPS.

---

# Part II — Tasks

Execution notes: tasks within a phase sharing no files may run in parallel (worktree isolation). Every task: TDD, `go vet ./...` clean, `go build ./...` green, commit with conventional message. Acceptance commands are run from repo root on Windows PowerShell.

## Phase 1 — Scaffold + domain (Task group P1)

### Task P1.1: Module scaffold + canonical JSON + PURL
**Files:** `go.mod`, `internal/domain/{purl.go,canonical.go,types.go}`, tests `internal/domain/{purl_test.go,canonical_test.go}`
**Produces:** C2 types compile; `ParsePURL` handles npm scoped (`pkg:npm/%40scope/name@v` percent-encoding per PURL spec), pypi, cargo, golang multi-segment (`pkg:golang/github.com/a/b@v1.2.0`); `CanonicalJSON` deterministic (map ordering test).
**Acceptance:** `go test ./internal/domain/ -run 'TestParsePURL|TestCanonicalJSON' -v` PASS; round-trip property: `ParsePURL(p.String()) == p`.

### Task P1.2: Environment fingerprint + hash
**Files:** `internal/domain/envfp.go`, `internal/environment/{collect.go,collect_windows.go,collect_unix.go}`, tests.
**Produces:** `EnvironmentFingerprint.Hash()` (C2); `environment.Collect(ctx, dir, hints map[string]string) EnvironmentFingerprint` — OS/arch from runtime, os version bucket (win: `11`, linux: distro major best-effort), runtime/pm versions from hints (adapters) with `<tool> --version` fallback probes, results cached.
**Acceptance:** `go test ./internal/domain/ ./internal/environment/ -v` PASS; hash stable across field order.

### Task P1.3: JSON Schemas v1 + validation fixtures
**Files:** `schemas/v1/{purl.json,environment.json,observation-batch.json,case.json,sample-manifest.json,verification-receipt.json,search-request.json,search-response.json,shard.json,adapters.json}`, `internal/domain/schemas_test.go` (marshal Go values, assert required fields present — no external validator dep).
**Acceptance:** `go test ./internal/domain/ -run TestSchemaFixtures -v` PASS.

## Phase 2 — Local evidence loop (Task group P2)

### Task P2.1: Config + CSX_HOME + identity
**Files:** `internal/config/config.go`, `internal/identity/identity.go`, tests.
**Produces:** `config.Load()/Save()` per C9 defaults (`mode:""` = uninitialized, serverUrl `https://codesamplex.dev`), `identity.LoadOrCreate(home)`, anon/rotating IDs + Sign/Verify per C10.
**Acceptance:** `go test ./internal/config/ ./internal/identity/ -v`; rotating ID changes across epochs, stable within.

### Task P2.2: SQLite local store + FTS5
**Files:** `internal/storage/localdb/{db.go,migrate.go,observations.go,samples.go,fts.go}`, tests.
**Produces:** C3 DDL; typed methods listed in C3; FTS5 BM25 query working under modernc.org/sqlite.
**Acceptance:** `go test ./internal/storage/localdb/ -v` PASS incl. FTS insert+rank test.

### Task P2.3: CAS store
**Files:** `internal/storage/cas/cas.go`, tests.
**Produces:** `Put(r io.Reader) (id string, err)`, `Get(id) (io.ReadCloser, error)`, `Has(id) bool`, `Delete(id)`, `TotalSize() int64`; layout `cas/sha256/aa/bb/<hex>`.
**Acceptance:** `go test ./internal/storage/cas/ -v`.

### Task P2.4: Sanitizer
**Files:** `internal/sanitizer/sanitizer.go`, `sanitizer_test.go` with table tests from C11 (win paths, node_modules retention, TS2345, ERR_REQUIRE_ESM, tokens, usernames).
**Acceptance:** `go test ./internal/sanitizer/ -v`; fingerprint stable; NO raw path survives any test case.

### Task P2.5: Node/TS adapter
**Files:** `adapters/node/{adapter.go,lockfiles.go,symbols.go}`, testdata fixtures (package-lock v3, pnpm-lock v9, yarn classic), tests.
**Produces:** C12 impl: resolved versions from all three lockfiles; `file:`/`link:`/`git+ssh`/`workspace:` ⇒ PRIVATE; import scan (.ts/.tsx/.js/.mjs/.cjs, regex: static import/require/dynamic import), member usage `imported.member(` → family `pkgIdent.member` PROBABLE, bare import ⇒ package-only usage; moduleSystem from package.json `type` + tsconfig `module`; ClassifyCommand: `tsc`→PROJECT_TYPECHECK, `npm/pnpm/yarn run build`/`vite build`/`next build`→PROJECT_COMPILE, `* test`/`jest`/`vitest`→PROJECT_TEST, `node`→PROJECT_PROCESS.
**Acceptance:** `go test ./adapters/node/ -v` against fixtures; axios@1.12.0 resolved from each lockfile fixture.

### Task P2.6: Public registry check
**Files:** `internal/registry/publiccheck.go`, tests with `httptest` fake registries.
**Produces:** C12 publicness client w/ per-ecosystem URL builders, 24h cache via localdb, UNKNOWN-on-error semantics, scoped-npm handling.
**Acceptance:** `go test ./internal/registry/ -v`; network-failure test yields UNKNOWN (excluded).

### Task P2.7: Evidence recorder + `csx run`
**Files:** `internal/evidence/{recorder.go,runner.go,batch.go}`, `internal/scanner/scanner.go`, `internal/cli/{cli.go,run.go}`, `cmd/csx/main.go`, tests (fixture project run with `node -e "process.exit(0)"` style commands).
**Produces:** C14 loop; `evidence.PendingBatches()` → `[]ObservationBatch` with anon IDs; `cli.Main(argv) int`.
**Acceptance:** `go build ./cmd/csx` ; in a temp fixture npm project: `csx run -- node -e "process.exit(0)"` records PROJECT_PROCESS PASS rows for public deps only (private fixture dep absent); `go test ./internal/evidence/ -v`.

## Phase 3 — Local search + shards + cache (P3)

### Task P3.1: Ranking + grades + delta
**Files:** `internal/search/{engine.go,rank.go,delta.go,grade.go}`, `internal/compatibility/confidence.go`, tests.
**Produces:** C7 pipeline over localdb docs+shards; `search.Engine{DB, Shards}.Search(ctx, SearchRequest) SearchResponse`; delta text builder (§11.5 layout); NO_SAFE_MATCH threshold.
**Acceptance:** `go test ./internal/search/ ./internal/compatibility/ -v` — fixture: ESM sample vs CJS request ⇒ ADAPTATION_REQUIRED with "Import syntax only"-style adaptation entry; unrelated query ⇒ Miss=true.

### Task P3.2: Shard sync + warm + HOT cache policy
**Files:** `internal/search/shardsync.go`, `internal/storage/cache_policy.go`, tests with httptest shard server.
**Produces:** ETag-aware sync of C6 shards for: project deps, recent, HOT, pinned; cache budget eviction (never evict pinned; LRU by last_used weighted by hot_score).
**Acceptance:** `go test ./internal/search/ -run TestShardSync -v`; 304 path covered.

## Phase 4 — Daemon + MCP + CLI + UI (P4)

### Task P4.1: Daemon core + local API
**Files:** `internal/daemon/{daemon.go,api.go,listen_windows.go,listen_unix.go}`, tests via httptest.
**Produces:** mux: `GET /local/v1/status,stats`, `POST /local/v1/search`, `GET /local/v1/samples/{id}`, `POST /local/v1/adoption`, `GET /local/v1/queue`, `POST /local/v1/sync`; listeners per C9; single-instance lock file; background tickers (upload 15min, shard warm 1h, idle verification per budget).
**Acceptance:** `go test ./internal/daemon/ -v`.

### Task P4.2: MCP stdio server
**Files:** `internal/mcp/{server.go,tools.go}`, `internal/cli/mcp.go` (`csx mcp` hidden command used by agent configs), tests driving stdin/stdout pipes.
**Produces:** C8 protocol + all 8 tools; tool results carry both human text and `structuredContent` JSON.
**Acceptance:** `go test ./internal/mcp/ -v` — initialize→tools/list→tools/call(search_known_solution) round-trip on pipes.

### Task P4.3: `csx init` + contract + agent integration install
**Files:** `internal/cli/init.go`, `internal/cli/agentinstall.go`, templates in `internal/cli/agentassets/` (Claude Code: `.claude/skills` csx skill + settings hook snippet; Codex/Gemini/OpenCode: markdown rule files + MCP registration JSON), tests.
**Produces:** §5.4 contract screen verbatim (JOIN COMMUNITY / LOCAL ONLY prompt; `--community/--local-only/--yes` bypass), writes config+identity, registers MCP (`csx mcp`) in agent config files it finds (only with user confirm per file, `--yes` accepts), prints what was installed.
**Acceptance:** `go test ./internal/cli/ -run TestInit -v` in temp HOME: config written, contract text contains the §5.4 block verbatim.

### Task P4.4: `csx ui` dashboard + remaining CLI
**Files:** `internal/daemon/ui.go`, `internal/daemon/uiassets/` (embedded html/css/js, no CDN), `internal/cli/{search.go,stats.go,sync.go,ui.go,daemon.go,config.go,version.go}`, tests.
**Produces:** §12.5 dashboard (community status, cache, deps, hits/misses, post-hit pass, estimated reasoning avoided (labeled Estimated), evidence sent, seeds, cross verifications, privacy preview = pending upload payloads rendered verbatim); CLI wired.
**Acceptance:** `go test ./internal/daemon/ -run TestUI -v` (page renders, privacy preview shows only sanitized fields); `csx --help` lists all C9 commands.

## Phase 5 — Server (P5)

### Task P5.1: serverstore + migrations + confidence reuse
**Files:** `internal/serverstore/{store.go,pg.go,migrations/0001_init.sql,migrate.go}`, `cmd/csx-server/main.go`, tests (aggregation logic pure; pg tests behind `//go:build integration` using CSX_TEST_DSN).
**Produces:** C4 DDL; Store interface: `IngestBatches`, `UpsertPackages`, `GetSnapshot`, `PutSnapshot`, `SaveSample`, `SaveReceipt`, `OpenJobs`, `AnnouncePeer`, `PeersForSample`, `PutShard`, `GetShard`, `DailyStats`, `PurgeDedup(olderThan)`.
**Acceptance:** `go build ./cmd/csx-server`; `go test ./internal/serverstore/ -v` (non-integration parts).

### Task P5.2: Evidence ingest + dedup + publicness gate
**Files:** `internal/httpapi/{api.go,evidence.go}`, `internal/registry/serverpublic.go`, tests with fake store + httptest.
**Produces:** `POST /v1/evidence/batches` per C5: schema validation, PURL parse, ecosystem allowlist, publicness (strict: registry check w/ cache; trust: CSX_PUBLIC_CHECK=trust for dev/e2e), dedup bucket update (peer+project, epoch), counter upserts; rejects carry reasons; never stores raw error text (only fingerprint+code).
**Acceptance:** `go test ./internal/httpapi/ -run TestIngest -v` — duplicate same-epoch batch does not inflate unique buckets; private purl rejected.

### Task P5.3: Aggregation → snapshots + failure clusters + regression
**Files:** `internal/compatibility/{aggregate.go,snapshot.go,clusters.go,regression.go}`, `internal/httpapi/registry.go`, tests.
**Produces:** snapshot builder job (interval CSX_SNAPSHOT_INTERVAL, default 5m; `RunOnce(ctx)` exported for tests/e2e): per (purl,symbol) snapshot JSON = env-dim matrix rows {envBucket, byStage counts, passRate, confidence, uniquePeerBuckets, lastSeen} + failures; failure clusters per C4 with hypotheses ({CONFIGURATION:0.72,...}-style heuristics: ESM error codes ⇒ CONFIGURATION-major, else UNKNOWN 1.0); regression rule §10.3 (V failRate≥0.25 ∧ V-1 passRate≥0.9 same env bucket, both ≥5 obs); registry GET endpoints read snapshots only (§14.5).
**Acceptance:** `go test ./internal/compatibility/ -v` — regression fixture flags candidate; snapshot marks ELEVATED_FAILURE env row.

### Task P5.4: Search API + shard publisher
**Files:** `internal/httpapi/{search.go,shards.go}`, `internal/compatibility/shardgen.go`, tests.
**Produces:** `POST /v1/search` running C7 pipeline over server data; shard generation per C6 (on snapshot rebuild), ETag = sha256(json), If-None-Match → 304; `GET /v1/shards/{ecosystem}/{package}/{major}`.
**Acceptance:** `go test ./internal/httpapi/ -run 'TestSearch|TestShard' -v`.

### Task P5.5: Sample registry + blob + verification queue + peers + identity + stats
**Files:** `internal/httpapi/{samples.go,verifications.go,peers.go,auth.go,stats.go}`, `internal/storage/blob/{blob.go,fs.go}` (BlobStore interface §14.2: `LocalCAS` now, seams for ObjectStorage), tests.
**Produces:** C5 endpoints: sample upload (multipart, recompute sha256 must equal claimed sampleId, static checks per C13, size limits), artifact download (Main Seeder fallback §15), receipt ingest (verify ed25519 signature w/ embedded pubkey, receipt_id = sha256(canonical), status transitions per C13), verification jobs open/claim (cross for new samples; matrix jobs choose one-variable-changed env per §10.2 from receipt env set), peer announce/for-sample with TTL expiry, GitHub device flow (501 when unconfigured; token→identities row, api token returned once), `GET /v1/stats` per §14.5 numbers + estimated reasoning avoided formula: `hits_adopted × avg_miss_llm_calls(=3 fixed v1 assumption, labeled estimated) − rework`, flagged `"estimated": true`.
**Acceptance:** `go test ./internal/httpapi/ -v` full suite — bad-signature receipt rejected; second-peer contract-PASS receipt flips sample to CROSS_PASS.

## Phase 6 — Website (P6)

### Task P6.1: Landing + stats + install
**Files:** `internal/web/{web.go,templates/base.html,templates/landing.html,static/site.css}`, `internal/web/install/{install.ps1,install.sh}` served at `/install.ps1|.sh`, tests.
**Produces:** §14.5 landing verbatim structure (tagline "Stop solving the same code twice.", Install block, Peers/Packages/Symbols/Evidence/Verified Samples/Post-hit success rate/Estimated reasoning avoided counters, flywheel lines); server-rendered, no external assets; SEO meta + OpenGraph.
**Acceptance:** `go test ./internal/web/ -run TestLanding -v` (renders with fake stats, contains tagline).

### Task P6.2: Compatibility Explorer + sample + seeder pages
**Files:** `internal/web/{explorer.go,templates/package.html,templates/symbol.html,templates/sample.html,templates/seeder.html,templates/stats.html,templates/explore.html}`, tests.
**Produces:** routes `/{ecosystem}/{name}`, `/{ecosystem}/{name}/{version}`, `/{ecosystem}/{name}/{version}/{symbol}` (matrix per §14.5: env rows × stage cols, confidence chips, evidence-class counts, failure clusters, regression flags, linked samples), `/samples/{id}` (files, contract, receipts, Origin Seeder, status), `/seeders/{login}`, `/explore?q=`, `/stats`, `/sitemap.xml`; reads snapshots only.
**Acceptance:** `go test ./internal/web/ -v` — symbol page shows HIGH/ELEVATED FAILURE rows from fixture snapshot.

### Task P6.3: i18n (9 locales) + SEO hardening
**Files:** `internal/web/i18n/{i18n.go,locales/{en,ko,ja,zh-CN,es,fr,de,pt-BR,ru}.json}`, template updates, `internal/web/seo.go`, tests.
**Produces:** message catalog (`i18n.T(lang, key, args...)`), lang negotiation (path prefix `/{lang}/` for landing+static pages, `?lang=`+cookie elsewhere, Accept-Language fallback), all 9 locale files with full landing/explorer strings (numbers/dates locale-formatted), hreflang alternates on every indexable page, localized sitemap.xml with alternates, robots.txt, JSON-LD blocks, canonical URLs, OpenGraph.
**Acceptance:** `go test ./internal/web/ -run 'TestI18n|TestSEO' -v` — every locale file has every key (completeness test); `/ko/` renders Korean tagline; hreflang set contains all 9 + x-default.

## Phase 7 — Adapters (P7) — parallelizable

### Task P7.1: Python adapter
**Files:** `adapters/python/{adapter.go,locks.go,symbols.go}` + fixtures (requirements.txt pinned, uv.lock, poetry.lock best-effort), tests.
**Produces:** resolved versions (uv.lock TOML `[[package]] name/version`, requirements `==` pins, poetry.lock), imports via regex (`^import x`, `^from x import y`) → family `module.attr` PROBABLE (dynamic ⇒ UNKNOWN per §13.3), module→dist name normalization (PEP 503), commands: `pytest`→PROJECT_TEST, `mypy`→PROJECT_TYPECHECK, `python`→PROJECT_PROCESS, `pip install`/`uv sync`→USED; capability ["A0","A1","A2"].
**Acceptance:** `go test ./adapters/python/ -v`.

### Task P7.2: Go adapter
**Files:** `adapters/goadapter/{adapter.go,symbols.go}` + fixtures, tests.
**Produces:** go.mod require + go.sum presence = resolved; imports via `go/parser` on *.go; selector usage `pkgIdent.Func(` → family `<importPath>.Func` PROBABLE; commands `go build|vet`→PROJECT_COMPILE, `go test`→PROJECT_TEST, `go run`→PROJECT_PROCESS; module publicness via proxy.golang.org; capability ["A0","A1","A2"].
**Acceptance:** `go test ./adapters/goadapter/ -v`.

### Task P7.3: Rust adapter
**Files:** `adapters/rust/{adapter.go,symbols.go}` + fixtures, tests.
**Produces:** Cargo.lock `[[package]]` resolved; `use serde::Deserialize;` regex → family `serde::Deserialize` PROBABLE, macro invocations ⇒ UNKNOWN (conservative §13.5); commands `cargo build|check`→PROJECT_COMPILE, `cargo test`→PROJECT_TEST, `cargo run`→PROJECT_PROCESS; capability ["A0","A1","A2"].
**Acceptance:** `go test ./adapters/rust/ -v`.

### Task P7.4: Capability matrix
**Files:** `schemas/v1/adapters.json` (final content), `docs/adapters.md`, server route `GET /v1/adapters` + web page `/adapters`, tests.
**Produces:** matrix A0–A4 per adapter with honest levels (node: A0–A2+A4 verifier; python/go/rust: A0–A2, verifier node-only in v1 marked explicitly; A3 none in v1 §19).
**Acceptance:** `go test ./internal/httpapi/ -run TestAdapters -v`.

## Phase 8 — Sample pipeline (P8)

### Task P8.1: Canonical artifact + leakage scan
**Files:** `internal/samples/{artifact.go,leakage.go}`, tests.
**Produces:** C13 canonical tar.gz (determinism test: same dir twice ⇒ same sampleId), unpack safety checks, leakage scanner with all C13 patterns.
**Acceptance:** `go test ./internal/samples/ -v` — planted `ghp_` token and abs path both flagged; determinism holds.

### Task P8.2: Sandbox + verifier engine
**Files:** `internal/sandbox/{sandbox.go,docker.go,native.go,capability.go}`, `internal/verifier/{engine.go,node_ts.go}`, tests (docker tests skip w/o docker).
**Produces:** capability detection; two-phase docker run per C13; native COMPILE_ONLY; `verifier.Run(ctx, sampleDir, manifest) (Receipt, error)` filling stages RESOLVE/TYPECHECK/COMPILE/LOAD/CONTRACT (SKIPPED where capability lacks), logsDigest, signing.
**Acceptance:** `go test ./internal/verifier/ -v`; with docker: fixture sample (node echo-server contract) reaches CONTRACT PASS.

### Task P8.3: Clean-room workflow + CLI + cross-verification client
**Files:** `internal/samples/{spec.go,workflow.go}`, `internal/cli/sample.go`, `internal/verifier/crossclient.go`, tests.
**Produces:** SanitizedSpec builder (§9.2 fields only), `csx sample propose|create|preview|publish|verify|list` per C9 (§9.4 approval: full file listing + contents + license + seeder prompt; `[PUBLISH]` requires typed `yes`), MCP `propose_public_sample` wiring, cross-verify worker: claim job → fetch artifact → verify → post receipt; idle budget enforcement (§9.6 budgets OFF/5m/15m/idle/unlimited from config).
**Acceptance:** `go test ./internal/samples/ ./internal/verifier/ -run 'TestSpec|TestWorkflow|TestCross' -v`; publish refuses while leakage findings exist.

## Phase 9 — P2P (P9)

### Task P9.1: Peer node + transfer + fallback order
**Files:** `internal/peer/{node.go,serve.go,fetch.go}`, tests (two in-process peers).
**Produces:** peer HTTP: `GET /peer/v1/samples/{id}` (artifact from CAS), `GET /peer/v1/ping`; announce loop to tracker (TTL 30m, re-announce 10m); `peer.Fetch(ctx, sampleId)` order Local CAS → tracker peers → server artifact → MISS (§15.1); hash re-verify on every fetch; HOT seeding within cache budget (§15.3).
**Acceptance:** `go test ./internal/peer/ -v` — peer A publishes to CAS, peer B fetches via announce/tracker fake, hash verified; server-fallback test passes with peer down.

## Phase 10 — E2E scenarios (P10)

### Task P10.1: E2E harness A–F
**Files:** `test/e2e/{e2e.ps1,fixtures/npmproj/...,fixtures/private-proj/...}`, `deploy/docker-compose.e2e.yml` (fixed ports, CSX_PUBLIC_CHECK=trust, CSX_SNAPSHOT_INTERVAL=5s), and a report template (docs/e2e-report-template.md was never created; the harness writes `docs/e2e-report.md` directly).
**Produces:** scripted proof of §25 A–F using built binaries + compose stack + two CSX_HOME peer dirs; each scenario prints PASS/FAIL + evidence (curl outputs, DB counts); writes `docs/e2e-report.md`.
**Acceptance:** `powershell -File test/e2e/e2e.ps1` exits 0 with all six scenarios PASS.

## Phase 11 — Deploy + release (P11)

### Task P11.1: Docker + Compose + Caddy + backup
**Files:** `deploy/{Dockerfile.server,docker-compose.yml,caddy/Caddyfile,backup.ps1,backup.sh}`, `docs/operations.md`, `.dockerignore`.
**Produces:** multi-stage build (golang:1.26-alpine → alpine, static binary), compose per C15 with healthchecks + restart policies + volumes (pgdata, blobs, caddy_data); Caddyfile: `codesamplex.dev, www.codesamplex.dev` block + `:80` local block selected via `CADDY_SITE` env; backup scripts; operations doc incl. Gabia DNS + Lightsail steps + restore.
**Acceptance:** `docker compose -f deploy/docker-compose.yml up -d --build` then `curl http://localhost/healthz` = ok, landing renders, `POST /v1/evidence/batches` accepts a batch.

### Task P11.2: Release artifacts + README (9 languages) + final verification
**Files:** `build.ps1` (cross-compile matrix windows/linux/darwin amd64+arm64 → `dist/`), `README.md` (en) + `docs/i18n/README.{ko,ja,zh-CN,es,fr,de,pt-BR,ru}.md` with language switcher links at top of each, `LICENSE` (Apache-2.0 for code per §3.14 open-source requirement — sample default license stays MIT-0), `.gitignore`.
**Produces:** dist binaries; README ×9 (install, contract, architecture, privacy guarantees — same structure per locale); full `go test ./...` green; commit + push to origin/main.
**Acceptance:** `powershell -File build.ps1` produces 6 binaries + csx-server linux; `go test ./...` PASS; all 9 README files exist with identical section structure; `git push` succeeds.

### Task P11.3: Lightsail provisioning + production deploy + domain connection
**Files:** `deploy/lightsail/{provision.ps1,userdata.sh,deploy.ps1}`, `docs/operations.md` (extended).
**Produces:** AWS CLI (authenticated, account 577638370886) provisioning: `create-instances` blueprint `ubuntu_24_04` bundle `medium_*` ($12 2GB plan — verify exact bundle id via `get-bundles`), open ports 22/80/443 (+48620 peer TCP), allocate+attach static IP, userdata installs docker+compose+swapfile, `deploy.ps1` rsyncs/scps compose bundle + images (or builds remotely) and starts stack with CADDY_SITE=codesamplex.dev; then domain connection: Lightsail DNS zone `codesamplex.dev` with A records (@, www) → static IP; nameserver delegation at Gabia (automate via logged-in browser session if available, else present the 4 NS values for the user to paste at Gabia and verify propagation with nslookup loop); final smoke: `https://codesamplex.dev/healthz` 200 with valid cert (once DNS resolves).
**Acceptance:** instance running; `curl http://<staticIP>/healthz` = ok; landing served; DNS zone created with records; propagation verified or exact pending user step reported.

---

# Part III — Self-review results (spec coverage map)

- §18 Client checklist → P1.1–P4.4 (single binary: P11.2 matrix; init/contract: P4.3; daemon/CLI/MCP: P4.1–P4.4; SQLite+FTS: P2.2; sample cache: P2.3/P9.1; agent installs: P4.3; run: P2.7; ui: P4.4).
- §18 Evidence checklist → P2.4–P2.7, P5.2 (publicness P2.6; lockfile resolved P2.5/P7.x; fingerprint P1.2; symbol confidence C12; stages C14; sanitization P2.4; aggregation/batching P2.7; anonymous upload C10/P5.2; private exclusion P2.5+P2.6+P5.2 double-gate).
- §18 Ecosystem checklist → P2.5, P7.1–P7.4.
- §18 Search checklist → P3.1–P3.2, P5.4 (grades, delta, fingerprint search, FTS, shard warm, env rerank, confidence).
- §18 Sample checklist → P8.1–P8.3 (schema C2; content-addressed C13; clean-room §9; leakage; preview/approval; Origin Seeder; clean verify; cross queue P5.5; idle budget P8.3).
- §18 Server checklist → P5.1–P5.5 (registry, ingest, aggregation, snapshots, search, shards, sample registry/blob, Main Seeder, tracker, GitHub identity, stats, backup P11.1).
- §18 Website checklist → P6.1–P6.2.
- §18 Distribution checklist → P2.3, P3.2, P9.1.
- §25 scenarios → P10.1. §24 order preserved. §19 exclusions honored (no DHT, no enterprise, no A3 instrumentation, no IDE extensions).
- Known honest limitations recorded in docs/adapters.md + receipts: symbol confidence PROBABLE without type-checker integration; COMPILE_ONLY fallback without Docker; GitHub identity 501 until client id configured; production DNS cutover is a manual Gabia step documented in docs/operations.md.
