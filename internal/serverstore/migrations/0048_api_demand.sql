-- API demand telemetry (#394): what the private dashboard could not say.
--
-- The safe Caddy log answers "how many calls per route family per day" and
-- nothing else: it deletes the request before the bytes reach disk, so it
-- cannot say who called, how long the answer took, which client build asked
-- or where the request came from. The activity buckets answer "how many
-- networks" and refuse, by design, to be joined to anything. So the demand
-- questions an operator actually asks -- is the p95 on search moving, is a
-- stale client build still the majority, are callers spread or one farm --
-- had no honest source.
--
-- These four tables are the server's own aggregate, flushed every thirty
-- seconds from memory. Every column is a fixed label or a count:
--
--   route     the registered mux pattern ("POST /v2/search"), never a path
--   outcome   success | rejected | failure  (2xx-3xx / 4xx / 5xx)
--   auth      authenticated | anonymous | unidentified
--   country   ISO 3166-1 alpha-2 from a trusted edge header, or '' when the
--             edge did not say; never an address, never a client claim
--   client_*  the parsed csx User-Agent token, never the raw string
--   caller_hash  the anonymous-client ledger's own SHA-256 pseudonym, or a
--             domain-separated digest of an Authorization value
--
-- The latency histogram is ten fixed buckets so a p95 can be read without
-- ever storing a per-request duration. The application prunes every table to
-- thirty-five days. There is no IP, no User-Agent, no query string, no path
-- parameter and nothing finer than an hour.
--
-- dedup_key is the concatenated dimension key the application computes; it
-- exists because the automatic additive migration grammar admits one
-- single-column UNIQUE per table, which is exactly what an upsert needs.
CREATE TABLE api_demand_hourly (
  id bigserial PRIMARY KEY,
  dedup_key text NOT NULL UNIQUE,
  hour timestamptz NOT NULL,
  route text NOT NULL,
  outcome text NOT NULL,
  auth text NOT NULL,
  requests bigint NOT NULL DEFAULT 0,
  latency_sum_ms bigint NOT NULL DEFAULT 0,
  latency_le_10 bigint NOT NULL DEFAULT 0,
  latency_le_25 bigint NOT NULL DEFAULT 0,
  latency_le_50 bigint NOT NULL DEFAULT 0,
  latency_le_100 bigint NOT NULL DEFAULT 0,
  latency_le_250 bigint NOT NULL DEFAULT 0,
  latency_le_500 bigint NOT NULL DEFAULT 0,
  latency_le_1000 bigint NOT NULL DEFAULT 0,
  latency_le_2500 bigint NOT NULL DEFAULT 0,
  latency_le_5000 bigint NOT NULL DEFAULT 0,
  latency_gt_5000 bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX api_demand_hourly_hour_idx ON api_demand_hourly (hour);

CREATE TABLE api_demand_client_daily (
  id bigserial PRIMARY KEY,
  dedup_key text NOT NULL UNIQUE,
  day text NOT NULL,
  client_kind text NOT NULL,
  client_version text NOT NULL DEFAULT '',
  protocol text NOT NULL DEFAULT '',
  requests bigint NOT NULL DEFAULT 0
);

CREATE INDEX api_demand_client_daily_day_idx ON api_demand_client_daily (day);

CREATE TABLE api_demand_country_daily (
  id bigserial PRIMARY KEY,
  dedup_key text NOT NULL UNIQUE,
  day text NOT NULL,
  country text NOT NULL DEFAULT '',
  requests bigint NOT NULL DEFAULT 0
);

CREATE INDEX api_demand_country_daily_day_idx ON api_demand_country_daily (day);

CREATE TABLE api_demand_callers_daily (
  id bigserial PRIMARY KEY,
  dedup_key text NOT NULL UNIQUE,
  day text NOT NULL,
  route text NOT NULL,
  caller_hash text NOT NULL,
  requests bigint NOT NULL DEFAULT 0
);

CREATE INDEX api_demand_callers_daily_day_idx ON api_demand_callers_daily (day, route);
