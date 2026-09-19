-- Fix-claim verification (#444): upstream bug-fix claims as a bounded work
-- queue whose only way forward is receipted execution.
--
-- fix_candidates is one row per FIX_CANDIDATE: the normalized upstream
-- claim with its provenance (candidate, a bounded JSON document that has no
-- status field by construction), the queue state (score, attempts, lease,
-- closed_at) and the evaluator's reading of every run recorded so far
-- (status, pair_outcome, evaluation). A row is born CLAIMED_FIX and only
-- RecordFixRuns -- runs that cite receipts -- moves it. No producer, no
-- model and no operator write reaches status. closed_at NULL is an open
-- row; a set closed_at with closed_reason takes it off the board and
-- deletes nothing.
--
-- fix_runs is append-only: one execution of the reproducer at one version
-- in one bounded environment, with the receipt that proves the verdict. A
-- re-run at the same version supersedes in the evaluator (latest wins) but
-- the earlier row stays, so a boundary's history is never erased.
--
-- fix_claim_ingest is the door counter for extraction precision: what
-- producers sent and what the validator refused. One row, keyed 'ingest'.
--
-- Every column is one of the types the automatic additive migration
-- allowlist admits (internal/deploygate), so this ships without an
-- exact-statement exception, like 0047 and 0048.
CREATE TABLE fix_candidates (
  id bigserial PRIMARY KEY,
  dedup_key text NOT NULL UNIQUE,
  ecosystem text NOT NULL,
  name text NOT NULL,
  claimed_bad_version text NOT NULL DEFAULT '',
  claimed_fixed_version text NOT NULL,
  candidate jsonb NOT NULL,
  status text NOT NULL DEFAULT 'CLAIMED_FIX',
  pair_outcome text NOT NULL DEFAULT '',
  score bigint NOT NULL DEFAULT 0,
  attempts bigint NOT NULL DEFAULT 0,
  closed_at timestamptz,
  closed_reason text NOT NULL DEFAULT '',
  reproducer_source text NOT NULL DEFAULT '',
  reproducer_sample_id text NOT NULL DEFAULT '',
  claimed_by text NOT NULL DEFAULT '',
  claimed_at timestamptz,
  lease_expires_at timestamptz,
  evaluation jsonb NOT NULL DEFAULT '{}',
  run_count bigint NOT NULL DEFAULT 0,
  farm_seconds bigint NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  evaluated_at timestamptz
);

CREATE INDEX fix_candidates_package_idx ON fix_candidates (ecosystem, name, claimed_fixed_version);
CREATE INDEX fix_candidates_bad_idx ON fix_candidates (ecosystem, name, claimed_bad_version);
CREATE INDEX fix_candidates_queue_idx ON fix_candidates (closed_at, attempts, score, id);
CREATE INDEX fix_candidates_status_idx ON fix_candidates (status);

CREATE TABLE fix_runs (
  id bigserial PRIMARY KEY,
  candidate_id bigint NOT NULL,
  session_id text NOT NULL DEFAULT '',
  version text NOT NULL,
  env_os text NOT NULL DEFAULT '',
  env_arch text NOT NULL DEFAULT '',
  env_runtime text NOT NULL DEFAULT '',
  env_runtime_version text NOT NULL DEFAULT '',
  verdict text NOT NULL,
  failure_fingerprint text NOT NULL DEFAULT '',
  receipt_id text NOT NULL DEFAULT '',
  sample_id text NOT NULL DEFAULT '',
  farm_seconds bigint NOT NULL DEFAULT 0,
  observed_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX fix_runs_candidate_idx ON fix_runs (candidate_id, id);

CREATE TABLE fix_claim_ingest (
  singleton text PRIMARY KEY,
  ingested bigint NOT NULL DEFAULT 0,
  rejected bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);
