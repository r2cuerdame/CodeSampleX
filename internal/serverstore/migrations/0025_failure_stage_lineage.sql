-- Additive lineage for actual-stage failure classification. Outer command is
-- evidence only and is deliberately absent from the failure fingerprint.
ALTER TABLE evidence_agg ADD COLUMN outer_command TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN outer_stage TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN actual_toolchain TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN stage_evidence TEXT NOT NULL DEFAULT '';
ALTER TABLE evidence_agg ADD COLUMN failure_evidence_gap TEXT NOT NULL DEFAULT '';

ALTER TABLE failure_clusters ADD COLUMN outer_commands JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE failure_clusters ADD COLUMN actual_toolchain TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_clusters ADD COLUMN stage_evidence TEXT NOT NULL DEFAULT '';
ALTER TABLE failure_clusters ADD COLUMN failure_evidence_gap TEXT NOT NULL DEFAULT '';
