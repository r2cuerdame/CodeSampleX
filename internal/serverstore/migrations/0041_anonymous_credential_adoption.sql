-- Daily rollout-readiness counters only. No raw credential or request metadata.
ALTER TABLE anonymous_client_days ADD COLUMN credential_present_count BIGINT NOT NULL DEFAULT 0 CHECK (credential_present_count >= 0);
ALTER TABLE anonymous_client_days ADD COLUMN credential_issued_count BIGINT NOT NULL DEFAULT 0 CHECK (credential_issued_count >= 0);
ALTER TABLE anonymous_analytics_collection ADD COLUMN credential_adoption_started_at TIMESTAMPTZ NOT NULL DEFAULT now();
