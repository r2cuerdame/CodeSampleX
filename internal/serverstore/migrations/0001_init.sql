-- CodeSampleX server schema v1 — plan contract C4, amended (task P5.1):
-- evidence_dedup gains a count column recording that bucket's
-- last-contributed count for its agg row+epoch, enabling delta-merge ingest.
-- Statements must not contain semicolons outside statement ends (the
-- migration runner splits on ';' after stripping -- line comments).

CREATE TABLE packages(
  purl TEXT PRIMARY KEY,
  ecosystem TEXT NOT NULL,
  name TEXT NOT NULL,
  version TEXT NOT NULL,
  major TEXT NOT NULL,
  publicness TEXT NOT NULL DEFAULT 'UNKNOWN',
  checked_at TIMESTAMPTZ,
  first_seen TIMESTAMPTZ DEFAULT now(),
  last_seen TIMESTAMPTZ DEFAULT now());

CREATE INDEX packages_name_idx ON packages(ecosystem, name);

CREATE TABLE symbols(
  id BIGSERIAL PRIMARY KEY,
  ecosystem TEXT NOT NULL,
  package_name TEXT NOT NULL,
  family TEXT NOT NULL,
  kind TEXT DEFAULT 'function',
  UNIQUE(ecosystem, package_name, family));

-- Aggregated automatic evidence. observation_count only ever grows by
-- delta-merge contributions; unique_*_buckets mirror evidence_dedup.
CREATE TABLE evidence_agg(
  id BIGSERIAL PRIMARY KEY,
  purl TEXT NOT NULL,
  symbol TEXT NOT NULL DEFAULT '',
  symbol_confidence TEXT NOT NULL DEFAULT 'UNKNOWN',
  env_hash TEXT NOT NULL,
  env_json JSONB NOT NULL,
  stage TEXT NOT NULL,
  result TEXT NOT NULL,
  error_fp TEXT NOT NULL DEFAULT '',
  error_code TEXT NOT NULL DEFAULT '',
  observation_count BIGINT NOT NULL DEFAULT 0,
  unique_peer_buckets INT NOT NULL DEFAULT 0,
  unique_project_buckets INT NOT NULL DEFAULT 0,
  first_seen TIMESTAMPTZ DEFAULT now(),
  last_seen TIMESTAMPTZ DEFAULT now(),
  UNIQUE(purl,symbol,env_hash,stage,result,error_fp));

CREATE INDEX evidence_agg_target_idx ON evidence_agg(purl, symbol);

-- Rotating anonymous buckets, purged after 30d (goal.md 14.4). count is the
-- bucket's last-contributed epoch total for its agg row (C4 amendment).
CREATE TABLE evidence_dedup(
  bucket_kind TEXT NOT NULL,
  bucket TEXT NOT NULL,
  agg_id BIGINT NOT NULL REFERENCES evidence_agg(id) ON DELETE CASCADE,
  epoch TEXT NOT NULL,
  count BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY(bucket_kind,bucket,agg_id,epoch));

CREATE INDEX evidence_dedup_epoch_idx ON evidence_dedup(epoch);

CREATE INDEX evidence_dedup_agg_idx ON evidence_dedup(agg_id, bucket_kind);

CREATE TABLE cases(
  case_id TEXT PRIMARY KEY,
  kind TEXT,
  goal TEXT,
  json JSONB NOT NULL,
  created_at TIMESTAMPTZ DEFAULT now());

CREATE TABLE samples(
  sample_id TEXT PRIMARY KEY,
  case_id TEXT REFERENCES cases(case_id),
  manifest JSONB NOT NULL,
  status TEXT NOT NULL DEFAULT 'PUBLISHED',
  origin_seeder TEXT,
  license TEXT NOT NULL DEFAULT 'MIT-0',
  size_bytes BIGINT NOT NULL,
  hot_score DOUBLE PRECISION NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ DEFAULT now());

CREATE INDEX samples_status_idx ON samples(status);

CREATE TABLE receipts(
  receipt_id TEXT PRIMARY KEY,
  sample_id TEXT NOT NULL REFERENCES samples(sample_id),
  peer_id TEXT NOT NULL,
  env_hash TEXT NOT NULL,
  receipt JSONB NOT NULL,
  contract_result TEXT,
  created_at TIMESTAMPTZ DEFAULT now());

CREATE INDEX receipts_sample_idx ON receipts(sample_id);

CREATE TABLE compatibility_snapshots(
  purl TEXT NOT NULL,
  symbol TEXT NOT NULL DEFAULT '',
  snapshot JSONB NOT NULL,
  generated_at TIMESTAMPTZ DEFAULT now(),
  PRIMARY KEY(purl,symbol));

CREATE TABLE failure_clusters(
  id BIGSERIAL PRIMARY KEY,
  ecosystem TEXT,
  package_name TEXT,
  symbol TEXT DEFAULT '',
  stage TEXT,
  error_fp TEXT,
  error_code TEXT,
  observation_count BIGINT DEFAULT 0,
  env_summary JSONB,
  hypotheses JSONB,
  regression_candidate BOOLEAN DEFAULT false,
  versions JSONB,
  first_seen TIMESTAMPTZ DEFAULT now(),
  last_seen TIMESTAMPTZ DEFAULT now(),
  UNIQUE(ecosystem,package_name,symbol,stage,error_fp));

CREATE INDEX failure_clusters_pkg_idx ON failure_clusters(package_name);

CREATE TABLE identities(
  login TEXT PRIMARY KEY,
  github_id BIGINT UNIQUE,
  display TEXT,
  token_hash TEXT,
  api_token_hash TEXT UNIQUE,
  created_at TIMESTAMPTZ DEFAULT now());

CREATE TABLE verification_jobs(
  id BIGSERIAL PRIMARY KEY,
  sample_id TEXT NOT NULL REFERENCES samples(sample_id),
  reason TEXT NOT NULL,
  want_env JSONB,
  status TEXT NOT NULL DEFAULT 'open',
  claimed_by TEXT,
  claimed_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ DEFAULT now());

CREATE INDEX verification_jobs_open_idx ON verification_jobs(status, created_at);

CREATE TABLE peers(
  peer_id TEXT PRIMARY KEY,
  addr TEXT NOT NULL,
  port INT NOT NULL,
  capabilities JSONB,
  sample_ids JSONB,
  announced_at TIMESTAMPTZ DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL);

CREATE INDEX peers_expiry_idx ON peers(expires_at);

CREATE TABLE shards(
  key TEXT PRIMARY KEY,
  etag TEXT NOT NULL,
  json JSONB NOT NULL,
  generated_at TIMESTAMPTZ DEFAULT now());

CREATE TABLE stats_daily(
  day DATE PRIMARY KEY,
  stats JSONB NOT NULL);
